package adapters

import (
	"bytes"
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureCatalogLogs redirects the warning writer the catalog alert log uses so
// a test can assert on what a sync actually logged.
func captureCatalogLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	common.LogWriterMu.Lock()
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = buffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previous
		common.LogWriterMu.Unlock()
	})
	return buffer
}

// TestManualSyncLogsCatalogAlerts pins the §13 logging position: alerts are
// written from the path that changes catalog state, and the manual
// preview/commit flow is that path whenever the scheduled task is off — which
// by default it is. A manual commit the coverage gate refused is exactly the
// case that used to log nothing at all.
func TestManualSyncLogsCatalogAlerts(t *testing.T) {
	payload := pricedCatalogPayload(5)
	_, source := setupScheduledFixture(t, &payload)

	preview, err := upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	baseline, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	require.Equal(t, model.PriceSyncRunStatusSucceeded, baseline.Status)

	payload = pricedCatalogPayload(1)
	logs := captureCatalogLogs(t)
	preview, err = upstreamprice.PreviewPriceSource(context.Background(), source.Id)
	require.NoError(t, err)
	require.True(t, preview.CoverageDropExceeded)
	refused, err := upstreamprice.SyncPriceSource(context.Background(), source.Id, preview.PreviewToken)
	require.NoError(t, err)
	require.Equal(t, model.PriceSyncRunStatusFailed, refused.Status)

	assert.Contains(t, logs.String(), "upstream price catalog alert: code="+upstreamprice.AlertCoverageDrop,
		"a manually committed run the gate refused must log its alert")
}

// TestScheduledPrePlanFailuresRaiseConsecutiveFailureAlert covers the failure
// shape that never reaches an adapter: a disabled channel refuses the sync
// before a plan exists. Such attempts used to update only last_error_at, so no
// matter how long a source stayed broken this way it never reached the
// three-failure alert (spec §13).
func TestScheduledPrePlanFailuresRaiseConsecutiveFailureAlert(t *testing.T) {
	payload := pricedCatalogPayload(3)
	db, source := setupScheduledFixture(t, &payload)
	require.NotNil(t, source.ChannelId)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", *source.ChannelId).
		Update("status", common.ChannelStatusManuallyDisabled).Error)

	for attempt := 0; attempt < upstreamprice.ConsecutiveFailureAlertThreshold; attempt++ {
		summary, err := upstreamprice.RunScheduledSync(context.Background(), nil)
		require.Error(t, err)
		require.Equal(t, 1, summary.Failed)
		// Re-arm the source so the next wake selects it again.
		require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).
			Update("last_error_at", common.GetTimestamp()-upstreamprice.MinScheduleIntervalSeconds).Error)
	}

	failing, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	alerts, err := upstreamprice.EvaluateSourceAlerts([]*model.PriceSource{failing}, common.GetTimestamp())
	require.NoError(t, err)

	var consecutive *string
	for i := range alerts {
		if alerts[i].Code != upstreamprice.AlertSourceConsecutiveFailures {
			continue
		}
		require.NotNil(t, alerts[i].Params)
		require.NotNil(t, alerts[i].Params.FailureCount)
		assert.Equal(t, upstreamprice.ConsecutiveFailureAlertThreshold, *alerts[i].Params.FailureCount)
		consecutive = &alerts[i].Detail
	}
	require.NotNil(t, consecutive, "three pre-plan failures must raise the consecutive-failure alert")
}
