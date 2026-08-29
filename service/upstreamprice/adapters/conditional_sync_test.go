package adapters

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// curatedCatalogServer serves a mutable curated catalog with a strong
// validator and honours If-None-Match, recording what every request asked for
// so a test can prove a sync did or did not download the body.
type curatedCatalogServer struct {
	mu           sync.Mutex
	body         string
	etag         string
	conditionals []string
	bodiesServed int
}

func (s *curatedCatalogServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conditional := r.Header.Get("If-None-Match")
	s.conditionals = append(s.conditionals, conditional)
	w.Header().Set("Content-Type", "application/json")
	if s.etag != "" {
		w.Header().Set("ETag", s.etag)
	}
	if conditional != "" && conditional == s.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.bodiesServed++
	_, _ = w.Write([]byte(s.body))
}

// serve replaces the representation the upstream publishes.
func (s *curatedCatalogServer) serve(body, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body, s.etag = body, etag
}

// observe returns the If-None-Match values seen since the last observe call
// and how many times the full body was written, then resets both counters.
func (s *curatedCatalogServer) observe() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conditionals, bodies := s.conditionals, s.bodiesServed
	s.conditionals, s.bodiesServed = nil, 0
	return conditionals, bodies
}

// curatedCatalogPayload builds a models.dev-shaped catalog: every named model
// gets flat input/output pricing, plus one model with no pricing at all, so a
// baseline run carries an unsupported item and is therefore partial.
func curatedCatalogPayload(models ...string) string {
	entries := make([]string, 0, len(models)+1)
	for i, name := range models {
		entries = append(entries, fmt.Sprintf(`%q:{"id":%q,"cost":{"input":%d,"output":%d}}`, name, name, i+1, 2*(i+1)))
	}
	entries = append(entries, `"no-pricing":{"id":"no-pricing","cost":{}}`)
	return `{"acme":{"id":"acme","models":{` + strings.Join(entries, ",") + `}}}`
}

// setupConditionalFixture wires an ETag-serving catalog, a uniquely-keyed
// curated reference adapter pointed at it, and an enabled source.
func setupConditionalFixture(t *testing.T, body, etag string) (*gorm.DB, *model.PriceSource, *curatedCatalogServer) {
	t.Helper()
	db := setupCatalogTestDB(t)
	catalog := &curatedCatalogServer{body: body, etag: etag}
	server := httptest.NewServer(catalog)
	t.Cleanup(server.Close)

	adapterKey := fmt.Sprintf("curated_test_%d", atomic.AddInt64(&testAdapterKeyCounter, 1))
	require.NoError(t, upstreamprice.RegisterAdapter(newCuratedAdapterForTest(adapterKey, server.URL)))
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "curated-reference",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleCuratedReference),
		Scope:      string(upstreamprice.ScopeUnknown),
	})
	require.NoError(t, err)
	return db, source, catalog
}

func syncOnce(t *testing.T, sourceId int) *dto.UpstreamPriceSyncResponse {
	t.Helper()
	preview, err := upstreamprice.PreviewPriceSource(context.Background(), sourceId)
	require.NoError(t, err)
	result, err := upstreamprice.SyncPriceSource(context.Background(), sourceId, preview.PreviewToken)
	require.NoError(t, err)
	return result
}

// catalogJudgment is the current/missing/stale projection with the per-run
// evidence that legitimately advances on every sync cleared, so two runs of
// the same content compare equal exactly when they judge the catalog the same.
func catalogJudgment(t *testing.T, sourceId int) []dto.UpstreamCurrentPriceEntry {
	t.Helper()
	catalog, err := upstreamprice.GetCurrentUpstreamPrices(&sourceId)
	require.NoError(t, err)
	entries := append([]dto.UpstreamCurrentPriceEntry{}, catalog.Entries...)
	for i := range entries {
		entries[i].RunId = 0
		entries[i].RunFinishedAt = nil
		entries[i].LastSeenAt = 0
	}
	return entries
}

func runItemsByModel(t *testing.T, runId int) map[string]model.PriceSyncRunItem {
	t.Helper()
	items, err := model.GetPriceSyncRunItems(runId)
	require.NoError(t, err)
	byModel := make(map[string]model.PriceSyncRunItem, len(items))
	for _, item := range items {
		byModel[item.SourceModelName] = *item
	}
	return byModel
}

// TestConditionalSyncReplaysBaselineOn304 is the issue's headline case: a
// second sync of unchanged content asks conditionally, downloads and parses
// nothing, and still produces a run that judges the catalog exactly as the
// full run did — same snapshots, same statuses, same freshness authority.
func TestConditionalSyncReplaysBaselineOn304(t *testing.T) {
	db, source, catalog := setupConditionalFixture(t, curatedCatalogPayload("m1", "m2", "m3"), `"v1"`)

	first := syncOnce(t, source.Id)
	assert.Equal(t, model.PriceSyncRunStatusPartial, first.Status)
	assert.Equal(t, 3, first.ValidCount)
	assert.Equal(t, 1, first.UnsupportedCount)
	assert.Equal(t, 3, first.NewSnapshotCount)
	baselineJudgment := catalogJudgment(t, source.Id)
	baselineItems := runItemsByModel(t, first.RunId)
	catalog.observe()

	second := syncOnce(t, source.Id)

	// Preview and commit both fetched conditionally, and neither transferred
	// the body.
	conditionals, bodies := catalog.observe()
	assert.Equal(t, []string{`"v1"`, `"v1"`}, conditionals)
	assert.Zero(t, bodies, "a 304 sync must not download the catalog")

	// The replayed run repeats the baseline manifest and persists no new
	// snapshot rows.
	assert.Equal(t, model.PriceSyncRunStatusPartial, second.Status)
	assert.Equal(t, first.DiscoveredCount, second.DiscoveredCount)
	assert.Equal(t, first.ValidCount, second.ValidCount)
	assert.Equal(t, first.UnsupportedCount, second.UnsupportedCount)
	assert.Equal(t, first.RejectedCount, second.RejectedCount)
	assert.Equal(t, first.MissingCount, second.MissingCount)
	assert.Equal(t, 0, second.NewSnapshotCount)
	assert.Equal(t, 3, second.IdempotentHitCount)
	assert.EqualValues(t, 3, countRows(t, db, &model.PriceSnapshot{}))

	// 304 is a success path, not a failure: the run records the conditional
	// answer and advances the freshness authority.
	replayed, err := model.GetPriceSyncRunById(second.RunId)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotModified, replayed.HttpStatus)
	assert.EqualValues(t, 0, replayed.ResponseBytes)
	assert.Equal(t, `"v1"`, replayed.SourceRevision)
	assert.Empty(t, replayed.ErrorSummary)
	require.NotNil(t, replayed.CoverageDropExceeded)
	assert.False(t, *replayed.CoverageDropExceeded)

	reloaded, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, second.RunId, *reloaded.LastSuccessRunId)

	// Run items point at the very same snapshot rows.
	replayedItems := runItemsByModel(t, second.RunId)
	require.Len(t, replayedItems, len(baselineItems))
	for name, baselineItem := range baselineItems {
		item, ok := replayedItems[name]
		require.True(t, ok, name)
		assert.Equal(t, baselineItem.Status, item.Status, name)
		assert.Equal(t, baselineItem.WarningCode, item.WarningCode, name)
		assert.Equal(t, baselineItem.SnapshotId, item.SnapshotId, name)
	}

	// current / missing / unsupported judgment is identical to the full path.
	assert.Equal(t, baselineJudgment, catalogJudgment(t, source.Id))
}

// TestConditionalSyncPreservesPartialBaseline: the replayed run status is
// derived from the copied manifest, so a baseline carrying missing models
// replays as partial with those models still labeled missing — it is never
// promoted to succeeded (spec §8.2 / §8.4).
func TestConditionalSyncPreservesPartialBaseline(t *testing.T) {
	_, source, catalog := setupConditionalFixture(t, curatedCatalogPayload("m1", "m2", "m3", "m4", "m5", "m6"), `"v1"`)

	require.Equal(t, model.PriceSyncRunStatusPartial, syncOnce(t, source.Id).Status)

	// The upstream drops one model: 5 of 6 valid stays inside the 20% gate, so
	// the run commits as partial with one missing item.
	catalog.serve(curatedCatalogPayload("m1", "m2", "m3", "m4", "m5"), `"v2"`)
	partial := syncOnce(t, source.Id)
	require.Equal(t, model.PriceSyncRunStatusPartial, partial.Status)
	require.Equal(t, 1, partial.MissingCount)
	baselineJudgment := catalogJudgment(t, source.Id)
	catalog.observe()

	replayed := syncOnce(t, source.Id)
	_, bodies := catalog.observe()
	assert.Zero(t, bodies)

	assert.Equal(t, model.PriceSyncRunStatusPartial, replayed.Status,
		"a partial baseline must not be replayed as succeeded")
	assert.Equal(t, 5, replayed.ValidCount)
	assert.Equal(t, 1, replayed.UnsupportedCount)
	assert.Equal(t, 1, replayed.MissingCount)
	assert.Equal(t, baselineJudgment, catalogJudgment(t, source.Id))

	missing := runItemsByModel(t, replayed.RunId)["acme/m6"]
	assert.Equal(t, model.PriceSyncItemStatusMissing, missing.Status)
	assert.Nil(t, missing.SnapshotId)
}

// TestConditionalSyncForcedFullFetchOnConfigChange: replaying a baseline
// re-affirms prices normalized under the configuration that produced them, so
// a source whose configuration moved must download and re-normalize instead —
// otherwise the stale mapping would be re-confirmed under the new digest.
func TestConditionalSyncForcedFullFetchOnConfigChange(t *testing.T) {
	_, source, catalog := setupConditionalFixture(t, curatedCatalogPayload("m1", "m2"), `"v1"`)

	syncOnce(t, source.Id)
	before := catalogJudgment(t, source.Id)
	byModel := map[string]dto.UpstreamCurrentPriceEntry{}
	for _, entry := range before {
		byModel[entry.SourceModelName] = entry
	}
	require.Equal(t, "m1", byModel["acme/m1"].CanonicalModelName)
	catalog.observe()

	reloaded, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	settings := `{"model_mappings":{"acme/m1":"renamed-m1"}}`
	_, err = upstreamprice.UpdatePriceSource(source.Id, &dto.UpstreamPriceSourceRequest{
		Name:       reloaded.Name,
		AdapterKey: reloaded.AdapterKey,
		Role:       reloaded.Role,
		Scope:      reloaded.Scope,
		Settings:   &settings,
	})
	require.NoError(t, err)

	syncOnce(t, source.Id)

	conditionals, bodies := catalog.observe()
	for _, conditional := range conditionals {
		assert.Empty(t, conditional, "a moved configuration must not send If-None-Match")
	}
	assert.Equal(t, len(conditionals), bodies, "every fetch must transfer the body")

	after := map[string]dto.UpstreamCurrentPriceEntry{}
	for _, entry := range catalogJudgment(t, source.Id) {
		after[entry.SourceModelName] = entry
	}
	assert.Equal(t, "renamed-m1", after["acme/m1"].CanonicalModelName,
		"the new mapping must be applied, which only a full fetch can do")
}

// TestConditionalSyncForcedFullFetchOnFingerprintVersionMismatch: a baseline
// snapshot written under an older canonical payload version is not what a full
// fetch would produce today, so replaying it would keep an outdated
// fingerprint current. The gate falls back to the full path.
func TestConditionalSyncForcedFullFetchOnFingerprintVersionMismatch(t *testing.T) {
	db, source, catalog := setupConditionalFixture(t, curatedCatalogPayload("m1", "m2"), `"v1"`)

	syncOnce(t, source.Id)
	catalog.observe()

	require.NoError(t, db.Model(&model.PriceSnapshot{}).
		Where("source_id = ?", source.Id).
		Update("fingerprint_version", "fp0").Error)

	result := syncOnce(t, source.Id)

	conditionals, bodies := catalog.observe()
	for _, conditional := range conditionals {
		assert.Empty(t, conditional, "a stale fingerprint version must not send If-None-Match")
	}
	assert.Equal(t, len(conditionals), bodies)

	full, err := model.GetPriceSyncRunById(result.RunId)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, full.HttpStatus)
	assert.Positive(t, full.ResponseBytes)
}

// TestConditionalSyncWithoutUpstreamETagStaysOnFullPath is the issue's
// falsifiable condition: an upstream that publishes no validator leaves the
// baseline revision empty, no conditional request is made, and the sync
// behaves exactly as it did before conditional fetching existed.
func TestConditionalSyncWithoutUpstreamETagStaysOnFullPath(t *testing.T) {
	db, source, catalog := setupConditionalFixture(t, curatedCatalogPayload("m1", "m2"), "")

	first := syncOnce(t, source.Id)
	baselineJudgment := catalogJudgment(t, source.Id)
	baselineRun, err := model.GetPriceSyncRunById(first.RunId)
	require.NoError(t, err)
	require.Empty(t, baselineRun.SourceRevision)
	catalog.observe()

	second := syncOnce(t, source.Id)

	conditionals, bodies := catalog.observe()
	for _, conditional := range conditionals {
		assert.Empty(t, conditional)
	}
	assert.Equal(t, len(conditionals), bodies)

	assert.Equal(t, first.Status, second.Status)
	assert.Equal(t, first.ValidCount, second.ValidCount)
	assert.Equal(t, 0, second.NewSnapshotCount)
	assert.Equal(t, 2, second.IdempotentHitCount)
	assert.EqualValues(t, 2, countRows(t, db, &model.PriceSnapshot{}))
	assert.Equal(t, baselineJudgment, catalogJudgment(t, source.Id))

	replayed, err := model.GetPriceSyncRunById(second.RunId)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, replayed.HttpStatus)
	assert.Positive(t, replayed.ResponseBytes)
}

// TestConditionalSyncPreviewCommitDivergenceRequiresRePreview: preview and
// commit can see different representations in either direction — here preview
// downloads changed content and the upstream reverts before commit, so commit
// gets a 304. Divergence is resolved by the existing preview digest check,
// which refuses the commit and asks for a new preview; it is never assumed
// impossible.
func TestConditionalSyncPreviewCommitDivergenceRequiresRePreview(t *testing.T) {
	_, source, catalog := setupConditionalFixture(t, curatedCatalogPayload("m1", "m2"), `"v1"`)

	syncOnce(t, source.Id)

	// The upstream publishes changed content, so preview downloads it.
	catalog.serve(curatedCatalogPayload("m1", "m2", "m3"), `"v2"`)
	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	require.Equal(t, 1, preview.NewCount)

	// Before commit the upstream reverts to the baseline representation, so
	// the commit fetch answers 304 and plans a replay instead.
	catalog.serve(curatedCatalogPayload("m1", "m2"), `"v1"`)
	_, err = upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-preview required")

	// Nothing was written, and a fresh preview agrees with the reverted state.
	fresh, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, fresh.NewCount)
	assert.Equal(t, 2, fresh.UnchangedCount)
	result, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, fresh.PreviewToken)
	require.NoError(t, err)
	assert.Equal(t, 0, result.NewSnapshotCount)
}
