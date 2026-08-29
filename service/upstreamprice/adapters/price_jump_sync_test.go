package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// mutableVercelServer serves the Vercel fixture with per-model pricing a test
// can reprice between runs, so the price-movement check runs against the
// expressions the real adapter generates rather than hand-written ones.
type mutableVercelServer struct {
	mu       sync.Mutex
	entries  []map[string]json.RawMessage
	repriced map[string]string
}

func (s *mutableVercelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := s.body()
	digest := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(digest[:8]) + `"`
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(body)
}

func (s *mutableVercelServer) body() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := make([]map[string]json.RawMessage, 0, len(s.entries))
	for _, entry := range s.entries {
		copied := make(map[string]json.RawMessage, len(entry))
		for key, value := range entry {
			copied[key] = value
		}
		var id string
		if err := common.Unmarshal(copied["id"], &id); err == nil {
			if pricing, ok := s.repriced[id]; ok {
				copied["pricing"] = json.RawMessage(pricing)
			}
		}
		data = append(data, copied)
	}
	payload, err := common.Marshal(map[string]any{"object": "list", "data": data})
	if err != nil {
		return []byte(`{"object":"list","data":[]}`)
	}
	return payload
}

// reprice replaces one model's whole pricing object.
func (s *mutableVercelServer) reprice(modelId, pricing string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repriced[modelId] = pricing
}

// setupPriceJumpFixture wires the mutable fixture to an enabled supplier_cost
// source, optionally with source settings.
func setupPriceJumpFixture(t *testing.T, settings string) (*gorm.DB, *model.PriceSource, *mutableVercelServer) {
	t.Helper()
	db := setupCatalogTestDB(t)

	fixture, err := os.ReadFile("testdata/vercel_models_sample.json")
	require.NoError(t, err)
	var payload struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, common.Unmarshal(fixture, &payload))
	catalog := &mutableVercelServer{entries: payload.Data, repriced: map[string]string{}}
	server := httptest.NewServer(catalog)
	t.Cleanup(server.Close)

	adapterKey := fmt.Sprintf("vercel_jump_%d", atomic.AddInt64(&testAdapterKeyCounter, 1))
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	request := &dto.UpstreamPriceSourceRequest{
		Name:       "vercel-jump",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	}
	if settings != "" {
		request.Settings = &settings
	}
	source, err := upstreamprice.CreatePriceSource(request)
	require.NoError(t, err)
	return db, source, catalog
}

func priceJumpAlertsOf(t *testing.T, sourceId int) []dto.UpstreamPriceAlert {
	t.Helper()
	response, err := upstreamprice.ListSourceAlerts()
	require.NoError(t, err)
	jumps := make([]dto.UpstreamPriceAlert, 0, len(response.Alerts))
	for _, alert := range response.Alerts {
		if alert.Code == upstreamprice.AlertPriceJump && alert.SourceId == sourceId {
			jumps = append(jumps, alert)
		}
	}
	return jumps
}

func latestRun(t *testing.T, db *gorm.DB, sourceId int) *model.PriceSyncRun {
	t.Helper()
	run := &model.PriceSyncRun{}
	require.NoError(t, db.Where("source_id = ?", sourceId).Order("id desc").First(run).Error)
	return run
}

// TestPriceJumpRecordedWithoutBlockingCommit is the end-to-end contract of the
// §13 price-movement alert: the first run has no baseline and reports nothing,
// a tenfold reprice is reported with both model names and its measured rate,
// and the commit that carried it succeeded exactly as it would have without the
// check.
func TestPriceJumpRecordedWithoutBlockingCommit(t *testing.T) {
	db, source, catalog := setupPriceJumpFixture(t, "")

	baseline := syncOnce(t, source.Id)
	assert.Equal(t, model.PriceSyncRunStatusPartial, baseline.Status)
	assert.Equal(t, 0, baseline.PriceJumpCount, "the first run has no baseline to move from")
	assert.Empty(t, latestRun(t, db, source.Id).PriceJumpSummary)
	assert.Empty(t, priceJumpAlertsOf(t, source.Id))

	// Ten times the input price, output untouched.
	catalog.reprice("alibaba/qwen-3-14b", `{"input":"0.0000012","output":"0.00000024"}`)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, preview.ChangedCount)
	assert.Equal(t, 1, preview.PriceJumpCount, "preview must show the movement a commit would record")

	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, model.PriceSyncRunStatusPartial, result.Status,
		"a price movement is evidence, not a gate: the run status is unchanged by it")
	assert.Equal(t, 1, result.PriceJumpCount)
	assert.Equal(t, 6, result.ValidCount)

	run := latestRun(t, db, source.Id)
	require.NotEmpty(t, run.PriceJumpSummary)
	assert.Equal(t, result.RunId, run.Id)

	jumps := priceJumpAlertsOf(t, source.Id)
	require.Len(t, jumps, 1)
	assert.Equal(t, "qwen-3-14b", jumps[0].CanonicalModelName)
	require.NotNil(t, jumps[0].Params)
	assert.Equal(t, "alibaba/qwen-3-14b", jumps[0].Params.SourceModelName)
	assert.Equal(t, upstreamprice.PriceJumpDimensionInput, jumps[0].Params.Dimension)
	require.NotNil(t, jumps[0].Params.ChangeRate)
	assert.InDelta(t, 9.0, *jumps[0].Params.ChangeRate, 1e-9)
	require.NotNil(t, jumps[0].Params.PreviousUSD)
	assert.InDelta(t, 0.12, *jumps[0].Params.PreviousUSD, 1e-9)
	require.NotNil(t, jumps[0].Params.CurrentUSD)
	assert.InDelta(t, 1.2, *jumps[0].Params.CurrentUSD, 1e-9)
	require.NotNil(t, jumps[0].Params.RunId)
	assert.Equal(t, result.RunId, *jumps[0].Params.RunId)
}

// TestPriceJumpBelowThresholdIsQuiet pins the negative half of the contract: a
// real reprice that stays under the threshold changes the fingerprint, commits
// a new snapshot, and raises nothing.
func TestPriceJumpBelowThresholdIsQuiet(t *testing.T) {
	db, source, catalog := setupPriceJumpFixture(t, "")
	syncOnce(t, source.Id)

	// Twenty percent, well under the 50% default.
	catalog.reprice("alibaba/qwen-3-14b", `{"input":"0.000000144","output":"0.00000024"}`)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, preview.ChangedCount, "the fixture must actually reprice the model")
	assert.Equal(t, 0, preview.PriceJumpCount)

	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NewSnapshotCount)
	assert.Empty(t, latestRun(t, db, source.Id).PriceJumpSummary)
	assert.Empty(t, priceJumpAlertsOf(t, source.Id))
}

// TestPriceJumpSourceThresholdOverride pins that the per-source threshold is
// the authority: the same 20% reprice that is quiet by default is reported once
// the source asks for a tighter threshold.
func TestPriceJumpSourceThresholdOverride(t *testing.T) {
	_, source, catalog := setupPriceJumpFixture(t, `{"price_jump_threshold":0.1}`)
	syncOnce(t, source.Id)

	catalog.reprice("alibaba/qwen-3-14b", `{"input":"0.000000144","output":"0.00000024"}`)
	result := syncOnce(t, source.Id)
	assert.Equal(t, 1, result.PriceJumpCount)

	jumps := priceJumpAlertsOf(t, source.Id)
	require.Len(t, jumps, 1)
	require.NotNil(t, jumps[0].Params.JumpThreshold)
	assert.Equal(t, 0.1, *jumps[0].Params.JumpThreshold)
}

// TestPriceJumpTierBoundaryMovedOnGeneratedExpression exercises the case the
// probe derivation exists for, against the expression the Vercel adapter
// actually generates: both tier coefficients are untouched and only the
// long-context boundary moves, so the price doubles for every request between
// the old boundary and the new one while both fixed sample points see nothing.
func TestPriceJumpTierBoundaryMovedOnGeneratedExpression(t *testing.T) {
	_, source, catalog := setupPriceJumpFixture(t, "")
	syncOnce(t, source.Id)

	// claude-sonnet-4 keeps every coefficient of both tiers and moves only its
	// tier edge, from 200001 down to 100001.
	catalog.reprice("anthropic/claude-sonnet-4", `{
		"input":"0.000003",
		"input_tiers":[{"cost":"0.000003","min":0,"max":100001},{"cost":"0.000006","min":100001}],
		"output":"0.000015",
		"output_tiers":[{"cost":"0.000015","min":0,"max":100001},{"cost":"0.0000225","min":100001}],
		"input_cache_read":"0.0000003",
		"input_cache_read_tiers":[{"cost":"0.0000003","min":0,"max":100001},{"cost":"0.0000006","min":100001}],
		"input_cache_write":"0.00000375",
		"input_cache_write_tiers":[{"cost":"0.00000375","min":0,"max":100001},{"cost":"0.0000075","min":100001}]
	}`)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, preview.ChangedCount)
	assert.Positive(t, preview.PriceJumpCount,
		"a tier boundary that moved must be probed at the boundary, not only at fixed sample points")

	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, model.PriceSyncRunStatusPartial, result.Status)

	jumps := priceJumpAlertsOf(t, source.Id)
	require.NotEmpty(t, jumps)
	dimensions := make([]string, 0, len(jumps))
	for _, jump := range jumps {
		assert.Equal(t, "claude-sonnet-4", jump.CanonicalModelName)
		assert.Equal(t, "anthropic/claude-sonnet-4", jump.Params.SourceModelName)
		require.NotNil(t, jump.Params.ChangeRate)
		assert.InDelta(t, 1.0, *jump.Params.ChangeRate, 1e-9,
			"the doubling tiers of this model double the price between the two boundaries")
		dimensions = append(dimensions, jump.Params.Dimension)
	}
	// Output is the sharp edge of the threshold: its long-context tier is 1.5x
	// the standard one, so its movement is exactly 0.5 and does not pass a
	// threshold of "more than 0.5". The dimensions that double do.
	assert.ElementsMatch(t, []string{
		upstreamprice.PriceJumpDimensionInput,
		upstreamprice.PriceJumpDimensionCacheRead,
		upstreamprice.PriceJumpDimensionCacheWrite,
	}, dimensions)
}

// TestPriceJumpNotEvaluatedOnConditionalReplay pins the interaction with the
// §8.1 conditional fetch: a 304 re-affirms the baseline snapshots byte for
// byte, so no fingerprint changed, nothing can have moved, and the replayed run
// records no movement rather than an unevaluated blank.
func TestPriceJumpNotEvaluatedOnConditionalReplay(t *testing.T) {
	db, source, _ := setupConditionalFixture(t, curatedCatalogPayload("acme/one", "acme/two"), `"v1"`)

	first := syncOnce(t, source.Id)
	assert.Empty(t, latestRun(t, db, source.Id).PriceJumpSummary)

	second := syncOnce(t, source.Id)
	require.NotEqual(t, first.RunId, second.RunId)
	assert.Equal(t, 0, second.PriceJumpCount)

	replayed := latestRun(t, db, source.Id)
	assert.Equal(t, second.RunId, replayed.Id)
	assert.Empty(t, replayed.PriceJumpSummary)
	assert.Empty(t, priceJumpAlertsOf(t, source.Id))
}
