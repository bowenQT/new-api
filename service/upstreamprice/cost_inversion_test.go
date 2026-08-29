package upstreamprice

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// captureSysLog redirects the writer common.SysError and the catalog alert log
// share, so a test can assert on what the post-write alerting actually wrote.
func captureSysLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	common.LogWriterMu.Lock()
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = buffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previous
		common.LogWriterMu.Unlock()
	})
	return buffer
}

// countQueriesOnTable counts the queries a test issues against one table, so a
// test can assert how many times a projection was actually executed rather than
// how long it took.
func countQueriesOnTable(t *testing.T, db *gorm.DB, table string) *int {
	t.Helper()
	count := 0
	name := "upstreamprice_test:count:" + table
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), table) {
			count++
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Query().After("gorm:query").Remove(name)
	})
	return &count
}

// seedInvertedCostSource writes one supplier cost source whose projected cost
// is far above the model's projected sale price, which is what the cost
// inversion check exists to report.
func seedInvertedCostSource(t *testing.T, db *gorm.DB, canonicalModel string) {
	t.Helper()
	channel := &model.Channel{Name: "cost-channel", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	seedCatalogSnapshot(t, db, "inverted-cost", RoleSupplierCost, &channel.Id, canonicalModel,
		`tier("base", p * 30 + c * 30)`, FormulaKindTokenExprV1, "", common.GetTimestamp())
}

// TestLogCostInversionAlertsSharesOneCatalogProjection pins the cost and the
// coverage of the post-write check: batching exists because a request-sized
// model list is the unit the comparison is specified in, not because each batch
// is a fresh comparison. One catalog projection serves every batch, and a model
// that lands in a later batch is still reported.
func TestLogCostInversionAlertsSharesOneCatalogProjection(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	seedInvertedCostSource(t, db, "tiered-model")

	// More models than one request may carry, with the inverted one last so it
	// is only reached by the second batch.
	canonicalModels := make([]string, 0, dto.MaxCompareModelsRequested+1)
	for i := 0; i < dto.MaxCompareModelsRequested; i++ {
		canonicalModels = append(canonicalModels, fmt.Sprintf("filler-model-%d", i))
	}
	canonicalModels = append(canonicalModels, "tiered-model")

	logs := captureSysLog(t)
	runItemQueries := countQueriesOnTable(t, db, "price_sync_run_items")
	logCostInversionAlerts(context.Background(), canonicalModels)

	assert.Equal(t, 1, *runItemQueries,
		"the catalog must be projected once for the whole check, not once per model batch")
	assert.Contains(t, logs.String(), "upstream price catalog alert: code="+AlertCostInversion,
		"an inversion in a later batch must still be reported")
	assert.Contains(t, logs.String(), `model="tiered-model"`)
}

// TestLogCostInversionAlertsRefusesUnusableGroupRatio keeps the refusal path
// the check shares with the comparison endpoint: a default group whose ratio is
// not a usable multiplier is a recorded failure, never a silent skip that would
// leave a corrupt margin unreported.
func TestLogCostInversionAlertsRefusesUnusableGroupRatio(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	seedInvertedCostSource(t, db, "tiered-model")
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default": -1}`))

	logs := captureSysLog(t)
	logCostInversionAlerts(context.Background(), []string{"tiered-model"})

	assert.Contains(t, logs.String(),
		`upstream price cost inversion check failed: group "default" has an unusable group ratio`)
	assert.NotContains(t, logs.String(), "upstream price catalog alert: code="+AlertCostInversion)
}

// TestLogCostInversionAlertsSkipsEmptyModelSet keeps a write that made no model
// current from projecting the catalog at all.
func TestLogCostInversionAlertsSkipsEmptyModelSet(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	seedInvertedCostSource(t, db, "tiered-model")

	sourceQueries := countQueriesOnTable(t, db, "price_sources")
	logCostInversionAlerts(context.Background(), nil)
	assert.Zero(t, *sourceQueries)
}
