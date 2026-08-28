package adapters

import (
	"context"
	"testing"

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

// TestScheduledSyncCommitsWithoutPreviewToken is the §8.4 end-to-end contract:
// the unattended path commits a due source through the same gates as a manual
// sync, records a run, and is no longer due immediately afterwards.
func TestScheduledSyncCommitsWithoutPreviewToken(t *testing.T) {
	db, source, _ := setupSyncFixture(t)
	enableScheduleForSource(t, db, source.Id)

	before := exportBillingConfig(t)

	summary := upstreamprice.RunScheduledSync(context.Background(), nil)
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
	second := upstreamprice.RunScheduledSync(context.Background(), nil)
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

	summary := upstreamprice.RunScheduledSync(context.Background(), nil)
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

	summary := upstreamprice.RunScheduledSync(context.Background(), nil)
	assert.Equal(t, 0, summary.Due)
	assert.Equal(t, 0, summary.Executed)

	unchanged, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	assert.Nil(t, unchanged.LastSuccessRunId)
}
