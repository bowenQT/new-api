package model

import (
	"os"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// testUpstreamPriceCatalogMigration proves the price-catalog schema contract
// on one database engine (spec §18.2): AutoMigrate succeeds, both composite
// unique keys are enforced, and a second AutoMigrate issues no
// schema-mutating DDL.
func testUpstreamPriceCatalogMigration(t *testing.T, db *gorm.DB, recorder *migrationSQLRecorder, strictRemigration bool) {
	t.Helper()

	models := []any{&PriceSource{}, &PriceSnapshot{}, &PriceSyncRun{}, &PriceSyncRunItem{}}
	require.NoError(t, db.Migrator().DropTable(models...))
	require.NoError(t, db.AutoMigrate(models...))

	fingerprint := strings.Repeat("a", 64)
	snapshot := PriceSnapshot{
		SourceId:           1,
		SourceModelName:    "openai/gpt-5.6-luna",
		CanonicalModelName: "gpt-5.6-luna",
		Role:               "supplier_cost",
		Scope:              "public",
		Provider:           "openai",
		MappingStatus:      "mapped_default",
		Currency:           "USD",
		FormulaKind:        "token_expr_v1",
		PriceExpr:          `tier("base", p * 0.2 + c * 1.2)`,
		ExprVersion:        "v1",
		FetchedAt:          1,
		LastSeenAt:         1,
		LastSeenRunId:      1,
		Fingerprint:        fingerprint,
		FingerprintVersion: "fp1",
		CreatedTime:        1,
	}
	require.NoError(t, db.Create(&snapshot).Error)

	duplicate := snapshot
	duplicate.Id = 0
	require.Error(t, db.Create(&duplicate).Error,
		"source_id + source_model_name + fingerprint unique key must reject a duplicate")

	differentFingerprint := snapshot
	differentFingerprint.Id = 0
	differentFingerprint.Fingerprint = strings.Repeat("b", 64)
	require.NoError(t, db.Create(&differentFingerprint).Error)

	// The nullable evidence columns must round-trip on every engine, both left
	// unset (a run written before the column existed reads back as the zero
	// value, never a scan failure) and carrying a value.
	blankRun := PriceSyncRun{SourceId: 1, Status: PriceSyncRunStatusSucceeded, AdapterKey: "vercel_gateway", StartedAt: 1}
	require.NoError(t, db.Create(&blankRun).Error)
	var readBlank PriceSyncRun
	require.NoError(t, db.First(&readBlank, blankRun.Id).Error)
	assert.Nil(t, readBlank.CoverageDropExceeded)
	assert.Empty(t, readBlank.PriceJumpSummary)

	gateRefused := true
	summary := `{"version":1,"probe_version":1,"threshold":0.5,"total":1,"entries":[` +
		`{"source_model_name":"openai/gpt-5.6-luna","canonical_model_name":"gpt-5.6-luna",` +
		`"dimension":"input","previous_usd":0.2,"current_usd":2,"change_rate":9}]}`
	evidenceRun := PriceSyncRun{
		SourceId:             1,
		Status:               PriceSyncRunStatusPartial,
		AdapterKey:           "vercel_gateway",
		StartedAt:            2,
		CoverageDropExceeded: &gateRefused,
		PriceJumpSummary:     summary,
	}
	require.NoError(t, db.Create(&evidenceRun).Error)
	var readEvidence PriceSyncRun
	require.NoError(t, db.First(&readEvidence, evidenceRun.Id).Error)
	require.NotNil(t, readEvidence.CoverageDropExceeded)
	assert.True(t, *readEvidence.CoverageDropExceeded)
	assert.Equal(t, summary, readEvidence.PriceJumpSummary)

	item := PriceSyncRunItem{RunId: 1, SourceModelName: "openai/gpt-5.6-luna", Status: PriceSyncItemStatusValid}
	require.NoError(t, db.Create(&item).Error)
	duplicateItem := PriceSyncRunItem{RunId: 1, SourceModelName: "openai/gpt-5.6-luna", Status: PriceSyncItemStatusRejected}
	require.Error(t, db.Create(&duplicateItem).Error,
		"run_id + source_model_name unique key must reject a duplicate run item")

	recorder.reset()
	require.NoError(t, db.AutoMigrate(models...))
	if strictRemigration {
		assert.Empty(t, recorder.schemaMutations(), "a second migration must not repeat schema DDL")
	} else {
		// glebarez/sqlite misreports columns of composite unique indexes as
		// needing an alter, so a re-migration rebuilds these tables (the
		// pre-existing PerfMetric/CasbinRule composite indexes behave the
		// same). Assert the rebuild keeps data and the unique keys intact.
		var snapshotCount int64
		require.NoError(t, db.Model(&PriceSnapshot{}).Count(&snapshotCount).Error)
		assert.EqualValues(t, 2, snapshotCount, "re-migration must preserve existing rows")
		stillDuplicate := snapshot
		stillDuplicate.Id = 0
		require.Error(t, db.Create(&stillDuplicate).Error,
			"unique key must survive the re-migration")
	}

	require.NoError(t, db.Migrator().DropTable(models...))
}

func TestUpstreamPriceCatalogMigrationSQLite(t *testing.T) {
	recorder := &migrationSQLRecorder{}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: recorder})
	require.NoError(t, err)
	testUpstreamPriceCatalogMigration(t, db, recorder, false)
}

func TestUpstreamPriceCatalogMigrationConfiguredDatabases(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		dialector func(string) gorm.Dialector
	}{
		{name: "mysql", env: "TEST_MYSQL_DSN", dialector: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{name: "postgres", env: "TEST_POSTGRES_DSN", dialector: func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(test.env))
			if dsn == "" {
				t.Skip(test.env + " is not configured")
			}
			recorder := &migrationSQLRecorder{}
			db, err := gorm.Open(test.dialector(dsn), &gorm.Config{Logger: recorder})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			testUpstreamPriceCatalogMigration(t, db, recorder, true)
		})
	}
}
