package model

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUpstreamPriceDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB, LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&Channel{}, &PriceSource{}, &PriceSnapshot{}, &PriceSyncRun{}, &PriceSyncRunItem{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		_ = sqlDB.Close()
	})
	return db
}

func testPriceSource(t *testing.T) *PriceSource {
	t.Helper()
	channel := &Channel{
		Name:   "price-source-channel",
		Key:    "test-key",
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, DB.Create(channel).Error)
	source := &PriceSource{
		Name:       "vercel-cost",
		AdapterKey: "vercel_gateway",
		Role:       PriceSourceRoleSupplierCost,
		Scope:      "public",
		ChannelId:  &channel.Id,
		Enabled:    true,
	}
	require.NoError(t, InsertPriceSource(source))
	return source
}

func testSnapshot(sourceId int, sourceModelName, fingerprint, priceExpr string) *PriceSnapshot {
	return &PriceSnapshot{
		SourceId:           sourceId,
		SourceModelName:    sourceModelName,
		CanonicalModelName: strings.TrimPrefix(sourceModelName, "openai/"),
		Role:               "supplier_cost",
		Scope:              "public",
		Provider:           "openai",
		MappingStatus:      "mapped_default",
		Currency:           "USD",
		FormulaKind:        "token_expr_v1",
		PriceExpr:          priceExpr,
		ExprVersion:        "v1",
		Fingerprint:        fingerprint,
		FingerprintVersion: "fp1",
	}
}

func commitValidRun(t *testing.T, source *PriceSource, baseRunId *int, fingerprint, priceExpr string) *PriceSyncRun {
	t.Helper()
	run, err := CommitPriceSync(&PriceSyncCommit{
		SourceId:               source.Id,
		ExpectedConfigRevision: source.ConfigRevision,
		ExpectedBaseRunId:      baseRunId,
		Run: PriceSyncRun{
			Status:          PriceSyncRunStatusSucceeded,
			AdapterKey:      source.AdapterKey,
			StartedAt:       common.GetTimestamp(),
			DiscoveredCount: 1,
			ValidCount:      1,
		},
		Items: []PriceSyncCommitItem{{
			SourceModelName: "openai/gpt-5.6-luna",
			Status:          PriceSyncItemStatusValid,
			Snapshot:        testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprint, priceExpr),
		}},
	})
	require.NoError(t, err)
	return run
}

const (
	fingerprintA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fingerprintB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestUpstreamPriceTablesMigrateWithUniqueFingerprint(t *testing.T) {
	db := setupUpstreamPriceDB(t)

	for _, table := range []interface{}{&PriceSource{}, &PriceSnapshot{}, &PriceSyncRun{}, &PriceSyncRunItem{}} {
		assert.True(t, db.Migrator().HasTable(table))
	}

	source := testPriceSource(t)
	first := testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprintA, "p * 1")
	first.FetchedAt = 1
	first.LastSeenAt = 1
	first.LastSeenRunId = 1
	require.NoError(t, db.Create(first).Error)

	// Same (source_id, source_model_name, fingerprint) must violate the
	// composite unique key.
	duplicate := testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprintA, "p * 1")
	require.Error(t, db.Create(duplicate).Error)

	// A different fingerprint for the same model is a new row.
	other := testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprintB, "p * 2")
	require.NoError(t, db.Create(other).Error)
}

func TestCommitPriceSyncFingerprintIdempotency(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)

	run1 := commitValidRun(t, source, nil, fingerprintA, "p * 1")
	assert.Equal(t, 1, run1.NewSnapshotCount)
	assert.Equal(t, 0, run1.IdempotentHitCount)

	var original PriceSnapshot
	require.NoError(t, db.First(&original, "fingerprint = ?", fingerprintA).Error)
	assert.Equal(t, run1.Id, original.LastSeenRunId)

	// Same content again: no new row, observation evidence advances.
	run2 := commitValidRun(t, source, &run1.Id, fingerprintA, "p * 1")
	assert.Equal(t, 0, run2.NewSnapshotCount)
	assert.Equal(t, 1, run2.IdempotentHitCount)

	var snapshots []PriceSnapshot
	require.NoError(t, db.Find(&snapshots).Error)
	require.Len(t, snapshots, 1)
	assert.Equal(t, run2.Id, snapshots[0].LastSeenRunId)
	assert.GreaterOrEqual(t, snapshots[0].LastSeenAt, original.LastSeenAt)
	// Immutable content fields stay untouched.
	assert.Equal(t, original.FetchedAt, snapshots[0].FetchedAt)
	assert.Equal(t, original.CreatedTime, snapshots[0].CreatedTime)
	assert.Equal(t, original.PriceExpr, snapshots[0].PriceExpr)

	reloaded, err := GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, run2.Id, *reloaded.LastSuccessRunId)
}

func TestCommitPriceSyncOscillationABA(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)

	run1 := commitValidRun(t, source, nil, fingerprintA, "p * 1")
	run2 := commitValidRun(t, source, &run1.Id, fingerprintB, "p * 2")
	run3 := commitValidRun(t, source, &run2.Id, fingerprintA, "p * 1")

	// A→B→A: returning to A hits the original A snapshot, no third row.
	var snapshots []PriceSnapshot
	require.NoError(t, db.Order("id asc").Find(&snapshots).Error)
	require.Len(t, snapshots, 2)
	assert.Equal(t, 0, run3.NewSnapshotCount)
	assert.Equal(t, 1, run3.IdempotentHitCount)

	// Current price follows run semantics: last_seen_run_id of A aligns with
	// last_success_run_id, so the current price is A again.
	reloaded, err := GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, run3.Id, *reloaded.LastSuccessRunId)

	items, err := GetPriceSyncRunItems(run3.Id)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].SnapshotId)
	current, err := GetPriceSnapshotsByIds([]int{*items[0].SnapshotId})
	require.NoError(t, err)
	require.Len(t, current, 1)
	assert.Equal(t, fingerprintA, current[0].Fingerprint)
	assert.Equal(t, run3.Id, current[0].LastSeenRunId)

	// The B snapshot still exists but stopped being current.
	var snapshotB PriceSnapshot
	require.NoError(t, db.First(&snapshotB, "fingerprint = ?", fingerprintB).Error)
	assert.Equal(t, run2.Id, snapshotB.LastSeenRunId)
	assert.NotEqual(t, *reloaded.LastSuccessRunId, snapshotB.LastSeenRunId)
}

func TestCommitPriceSyncRunItemStatuses(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)

	run, err := CommitPriceSync(&PriceSyncCommit{
		SourceId:               source.Id,
		ExpectedConfigRevision: source.ConfigRevision,
		ExpectedBaseRunId:      nil,
		Run: PriceSyncRun{
			Status:           PriceSyncRunStatusPartial,
			AdapterKey:       source.AdapterKey,
			StartedAt:        common.GetTimestamp(),
			DiscoveredCount:  3,
			ValidCount:       1,
			UnsupportedCount: 1,
			RejectedCount:    1,
			MissingCount:     1,
		},
		Items: []PriceSyncCommitItem{
			{
				SourceModelName: "openai/gpt-5.6-luna",
				Status:          PriceSyncItemStatusValid,
				Snapshot:        testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprintA, "p * 1"),
			},
			{SourceModelName: "vendor/video-model", Status: PriceSyncItemStatusUnsupported, WarningCode: "no_token_pricing"},
			{SourceModelName: "vendor/broken-model", Status: PriceSyncItemStatusRejected, WarningCode: "invalid_tiers"},
			{SourceModelName: "vendor/gone-model", Status: PriceSyncItemStatusMissing},
		},
	})
	require.NoError(t, err)

	items, err := GetPriceSyncRunItems(run.Id)
	require.NoError(t, err)
	require.Len(t, items, 4)
	byStatus := map[string]*PriceSyncRunItem{}
	for _, item := range items {
		byStatus[item.Status] = item
	}
	// Only the valid item points at a snapshot; unsupported and rejected
	// items never become missing and carry their warning codes.
	require.NotNil(t, byStatus[PriceSyncItemStatusValid].SnapshotId)
	assert.Nil(t, byStatus[PriceSyncItemStatusUnsupported].SnapshotId)
	assert.Equal(t, "no_token_pricing", byStatus[PriceSyncItemStatusUnsupported].WarningCode)
	assert.Nil(t, byStatus[PriceSyncItemStatusRejected].SnapshotId)
	assert.Nil(t, byStatus[PriceSyncItemStatusMissing].SnapshotId)

	// Partial runs advance last_success_run_id.
	reloaded, err := GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, run.Id, *reloaded.LastSuccessRunId)

	var snapshotCount int64
	require.NoError(t, db.Model(&PriceSnapshot{}).Count(&snapshotCount).Error)
	assert.EqualValues(t, 1, snapshotCount)
}

func TestCommitPriceSyncFailedRunDoesNotAdvance(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	run1 := commitValidRun(t, source, nil, fingerprintA, "p * 1")

	failed, err := CommitPriceSync(&PriceSyncCommit{
		SourceId:               source.Id,
		ExpectedConfigRevision: source.ConfigRevision,
		ExpectedBaseRunId:      &run1.Id,
		Run: PriceSyncRun{
			Status:       PriceSyncRunStatusFailed,
			AdapterKey:   source.AdapterKey,
			StartedAt:    common.GetTimestamp(),
			ErrorSummary: "no valid observations",
		},
		Items: []PriceSyncCommitItem{
			{SourceModelName: "openai/gpt-5.6-luna", Status: PriceSyncItemStatusMissing},
		},
	})
	require.NoError(t, err)

	// Failed runs write no snapshots and never advance the success pointer.
	var snapshotCount int64
	require.NoError(t, db.Model(&PriceSnapshot{}).Count(&snapshotCount).Error)
	assert.EqualValues(t, 1, snapshotCount)

	reloaded, err := GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, run1.Id, *reloaded.LastSuccessRunId)
	require.NotNil(t, reloaded.LastErrorAt)
	assert.Equal(t, "no valid observations", reloaded.LastErrorSummary)
	assert.Equal(t, PriceSyncRunStatusFailed, failed.Status)
}

func TestCommitPriceSyncCASConflicts(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	run1 := commitValidRun(t, source, nil, fingerprintA, "p * 1")

	// Stale config revision is refused and writes nothing.
	_, err := CommitPriceSync(&PriceSyncCommit{
		SourceId:               source.Id,
		ExpectedConfigRevision: source.ConfigRevision + 1,
		ExpectedBaseRunId:      &run1.Id,
		Run:                    PriceSyncRun{Status: PriceSyncRunStatusSucceeded, ValidCount: 1},
		Items: []PriceSyncCommitItem{{
			SourceModelName: "openai/gpt-5.6-luna",
			Status:          PriceSyncItemStatusValid,
			Snapshot:        testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprintB, "p * 2"),
		}},
	})
	require.ErrorIs(t, err, ErrPriceSyncConflict)

	// Stale base run id (nil vs run1) is refused too.
	_, err = CommitPriceSync(&PriceSyncCommit{
		SourceId:               source.Id,
		ExpectedConfigRevision: source.ConfigRevision,
		ExpectedBaseRunId:      nil,
		Run:                    PriceSyncRun{Status: PriceSyncRunStatusSucceeded, ValidCount: 1},
		Items: []PriceSyncCommitItem{{
			SourceModelName: "openai/gpt-5.6-luna",
			Status:          PriceSyncItemStatusValid,
			Snapshot:        testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprintB, "p * 2"),
		}},
	})
	require.ErrorIs(t, err, ErrPriceSyncConflict)

	// The whole commit rolled back: one run, one snapshot, one item set.
	var runCount, snapshotCount int64
	require.NoError(t, db.Model(&PriceSyncRun{}).Count(&runCount).Error)
	require.NoError(t, db.Model(&PriceSnapshot{}).Count(&snapshotCount).Error)
	assert.EqualValues(t, 1, runCount)
	assert.EqualValues(t, 1, snapshotCount)
}

func TestUpdatePriceSourceCASBumpsRevision(t *testing.T) {
	setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	require.EqualValues(t, 1, source.ConfigRevision)

	source.Name = "renamed"
	require.NoError(t, UpdatePriceSourceCAS(source, 1))
	assert.EqualValues(t, 2, source.ConfigRevision)

	// A stale expected revision conflicts.
	source.Name = "renamed-again"
	require.ErrorIs(t, UpdatePriceSourceCAS(source, 1), ErrPriceSyncConflict)

	reloaded, err := GetPriceSourceById(source.Id)
	require.NoError(t, err)
	assert.Equal(t, "renamed", reloaded.Name)
	assert.EqualValues(t, 2, reloaded.ConfigRevision)
}

func TestDisabledSourceHistoryRemainsQueryable(t *testing.T) {
	setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	run := commitValidRun(t, source, nil, fingerprintA, "p * 1")

	source.Enabled = false
	require.NoError(t, UpdatePriceSourceCAS(source, source.ConfigRevision))

	// Historical snapshots and run details stay readable after disable.
	snapshot, err := GetLatestPriceSnapshotForModel(source.Id, "openai/gpt-5.6-luna")
	require.NoError(t, err)
	assert.Equal(t, fingerprintA, snapshot.Fingerprint)

	items, err := GetPriceSyncRunItems(run.Id)
	require.NoError(t, err)
	assert.Len(t, items, 1)

	persisted, err := GetPriceSyncRunById(run.Id)
	require.NoError(t, err)
	assert.Equal(t, PriceSyncRunStatusSucceeded, persisted.Status)
}

// buildCommit prepares a fresh valid commit against the given base run.
func buildCommit(source *PriceSource, baseRunId *int, fingerprint string) *PriceSyncCommit {
	return &PriceSyncCommit{
		SourceId:               source.Id,
		ExpectedConfigRevision: source.ConfigRevision,
		ExpectedBaseRunId:      baseRunId,
		Run: PriceSyncRun{
			Status:          PriceSyncRunStatusSucceeded,
			AdapterKey:      source.AdapterKey,
			StartedAt:       common.GetTimestamp(),
			DiscoveredCount: 1,
			ValidCount:      1,
		},
		Items: []PriceSyncCommitItem{{
			SourceModelName: "openai/gpt-5.6-luna",
			Status:          PriceSyncItemStatusValid,
			Snapshot:        testSnapshot(source.Id, "openai/gpt-5.6-luna", fingerprint, "p * 9"),
		}},
	}
}

func TestCommitRefusedWhenChannelDeletedInsideTransaction(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	run1 := commitValidRun(t, source, nil, fingerprintA, "p * 1")

	// Channel disappears between fetch and commit: the in-transaction check
	// refuses the commit and rolls everything back.
	require.NoError(t, db.Delete(&Channel{}, *source.ChannelId).Error)
	_, err := CommitPriceSync(buildCommit(source, &run1.Id, fingerprintB))
	require.ErrorIs(t, err, ErrPriceSourceOrphaned)

	var runCount, itemCount, snapshotCount int64
	require.NoError(t, db.Model(&PriceSyncRun{}).Count(&runCount).Error)
	require.NoError(t, db.Model(&PriceSyncRunItem{}).Count(&itemCount).Error)
	require.NoError(t, db.Model(&PriceSnapshot{}).Count(&snapshotCount).Error)
	assert.EqualValues(t, 1, runCount, "no run residue from the refused commit")
	assert.EqualValues(t, 1, itemCount)
	assert.EqualValues(t, 1, snapshotCount)

	reloaded, err := GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.Equal(t, run1.Id, *reloaded.LastSuccessRunId)
}

func TestCommitRefusedWhenChannelDisabledInsideTransaction(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	run1 := commitValidRun(t, source, nil, fingerprintA, "p * 1")

	require.NoError(t, db.Model(&Channel{}).Where("id = ?", *source.ChannelId).
		Update("status", common.ChannelStatusManuallyDisabled).Error)
	_, err := CommitPriceSync(buildCommit(source, &run1.Id, fingerprintB))
	require.ErrorIs(t, err, ErrPriceSourceChannelDisabled)

	var runCount, snapshotCount int64
	require.NoError(t, db.Model(&PriceSyncRun{}).Count(&runCount).Error)
	require.NoError(t, db.Model(&PriceSnapshot{}).Count(&snapshotCount).Error)
	assert.EqualValues(t, 1, runCount)
	assert.EqualValues(t, 1, snapshotCount)
}

func TestPriceSyncRunItemUniquePerRunAndModel(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	run := commitValidRun(t, source, nil, fingerprintA, "p * 1")

	duplicate := PriceSyncRunItem{
		RunId:           run.Id,
		SourceModelName: "openai/gpt-5.6-luna",
		Status:          PriceSyncItemStatusRejected,
	}
	require.Error(t, db.Create(&duplicate).Error,
		"(run_id, source_model_name) must be unique")
}

// TestConcurrentCommitSameBaseExactlyOneSucceeds exercises the SQLite
// concurrency contract (spec §7.3/§18.2): without row locks, one of two
// conflicting commit transactions fails and rolls back completely.
func TestConcurrentCommitSameBaseExactlyOneSucceeds(t *testing.T) {
	db := setupUpstreamPriceDB(t)
	source := testPriceSource(t)
	run1 := commitValidRun(t, source, nil, fingerprintA, "p * 1")

	fingerprints := []string{
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = CommitPriceSync(buildCommit(source, &run1.Id, fingerprints[i]))
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "exactly one concurrent commit must win, got errors: %v", errs)

	// The loser leaves no run/item/snapshot residue.
	var runCount, itemCount, snapshotCount int64
	require.NoError(t, db.Model(&PriceSyncRun{}).Count(&runCount).Error)
	require.NoError(t, db.Model(&PriceSyncRunItem{}).Count(&itemCount).Error)
	require.NoError(t, db.Model(&PriceSnapshot{}).Count(&snapshotCount).Error)
	assert.EqualValues(t, 2, runCount)
	assert.EqualValues(t, 2, itemCount)
	assert.EqualValues(t, 2, snapshotCount)

	reloaded, err := GetPriceSourceById(source.Id)
	require.NoError(t, err)
	require.NotNil(t, reloaded.LastSuccessRunId)
	assert.NotEqual(t, run1.Id, *reloaded.LastSuccessRunId)
}

func TestTruncateSummaryRespectsUTF8Boundaries(t *testing.T) {
	short := "plain error"
	assert.Equal(t, short, truncateSummary(short))

	long := strings.Repeat("配", 100) // 300 bytes of 3-byte runes
	truncated := truncateSummary(long)
	assert.LessOrEqual(t, len(truncated), 255)
	assert.True(t, utf8.ValidString(truncated), "truncation must not split a UTF-8 sequence")
	assert.Equal(t, strings.Repeat("配", 85), truncated)
}
