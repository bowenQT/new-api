package upstreamprice

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// Catalog health alerts (spec §13). Alerts are computed on read, surfaced in
// the catalog and comparison responses, and written to the backend log from
// the paths that actually change catalog state. They are deliberately not
// wired to any notification channel (spec §21 Q6).
const (
	AlertSourceConsecutiveFailures = "source_consecutive_failures"
	AlertSourceStale               = "source_stale"
	AlertCoverageDrop              = "coverage_drop"
	AlertCostInversion             = "cost_inversion"
	// AlertSourceConfigChanged fires while a source's last successful run
	// executed under a different configuration than the source carries now.
	// Its prices stop counting as confirmed costs until the next sync, so the
	// admin must be told why they dropped out of the margin.
	AlertSourceConfigChanged = "source_config_changed"
)

// ConsecutiveFailureAlertThreshold is the number of trailing failed runs that
// raises a source-failure alert (spec §13).
const ConsecutiveFailureAlertThreshold = 3

// EvaluateSourceAlerts derives the source-level alerts of the given sources:
// consecutive sync failures, cost sources past their staleness threshold, and
// model coverage dropping more than the configured gate between the last two
// successful runs. It reads only; nothing is persisted.
func EvaluateSourceAlerts(sources []*model.PriceSource, now int64) ([]dto.UpstreamPriceAlert, error) {
	alerts := make([]dto.UpstreamPriceAlert, 0)
	for _, source := range sources {
		config, err := SourceConfigFromModel(source)
		if err != nil {
			return nil, err
		}

		recentRuns, err := model.GetRecentPriceSyncRuns(source.Id, ConsecutiveFailureAlertThreshold)
		if err != nil {
			return nil, err
		}
		failures := 0
		for _, run := range recentRuns {
			if run.Status != model.PriceSyncRunStatusFailed {
				break
			}
			failures++
		}
		if failures >= ConsecutiveFailureAlertThreshold {
			alerts = append(alerts, dto.UpstreamPriceAlert{
				Code:       AlertSourceConsecutiveFailures,
				SourceId:   source.Id,
				SourceName: source.Name,
				Detail:     fmt.Sprintf("last %d sync runs failed", failures),
			})
		}

		successfulRuns, err := model.GetRecentSuccessfulPriceSyncRuns(source.Id, 2)
		if err != nil {
			return nil, err
		}
		if len(successfulRuns) > 0 && priceSourceConfigChanged(config, successfulRuns[0]) {
			alerts = append(alerts, dto.UpstreamPriceAlert{
				Code:       AlertSourceConfigChanged,
				SourceId:   source.Id,
				SourceName: source.Name,
				Detail:     fmt.Sprintf("source configuration changed after run %d; its prices are not confirmed until the next successful sync", successfulRuns[0].Id),
			})
		}
		if len(successfulRuns) > 0 && PriceRole(source.Role) == RoleSupplierCost {
			latest := successfulRuns[0]
			threshold := staleThresholdSeconds(source, config.Settings)
			if latest.FinishedAt != nil && now-*latest.FinishedAt > threshold {
				alerts = append(alerts, dto.UpstreamPriceAlert{
					Code:       AlertSourceStale,
					SourceId:   source.Id,
					SourceName: source.Name,
					Detail:     fmt.Sprintf("last successful run is %d seconds old, threshold %d seconds", now-*latest.FinishedAt, threshold),
				})
			}
		}
		if len(successfulRuns) == 2 {
			latest, previous := successfulRuns[0], successfulRuns[1]
			gate := coverageDropThreshold(config)
			if previous.ValidCount > 0 {
				drop := 1 - float64(latest.ValidCount)/float64(previous.ValidCount)
				if drop > gate {
					alerts = append(alerts, dto.UpstreamPriceAlert{
						Code:       AlertCoverageDrop,
						SourceId:   source.Id,
						SourceName: source.Name,
						Detail:     fmt.Sprintf("valid model coverage fell from %d to %d (gate %.4f)", previous.ValidCount, latest.ValidCount, gate),
					})
				}
			}
		}
	}
	return alerts, nil
}

// LogPriceCatalogAlerts writes alerts to the backend log. Callers invoke it
// from state-changing paths (scheduled and manual sync), not from read
// queries, so the log cadence follows catalog changes rather than UI traffic.
func LogPriceCatalogAlerts(ctx context.Context, alerts []dto.UpstreamPriceAlert) {
	for _, alert := range alerts {
		message := fmt.Sprintf("upstream price catalog alert: code=%s source=%d name=%q", alert.Code, alert.SourceId, alert.SourceName)
		if alert.CanonicalModelName != "" {
			message += fmt.Sprintf(" model=%q", alert.CanonicalModelName)
		}
		message += ": " + alert.Detail
		logger.LogWarn(ctx, message)
	}
}

// LogSourceAlertsAfterSync evaluates and logs one source's alerts right after
// its catalog state changed. Evaluation failures are logged, never returned:
// alerting must not turn a successful sync into a failure.
func LogSourceAlertsAfterSync(ctx context.Context, source *model.PriceSource) {
	alerts, err := EvaluateSourceAlerts([]*model.PriceSource{source}, common.GetTimestamp())
	if err != nil {
		common.SysError(fmt.Sprintf("upstream price alert evaluation failed for source %d: %v", source.Id, err))
		return
	}
	LogPriceCatalogAlerts(ctx, alerts)
}
