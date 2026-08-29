package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// Upstream price catalog persistence (docs/downstream/upstream-price-catalog-spec.md §7).
//
// PriceSnapshot rows are immutable observations: after insert, only the
// observation-evidence fields last_seen_at / last_seen_run_id may change, and
// only on a fingerprint-idempotent hit. The current price for a source is
// defined by run semantics: the snapshots pointed to by status=valid run items
// of the run referenced by PriceSource.last_success_run_id.

const (
	PriceSyncRunStatusSucceeded = "succeeded"
	PriceSyncRunStatusPartial   = "partial"
	PriceSyncRunStatusFailed    = "failed"

	PriceSyncItemStatusValid       = "valid"
	PriceSyncItemStatusUnsupported = "unsupported"
	PriceSyncItemStatusRejected    = "rejected"
	PriceSyncItemStatusMissing     = "missing"
)

// ErrPriceSyncConflict is returned when a commit loses the config-revision or
// base-run CAS check inside the commit transaction. The caller must re-preview.
var ErrPriceSyncConflict = errors.New("price sync conflict: source configuration or baseline changed, re-preview required")

// ErrPriceSourceOrphaned is returned when a supplier_cost source's linked
// channel no longer exists at commit time (spec §7.1: orphan sources refuse
// commit; historical snapshots are retained).
var ErrPriceSourceOrphaned = errors.New("price source is orphaned (linked channel no longer exists); commit refused")

// ErrPriceSourceChannelDisabled is returned when the linked channel exists
// but is disabled at commit time.
var ErrPriceSourceChannelDisabled = errors.New("price source channel is disabled; commit refused")

// PriceSourceRoleSupplierCost mirrors the service-layer supplier_cost role
// for the in-transaction channel check.
const PriceSourceRoleSupplierCost = "supplier_cost"

// PriceSource registers one price origin (spec §7.1).
type PriceSource struct {
	Id                      int    `json:"id"`
	Name                    string `json:"name" gorm:"type:varchar(128)"`
	AdapterKey              string `json:"adapter_key" gorm:"type:varchar(64);index"`
	Role                    string `json:"role" gorm:"type:varchar(32)"`
	Scope                   string `json:"scope" gorm:"type:varchar(32)"`
	ChannelId               *int   `json:"channel_id" gorm:"index"`
	Enabled                 bool   `json:"enabled"`
	ScheduleEnabled         bool   `json:"schedule_enabled"`
	ScheduleIntervalSeconds int64  `json:"schedule_interval_seconds" gorm:"type:bigint"`
	Settings                string `json:"settings" gorm:"type:text"`
	ConfigRevision          int64  `json:"config_revision" gorm:"type:bigint"`
	LastSuccessRunId        *int   `json:"last_success_run_id"`
	LastSuccessAt           *int64 `json:"last_success_at" gorm:"type:bigint"`
	LastErrorAt             *int64 `json:"last_error_at" gorm:"type:bigint"`
	LastErrorSummary        string `json:"last_error_summary" gorm:"type:varchar(255)"`
	CreatedTime             int64  `json:"created_time" gorm:"type:bigint"`
	UpdatedTime             int64  `json:"updated_time" gorm:"type:bigint"`
}

// PriceSnapshot is one immutable normalized price observation (spec §7.2).
//
// Fingerprint stores a lowercase SHA-256 hex digest (ASCII only). Spec §7.2
// recommends ascii/binary collation on MySQL; varchar(64) is used here because
// the digest is fixed-length ASCII, so utf8mb4 unique-key length limits are
// still safely met (64*4 bytes) and case never varies. The composite unique
// key implements fingerprint idempotency.
type PriceSnapshot struct {
	Id                 int    `json:"id"`
	SourceId           int    `json:"source_id" gorm:"uniqueIndex:idx_price_snapshot_identity,priority:1;index:idx_price_snapshot_seen,priority:1"`
	SourceModelName    string `json:"source_model_name" gorm:"type:varchar(255);uniqueIndex:idx_price_snapshot_identity,priority:2"`
	CanonicalModelName string `json:"canonical_model_name" gorm:"type:varchar(255);index"`
	Role               string `json:"role" gorm:"type:varchar(32)"`
	Scope              string `json:"scope" gorm:"type:varchar(32)"`
	Provider           string `json:"provider" gorm:"type:varchar(64)"`
	MappingStatus      string `json:"mapping_status" gorm:"type:varchar(16)"`
	Currency           string `json:"currency" gorm:"type:varchar(8)"`
	FormulaKind        string `json:"formula_kind" gorm:"type:varchar(32)"`
	PriceExpr          string `json:"price_expr" gorm:"type:text"`
	ExprVersion        string `json:"expr_version" gorm:"type:varchar(32)"`
	EffectiveAt        *int64 `json:"effective_at" gorm:"type:bigint"`
	FetchedAt          int64  `json:"fetched_at" gorm:"type:bigint"`
	LastSeenAt         int64  `json:"last_seen_at" gorm:"type:bigint"`
	LastSeenRunId      int    `json:"last_seen_run_id" gorm:"index:idx_price_snapshot_seen,priority:2"`
	Fingerprint        string `json:"fingerprint" gorm:"type:varchar(64);uniqueIndex:idx_price_snapshot_identity,priority:3"`
	FingerprintVersion string `json:"fingerprint_version" gorm:"type:varchar(16)"`
	Metadata           string `json:"metadata" gorm:"type:text"`
	CreatedTime        int64  `json:"created_time" gorm:"type:bigint"`
}

// PriceSyncRun records one sync batch (spec §7.3). Its monotonically
// increasing id is the ordering authority; repository timestamps are
// second-granularity and must not be used for ordering.
type PriceSyncRun struct {
	Id                   int    `json:"id"`
	SourceId             int    `json:"source_id" gorm:"index"`
	Status               string `json:"status" gorm:"type:varchar(16)"`
	AdapterKey           string `json:"adapter_key" gorm:"type:varchar(64)"`
	StartedAt            int64  `json:"started_at" gorm:"type:bigint"`
	FinishedAt           *int64 `json:"finished_at" gorm:"type:bigint"`
	DurationMs           int64  `json:"duration_ms" gorm:"type:bigint"`
	HttpStatus           int    `json:"http_status"`
	ResponseBytes        int64  `json:"response_bytes" gorm:"type:bigint"`
	SourceConfigRevision int64  `json:"source_config_revision" gorm:"type:bigint"`
	SourceConfigDigest   string `json:"source_config_digest" gorm:"type:varchar(64)"`
	SourceRevision       string `json:"source_revision" gorm:"type:varchar(128)"`
	DiscoveredCount      int    `json:"discovered_count"`
	ValidCount           int    `json:"valid_count"`
	UnsupportedCount     int    `json:"unsupported_count"`
	RejectedCount        int    `json:"rejected_count"`
	MissingCount         int    `json:"missing_count"`
	NewSnapshotCount     int    `json:"new_snapshot_count"`
	IdempotentHitCount   int    `json:"idempotent_hit_count"`
	ErrorSummary         string `json:"error_summary" gorm:"type:varchar(255)"`
	// CoverageDropExceeded records whether the coverage gate refused this run,
	// as an explicit marker rather than something alerting has to infer from
	// ErrorSummary text or from counts. It is a pointer so the column stays
	// nullable on every supported database: rows written before the column
	// existed read back as nil ("not known to be a gate refusal") instead of
	// failing to scan, and no boolean default is declared, so AutoMigrate
	// cannot churn on MySQL/PostgreSQL default normalization.
	CoverageDropExceeded *bool `json:"coverage_drop_exceeded,omitempty"`
	// PriceJumpSummary holds the bounded JSON evidence of the price movements
	// this run observed against its baseline run (spec §7.3, §13). An empty
	// string means the run evaluated no movement — a run written before the
	// column existed, a run with no changed fingerprint, or a run whose probes
	// all stayed under the source threshold — and never means "not known", so
	// alerting reads it directly. It is a plain text column with no GORM
	// default, so AutoMigrate cannot churn on it.
	PriceJumpSummary string `json:"price_jump_summary,omitempty" gorm:"type:text"`
}

// PriceSyncRunItem is the per-model detail of one run (spec §7.3). The
// composite unique key guarantees at most one item per model per run, so
// duplicate source model names can never produce ambiguous run semantics.
type PriceSyncRunItem struct {
	Id              int    `json:"id"`
	RunId           int    `json:"run_id" gorm:"uniqueIndex:idx_price_run_item_model,priority:1"`
	SourceModelName string `json:"source_model_name" gorm:"type:varchar(255);uniqueIndex:idx_price_run_item_model,priority:2"`
	Status          string `json:"status" gorm:"type:varchar(16)"`
	SnapshotId      *int   `json:"snapshot_id"`
	WarningCode     string `json:"warning_code" gorm:"type:varchar(64)"`
}

func GetAllPriceSources() ([]*PriceSource, error) {
	var sources []*PriceSource
	err := DB.Order("id asc").Find(&sources).Error
	return sources, err
}

// CountSchedulablePriceSources counts sources that are both enabled and
// scheduled, so the background task creates no rows when nothing is scheduled.
func CountSchedulablePriceSources() (int64, error) {
	var count int64
	err := DB.Model(&PriceSource{}).Where("enabled = ? AND schedule_enabled = ?", true, true).Count(&count).Error
	return count, err
}

func GetPriceSourceById(id int) (*PriceSource, error) {
	if id <= 0 {
		return nil, errors.New("invalid price source id")
	}
	source := &PriceSource{}
	if err := DB.First(source, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return source, nil
}

func InsertPriceSource(source *PriceSource) error {
	now := common.GetTimestamp()
	source.ConfigRevision = 1
	source.CreatedTime = now
	source.UpdatedTime = now
	return DB.Create(source).Error
}

// UpdatePriceSourceCAS persists configuration changes with an optimistic
// config-revision check. Every accepted update increments config_revision so
// outstanding preview tokens become invalid (spec §8.1).
func UpdatePriceSourceCAS(source *PriceSource, expectedRevision int64) error {
	result := DB.Model(&PriceSource{}).
		Where("id = ? AND config_revision = ?", source.Id, expectedRevision).
		Updates(map[string]interface{}{
			"name":                      source.Name,
			"adapter_key":               source.AdapterKey,
			"role":                      source.Role,
			"scope":                     source.Scope,
			"channel_id":                source.ChannelId,
			"enabled":                   source.Enabled,
			"schedule_enabled":          source.ScheduleEnabled,
			"schedule_interval_seconds": source.ScheduleIntervalSeconds,
			"settings":                  source.Settings,
			"config_revision":           expectedRevision + 1,
			"updated_time":              common.GetTimestamp(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPriceSyncConflict
	}
	source.ConfigRevision = expectedRevision + 1
	return nil
}

func GetPriceSyncRunById(id int) (*PriceSyncRun, error) {
	run := &PriceSyncRun{}
	if err := DB.First(run, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return run, nil
}

// GetRecentPriceSyncRuns returns a source's most recent runs, newest first.
// Run id is the ordering authority (spec §7.3).
func GetRecentPriceSyncRuns(sourceId int, limit int) ([]*PriceSyncRun, error) {
	if limit <= 0 {
		return nil, nil
	}
	var runs []*PriceSyncRun
	err := DB.Where("source_id = ?", sourceId).Order("id desc").Limit(limit).Find(&runs).Error
	return runs, err
}

// GetRecentSuccessfulPriceSyncRuns returns a source's most recent runs that
// advanced last_success_run_id (succeeded or partial), newest first.
func GetRecentSuccessfulPriceSyncRuns(sourceId int, limit int) ([]*PriceSyncRun, error) {
	if limit <= 0 {
		return nil, nil
	}
	var runs []*PriceSyncRun
	err := DB.Where("source_id = ? AND status IN ?", sourceId, []string{PriceSyncRunStatusSucceeded, PriceSyncRunStatusPartial}).
		Order("id desc").
		Limit(limit).
		Find(&runs).Error
	return runs, err
}

// GetPriceSyncRunsByIds loads the named runs in one query so a list view can
// annotate every source with its last successful run without issuing a query
// per source.
func GetPriceSyncRunsByIds(ids []int) ([]*PriceSyncRun, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var runs []*PriceSyncRun
	err := DB.Where("id IN ?", ids).Find(&runs).Error
	return runs, err
}

func GetPriceSyncRunItems(runId int) ([]*PriceSyncRunItem, error) {
	var items []*PriceSyncRunItem
	err := DB.Where("run_id = ?", runId).Order("id asc").Find(&items).Error
	return items, err
}

func GetPriceSnapshotsByIds(ids []int) ([]*PriceSnapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var snapshots []*PriceSnapshot
	err := DB.Where("id IN ?", ids).Find(&snapshots).Error
	return snapshots, err
}

// priceSnapshotLookupBatchSize bounds how many models one lookup query covers,
// so a run with thousands of missing models issues a few bounded queries
// instead of one unbounded condition list.
const priceSnapshotLookupBatchSize = 200

// GetLatestPriceSnapshotsForModels returns the most recently observed snapshot
// of each named source model, regardless of whether it is still current. It is
// what the catalog shows for models a source stopped returning.
//
// Selection follows run id, the ordering authority (spec §7.3): the highest
// last_seen_run_id wins, and the lowest snapshot id breaks a tie between rows
// that share it. Models with no snapshot at all are absent from the result.
//
// The lookup is deliberately two steps — first each model's highest run id,
// then only the rows at those exact (model, run) pairs — so a model with a long
// observation history never loads that history into memory.
func GetLatestPriceSnapshotsForModels(sourceId int, sourceModelNames []string) (map[string]*PriceSnapshot, error) {
	names := make([]string, 0, len(sourceModelNames))
	seen := make(map[string]bool, len(sourceModelNames))
	for _, name := range sourceModelNames {
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil
	}

	type latestObservation struct {
		SourceModelName  string
		MaxLastSeenRunId int
	}
	observations := make([]latestObservation, 0, len(names))
	for batch := range slices.Chunk(names, priceSnapshotLookupBatchSize) {
		var rows []latestObservation
		if err := DB.Model(&PriceSnapshot{}).
			Select("source_model_name, MAX(last_seen_run_id) AS max_last_seen_run_id").
			Where("source_id = ? AND source_model_name IN ?", sourceId, batch).
			Group("source_model_name").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		observations = append(observations, rows...)
	}

	latest := make(map[string]*PriceSnapshot, len(observations))
	for batch := range slices.Chunk(observations, priceSnapshotLookupBatchSize) {
		conditions := make([]string, 0, len(batch))
		args := make([]interface{}, 0, len(batch)*2)
		for _, observation := range batch {
			conditions = append(conditions, "(source_model_name = ? AND last_seen_run_id = ?)")
			args = append(args, observation.SourceModelName, observation.MaxLastSeenRunId)
		}
		var snapshots []*PriceSnapshot
		if err := DB.Where("source_id = ?", sourceId).
			Where("("+strings.Join(conditions, " OR ")+")", args...).
			Order("id asc").
			Find(&snapshots).Error; err != nil {
			return nil, err
		}
		// Every model appears in exactly one pair, and the rows come back by
		// ascending id, so the first row of a model is the lowest id among the
		// rows sharing its highest run id.
		for _, snapshot := range snapshots {
			if _, ok := latest[snapshot.SourceModelName]; !ok {
				latest[snapshot.SourceModelName] = snapshot
			}
		}
	}
	return latest, nil
}

// PriceSyncCommitItem is one prepared run item. Snapshot is only set for
// status=valid items on runs that will persist snapshots.
type PriceSyncCommitItem struct {
	SourceModelName string
	Status          string
	WarningCode     string
	Snapshot        *PriceSnapshot
}

// PriceSyncCommit is the prepared input of one commit transaction.
type PriceSyncCommit struct {
	SourceId               int
	ExpectedConfigRevision int64
	ExpectedBaseRunId      *int
	Run                    PriceSyncRun
	Items                  []PriceSyncCommitItem
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// truncateSummary caps an error summary at 255 bytes without splitting a
// UTF-8 sequence.
func truncateSummary(s string) string {
	const maxSummaryBytes = 255
	if len(s) <= maxSummaryBytes {
		return s
	}
	cut := maxSummaryBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// CommitPriceSync writes one sync run, its items, and (for non-failed runs)
// its snapshots in a single transaction, serialized per source through
// lockForUpdate on the PriceSource row (spec §7.3/§8.1). On SQLite the helper
// takes no row lock and the contract degrades to conflicting transactions
// failing; any GORM error rolls the whole commit back.
//
// Inside the lock it CAS-checks config_revision and last_success_run_id
// against the values captured at preview time; a mismatch returns
// ErrPriceSyncConflict and writes nothing.
func CommitPriceSync(commit *PriceSyncCommit) (*PriceSyncRun, error) {
	if commit == nil {
		return nil, errors.New("nil price sync commit")
	}
	run := commit.Run
	err := DB.Transaction(func(tx *gorm.DB) error {
		var source PriceSource
		if err := lockForUpdate(tx).First(&source, "id = ?", commit.SourceId).Error; err != nil {
			return err
		}
		if source.ConfigRevision != commit.ExpectedConfigRevision {
			return ErrPriceSyncConflict
		}
		if !intPtrEqual(source.LastSuccessRunId, commit.ExpectedBaseRunId) {
			return ErrPriceSyncConflict
		}
		// Authoritative orphan/disabled-channel check inside the locked
		// transaction: a channel deleted or disabled between fetch and commit
		// rolls the whole commit back. The service-layer pre-check is only a
		// fast fail.
		if source.Role == PriceSourceRoleSupplierCost {
			if source.ChannelId == nil {
				return ErrPriceSourceOrphaned
			}
			// Row-lock the channel with the standard helper so a concurrent
			// channel write serializes against this commit on MySQL/PostgreSQL;
			// the SQLite branch skips the lock and relies on write-conflict
			// rollback.
			var channel Channel
			if err := lockForUpdate(tx).Select("id", "status").First(&channel, "id = ?", *source.ChannelId).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrPriceSourceOrphaned
				}
				return err
			}
			if channel.Status != common.ChannelStatusEnabled {
				return ErrPriceSourceChannelDisabled
			}
		}

		now := common.GetTimestamp()
		run.SourceId = commit.SourceId
		// Preserve the revision captured when the fetch actually ran; the CAS
		// above guarantees it still matches the locked row.
		if run.SourceConfigRevision == 0 {
			run.SourceConfigRevision = source.ConfigRevision
		}
		if run.FinishedAt == nil {
			run.FinishedAt = &now
		}
		run.ErrorSummary = truncateSummary(run.ErrorSummary)
		if err := tx.Create(&run).Error; err != nil {
			return err
		}

		persistSnapshots := run.Status == PriceSyncRunStatusSucceeded || run.Status == PriceSyncRunStatusPartial
		newSnapshots := 0
		idempotentHits := 0
		for i := range commit.Items {
			item := commit.Items[i]
			runItem := PriceSyncRunItem{
				RunId:           run.Id,
				SourceModelName: item.SourceModelName,
				Status:          item.Status,
				WarningCode:     item.WarningCode,
			}
			if persistSnapshots && item.Status == PriceSyncItemStatusValid {
				if item.Snapshot == nil {
					return fmt.Errorf("valid item %q has no snapshot payload", item.SourceModelName)
				}
				snapshotId, isNew, err := upsertPriceSnapshot(tx, item.Snapshot, run.Id, now)
				if err != nil {
					return err
				}
				if isNew {
					newSnapshots++
				} else {
					idempotentHits++
				}
				runItem.SnapshotId = &snapshotId
			}
			if err := tx.Create(&runItem).Error; err != nil {
				return err
			}
		}

		run.NewSnapshotCount = newSnapshots
		run.IdempotentHitCount = idempotentHits
		if err := tx.Model(&PriceSyncRun{}).Where("id = ?", run.Id).Updates(map[string]interface{}{
			"new_snapshot_count":   newSnapshots,
			"idempotent_hit_count": idempotentHits,
		}).Error; err != nil {
			return err
		}

		if !persistSnapshots {
			return stampPriceSourceFailure(tx, commit.SourceId, run.ErrorSummary, now)
		}
		return tx.Model(&PriceSource{}).Where("id = ?", commit.SourceId).Updates(map[string]interface{}{
			"last_success_run_id": run.Id,
			"last_success_at":     now,
			"updated_time":        now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// RecordPriceSourceFailure stamps a source's failure time and summary for an
// attempt that produced no run at all — a preflight refusal, an unavailable
// adapter, or a commit transaction that never wrote its run. The scheduler's
// due check reads last_error_at, so an attempt that leaves no run must still
// leave a timestamp or the source retries on every wake (spec §8.4).
func RecordPriceSourceFailure(sourceId int, errorSummary string) error {
	return stampPriceSourceFailure(DB, sourceId, errorSummary, common.GetTimestamp())
}

// stampPriceSourceFailure records a failed sync attempt on the source row: the
// failure time, its bounded summary, and the row's update time. It runs on the
// caller's handle, so a failed commit stamps the source inside the very
// transaction that wrote its failed run and both roll back together (spec
// §8.4).
func stampPriceSourceFailure(tx *gorm.DB, sourceId int, errorSummary string, now int64) error {
	return tx.Model(&PriceSource{}).Where("id = ?", sourceId).Updates(map[string]interface{}{
		"last_error_at":      now,
		"last_error_summary": truncateSummary(errorSummary),
		"updated_time":       now,
	}).Error
}

// upsertPriceSnapshot implements fingerprint idempotency: an existing
// (source_id, source_model_name, fingerprint) row only gets its observation
// evidence fields refreshed; every other field stays immutable (spec §4.3).
func upsertPriceSnapshot(tx *gorm.DB, snapshot *PriceSnapshot, runId int, now int64) (int, bool, error) {
	var existing PriceSnapshot
	err := tx.Where(
		"source_id = ? AND source_model_name = ? AND fingerprint = ?",
		snapshot.SourceId, snapshot.SourceModelName, snapshot.Fingerprint,
	).First(&existing).Error
	if err == nil {
		updateErr := tx.Model(&PriceSnapshot{}).Where("id = ?", existing.Id).Updates(map[string]interface{}{
			"last_seen_at":     now,
			"last_seen_run_id": runId,
		}).Error
		if updateErr != nil {
			return 0, false, updateErr
		}
		return existing.Id, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, err
	}
	fresh := *snapshot
	fresh.Id = 0
	fresh.FetchedAt = now
	fresh.LastSeenAt = now
	fresh.LastSeenRunId = runId
	fresh.CreatedTime = now
	if err := tx.Create(&fresh).Error; err != nil {
		return 0, false, err
	}
	return fresh.Id, true, nil
}

// RecordFailedPriceSyncRun persists a run for a sync attempt that failed
// before any snapshot could be considered (fetch error, gate refusal outside
// the commit path). It never advances last_success_run_id.
func RecordFailedPriceSyncRun(sourceId int, run PriceSyncRun) (*PriceSyncRun, error) {
	err := DB.Transaction(func(tx *gorm.DB) error {
		var source PriceSource
		if err := lockForUpdate(tx).First(&source, "id = ?", sourceId).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		run.SourceId = sourceId
		run.Status = PriceSyncRunStatusFailed
		// Keep the revision in effect when the failed fetch actually ran; only
		// fall back to the current row value when the caller had none.
		if run.SourceConfigRevision == 0 {
			run.SourceConfigRevision = source.ConfigRevision
		}
		if run.FinishedAt == nil {
			run.FinishedAt = &now
		}
		run.ErrorSummary = truncateSummary(run.ErrorSummary)
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		return stampPriceSourceFailure(tx, sourceId, run.ErrorSummary, now)
	})
	if err != nil {
		return nil, err
	}
	return &run, nil
}
