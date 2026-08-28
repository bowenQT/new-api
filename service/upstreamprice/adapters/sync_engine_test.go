package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var testAdapterKeyCounter int64

func nextTestAdapterKey() string {
	return fmt.Sprintf("vercel_test_%d", atomic.AddInt64(&testAdapterKeyCounter, 1))
}

func setupCatalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.User{},
		&model.PriceSource{},
		&model.PriceSnapshot{},
		&model.PriceSyncRun{},
		&model.PriceSyncRunItem{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		_ = sqlDB.Close()
	})
	return db
}

func createTestChannel(t *testing.T, db *gorm.DB) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Name:   "vercel-channel",
		Key:    "test-key",
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(channel).Error)
	return channel
}

// setupSyncFixture wires a fixture server, a uniquely-keyed test adapter, an
// enabled channel, and a supplier_cost source created through the service.
func setupSyncFixture(t *testing.T) (*gorm.DB, *model.PriceSource, string) {
	t.Helper()
	db := setupCatalogTestDB(t)
	server := serveFixture(t)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)

	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "vercel-cost",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)
	return db, source, adapterKey
}

func countRows(t *testing.T, db *gorm.DB, model interface{}) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(model).Count(&count).Error)
	return count
}

func TestSourceRoleChannelConstraints(t *testing.T) {
	db := setupCatalogTestDB(t)
	server := serveFixture(t)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)

	// supplier_cost without a channel is refused.
	_, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "no-channel",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel")

	// A missing channel id is refused.
	missing := 9999
	_, err = upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "missing-channel",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &missing,
	})
	require.Error(t, err)

	// The Vercel adapter only allows supplier_cost/public, so a reference
	// role is refused for it (adapter allowed-set enforcement).
	_, err = upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "wrong-role",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleCuratedReference),
		Scope:      string(upstreamprice.ScopePublic),
	})
	require.Error(t, err)

	// Phase 1 refuses enabling background scheduling.
	enabled := true
	_, err = upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:            "scheduled",
		AdapterKey:      adapterKey,
		Role:            string(upstreamprice.RoleSupplierCost),
		Scope:           string(upstreamprice.ScopePublic),
		ChannelId:       &channel.Id,
		ScheduleEnabled: &enabled,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Phase 1")
}

func TestPreviewDoesNotPersistAnything(t *testing.T) {
	db, source, _ := setupSyncFixture(t)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)

	assert.Equal(t, 8, preview.DiscoveredCount)
	assert.Equal(t, 6, preview.ValidCount)
	assert.Equal(t, 2, preview.UnsupportedCount)
	assert.Equal(t, 0, preview.RejectedCount)
	assert.Equal(t, 0, preview.MissingCount)
	assert.Equal(t, 6, preview.NewCount)
	assert.Equal(t, model.PriceSyncRunStatusPartial, preview.ProjectedRunStatus)
	assert.NotEmpty(t, preview.PreviewToken)
	assert.Nil(t, preview.BaseRunId)

	assert.EqualValues(t, 0, countRows(t, db, &model.PriceSnapshot{}))
	assert.EqualValues(t, 0, countRows(t, db, &model.PriceSyncRun{}))
	assert.EqualValues(t, 0, countRows(t, db, &model.PriceSyncRunItem{}))

	reloaded, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	assert.Nil(t, reloaded.LastSuccessRunId)
}

func TestSyncCommitWritesRunItemsAndSnapshots(t *testing.T) {
	db, source, _ := setupSyncFixture(t)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)

	assert.Equal(t, model.PriceSyncRunStatusPartial, result.Status)
	assert.Equal(t, 8, result.DiscoveredCount)
	assert.Equal(t, 6, result.ValidCount)
	assert.Equal(t, 2, result.UnsupportedCount)
	assert.Equal(t, 0, result.RejectedCount)
	assert.Equal(t, 0, result.MissingCount)
	assert.Equal(t, 6, result.NewSnapshotCount)
	assert.Equal(t, 0, result.IdempotentHitCount)

	assert.EqualValues(t, 6, countRows(t, db, &model.PriceSnapshot{}))
	assert.EqualValues(t, 1, countRows(t, db, &model.PriceSyncRun{}))
	assert.EqualValues(t, 8, countRows(t, db, &model.PriceSyncRunItem{}))

	reloaded, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, result.RunId, *reloaded.LastSuccessRunId)

	// Current catalog projection: 6 current entries with prices, 2
	// unsupported entries labeled but priceless.
	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	statuses := map[string]int{}
	byModel := map[string]dto.UpstreamCurrentPriceEntry{}
	for _, entry := range catalog.Entries {
		statuses[entry.Status]++
		byModel[entry.SourceModelName] = entry
	}
	assert.Equal(t, 6, statuses[upstreamprice.CatalogStatusCurrent])
	assert.Equal(t, 2, statuses[upstreamprice.CatalogStatusUnsupported])

	sonnet := byModel["anthropic/claude-sonnet-4"]
	assert.Equal(t, upstreamprice.CatalogStatusCurrent, sonnet.Status)
	assert.Equal(t, "claude-sonnet-4", sonnet.CanonicalModelName)
	assert.Equal(t, upstreamprice.MappingStatusDefault, sonnet.MappingStatus)
	assert.False(t, sonnet.Stale)
	assert.False(t, sonnet.Orphaned)
	assert.False(t, sonnet.VariesByProvider)

	sol := byModel["openai/gpt-5.6-sol"]
	assert.True(t, sol.VariesByProvider, "varies_by_provider must reach the catalog projection")

	video := byModel["alibaba/wan-v2.5-t2v-preview"]
	assert.Equal(t, upstreamprice.CatalogStatusUnsupported, video.Status)
	assert.Equal(t, upstreamprice.WarningNoTokenPricing, video.WarningCode)
	assert.Empty(t, video.PriceExpr)

	// Second full round: identical content hits fingerprint idempotency.
	preview2, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, preview2.NewCount)
	assert.Equal(t, 6, preview2.UnchangedCount)
	result2, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview2.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, 0, result2.NewSnapshotCount)
	assert.Equal(t, 6, result2.IdempotentHitCount)
	assert.EqualValues(t, 6, countRows(t, db, &model.PriceSnapshot{}), "idempotent hits must not add rows")

	// Snapshot evidence advanced to the new run.
	var sonnetSnapshot model.PriceSnapshot
	require.NoError(t, db.Where("source_model_name = ?", "anthropic/claude-sonnet-4").First(&sonnetSnapshot).Error)
	assert.Equal(t, result2.RunId, sonnetSnapshot.LastSeenRunId)

	// The first preview token is now stale: its base run no longer matches.
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.Error(t, err)
}

func TestSyncRejectsAfterConfigRevisionChange(t *testing.T) {
	_, source, adapterKey := setupSyncFixture(t)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)

	// Any accepted source update bumps config_revision and invalidates the
	// outstanding preview token.
	_, err = upstreamprice.UpdatePriceSource(source.Id, &dto.UpstreamPriceSourceRequest{
		Name:       "vercel-cost-renamed",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  source.ChannelId,
	})
	require.NoError(t, err)

	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrPriceSyncConflict)
}

func TestSyncRejectsTamperedAndForeignTokens(t *testing.T) {
	_, source, _ := setupSyncFixture(t)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)

	// Tampered token: flip one character of the signature.
	token := preview.PreviewToken
	tampered := token[:len(token)-1]
	if token[len(token)-1] == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, tampered)
	require.Error(t, err)

	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, "garbage")
	require.Error(t, err)
}

func TestOrphanSourcePreviewAllowedCommitRefused(t *testing.T) {
	db, source, _ := setupSyncFixture(t)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	snapshotCount := countRows(t, db, &model.PriceSnapshot{})
	require.Positive(t, snapshotCount)

	// Delete the linked channel: the source becomes orphaned.
	require.NoError(t, db.Delete(&model.Channel{}, *source.ChannelId).Error)

	views, err := upstreamprice.ListPriceSources()
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.True(t, views[0].Orphaned)

	// Preview stays available for diagnostics.
	orphanPreview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 6, orphanPreview.ValidCount)

	// Commit is refused.
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, orphanPreview.PreviewToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "orphaned")

	// Historical snapshots are retained and the catalog labels the orphan.
	assert.Equal(t, snapshotCount, countRows(t, db, &model.PriceSnapshot{}))
	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	require.NotEmpty(t, catalog.Entries)
	for _, entry := range catalog.Entries {
		assert.True(t, entry.Orphaned)
	}
}

func TestDisabledSourceCommitRefusedHistoryQueryable(t *testing.T) {
	_, source, adapterKey := setupSyncFixture(t)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)

	disabled := false
	_, err = upstreamprice.UpdatePriceSource(source.Id, &dto.UpstreamPriceSourceRequest{
		Name:       "vercel-cost",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  source.ChannelId,
		Enabled:    &disabled,
	})
	require.NoError(t, err)

	// Disabled sources refuse commit...
	freshPreview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, freshPreview.PreviewToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")

	// ...but history remains queryable.
	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	assert.NotEmpty(t, catalog.Entries)
}

// serveFixtureDropping serves the fixture minus any model ids present in the
// mutable drop set, so tests can make historical models go missing between
// runs.
func serveFixtureDropping(t *testing.T, drop map[string]bool) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile("testdata/vercel_models_sample.json")
	require.NoError(t, err)
	var payload struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	require.NoError(t, common.Unmarshal(fixture, &payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filtered := make([]json.RawMessage, 0, len(payload.Data))
		for _, raw := range payload.Data {
			var entry struct {
				Id string `json:"id"`
			}
			require.NoError(t, common.Unmarshal(raw, &entry))
			if !drop[entry.Id] {
				filtered = append(filtered, raw)
			}
		}
		response := payload
		response.Data = filtered
		body, err := common.Marshal(response)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMissingModelSemanticsAcrossRuns(t *testing.T) {
	db := setupCatalogTestDB(t)

	// A mutable server lets the upstream "lose" models between runs.
	drop := map[string]bool{}
	server := serveFixtureDropping(t, drop)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "vercel-missing",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)

	// Drop one model from the upstream response; the rest keeps coverage
	// above the gate (5 of 6 valid ≈ 17% drop < 20%).
	drop["alibaba/qwen-3-14b"] = true
	preview2, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, preview2.MissingCount)
	assert.Contains(t, preview2.Missing, "alibaba/qwen-3-14b")
	assert.False(t, preview2.CoverageDropExceeded)

	result2, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview2.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, model.PriceSyncRunStatusPartial, result2.Status)
	assert.Equal(t, 1, result2.MissingCount)

	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	var missingEntry *dto.UpstreamCurrentPriceEntry
	unsupportedCount := 0
	for i := range catalog.Entries {
		entry := catalog.Entries[i]
		if entry.SourceModelName == "alibaba/qwen-3-14b" {
			missingEntry = &catalog.Entries[i]
		}
		if entry.Status == upstreamprice.CatalogStatusUnsupported {
			unsupportedCount++
		}
	}
	// Unsupported models are never counted as missing (spec §8.2).
	require.NotNil(t, missingEntry)
	assert.Equal(t, upstreamprice.CatalogStatusMissing, missingEntry.Status)
	assert.Equal(t, `tier("base", p * 0.12 + c * 0.24)`, missingEntry.PriceExpr,
		"missing models keep their last observed snapshot for display")
	assert.Equal(t, 2, unsupportedCount)
}
