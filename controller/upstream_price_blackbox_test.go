// Package controller_test hosts black-box acceptance tests for the upstream
// price catalog (spec §18.3, §20.1). It is an external test package because
// the routes are exercised through the real production registration
// (router.SetApiRouter), and the router package imports controller.
package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUpstreamPriceControllerDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Channel{},
		&model.Ability{},
		&model.Model{},
		&model.Vendor{},
		&model.Log{},
		&model.PriceSource{},
		&model.PriceSnapshot{},
		&model.PriceSyncRun{},
		&model.PriceSyncRunItem{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled = previousRedis
		_ = sqlDB.Close()
	})
	return db
}

func createUserWithPAT(t *testing.T, db *gorm.DB, username string, role int, token string) {
	t.Helper()
	require.Len(t, token, 32, "access tokens are char(32)")
	user := &model.User{
		Username:    username,
		Password:    "unused-password-hash",
		Role:        role,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AccessToken: &token,
		AffCode:     username, // aff_code is unique per user
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
}

// TestUpstreamPriceRoutesRootOnly exercises every catalog route through
// the real production registration (router.SetApiRouter) with the real
// RootAuth middleware: anonymous 401, common/admin 403, root reaches the
// handler (HTTP 200 even for business errors, per the ApiError convention).
func TestUpstreamPriceRoutesRootOnly(t *testing.T) {
	db := setupUpstreamPriceControllerDB(t)
	commonToken := "commonuser-pat-aaaaaaaaaaaaaaaaaa"[:32]
	adminToken := "adminuser-pat-bbbbbbbbbbbbbbbbbbb"[:32]
	rootToken := "rootuser-pat-cccccccccccccccccccc"[:32]
	createUserWithPAT(t, db, "pc-common", common.RoleCommonUser, commonToken)
	createUserWithPAT(t, db, "pc-admin", common.RoleAdminUser, adminToken)
	createUserWithPAT(t, db, "pc-root", common.RoleRootUser, rootToken)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	router.SetApiRouter(engine)

	request := func(method, path, token string) *httptest.ResponseRecorder {
		var req *http.Request
		if method == http.MethodGet {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/upstream-price-sources"},
		{http.MethodGet, "/api/upstream-price-sources/adapters"},
		{http.MethodPost, "/api/upstream-price-sources"},
		{http.MethodPut, "/api/upstream-price-sources/999999"},
		{http.MethodPost, "/api/upstream-price-sources/999999/preview"},
		{http.MethodPost, "/api/upstream-price-sources/999999/sync"},
		{http.MethodGet, "/api/upstream-prices/current"},
		{http.MethodPost, "/api/upstream-prices/compare"},
	}
	rootWriteRoutes := 0
	for _, route := range routes {
		label := route.method + " " + route.path

		anonymous := request(route.method, route.path, "")
		assert.Equal(t, http.StatusUnauthorized, anonymous.Code, label)

		asCommon := request(route.method, route.path, commonToken)
		assert.Equal(t, http.StatusForbidden, asCommon.Code, label)
		asAdmin := request(route.method, route.path, adminToken)
		assert.Equal(t, http.StatusForbidden, asAdmin.Code, label)

		// Root passes authentication and reaches the handler; business
		// errors (missing source, invalid body) still answer HTTP 200 with
		// success:false per the ApiError convention.
		asRoot := request(route.method, route.path, rootToken)
		assert.Equal(t, http.StatusOK, asRoot.Code, label)
		assert.Contains(t, asRoot.Body.String(), `"success"`, label)
		if route.method != http.MethodGet {
			rootWriteRoutes++
		}
	}

	// Root write requests trigger the async admin audit fallback; wait for
	// those writes so the goroutines never outlive the test database.
	require.Eventually(t, func() bool {
		var auditLogs int64
		if err := db.Model(&model.Log{}).Where("type = ?", model.LogTypeManage).Count(&auditLogs).Error; err != nil {
			return false
		}
		return auditLogs >= int64(rootWriteRoutes)
	}, 5*time.Second, 20*time.Millisecond, "admin audit fallback records every root write request")
}

func TestPublicPricingExposesNoCostFields(t *testing.T) {
	db := setupUpstreamPriceControllerDB(t)
	model.InvalidatePricingCache()
	t.Cleanup(model.InvalidatePricingCache)

	// Seed a channel + ability so the pricing payload is non-empty, plus a
	// synced-looking price snapshot that must never leak into /api/pricing.
	channel := &model.Channel{Name: "pricing-channel", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     "default",
		Model:     "claude-sonnet-4",
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)
	require.NoError(t, db.Create(&model.PriceSnapshot{
		SourceId:           1,
		SourceModelName:    "anthropic/claude-sonnet-4",
		CanonicalModelName: "claude-sonnet-4",
		Role:               "supplier_cost",
		Scope:              "public",
		Currency:           "USD",
		FormulaKind:        "token_expr_v1",
		PriceExpr:          `tier("base", p * 3 + c * 15)`,
		Fingerprint:        strings.Repeat("e", 64),
		FingerprintVersion: "fp1",
		LastSeenRunId:      1,
	}).Error)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/pricing", controller.GetPricing)
	req := httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)

	body := recorder.Body.String()
	assert.Contains(t, body, "claude-sonnet-4", "pricing payload should include the seeded model")
	for _, forbidden := range []string{
		"supplier_cost",
		"supplier",
		"channel_cost",
		"cost",
		"margin",
		"fingerprint",
		"price_expr",
		"upstream_price",
		"source_model_name",
	} {
		assert.NotContains(t, body, forbidden,
			"public pricing API must not expose catalog cost field %q", forbidden)
	}
}
