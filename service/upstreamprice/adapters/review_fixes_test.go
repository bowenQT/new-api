package adapters

import (
	"context"
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogRoleScopeFromSnapshotHistoricalAuthority: snapshots are the
// historical authority for role/scope/provider; editing the source
// declaration later must not reinterpret persisted observations.
func TestCatalogRoleScopeFromSnapshotHistoricalAuthority(t *testing.T) {
	db, source, _ := setupSyncFixture(t)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)

	// Rewrite the source declaration directly in the DB (the service layer
	// would refuse this combination for the Vercel adapter).
	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).Updates(map[string]interface{}{
		"role":  string(upstreamprice.RoleCuratedReference),
		"scope": string(upstreamprice.ScopeUnknown),
	}).Error)

	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	require.NotEmpty(t, catalog.Entries)
	for _, entry := range catalog.Entries {
		switch entry.Status {
		case upstreamprice.CatalogStatusCurrent, upstreamprice.CatalogStatusMissing:
			// Snapshot-backed entries keep the values recorded at write time.
			assert.Equal(t, string(upstreamprice.RoleSupplierCost), entry.Role, entry.SourceModelName)
			assert.Equal(t, string(upstreamprice.ScopePublic), entry.Scope, entry.SourceModelName)
		default:
			// Entries without a snapshot can only reflect the source
			// declaration.
			assert.Equal(t, string(upstreamprice.RoleCuratedReference), entry.Role, entry.SourceModelName)
		}
	}
}

// duplicateFixturePayload returns the response body used by the duplicate
// tests: one normal model plus two same-id entries with different prices, in
// the given order.
func duplicateFixturePayload(reversed bool) string {
	normal := `{"id":"alibaba/qwen-3-14b","owned_by":"alibaba","pricing":{"input":"0.00000012","output":"0.00000024"}}`
	dupA := `{"id":"dup/model","owned_by":"dup","pricing":{"input":"0.000001","output":"0.000002"}}`
	dupB := `{"id":"dup/model","owned_by":"dup","pricing":{"input":"0.000003","output":"0.000004"}}`
	first, second := dupA, dupB
	if reversed {
		first, second = dupB, dupA
	}
	return `{"object":"list","data":[` + normal + `,` + first + `,` + second + `]}`
}

func TestDuplicateModelDeterministicRejection(t *testing.T) {
	db := setupCatalogTestDB(t)
	reversed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(duplicateFixturePayload(reversed)))
	}))
	t.Cleanup(server.Close)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "vercel-dup",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)

	assertDuplicateHandling := func(preview *dto.UpstreamPricePreviewResponse) {
		assert.Equal(t, 3, preview.DiscoveredCount)
		assert.Equal(t, 1, preview.ValidCount)
		assert.Equal(t, 1, preview.RejectedCount)
		duplicateItems := 0
		for _, item := range preview.Items {
			if item.SourceModelName == "dup/model" {
				duplicateItems++
				assert.Equal(t, model.PriceSyncItemStatusRejected, item.Status)
				assert.Equal(t, upstreamprice.WarningDuplicateModel, item.WarningCode)
				assert.Empty(t, item.PriceExpr)
			}
		}
		assert.Equal(t, 1, duplicateItems, "exactly one run item per duplicated model")
	}

	// Order A.
	previewA, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assertDuplicateHandling(previewA)

	// Order B: identical outcome regardless of response ordering.
	reversed = true
	previewB, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assertDuplicateHandling(previewB)

	// Commit stores no snapshot for the duplicated model and exactly one
	// rejected run item.
	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, previewB.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, 1, result.RejectedCount)
	var duplicateSnapshots int64
	require.NoError(t, db.Model(&model.PriceSnapshot{}).Where("source_model_name = ?", "dup/model").Count(&duplicateSnapshots).Error)
	assert.EqualValues(t, 0, duplicateSnapshots)
	var items []model.PriceSyncRunItem
	require.NoError(t, db.Where("source_model_name = ?", "dup/model").Find(&items).Error)
	require.Len(t, items, 1)
	assert.Equal(t, model.PriceSyncItemStatusRejected, items[0].Status)
	assert.Equal(t, upstreamprice.WarningDuplicateModel, items[0].WarningCode)
}

func TestSourceSettingsStrictAtWriteAndCanonicalized(t *testing.T) {
	db := setupCatalogTestDB(t)
	server := serveFixture(t)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)

	// Credential/endpoint-shaped settings are refused at create time.
	hostile := `{"api_key":"sk-secret","url":"https://evil.example"}`
	_, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "hostile-settings",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
		Settings:   &hostile,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")

	// Valid settings are persisted in canonical serialized form, not as the
	// client's raw JSON.
	messy := `  {"coverage_drop_threshold": 0.50,   "model_mappings": {"openai/x": "x-mapped"}}  `
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "canonical-settings",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
		Settings:   &messy,
	})
	require.NoError(t, err)

	stored, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	assert.NotEqual(t, messy, stored.Settings)
	parsed, err := upstreamprice.ParseSourceSettings(stored.Settings)
	require.NoError(t, err)
	require.NotNil(t, parsed.CoverageDropThreshold)
	assert.Equal(t, 0.5, *parsed.CoverageDropThreshold)
	assert.Equal(t, map[string]string{"openai/x": "x-mapped"}, parsed.ModelMappings)
	expected, err := common.Marshal(parsed)
	require.NoError(t, err)
	assert.Equal(t, string(expected), stored.Settings, "stored settings must be the canonical serialization")
}

// TestOversizedModelNameBoundedIdentity: a model name over 255 bytes fails
// closed at the plan boundary into a bounded diagnostic identity (200 bytes +
// "#" + 12 hex of SHA-256); the original never reaches storage, and distinct
// oversized names sharing a 200-byte prefix stay distinguishable.
func TestOversizedModelNameBoundedIdentity(t *testing.T) {
	db := setupCatalogTestDB(t)
	sharedPrefix := "vendor/" + strings.Repeat("x", 250)
	oversizedA := sharedPrefix + "-variant-a"
	oversizedB := sharedPrefix + "-variant-b"
	normal := `{"id":"alibaba/qwen-3-14b","owned_by":"alibaba","pricing":{"input":"0.00000012","output":"0.00000024"}}`
	entryA := `{"id":"` + oversizedA + `","owned_by":"vendor","pricing":{"input":"0.000001","output":"0.000002"}}`
	entryB := `{"id":"` + oversizedB + `","owned_by":"vendor","pricing":{"video_duration_pricing":[]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[` + normal + `,` + entryA + `,` + entryB + `]}`))
	}))
	t.Cleanup(server.Close)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "vercel-oversized",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, preview.ValidCount)
	assert.Equal(t, 2, preview.RejectedCount)

	boundedItems := []string{}
	for _, item := range preview.Items {
		if item.WarningCode == upstreamprice.WarningFieldTooLong {
			boundedItems = append(boundedItems, item.SourceModelName)
			assert.Equal(t, model.PriceSyncItemStatusRejected, item.Status)
			// Bounded shape: 200-byte prefix + "#" + 12 hex chars, within the
			// column limit, never the original value.
			assert.LessOrEqual(t, len(item.SourceModelName), 255)
			assert.Equal(t, 213, len(item.SourceModelName))
			assert.Contains(t, item.SourceModelName, "#")
			assert.True(t, strings.HasPrefix(item.SourceModelName, "vendor/"))
			assert.NotEqual(t, oversizedA, item.SourceModelName)
			assert.NotEqual(t, oversizedB, item.SourceModelName)
		}
	}
	// Both oversized models share the 200-byte prefix but keep distinct hash
	// suffixes, so both diagnostic items survive.
	require.Len(t, boundedItems, 2)
	assert.NotEqual(t, boundedItems[0], boundedItems[1])
	assert.Equal(t, boundedItems[0][:201], boundedItems[1][:201])

	// Commit persists the bounded identities, no snapshots for them, and the
	// stored values respect the column bound.
	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, 2, result.RejectedCount)
	assert.Equal(t, 1, result.NewSnapshotCount)
	var items []model.PriceSyncRunItem
	require.NoError(t, db.Where("warning_code = ?", upstreamprice.WarningFieldTooLong).Find(&items).Error)
	require.Len(t, items, 2)
	for _, item := range items {
		assert.LessOrEqual(t, len(item.SourceModelName), 255)
		assert.Nil(t, item.SnapshotId)
	}
	assert.NotEqual(t, items[0].SourceModelName, items[1].SourceModelName)
	var snapshotCount int64
	require.NoError(t, db.Model(&model.PriceSnapshot{}).Count(&snapshotCount).Error)
	assert.EqualValues(t, 1, snapshotCount)
}

// fixtureServerCounting serves statusFirst for the first request, then the
// fixture, counting requests.
func fixtureServerCounting(t *testing.T, statusFirst int) (*httptest.Server, *int64) {
	t.Helper()
	fixture, err := os.ReadFile("testdata/vercel_models_sample.json")
	require.NoError(t, err)
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		if n == 1 && statusFirst != http.StatusOK {
			w.WriteHeader(statusFirst)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestFetchRetriesOn503ThenSucceeds(t *testing.T) {
	server, requests := fixtureServerCounting(t, http.StatusServiceUnavailable)
	adapter := newVercelAdapterForTest(nextTestAdapterKey(), server.URL)

	observations, meta, err := adapter.Fetch(context.Background(), vercelSourceConfig(adapter.key))
	require.NoError(t, err)
	assert.Len(t, observations, 6)
	assert.Equal(t, http.StatusOK, meta.HTTPStatus)
	assert.EqualValues(t, 2, atomic.LoadInt64(requests), "exactly one retry after the 503")
}

func TestFetchDoesNotRetryNonRetryableStatus(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	adapter := newVercelAdapterForTest(nextTestAdapterKey(), server.URL)

	_, meta, err := adapter.Fetch(context.Background(), vercelSourceConfig(adapter.key))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http 400")
	assert.Equal(t, http.StatusBadRequest, meta.HTTPStatus)
	assert.EqualValues(t, 1, atomic.LoadInt64(&requests), "400 must not be retried")
}

func TestFetchRefusesRedirectWithoutRetry(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		http.Redirect(w, r, "https://evil.example/v1/models", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	adapter := newVercelAdapterForTest(nextTestAdapterKey(), server.URL)

	_, _, err := adapter.Fetch(context.Background(), vercelSourceConfig(adapter.key))
	require.Error(t, err)
	assert.ErrorIs(t, err, upstreamprice.ErrRedirectRefused)
	assert.EqualValues(t, 1, atomic.LoadInt64(&requests), "redirects fail immediately, no retry")
}

func TestFetchRefusesOversizedResponse(t *testing.T) {
	server := serveFixture(t)
	adapter := newVercelAdapterForTest(nextTestAdapterKey(), server.URL)
	// Package-internal injection point, mirroring allowTestEndpoint: the
	// production limit stays an immutable exported constant.
	adapter.maxResponseBytes = 128

	_, _, err := adapter.Fetch(context.Background(), vercelSourceConfig(adapter.key))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestCoverageDropGateRefusesCommitKeepsCurrent(t *testing.T) {
	db := setupCatalogTestDB(t)
	drop := map[string]bool{}
	server := serveFixtureDropping(t, drop)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "vercel-coverage",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	first, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)

	// Lose 2 of 6 valid models: 33% drop exceeds the 20% default gate.
	drop["alibaba/qwen-3-14b"] = true
	drop["alibaba/qwen3.5-flash"] = true

	preview2, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.True(t, preview2.CoverageDropExceeded)
	assert.Equal(t, model.PriceSyncRunStatusFailed, preview2.ProjectedRunStatus)

	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview2.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, model.PriceSyncRunStatusFailed, result.Status)
	assert.Contains(t, result.ErrorSummary, "coverage")

	// The refused commit changes nothing: current pointer, snapshots, and
	// the catalog all stay on the first run.
	reloaded, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, first.RunId, *reloaded.LastSuccessRunId)
	assert.EqualValues(t, 6, countRows(t, db, &model.PriceSnapshot{}))

	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	currentCount := 0
	for _, entry := range catalog.Entries {
		if entry.Status == upstreamprice.CatalogStatusCurrent {
			currentCount++
		}
	}
	assert.Equal(t, 6, currentCount)
}

func TestStaleThresholdMarksCatalogEntries(t *testing.T) {
	db := setupCatalogTestDB(t)
	server := serveFixture(t)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	settings := `{"stale_threshold_seconds": 3600}`
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "vercel-stale",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
		Settings:   &settings,
	})
	require.NoError(t, err)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)

	// Fresh run: not stale.
	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	require.NotEmpty(t, catalog.Entries)
	assert.False(t, catalog.Entries[0].Stale)

	// Age the run past the explicit threshold: every entry flips to stale
	// but keeps its price.
	aged := common.GetTimestamp() - 7200
	require.NoError(t, db.Model(&model.PriceSyncRun{}).Where("id = ?", result.RunId).
		Update("finished_at", aged).Error)
	catalog, err = upstreamprice.GetCurrentUpstreamPrices(&source.Id)
	require.NoError(t, err)
	require.NotEmpty(t, catalog.Entries)
	for _, entry := range catalog.Entries {
		assert.True(t, entry.Stale, entry.SourceModelName)
	}
}
