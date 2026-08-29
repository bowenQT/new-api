package adapters

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		name             string
		baseline         string
		refused          string
		wantCoverageGate bool
	}{
		{
			name:    "zero valid observations",
			refused: `{"object":"list","data":[{"id":"vendor/unpriced","owned_by":"vendor","pricing":{}}]}`,
		},
		{
			name:             "coverage drop gate",
			baseline:         pricedCatalogPayload(5),
			refused:          pricedCatalogPayload(1),
			wantCoverageGate: true,
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

			// The refusal reason is recorded explicitly, so alerting can tell a
			// coverage collapse apart from any other failed run without reading
			// the error summary text.
			require.NotNil(t, runs[0].CoverageDropExceeded)
			assert.Equal(t, testCase.wantCoverageGate, *runs[0].CoverageDropExceeded)
		})
	}
}

// TestScheduledSyncFailsOnOrphanCheckError separates the two outcomes of the
// orphan preflight: a channel confirmed gone is a skip, but a database error is
// not an answer at all. Counting it as a skip finished the whole pass
// successfully and left no backoff timestamp, so the broken source was retried
// on every wake and the failure never surfaced.
func TestScheduledSyncFailsOnOrphanCheckError(t *testing.T) {
	payload := pricedCatalogPayload(3)
	db, source := setupScheduledFixture(t, &payload)
	require.NoError(t, db.Migrator().DropTable(&model.Channel{}))

	summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, 1, summary.Due)
	assert.Equal(t, 1, summary.Failed)
	assert.Equal(t, 0, summary.Skipped)
	assert.Equal(t, 0, summary.Executed)

	failed, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, failed.LastErrorAt)
	assert.Contains(t, failed.LastErrorSummary, "orphan check failed")

	runs, err := model.GetRecentPriceSyncRuns(source.Id, 1)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, model.PriceSyncRunStatusFailed, runs[0].Status)

	// Backed off like any other failed attempt.
	second, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Due)
}

// TestScheduledSyncDoesNotBackOffOnConfigConflict pins the one scheduled
// failure that must leave no trace on the source: an admin edited the source
// while the fetch was in flight, so the CAS refused a commit computed under the
// superseded configuration. Backing off here would delay the new
// configuration's first real sync by a full interval, and recording a failure
// would blame it for a conflict it did not cause.
func TestScheduledSyncDoesNotBackOffOnConfigConflict(t *testing.T) {
	db := setupCatalogTestDB(t)
	payload := pricedCatalogPayload(3)
	editedSourceId := int64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The admin's edit lands while this fetch is still running.
		if id := atomic.LoadInt64(&editedSourceId); id > 0 {
			require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", id).
				Update("config_revision", gorm.Expr("config_revision + 1")).Error)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)

	adapterKey := nextTestAdapterKey()
	require.NoError(t, upstreamprice.RegisterAdapter(newVercelAdapterForTest(adapterKey, server.URL)))
	channel := createTestChannel(t, db)
	source, err := upstreamprice.CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:       "conflicting-cost",
		AdapterKey: adapterKey,
		Role:       string(upstreamprice.RoleSupplierCost),
		Scope:      string(upstreamprice.ScopePublic),
		ChannelId:  &channel.Id,
	})
	require.NoError(t, err)
	enableScheduleForSource(t, db, source.Id)
	atomic.StoreInt64(&editedSourceId, int64(source.Id))

	summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, 1, summary.Executed)
	assert.Equal(t, 1, summary.Failed)

	conflicted, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	assert.Nil(t, conflicted.LastErrorAt, "a superseded configuration must not back off the new one")
	assert.Empty(t, conflicted.LastErrorSummary)
	assert.Nil(t, conflicted.LastSuccessRunId)

	runs, err := model.GetRecentPriceSyncRuns(source.Id, 1)
	require.NoError(t, err)
	assert.Empty(t, runs, "a CAS conflict must not count toward consecutive failures")

	// The source stays due, so the next wake retries under the new configuration.
	atomic.StoreInt64(&editedSourceId, 0)
	second, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, second.Due)
	assert.Equal(t, 1, second.Succeeded)
}

// TestScheduledSyncBacksOffAfterPreflightFailure is the §8.4 backoff contract:
// a failure that never reaches the adapter — here a disabled supplier channel —
// still stamps last_error_at, so the source waits a full interval instead of
// retrying on every 15-minute wake, and records a run so the attempt is visible
// to the consecutive-failure alert.
func TestScheduledSyncBacksOffAfterPreflightFailure(t *testing.T) {
	payload := pricedCatalogPayload(3)
	db, source := setupScheduledFixture(t, &payload)
	require.NotNil(t, source.ChannelId)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", *source.ChannelId).
		Update("status", common.ChannelStatusManuallyDisabled).Error)

	summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, 1, summary.Due)
	assert.Equal(t, 1, summary.Executed)
	assert.Equal(t, 1, summary.Failed)

	failed, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, failed.LastErrorAt)
	assert.Contains(t, failed.LastErrorSummary, "channel is disabled")
	assert.Nil(t, failed.LastSuccessRunId)

	// The attempt is recorded as a failed run carrying no items, so it both
	// backs the source off and counts toward the consecutive-failure alert.
	runs, err := model.GetRecentPriceSyncRuns(source.Id, 1)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, model.PriceSyncRunStatusFailed, runs[0].Status)
	assert.Contains(t, runs[0].ErrorSummary, "channel is disabled")
	items, err := model.GetPriceSyncRunItems(runs[0].Id)
	require.NoError(t, err)
	assert.Empty(t, items)

	second, err := upstreamprice.RunScheduledSync(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Due)
	assert.Equal(t, 0, second.Executed)
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
