package adapters

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	relaykitdto "github.com/QuantumNous/new-api/relaykit/dto"
	relaykittypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/upstreamprice"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billingConfigExport captures every sale-pricing configuration surface the
// price catalog must never touch (spec §4.3, §18.3), as exported JSON.
type billingConfigExport struct {
	ModelRatio           string
	ModelPrice           string
	CompletionRatio      string
	CacheRatio           string
	CreateCacheRatio     string
	ImageRatio           string
	AudioRatio           string
	AudioCompletionRatio string
	GroupRatio           string
	GroupGroupRatio      string
	BillingMode          string
	BillingExpr          string
}

func exportBillingConfig(t *testing.T) billingConfigExport {
	t.Helper()
	billingMode, err := common.Marshal(billing_setting.GetBillingModeCopy())
	require.NoError(t, err)
	billingExpr, err := common.Marshal(billing_setting.GetBillingExprCopy())
	require.NoError(t, err)
	return billingConfigExport{
		ModelRatio:           ratio_setting.ModelRatio2JSONString(),
		ModelPrice:           ratio_setting.ModelPrice2JSONString(),
		CompletionRatio:      ratio_setting.CompletionRatio2JSONString(),
		CacheRatio:           ratio_setting.CacheRatio2JSONString(),
		CreateCacheRatio:     ratio_setting.CreateCacheRatio2JSONString(),
		ImageRatio:           ratio_setting.ImageRatio2JSONString(),
		AudioRatio:           ratio_setting.AudioRatio2JSONString(),
		AudioCompletionRatio: ratio_setting.AudioCompletionRatio2JSONString(),
		GroupRatio:           ratio_setting.GroupRatio2JSONString(),
		GroupGroupRatio:      ratio_setting.GroupGroupRatio2JSONString(),
		BillingMode:          string(billingMode),
		BillingExpr:          string(billingExpr),
	}
}

// TestBillingIsolationAcrossFullSync is the §18.3 regression: a full
// preview + commit against the Vercel fixture must leave every sale-pricing
// configuration export byte-identical, and user quota and channel balance
// unchanged, while snapshots are actually written.
func TestBillingIsolationAcrossFullSync(t *testing.T) {
	db := setupCatalogTestDB(t)
	server := serveFixture(t)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	channel.Balance = 123.45
	require.NoError(t, db.Save(channel).Error)

	user := &model.User{
		Username:    "billing-isolation-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		Quota:       500000,
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)

	// Initialize live ratio/billing configuration, including an entry for a
	// model the fixture also prices, so any leak would be visible.
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-5.6-luna": 0.1, "claude-sonnet-4": 1.5}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"claude-sonnet-4": 5}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"flux-kontext-max": 0.5}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default": 1, "vip": 0.9}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"claude-sonnet-4": "tiered_expr"}`,
		"billing_setting.billing_expr": `{"claude-sonnet-4": "tier(\"base\", p * 3 + c * 15)"}`,
	}))

	before := exportBillingConfig(t)

	// Billing read-path probe: the exact values the charging path reads for
	// fixed models, captured before the sync (M8a black-box invariance).
	type billingReadProbe struct {
		modelRatio      float64
		modelRatioOk    bool
		completionRatio float64
		modelPrice      float64
		modelPriceOk    bool
		billingMode     string
		billingExpr     string
		billingExprOk   bool
	}
	readBillingProbe := func(modelName string) billingReadProbe {
		probe := billingReadProbe{}
		probe.modelRatio, probe.modelRatioOk, _ = ratio_setting.GetModelRatio(modelName)
		probe.completionRatio = ratio_setting.GetCompletionRatio(modelName)
		probe.modelPrice, probe.modelPriceOk = ratio_setting.GetModelPrice(modelName, false)
		probe.billingMode = billing_setting.GetBillingMode(modelName)
		probe.billingExpr, probe.billingExprOk = billing_setting.GetBillingExpr(modelName)
		return probe
	}
	probedModels := []string{"claude-sonnet-4", "gpt-5.6-luna", "flux-kontext-max"}
	probesBefore := map[string]billingReadProbe{}
	for _, name := range probedModels {
		probesBefore[name] = readBillingProbe(name)
	}

	// Real billing computation probe (M8b): the actual relay pre-consume
	// price entry point, for a fixed usage vector, per billing mode — ratio
	// (gpt-5.6-luna), per-call price (flux-kontext-max), and tiered
	// expression (claude-sonnet-4).
	computeRealPrice := func(modelName string) hosttypes.PriceData {
		gin.SetMode(gin.TestMode)
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		info := &relaycommon.RelayInfo{
			UserId:          user.Id,
			UserGroup:       "default",
			UsingGroup:      "default",
			OriginModelName: modelName,
			UserSetting:     relaykitdto.UserSetting{},
		}
		price, err := relayhelper.ModelPriceHelper(ginCtx, info, 1000, &relaykittypes.TokenCountMeta{MaxTokens: 512})
		require.NoError(t, err, modelName)
		return price
	}
	realPricesBefore := map[string]hosttypes.PriceData{}
	for _, name := range probedModels {
		realPricesBefore[name] = computeRealPrice(name)
	}

	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "vercel-isolation",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	require.Equal(t, 6, result.NewSnapshotCount, "snapshots must actually be written")
	require.EqualValues(t, 6, countRows(t, db, &model.PriceSnapshot{}))

	after := exportBillingConfig(t)
	// Byte-identical exports: the catalog write path must not touch any
	// sale-pricing configuration.
	assert.Equal(t, before.ModelRatio, after.ModelRatio)
	assert.Equal(t, before.ModelPrice, after.ModelPrice)
	assert.Equal(t, before.CompletionRatio, after.CompletionRatio)
	assert.Equal(t, before.CacheRatio, after.CacheRatio)
	assert.Equal(t, before.CreateCacheRatio, after.CreateCacheRatio)
	assert.Equal(t, before.ImageRatio, after.ImageRatio)
	assert.Equal(t, before.AudioRatio, after.AudioRatio)
	assert.Equal(t, before.AudioCompletionRatio, after.AudioCompletionRatio)
	assert.Equal(t, before.GroupRatio, after.GroupRatio)
	assert.Equal(t, before.GroupGroupRatio, after.GroupGroupRatio)
	assert.Equal(t, before.BillingMode, after.BillingMode)
	assert.Equal(t, before.BillingExpr, after.BillingExpr)

	// The values the charging path actually reads are identical after the
	// sync, model by model.
	for _, name := range probedModels {
		assert.Equal(t, probesBefore[name], readBillingProbe(name), name)
	}

	// The full PriceData the relay pre-consume entry point computes is
	// value-for-value identical after the sync, across all three billing
	// modes.
	for _, name := range probedModels {
		assert.Equal(t, realPricesBefore[name], computeRealPrice(name), name)
	}

	// User quota and channel balance are untouched.
	var reloadedUser model.User
	require.NoError(t, db.First(&reloadedUser, "id = ?", user.Id).Error)
	assert.Equal(t, user.Quota, reloadedUser.Quota)
	var reloadedChannel model.Channel
	require.NoError(t, db.First(&reloadedChannel, "id = ?", channel.Id).Error)
	assert.Equal(t, 123.45, reloadedChannel.Balance)
}
