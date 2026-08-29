package upstreamprice

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// Scheduled catalog sync (spec §8.4).
//
// `upstream_price_sync` is a single system task type: it wakes on one short
// fixed cadence and selects the sources whose own schedule_interval_seconds
// has elapsed. There is no per-source task type and no dedicated goroutine;
// the existing SystemTask lease provides mutual exclusion across instances.
//
// The scheduled path has no sale-pricing write capability whatsoever: it only
// calls SyncPriceSourceWithoutPreview, which writes runs, run items, and price
// snapshots.

// ScheduleTaskEnabledEnvKey switches the whole background sync on. It defaults
// to off, matching the spec's "scheduled sync stays disabled by default".
const ScheduleTaskEnabledEnvKey = "UPSTREAM_PRICE_SYNC_TASK_ENABLED"

const (
	// ScheduleWakeInterval is how often the single task type wakes to look for
	// due sources. It is deliberately far shorter than the six-hour per-source
	// minimum so a source runs close to its own interval.
	ScheduleWakeInterval = 15 * time.Minute
	// scheduledSourceTimeout bounds one source's fetch and commit.
	scheduledSourceTimeout = 3 * time.Minute
	// scheduledTotalTimeout bounds the whole pass regardless of how many
	// sources are due.
	scheduledTotalTimeout = 30 * time.Minute
)

// ScheduledSyncSummary is the task result recorded on the system task row.
//
// Succeeded counts the sources whose run actually committed observations, so a
// partial run — valid observations were written, some models were unsupported,
// rejected, or missing — is counted there and additionally reported in Partial.
// Failed counts every source whose run did not commit anything, whether it
// errored outright or was refused by a gate (zero valid observations, coverage
// drop), because such a run advances nothing (spec §8.4).
type ScheduledSyncSummary struct {
	Due       int  `json:"due"`
	Executed  int  `json:"executed"`
	Succeeded int  `json:"succeeded"`
	Partial   int  `json:"partial"`
	Failed    int  `json:"failed"`
	Skipped   int  `json:"skipped"`
	TimedOut  bool `json:"timed_out"`
}

// ScheduledSyncEnabled reports whether the background sync should run at all:
// the deployment switch must be on and at least one source must be both
// enabled and scheduled.
func ScheduledSyncEnabled() bool {
	if !common.GetEnvOrDefaultBool(ScheduleTaskEnabledEnvKey, false) {
		return false
	}
	count, err := model.CountSchedulablePriceSources()
	if err != nil {
		common.SysError(fmt.Sprintf("upstream price schedule lookup failed: %v", err))
		return false
	}
	return count > 0
}

// scheduledSourceDue reports whether a source's own interval has elapsed since
// its last attempt, successful or not, so a failing source backs off to its
// configured interval instead of retrying on every wake.
func scheduledSourceDue(source *model.PriceSource, now int64) bool {
	if !source.Enabled || !source.ScheduleEnabled {
		return false
	}
	if source.ScheduleIntervalSeconds < MinScheduleIntervalSeconds {
		return false
	}
	lastAttempt := int64(0)
	if source.LastSuccessAt != nil && *source.LastSuccessAt > lastAttempt {
		lastAttempt = *source.LastSuccessAt
	}
	if source.LastErrorAt != nil && *source.LastErrorAt > lastAttempt {
		lastAttempt = *source.LastErrorAt
	}
	if lastAttempt == 0 {
		return true
	}
	return now-lastAttempt >= source.ScheduleIntervalSeconds
}

// RunScheduledSync executes one scheduled pass: due sources are synced one at
// a time under a per-source and an overall timeout, orphaned sources are
// refused, and catalog alerts are logged for whatever actually changed.
//
// The returned error is the pass's outcome for the system task row: any source
// that failed — including a run the coverage or zero-observation gate refused —
// and the overall timeout both make the pass a failure, so a refused sync is
// never reported as a successful task (spec §8.4).
func RunScheduledSync(ctx context.Context, progress func(processed, total int)) (ScheduledSyncSummary, error) {
	summary := ScheduledSyncSummary{}
	ctx, cancel := context.WithTimeout(ctx, scheduledTotalTimeout)
	defer cancel()

	sources, err := model.GetAllPriceSources()
	if err != nil {
		common.SysError(fmt.Sprintf("upstream price scheduled sync could not list sources: %v", err))
		return summary, fmt.Errorf("upstream price scheduled sync could not list sources: %w", err)
	}
	now := common.GetTimestamp()
	due := make([]*model.PriceSource, 0, len(sources))
	for _, source := range sources {
		if scheduledSourceDue(source, now) {
			due = append(due, source)
		}
	}
	summary.Due = len(due)
	if progress != nil {
		progress(0, summary.Due)
	}

	for index, source := range due {
		if ctx.Err() != nil {
			summary.Skipped += len(due) - index
			summary.TimedOut = true
			logger.LogWarn(ctx, "upstream price scheduled sync stopped: overall timeout reached")
			break
		}
		// Orphaned sources may still preview for diagnostics, but never commit
		// (spec §7.1); the scheduled path refuses them before fetching.
		orphaned, err := IsPriceSourceOrphaned(source)
		if err != nil {
			// A failed lookup is not "the channel is confirmed gone": the source
			// was neither run nor safely skipped. Counting it as a skip would
			// finish the task successfully and leave no backoff timestamp, so
			// the same broken source would be retried on every wake and the
			// failure would never surface (spec §8.4).
			summary.Failed++
			logger.LogWarn(ctx, fmt.Sprintf("upstream price scheduled sync orphan check failed for source %d (%q): %v", source.Id, source.Name, err))
			_ = recordScheduledAttemptFailure(ctx, source, fmt.Errorf("orphan check failed: %w", err))
			continue
		}
		if orphaned {
			summary.Skipped++
			logger.LogWarn(ctx, fmt.Sprintf("upstream price scheduled sync skipped orphaned source %d (%q)", source.Id, source.Name))
			continue
		}

		summary.Executed++
		sourceCtx, cancelSource := context.WithTimeout(ctx, scheduledSourceTimeout)
		result, err := SyncPriceSourceWithoutPreview(sourceCtx, source)
		cancelSource()
		switch {
		case err != nil:
			summary.Failed++
			logger.LogWarn(ctx, fmt.Sprintf("upstream price scheduled sync failed for source %d (%q): %v", source.Id, source.Name, err))
		case result.Status == model.PriceSyncRunStatusFailed:
			// A refused commit returns no error but advances nothing: the run
			// had zero valid observations or the coverage gate rejected it.
			summary.Failed++
			logger.LogWarn(ctx, fmt.Sprintf("upstream price scheduled sync refused run %d for source %d (%q): valid=%d error=%q",
				result.RunId, source.Id, source.Name, result.ValidCount, result.ErrorSummary))
		default:
			summary.Succeeded++
			if result.Status == model.PriceSyncRunStatusPartial {
				summary.Partial++
			}
			logger.LogInfo(ctx, fmt.Sprintf("upstream price scheduled sync committed run %d for source %d (%q): status=%s valid=%d new=%d",
				result.RunId, source.Id, source.Name, result.Status, result.ValidCount, result.NewSnapshotCount))
		}
		// Alerts, including cost inversion, are logged by the shared post-write
		// path inside the sync itself, so the manual flow gets them too.
		if progress != nil {
			progress(index+1, summary.Due)
		}
	}

	if summary.Failed > 0 || summary.TimedOut {
		return summary, fmt.Errorf("upstream price scheduled sync incomplete: %d of %d due sources failed, %d skipped, timed_out=%t",
			summary.Failed, summary.Due, summary.Skipped, summary.TimedOut)
	}
	return summary, nil
}
