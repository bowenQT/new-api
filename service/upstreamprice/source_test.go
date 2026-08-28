package upstreamprice

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListPriceSourcesReportsLastSuccessAggregates pins the source list
// aggregates the admin UI shows without a per-source catalog query (spec
// §8.3): coverage and missing counts plus the completion time of the last
// successful run, and the staleness label derived from it.
func TestListPriceSourcesReportsLastSuccessAggregates(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	staleAt := now - DefaultManualStaleThresholdSeconds - 3600

	fresh := seedCatalogSnapshot(t, db, "fresh-source", RoleCuratedReference, nil, "fresh-model",
		`tier("base", p * 1 + c * 2)`, FormulaKindTokenExprV1, "", now)
	require.NoError(t, db.Model(&model.PriceSyncRun{}).Where("id = ?", *fresh.LastSuccessRunId).
		Updates(map[string]interface{}{"valid_count": 3, "missing_count": 2}).Error)

	stale := seedCatalogSnapshot(t, db, "stale-source", RoleCuratedReference, nil, "stale-model",
		`tier("base", p * 1 + c * 2)`, FormulaKindTokenExprV1, "", staleAt)

	neverSynced := &model.PriceSource{
		Name:       "never-synced-source",
		AdapterKey: "test_adapter",
		Role:       string(RoleCuratedReference),
		Scope:      string(ScopeUnknown),
		Enabled:    true,
	}
	require.NoError(t, model.InsertPriceSource(neverSynced))

	views, err := ListPriceSources()
	require.NoError(t, err)
	byName := map[string]int{}
	for index, view := range views {
		byName[view.Name] = index
	}
	require.Len(t, views, 3)

	freshView := views[byName["fresh-source"]]
	require.NotNil(t, freshView.LastSuccessFinishedAt)
	assert.Equal(t, now, *freshView.LastSuccessFinishedAt)
	assert.Equal(t, 3, freshView.CoverageCount)
	assert.Equal(t, 2, freshView.MissingCount)
	assert.False(t, freshView.Stale)

	staleView := views[byName["stale-source"]]
	require.NotNil(t, staleView.LastSuccessFinishedAt)
	assert.Equal(t, staleAt, *staleView.LastSuccessFinishedAt)
	assert.Equal(t, 1, staleView.CoverageCount)
	assert.True(t, staleView.Stale)
	assert.Equal(t, stale.Id, staleView.Id)

	neverSyncedView := views[byName["never-synced-source"]]
	assert.Nil(t, neverSyncedView.LastSuccessFinishedAt)
	assert.Equal(t, 0, neverSyncedView.CoverageCount)
	assert.Equal(t, 0, neverSyncedView.MissingCount)
	assert.False(t, neverSyncedView.Stale)
}
