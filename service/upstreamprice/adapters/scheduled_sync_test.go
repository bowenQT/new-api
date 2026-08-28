package adapters

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func enableScheduleForSource(t *testing.T, db *gorm.DB, sourceId int) {
	t.Helper()
	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", sourceId).Updates(map[string]interface{}{
		"schedule_enabled":          true,
		"schedule_interval_seconds": upstreamprice.MinScheduleIntervalSeconds,
	}).Error)
}

// pricedCatalogPayload builds a catalog response with count fully priced
// models, so the coverage gate has a baseline to compare against.
func pricedCatalogPayload(count int) string {
	models := make([]string, 0, count)
	for i := 0; i < count; i++ {
		models = append(models, fmt.Sprintf(
			`{"id":"vendor/model-%d","owned_by":"vendor","pricing":{"input":"0.000001","output":"0.000002"}}`, i))
	}
	return `{"object":"list","data":[` + strings.Join(models, ",") + `]}`
}

// setupScheduledFixture wires a scheduled supplier_cost source whose upstream
// payload the test can swap between passes.
func setupScheduledFixture(t *testing.T, payload *string) (*gorm.DB, *model.PriceSource) {
	t.Helper()
	db := setupCatalogTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(*payload))
	}))
	t.Cleanup(server.Close)
	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "scheduled-cost",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)
	enableScheduleForSource(t, db, source.Id)
	return db, source
}

// TestScheduledSyncCommitsWithoutPreviewToken is the §8.4 end-to-end contract:
// the unattended path commits a due source through the same gates as a manual
// sync, records a run, and is no longer due immediately afterwards.
func TestScheduledSyncCommitsWithoutPreviewToken(t *testing.T) {
	db, source, _ := setupSyncFixture(t)
	enableScheduleForSource(t, db, source.Id)

	before := exportBillingConfig(t)

	summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Due)
	assert.Equal(t, 1, summary.Executed)
	assert.Equal(t, 1, summary.Succeeded)
	assert.Equal(t, 0, summary.Failed)
	assert.Equal(t, 0, summary.Skipped)

	synced, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, synced.LastSuccessRunId)
	run, err := model.GetPriceSyncRunById(*synced.LastSuccessRunId)
	require.NoError(t, err)
	assert.Greater(t, run.ValidCount, 0)

	// The scheduled path has no sale-pricing write capability at all.
	assert.Equal(t, before, exportBillingConfig(t))

	// Its own interval must gate the next execution.
	second, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Due)
	assert.Equal(t, 0, second.Executed)
}

// TestScheduledSyncReportsRefusedRunAsFailure is the §8.4 outcome contract: a
// run the gates refused commits nothing and returns no error, so the pass must
// still count it as a failure and report the whole pass as failed. Both refusal
// reasons are covered: zero valid observations and the coverage drop gate.
func TestScheduledSyncReportsRefusedRunAsFailure(t *testing.T) {
	cases := []struct {
		name     string
		baseline string
		refused  string
	}{
		{
			name:    "zero valid observations",
			refused: `{"object":"list","data":[{"id":"vendor/unpriced","owned_by":"vendor","pricing":{}}]}`,
		},
		{
			name:     "coverage drop gate",
			baseline: pricedCatalogPayload(5),
			refused:  pricedCatalogPayload(1),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := testCase.refused
			if testCase.baseline != "" {
				payload = testCase.baseline
			}
			db, source := setupScheduledFixture(t, &payload)

			if testCase.baseline != "" {
				baseline, err := upstreamprice.RunScheduledSync(context.Background(), nil)
				require.NoError(t, err)
				assert.Equal(t, 1, baseline.Succeeded)
				// Re-arm the source so the second pass selects it again.
				require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).
					Update("last_success_at", common.GetTimestamp()-upstreamprice.MinScheduleIntervalSeconds).Error)
				payload = testCase.refused
			}

			summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
			require.Error(t, err)
			assert.Equal(t, 1, summary.Due)
			assert.Equal(t, 1, summary.Executed)
			assert.Equal(t, 0, summary.Succeeded)
			assert.Equal(t, 1, summary.Failed)
			assert.False(t, summary.TimedOut)

			refused, err := model.GetPriceSourceById(source.Id)
			require.NoError(t, err)
			require.NotNil(t, refused.LastErrorAt)
			assert.NotEmpty(t, refused.LastErrorSummary)

			// The refused run exists and never became the baseline.
			runs, err := model.GetRecentPriceSyncRuns(source.Id, 1)
			require.NoError(t, err)
			require.Len(t, runs, 1)
			assert.Equal(t, model.PriceSyncRunStatusFailed, runs[0].Status)
			assert.NotEqual(t, &runs[0].Id, refused.LastSuccessRunId)
		})
	}
}

// TestScheduledSyncRefusesOrphanedSource pins spec §7.1: an orphaned source may
// still preview for diagnostics, but the scheduled path refuses to execute it.
func TestScheduledSyncRefusesOrphanedSource(t *testing.T) {
	db, source, _ := setupSyncFixture(t)
	enableScheduleForSource(t, db, source.Id)
	require.NotNil(t, source.ChannelId)
	require.NoError(t, db.Delete(&model.Channel{}, "id = ?", *source.ChannelId).Error)

	// A skipped orphan is not a failed pass.
	summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Due)
	assert.Equal(t, 0, summary.Executed)
	assert.Equal(t, 1, summary.Skipped)

	unchanged, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	assert.Nil(t, unchanged.LastSuccessRunId)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	assert.Greater(t, preview.ValidCount, 0)
}

// TestScheduledSyncSkipsDisabledSchedule proves the default-off posture: a
// source that exists but is not scheduled is never selected.
func TestScheduledSyncSkipsDisabledSchedule(t *testing.T) {
	_, source, _ := setupSyncFixture(t)
	require.False(t, source.ScheduleEnabled)

	summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Due)
	assert.Equal(t, 0, summary.Executed)

	unchanged, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	assert.Nil(t, unchanged.LastSuccessRunId)
}
