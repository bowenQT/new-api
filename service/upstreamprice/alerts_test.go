package upstreamprice

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
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

// TestEvaluateSourceAlertsCoverageDropOnRefusedRun covers the case the
// two-successful-runs comparison structurally cannot see: the gate refused the
// collapsed run, so it stayed failed and never became the baseline, leaving the
// last two successful runs looking healthy. That is the run the admin most
// needs told about, and only an explicit marker on the run distinguishes it
// from a fetch failure.
func TestEvaluateSourceAlertsCoverageDropOnRefusedRun(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	source := createAlertTestSource(t, RoleCuratedReference, nil, "")
	baseline := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 100, now)
	source.LastSuccessRunId = &baseline.Id

	// A fetch failure is a failed run too, but it is not a coverage refusal and
	// must not be reported as one.
	createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusFailed, 0, now)
	assert.NotContains(t, alertCodes(t, source, now), AlertCoverageDrop)

	refused := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusFailed, 50, now)
	gateRefused := true
	require.NoError(t, db.Model(&model.PriceSyncRun{}).Where("id = ?", refused.Id).
		Update("coverage_drop_exceeded", &gateRefused).Error)

	alerts, err := EvaluateSourceAlerts([]*model.PriceSource{source}, now)
	require.NoError(t, err)
	var coverage *dto.UpstreamPriceAlert
	for i := range alerts {
		if alerts[i].Code == AlertCoverageDrop {
			coverage = &alerts[i]
		}
	}
	require.NotNil(t, coverage, "a gate-refused coverage collapse must raise an alert")
	require.NotNil(t, coverage.Params)
	require.NotNil(t, coverage.Params.RunId)
	assert.Equal(t, refused.Id, *coverage.Params.RunId)
	require.NotNil(t, coverage.Params.PreviousValidCount)
	assert.Equal(t, 100, *coverage.Params.PreviousValidCount)
	require.NotNil(t, coverage.Params.ValidCount)
	assert.Equal(t, 50, *coverage.Params.ValidCount)
	require.NotNil(t, coverage.Params.DropThreshold)
	assert.Equal(t, DefaultCoverageDropThreshold, *coverage.Params.DropThreshold)
	require.NotNil(t, coverage.Params.GateRefused)
	assert.True(t, *coverage.Params.GateRefused)
}

// TestEvaluateSourceAlertsPriceJump pins the derivation contract of the §13
// price movement alert: it reads the evidence the last successful run stored
// and reports every field the admin needs to judge the movement, including both
// names of the model.
func TestEvaluateSourceAlertsPriceJump(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	source := createAlertTestSource(t, RoleCuratedReference, nil, "")

	// A successful run that recorded no movement raises nothing, and neither
	// does a run written before the column existed — both read back empty.
	quiet := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 10, now)
	source.LastSuccessRunId = &quiet.Id
	assert.NotContains(t, alertCodes(t, source, now), AlertPriceJump)

	jumped := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 10, now)
	previous, current := 0.2, 2.0
	rate := 9.0
	summary := encodePriceJumpSummary(DefaultPriceJumpThreshold, []priceJumpEntry{{
		SourceModelName:    "openai/gpt-5.6-luna",
		CanonicalModelName: "gpt-5.6-luna",
		Dimension:          PriceJumpDimensionInput,
		ProbeContext:       "p=1000000,len=1000",
		PreviousUSD:        &previous,
		CurrentUSD:         &current,
		ChangeRate:         &rate,
	}})
	require.NotEmpty(t, summary)
	require.NoError(t, db.Model(&model.PriceSyncRun{}).Where("id = ?", jumped.Id).
		Update("price_jump_summary", summary).Error)
	source.LastSuccessRunId = &jumped.Id

	alerts, err := EvaluateSourceAlerts([]*model.PriceSource{source}, now)
	require.NoError(t, err)
	var jump *dto.UpstreamPriceAlert
	for i := range alerts {
		if alerts[i].Code == AlertPriceJump {
			jump = &alerts[i]
		}
	}
	require.NotNil(t, jump, "a recorded price movement must raise an alert")
	assert.Equal(t, "gpt-5.6-luna", jump.CanonicalModelName)
	require.NotNil(t, jump.Params)
	assert.Equal(t, "openai/gpt-5.6-luna", jump.Params.SourceModelName)
	assert.Equal(t, PriceJumpDimensionInput, jump.Params.Dimension)
	assert.Equal(t, "p=1000000,len=1000", jump.Params.ProbeContext)
	require.NotNil(t, jump.Params.RunId)
	assert.Equal(t, jumped.Id, *jump.Params.RunId)
	require.NotNil(t, jump.Params.PreviousUSD)
	assert.Equal(t, 0.2, *jump.Params.PreviousUSD)
	require.NotNil(t, jump.Params.CurrentUSD)
	assert.Equal(t, 2.0, *jump.Params.CurrentUSD)
	require.NotNil(t, jump.Params.ChangeRate)
	assert.Equal(t, 9.0, *jump.Params.ChangeRate)
	require.NotNil(t, jump.Params.JumpThreshold)
	assert.Equal(t, DefaultPriceJumpThreshold, *jump.Params.JumpThreshold)
	require.NotNil(t, jump.Params.JumpCount)
	assert.Equal(t, 1, *jump.Params.JumpCount)
	require.NotNil(t, jump.Params.ReportedCount)
	assert.Equal(t, 1, *jump.Params.ReportedCount)
	assert.Nil(t, jump.Params.FromZero)

	// The alert follows the newest successful run: a later quiet sync clears it.
	cleared := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 10, now)
	source.LastSuccessRunId = &cleared.Id
	assert.NotContains(t, alertCodes(t, source, now), AlertPriceJump)
}

// TestEvaluateSourceAlertsPriceJumpTruncatedCount pins that a truncated summary
// reports the movements it carries while still stating how many were observed,
// so an admin is never shown 20 movements as if they were all of them.
func TestEvaluateSourceAlertsPriceJumpTruncatedCount(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	source := createAlertTestSource(t, RoleCuratedReference, nil, "")
	run := createAlertTestRun(t, db, source.Id, model.PriceSyncRunStatusSucceeded, 200, now)

	entries := make([]priceJumpEntry, 0, MaxPriceJumpEntries*2)
	for i := 0; i < MaxPriceJumpEntries*2; i++ {
		rate := float64(i + 1)
		entries = append(entries, priceJumpEntry{
			SourceModelName: fmt.Sprintf("vendor/model-%02d", i),
			Dimension:       PriceJumpDimensionInput,
			ChangeRate:      &rate,
		})
	}
	require.NoError(t, db.Model(&model.PriceSyncRun{}).Where("id = ?", run.Id).
		Update("price_jump_summary", encodePriceJumpSummary(DefaultPriceJumpThreshold, entries)).Error)
	source.LastSuccessRunId = &run.Id

	alerts, err := EvaluateSourceAlerts([]*model.PriceSource{source}, now)
	require.NoError(t, err)
	jumps := make([]dto.UpstreamPriceAlert, 0, MaxPriceJumpEntries)
	for _, alert := range alerts {
		if alert.Code == AlertPriceJump {
			jumps = append(jumps, alert)
		}
	}
	require.Len(t, jumps, MaxPriceJumpEntries)
	require.NotNil(t, jumps[0].Params.JumpCount)
	assert.Equal(t, MaxPriceJumpEntries*2, *jumps[0].Params.JumpCount)
	require.NotNil(t, jumps[0].Params.ReportedCount)
	assert.Equal(t, MaxPriceJumpEntries, *jumps[0].Params.ReportedCount)
}

// TestListSourceAlertsMatchesCatalogProjection locks the contract the
// source-alerts endpoint exists for: it is the same evaluation the catalog
// projection appends to its own response, only without the projection. The two
// must stay item-for-item equal, or the sources page and the catalog page would
// disagree about the health of the same catalog.
func TestListSourceAlertsMatchesCatalogProjection(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()

	// A cost source that is stale, lost most of its coverage between its last
	// two successful runs, and was repointed at another channel afterwards.
	channelId := 1
	require.NoError(t, db.Create(&model.Channel{Id: channelId, Name: "cost-channel"}).Error)
	otherChannelId := channelId + 1
	require.NoError(t, db.Create(&model.Channel{Id: otherChannelId, Name: "other-channel"}).Error)
	degraded := &model.PriceSource{
		Name:       "degraded-cost-source",
		AdapterKey: "test_adapter",
		Role:       string(RoleSupplierCost),
		Scope:      string(ScopePublic),
		ChannelId:  &channelId,
		Enabled:    true,
		Settings:   `{"stale_threshold_seconds":3600}`,
	}
	require.NoError(t, model.InsertPriceSource(degraded))
	createAlertTestRun(t, db, degraded.Id, model.PriceSyncRunStatusSucceeded, 100, now-7200)
	latest := createAlertTestRun(t, db, degraded.Id, model.PriceSyncRunStatusSucceeded, 20, now-7200)
	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", degraded.Id).
		Updates(map[string]any{"last_success_run_id": latest.Id, "channel_id": otherChannelId}).Error)

	// A second source with three trailing failures, so the comparison covers
	// more than one source and more than one alert code.
	failing := &model.PriceSource{
		Name:       "failing-reference-source",
		AdapterKey: "test_adapter",
		Role:       string(RoleCuratedReference),
		Scope:      string(ScopePublic),
		Enabled:    true,
	}
	require.NoError(t, model.InsertPriceSource(failing))
	for i := 0; i < ConsecutiveFailureAlertThreshold; i++ {
		createAlertTestRun(t, db, failing.Id, model.PriceSyncRunStatusFailed, 0, now)
	}

	response, err := ListSourceAlerts()
	require.NoError(t, err)
	require.NotNil(t, response)
	catalog, err := GetCurrentUpstreamPrices(nil)
	require.NoError(t, err)

	codes := make([]string, 0, len(response.Alerts))
	for _, alert := range response.Alerts {
		codes = append(codes, alert.Code)
	}
	assert.ElementsMatch(t, []string{
		AlertSourceStale,
		AlertCoverageDrop,
		AlertSourceConfigChanged,
		AlertSourceConsecutiveFailures,
	}, codes, "the fixture must exercise every source-level alert code")
	assert.Equal(t, catalog.Alerts, response.Alerts)
	assert.Positive(t, response.GeneratedAt)
}
