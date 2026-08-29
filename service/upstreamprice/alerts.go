package upstreamprice

import (
	"context"
	"fmt"
	"slices"

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
	// AlertPriceJump fires while the last successful run recorded a price
	// movement past the source's threshold (spec §13). It is derived from the
	// evidence that run stored, so it persists until the next successful sync
	// says otherwise, exactly like source_config_changed.
	AlertPriceJump = "price_jump"
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
				Params:     &dto.UpstreamPriceAlertParams{FailureCount: common.GetPointer(failures)},
			})
		}

		successfulRuns, err := model.GetRecentSuccessfulPriceSyncRuns(source.Id, 2)
		if err != nil {
			return nil, err
		}
		if len(successfulRuns) > 0 {
			latest := successfulRuns[0]
			if priceSourceConfigChanged(config, latest) {
				alerts = append(alerts, dto.UpstreamPriceAlert{
					Code:       AlertSourceConfigChanged,
					SourceId:   source.Id,
					SourceName: source.Name,
					Detail:     fmt.Sprintf("source configuration changed after run %d; its prices are not confirmed until the next successful sync", latest.Id),
					Params:     &dto.UpstreamPriceAlertParams{RunId: common.GetPointer(latest.Id)},
				})
			}
			alerts = append(alerts, priceJumpAlerts(source, latest)...)
			// The role gate stays here rather than inside sourceStale: a cost
			// nobody can bill against going stale is the health problem, while a
			// reference price is allowed to age without raising anything.
			if PriceRole(source.Role) == RoleSupplierCost && sourceStale(source, config.Settings, latest, now) {
				age, threshold := now-*latest.FinishedAt, staleThresholdSeconds(source, config.Settings)
				alerts = append(alerts, dto.UpstreamPriceAlert{
					Code:       AlertSourceStale,
					SourceId:   source.Id,
					SourceName: source.Name,
					Detail:     fmt.Sprintf("last successful run is %d seconds old, threshold %d seconds", age, threshold),
					Params: &dto.UpstreamPriceAlertParams{
						RunId:            common.GetPointer(latest.Id),
						AgeSeconds:       common.GetPointer(age),
						ThresholdSeconds: common.GetPointer(threshold),
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

// ListSourceAlerts returns the source-level alerts of every registered source.
// It is the same evaluation the catalog projection appends to its response, so
// a client that only needs the health signals does not have to project the
// whole catalog to get them.
func ListSourceAlerts() (*dto.UpstreamPriceSourceAlertsResponse, error) {
	sources, err := model.GetAllPriceSources()
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	alerts, err := EvaluateSourceAlerts(sources, now)
	if err != nil {
		return nil, err
	}
	return &dto.UpstreamPriceSourceAlertsResponse{GeneratedAt: now, Alerts: alerts}, nil
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
	return dto.UpstreamPriceAlert{
		Code:       AlertCoverageDrop,
		SourceId:   source.Id,
		SourceName: source.Name,
		Detail:     detail,
		Params: &dto.UpstreamPriceAlertParams{
			RunId:              common.GetPointer(observed.Id),
			PreviousValidCount: common.GetPointer(baseline.ValidCount),
			ValidCount:         common.GetPointer(observed.ValidCount),
			DropThreshold:      common.GetPointer(gate),
			GateRefused:        common.GetPointer(gateRefused),
		},
	}
}

// priceJumpAlerts turns the price movements the last successful run recorded
// into one alert per movement (spec §13).
//
// The measurement itself happened during that run's planning, where both the
// baseline snapshot and the incoming price were in hand; alerting only reads
// the bounded evidence it stored, so no expression is re-evaluated and no
// additional query is issued here. A run written before the column existed, a
// run whose fingerprints did not change, and a 304 replay all carry no summary
// and raise nothing.
func priceJumpAlerts(source *model.PriceSource, run *model.PriceSyncRun) []dto.UpstreamPriceAlert {
	summary := decodePriceJumpSummary(run.PriceJumpSummary)
	if len(summary.Entries) == 0 {
		return nil
	}
	runId, threshold := run.Id, summary.Threshold
	total, reported := summary.Total, len(summary.Entries)
	alerts := make([]dto.UpstreamPriceAlert, 0, reported)
	for _, entry := range summary.Entries {
		params := &dto.UpstreamPriceAlertParams{
			RunId:           &runId,
			SourceModelName: entry.SourceModelName,
			Dimension:       entry.Dimension,
			ProbeContext:    entry.ProbeContext,
			PreviousUSD:     entry.PreviousUSD,
			CurrentUSD:      entry.CurrentUSD,
			ChangeRate:      entry.ChangeRate,
			JumpThreshold:   &threshold,
			JumpCount:       &total,
			ReportedCount:   &reported,
		}
		if entry.FromZero {
			params.FromZero = common.GetPointer(true)
		}
		alerts = append(alerts, dto.UpstreamPriceAlert{
			Code:               AlertPriceJump,
			SourceId:           source.Id,
			SourceName:         source.Name,
			CanonicalModelName: entry.CanonicalModelName,
			Detail:             priceJumpDetail(runId, entry),
			Params:             params,
		})
	}
	return alerts
}

// priceJumpDetail is the English fallback sentence of one movement. Clients
// that understand Params render their own localized message from the same
// facts; this string is what an older client and the backend log show.
func priceJumpDetail(runId int, entry priceJumpEntry) string {
	if entry.Dimension == PriceJumpDimensionExprUnverified {
		return fmt.Sprintf("run %d changed the price of %q in a way this check could not measure; review it manually",
			runId, entry.SourceModelName)
	}
	previous, current := float64(0), float64(0)
	if entry.PreviousUSD != nil {
		previous = *entry.PreviousUSD
	}
	if entry.CurrentUSD != nil {
		current = *entry.CurrentUSD
	}
	if entry.FromZero {
		return fmt.Sprintf("run %d moved the %s price of %q from 0 to %g USD at %s",
			runId, entry.Dimension, entry.SourceModelName, current, entry.ProbeContext)
	}
	rate := float64(0)
	if entry.ChangeRate != nil {
		rate = *entry.ChangeRate
	}
	return fmt.Sprintf("run %d moved the %s price of %q from %g to %g USD (%.2f%%) at %s",
		runId, entry.Dimension, entry.SourceModelName, previous, current, rate*100, entry.ProbeContext)
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
// covered end to end, but the batches share one projection basis: the batch
// size is the unit the comparison is specified in, not a reason to re-read the
// whole catalog per batch.
//
// This path evaluates no source-level alert, so unlike CompareUpstreamPrices it
// needs no second read of the registered sources: a cost inversion is a
// model-level comparison of a projected cost against the sale price, and the
// caller has already evaluated and logged the source alerts of the source this
// write touched.
func logCostInversionAlerts(ctx context.Context, canonicalModels []string) {
	if len(canonicalModels) == 0 {
		return
	}
	basis, err := newCompareBasis("", nil)
	if err != nil {
		common.SysError(fmt.Sprintf("upstream price cost inversion check failed: %v", err))
		return
	}
	for batch := range slices.Chunk(canonicalModels, dto.MaxCompareModelsRequested) {
		names, _, _ := selectCompareModels(batch, "", basis.pricesByModel)
		_, inversions := basis.compare(names)
		LogPriceCatalogAlerts(ctx, inversions)
	}
}
