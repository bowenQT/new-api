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
			failureCount := failures
			alerts = append(alerts, dto.UpstreamPriceAlert{
				Code:       AlertSourceConsecutiveFailures,
				SourceId:   source.Id,
				SourceName: source.Name,
				Detail:     fmt.Sprintf("last %d sync runs failed", failures),
				Params:     &dto.UpstreamPriceAlertParams{FailureCount: &failureCount},
			})
		}

		successfulRuns, err := model.GetRecentSuccessfulPriceSyncRuns(source.Id, 2)
		if err != nil {
			return nil, err
		}
		if len(successfulRuns) > 0 && priceSourceConfigChanged(config, successfulRuns[0]) {
			runId := successfulRuns[0].Id
			alerts = append(alerts, dto.UpstreamPriceAlert{
				Code:       AlertSourceConfigChanged,
				SourceId:   source.Id,
				SourceName: source.Name,
				Detail:     fmt.Sprintf("source configuration changed after run %d; its prices are not confirmed until the next successful sync", runId),
				Params:     &dto.UpstreamPriceAlertParams{RunId: &runId},
			})
		}
		if len(successfulRuns) > 0 && PriceRole(source.Role) == RoleSupplierCost {
			latest := successfulRuns[0]
			threshold := staleThresholdSeconds(source, config.Settings)
			if latest.FinishedAt != nil && now-*latest.FinishedAt > threshold {
				runId, age, thresholdSeconds := latest.Id, now-*latest.FinishedAt, threshold
				alerts = append(alerts, dto.UpstreamPriceAlert{
					Code:       AlertSourceStale,
					SourceId:   source.Id,
					SourceName: source.Name,
					Detail:     fmt.Sprintf("last successful run is %d seconds old, threshold %d seconds", age, thresholdSeconds),
					Params: &dto.UpstreamPriceAlertParams{
						RunId:            &runId,
						AgeSeconds:       &age,
						ThresholdSeconds: &thresholdSeconds,
					},
				})
			}
		}

		// A coverage collapse the gate actually refused is the case that most
		// needs an alert, and it is exactly the case comparing two successful
		// runs cannot see: the refused run is failed, so it never becomes the
		// baseline and the last two successful runs still look healthy. The
		// refused run carries an explicit coverage_drop_exceeded marker, so this
		// never depends on parsing an error summary.
		gate := coverageDropThreshold(config)
		switch {
		case len(recentRuns) > 0 && coverageGateRefused(recentRuns[0]) && len(successfulRuns) > 0:
			alerts = append(alerts, coverageDropAlert(source, successfulRuns[0], recentRuns[0], gate, true))
		case len(successfulRuns) == 2:
			latest, previous := successfulRuns[0], successfulRuns[1]
			if previous.ValidCount > 0 && 1-float64(latest.ValidCount)/float64(previous.ValidCount) > gate {
				alerts = append(alerts, coverageDropAlert(source, previous, latest, gate, false))
			}
		}
	}
	return alerts, nil
}

// coverageGateRefused reports whether a run is a failed run the coverage gate
// refused, as opposed to a fetch failure, a zero-observation run, or a
// pre-plan failure.
func coverageGateRefused(run *model.PriceSyncRun) bool {
	return run.Status == model.PriceSyncRunStatusFailed &&
		run.CoverageDropExceeded != nil && *run.CoverageDropExceeded
}

// coverageDropAlert describes one model-coverage collapse, either between two
// runs that both committed or between the last committed run and the run the
// gate refused because of it.
func coverageDropAlert(source *model.PriceSource, baseline, observed *model.PriceSyncRun, gate float64, gateRefused bool) dto.UpstreamPriceAlert {
	detail := fmt.Sprintf("valid model coverage fell from %d to %d (gate %.4f)", baseline.ValidCount, observed.ValidCount, gate)
	if gateRefused {
		detail = fmt.Sprintf("run %d was refused by the coverage gate: valid model coverage fell from %d to %d (gate %.4f)",
			observed.Id, baseline.ValidCount, observed.ValidCount, gate)
	}
	runId, previousValid, validCount, dropThreshold, refused := observed.Id, baseline.ValidCount, observed.ValidCount, gate, gateRefused
	return dto.UpstreamPriceAlert{
		Code:       AlertCoverageDrop,
		SourceId:   source.Id,
		SourceName: source.Name,
		Detail:     detail,
		Params: &dto.UpstreamPriceAlertParams{
			RunId:              &runId,
			PreviousValidCount: &previousValid,
			ValidCount:         &validCount,
			DropThreshold:      &dropThreshold,
			GateRefused:        &refused,
		},
	}
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

// LogCatalogAlertsAfterWrite is the single post-write alerting point of the
// catalog: every path that writes a run — manual commit, scheduled commit, a
// gate-refused or failed run, and a pre-plan scheduled failure — calls it right
// after the write. Alerting therefore no longer depends on the scheduled task
// being enabled, which by default it is not (spec §8.4, §13).
//
// The source is re-read so alerts see the state the write just produced.
// canonicalModels are the canonical model names this write made current; when
// there are any, their cost inversion against the default group is evaluated
// too. Evaluation failures are logged, never returned: alerting must not turn a
// successful sync into a failure.
func LogCatalogAlertsAfterWrite(ctx context.Context, sourceId int, canonicalModels []string) {
	source, err := model.GetPriceSourceById(sourceId)
	if err != nil {
		common.SysError(fmt.Sprintf("upstream price alert reload failed for source %d: %v", sourceId, err))
		return
	}
	alerts, err := EvaluateSourceAlerts([]*model.PriceSource{source}, common.GetTimestamp())
	if err != nil {
		common.SysError(fmt.Sprintf("upstream price alert evaluation failed for source %d: %v", sourceId, err))
	} else {
		LogPriceCatalogAlerts(ctx, alerts)
	}
	logCostInversionAlerts(ctx, canonicalModels)
}

// logCostInversionAlerts compares the named canonical models against the
// default group's sale pricing and logs the ones whose worst cost exceeds the
// projected sale price (spec §13).
//
// The comparison is scoped to the models the sync just made current rather than
// run over the whole catalog: a manual commit is an interactive request, and a
// model no sync touched cannot have changed its cost. Models are compared in
// request-sized batches so a source larger than the per-request cap is still
// covered end to end.
func logCostInversionAlerts(ctx context.Context, canonicalModels []string) {
	for start := 0; start < len(canonicalModels); start += dto.MaxCompareModelsRequested {
		end := start + dto.MaxCompareModelsRequested
		if end > len(canonicalModels) {
			end = len(canonicalModels)
		}
		comparison, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: canonicalModels[start:end]})
		if err != nil {
			common.SysError(fmt.Sprintf("upstream price cost inversion check failed: %v", err))
			return
		}
		inversions := make([]dto.UpstreamPriceAlert, 0)
		for _, alert := range comparison.Alerts {
			if alert.Code == AlertCostInversion {
				inversions = append(inversions, alert)
			}
		}
		LogPriceCatalogAlerts(ctx, inversions)
	}
}
