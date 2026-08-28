package upstreamprice

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createAlertTestSource(t *testing.T, role PriceRole, channelId *int, settings string) *model.PriceSource {
	t.Helper()
	source := &model.PriceSource{
		Name:       "alert-source",
		AdapterKey: "test_adapter",
		Role:       string(role),
		Scope:      string(ScopePublic),
		ChannelId:  channelId,
		Enabled:    true,
		Settings:   settings,
	}
	require.NoError(t, model.InsertPriceSource(source))
	return source
}

func createAlertTestRun(t *testing.T, db *gorm.DB, sourceId int, status string, validCount int, finishedAt int64) *model.PriceSyncRun {
	t.Helper()
	source, err := model.GetPriceSourceById(sourceId)
	require.NoError(t, err)
	config, err := SourceConfigFromModel(source)
	require.NoError(t, err)
	run := &model.PriceSyncRun{
		SourceId:           sourceId,
		Status:             status,
		AdapterKey:         "test_adapter",
		StartedAt:          finishedAt,
		FinishedAt:         &finishedAt,
		ValidCount:         validCount,
		SourceConfigDigest: sourceConfigDigest(config),
	}
	require.NoError(t, db.Create(run).Error)
	return run
}

func alertCodes(t *testing.T, source *model.PriceSource, now int64) []string {
	t.Helper()
	alerts, err := EvaluateSourceAlerts([]*model.PriceSource{source}, now)
	require.NoError(t, err)
	codes := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		assert.Equal(t, source.Id, alert.SourceId)
		assert.NotEmpty(t, alert.Detail)
		codes = append(codes, alert.Code)
	}
	return codes
}

// TestEvaluateSourceAlertsConsecutiveFailures pins the spec §13 threshold: an
// alert fires only once three trailing runs failed, and a newer successful run
// clears it.
func TestEvaluateSourceAlertsConsecutiveFailures(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	source := createAlertTestSource(t, RoleCuratedReference, nil, "")

	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusFailed, 0, now)
	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusFailed, 0, now)
	assert.NotContains(t, alertCodes(t, source, now), AlertSourceConsecutiveFailures)

	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusFailed, 0, now)
	assert.Contains(t, alertCodes(t, source, now), AlertSourceConsecutiveFailures)

	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusPartial, 5, now)
	assert.NotContains(t, alertCodes(t, source, now), AlertSourceConsecutiveFailures)
}

func TestEvaluateSourceAlertsStaleCostSource(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	channelId := 1
	source := createAlertTestSource(t, RoleSupplierCost, &channelId, `{"stale_threshold_seconds":3600}`)

	fresh := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 10, now-60)
	source.LastSuccessRunId = &fresh.Id
	assert.NotContains(t, alertCodes(t, source, now), AlertSourceStale)

	stale := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 10, now-7200)
	// The newest successful run is the stale one; run id is the ordering
	// authority, so the alert must follow it rather than the older fresh run.
	source.LastSuccessRunId = &stale.Id
	assert.Contains(t, alertCodes(t, source, now), AlertSourceStale)
}

// TestEvaluateSourceAlertsConfigChanged pins the fail-closed boundary of §9.2:
// a price-semantic configuration change (here, repointing the source at a
// different channel) invalidates the last successful run's observations, while
// toggling the scheduling switches does not.
func TestEvaluateSourceAlertsConfigChanged(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	channelId := 1
	source := createAlertTestSource(t, RoleSupplierCost, &channelId, "")
	run := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 10, now)
	source.LastSuccessRunId = &run.Id
	assert.NotContains(t, alertCodes(t, source, now), AlertSourceConfigChanged)

	source.ScheduleEnabled = true
	source.ScheduleIntervalSeconds = MinScheduleIntervalSeconds
	assert.NotContains(t, alertCodes(t, source, now), AlertSourceConfigChanged)

	otherChannel := channelId + 1
	source.ChannelId = &otherChannel
	assert.Contains(t, alertCodes(t, source, now), AlertSourceConfigChanged)
}

func TestEvaluateSourceAlertsCoverageDrop(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	source := createAlertTestSource(t, RoleCuratedReference, nil, "")

	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 100, now)
	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 90, now)
	assert.NotContains(t, alertCodes(t, source, now), AlertCoverageDrop)

	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusPartial, 50, now)
	assert.Contains(t, alertCodes(t, source, now), AlertCoverageDrop)
}
