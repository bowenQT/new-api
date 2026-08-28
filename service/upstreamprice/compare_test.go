package upstreamprice

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupCompareTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.PriceSource{},
		&model.PriceSnapshot{},
		&model.PriceSyncRun{},
		&model.PriceSyncRunItem{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		_ = sqlDB.Close()
	})
	return db
}

// setupCompareSaleConfig installs a known sale-pricing configuration and
// restores the process-wide configuration afterwards.
func setupCompareSaleConfig(t *testing.T) {
	t.Helper()
	savedModelRatio := ratio_setting.ModelRatio2JSONString()
	savedCompletionRatio := ratio_setting.CompletionRatio2JSONString()
	savedCacheRatio := ratio_setting.CacheRatio2JSONString()
	savedModelPrice := ratio_setting.ModelPrice2JSONString()
	savedGroupRatio := ratio_setting.GroupRatio2JSONString()
	savedConfig := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedConfig[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatio))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(savedCompletionRatio))
		require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(savedCacheRatio))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedModelPrice))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(savedGroupRatio))
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedConfig))
	})

	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"ratio-model": 1.5}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"ratio-model": 4}`))
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"ratio-model": 0.1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"per-call-model": 0.04}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default": 1, "vip": 0.8}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"tiered-model":"tiered_expr","request-rule-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{` +
			`"tiered-model":"tier(\"base\", p * 3 + c * 15)",` +
			`"request-rule-model":"tier(\"base\", p * 3 + (hour(\"UTC\") >= 0 ? 100 : 0))"` +
			`}`,
	}))
}

// seedCatalogSnapshot writes one source with a single successful run holding
// one valid snapshot, the shape the catalog query treats as a current price.
func seedCatalogSnapshot(t *testing.T, db *gorm.DB, sourceName string, role PriceRole, channelId *int, canonicalModel string, priceExpr string, formulaKind string, metadata string, finishedAt int64) *model.PriceSource {
	t.Helper()
	source := &model.PriceSource{
		Name:                    sourceName,
		AdapterKey:              "test_adapter",
		Role:                    string(role),
		Scope:                   string(ScopePublic),
		ChannelId:               channelId,
		Enabled:                 true,
		ScheduleIntervalSeconds: 0,
	}
	require.NoError(t, model.InsertPriceSource(source))

	run := &model.PriceSyncRun{
		SourceId:   source.Id,
		Status:     model.PriceSyncRunStatusSucceeded,
		AdapterKey: "test_adapter",
		StartedAt:  finishedAt,
		FinishedAt: &finishedAt,
		ValidCount: 1,
	}
	require.NoError(t, db.Create(run).Error)

	snapshot := &model.PriceSnapshot{
		SourceId:           source.Id,
		SourceModelName:    "provider/" + canonicalModel,
		CanonicalModelName: canonicalModel,
		Role:               string(role),
		Scope:              string(ScopePublic),
		Provider:           "provider",
		MappingStatus:      MappingStatusDefault,
		Currency:           CurrencyUSD,
		FormulaKind:        formulaKind,
		PriceExpr:          priceExpr,
		ExprVersion:        ExprVersionV1,
		FetchedAt:          finishedAt,
		LastSeenAt:         finishedAt,
		LastSeenRunId:      run.Id,
		Fingerprint:        fmt.Sprintf("%064d", source.Id),
		FingerprintVersion: FingerprintVersion,
		Metadata:           metadata,
	}
	require.NoError(t, db.Create(snapshot).Error)
	require.NoError(t, db.Create(&model.PriceSyncRunItem{
		RunId:           run.Id,
		SourceModelName: snapshot.SourceModelName,
		Status:          model.PriceSyncItemStatusValid,
		SnapshotId:      &snapshot.Id,
	}).Error)
	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).Updates(map[string]interface{}{
		"last_success_run_id": run.Id,
		"last_success_at":     finishedAt,
	}).Error)
	source.LastSuccessRunId = &run.Id
	return source
}

// TestProjectSalePriceModes pins the §9.3 projection formula for the three
// existing billing modes, including the fail-closed path for request rules.
func TestProjectSalePriceModes(t *testing.T) {
	setupCompareSaleConfig(t)
	usage := appliedUsage{P: 1_000_000, C: 1_000_000, CR: 500_000}
	params := usage.tokenParams()

	cases := []struct {
		name       string
		modelName  string
		wantMode   string
		wantStatus string
		wantAmount *float64
		wantNote   string
	}{
		{
			name:      "ratio mode weighs every provided dimension",
			modelName: "ratio-model",
			wantMode:  SaleBillingModeRatio,
			// (1e6 + 0.5e6*0.1 + 1e6*4) * 1.5 / 500000
			wantStatus: ProjectionOK,
			wantAmount: floatPtr((1_000_000 + 500_000*0.1 + 1_000_000*4) * 1.5 / common.QuotaPerUnit),
		},
		{
			name:       "per-call model price is the USD amount",
			modelName:  "per-call-model",
			wantMode:   SaleBillingModePerCall,
			wantStatus: ProjectionOK,
			wantAmount: floatPtr(0.04),
		},
		{
			name:       "tiered expression divides by one million",
			modelName:  "tiered-model",
			wantMode:   SaleBillingModeTieredExpr,
			wantStatus: ProjectionOK,
			wantAmount: floatPtr((1_000_000*3 + 1_000_000*15) / 1_000_000),
		},
		{
			name:       "request rules fail closed",
			modelName:  "request-rule-model",
			wantMode:   SaleBillingModeTieredExpr,
			wantStatus: ProjectionNotProjectable,
			wantNote:   RequestRuleProjectionNote,
		},
		{
			name:       "unpriced model is not configured",
			modelName:  "unknown-model",
			wantMode:   SaleBillingModeRatio,
			wantStatus: ProjectionNotConfigured,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mode, amount, status, note := projectSalePrice(testCase.modelName, params, usage)
			assert.Equal(t, testCase.wantMode, mode)
			assert.Equal(t, testCase.wantStatus, status)
			if testCase.wantAmount == nil {
				assert.Nil(t, amount)
			} else {
				require.NotNil(t, amount)
				assert.InDelta(t, *testCase.wantAmount, *amount, 1e-9)
			}
			if testCase.wantNote != "" {
				assert.Equal(t, testCase.wantNote, note)
			}
		})
	}
}

// TestCompareUpstreamPricesMultiChannelMargin covers the core comparison
// contract: several channels keep their own cost, the margin uses the worst
// one, references stay out of the margin, and inversion is reported.
func TestCompareUpstreamPricesMultiChannelMargin(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	now := common.GetTimestamp()

	cheapChannel := &model.Channel{Name: "cheap", Key: "k1", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(cheapChannel).Error)
	expensiveChannel := &model.Channel{Name: "expensive", Key: "k2", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(expensiveChannel).Error)

	// Sale: tier("base", p*3 + c*15) over p=c=1e6 -> $18.
	seedCatalogSnapshot(t, db, "cheap-cost", RoleSupplierCost, &cheapChannel.Id, "tiered-model",
		`tier("base", p * 1 + c * 5)`, FormulaKindTokenExprV1, "", now)
	seedCatalogSnapshot(t, db, "expensive-cost", RoleSupplierCost, &expensiveChannel.Id, "tiered-model",
		`tier("base", p * 2 + c * 10)`, FormulaKindTokenExprV1, "", now)
	seedCatalogSnapshot(t, db, "models-dev", RoleCuratedReference, nil, "tiered-model",
		`tier("base", p * 4 + c * 20)`, FormulaKindTokenExprV1, "", now)

	response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: []string{"tiered-model"}})
	require.NoError(t, err)
	require.Len(t, response.Entries, 1)
	entry := response.Entries[0]

	assert.Equal(t, DefaultCompareGroup, response.Group)
	assert.Equal(t, float64(1), response.GroupRatio)
	assert.Equal(t, float64(1_000_000), response.Usage.PromptTokens)
	assert.Equal(t, SaleBillingModeTieredExpr, entry.SaleBillingMode)
	require.NotNil(t, entry.SaleProjectedUSD)
	assert.InDelta(t, 18, *entry.SaleProjectedUSD, 1e-9)

	require.Len(t, entry.Costs, 2)
	require.Len(t, entry.References, 1)
	assert.False(t, entry.References[0].UsableForMargin)
	assert.InDelta(t, 24, *entry.References[0].AmountUSD, 1e-9)

	require.NotNil(t, entry.MinCostUSD)
	require.NotNil(t, entry.MaxCostUSD)
	assert.InDelta(t, 6, *entry.MinCostUSD, 1e-9)
	assert.InDelta(t, 12, *entry.MaxCostUSD, 1e-9)
	require.NotNil(t, entry.WorstMarginUSD)
	assert.InDelta(t, 6, *entry.WorstMarginUSD, 1e-9)
	require.NotNil(t, entry.WorstMarginRate)
	assert.InDelta(t, 6.0/18.0, *entry.WorstMarginRate, 1e-9)
	assert.False(t, entry.CostInverted)
	assert.True(t, entry.CostConfirmed)
}

func TestCompareUpstreamPricesStatusLabels(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	now := common.GetTimestamp()
	channel := &model.Channel{Name: "c", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)

	// Cost above the $0.04 per-call sale price, flagged varies_by_provider.
	seedCatalogSnapshot(t, db, "varies-cost", RoleSupplierCost, &channel.Id, "per-call-model",
		`tier("base", p * 1)`, FormulaKindTokenExprV1,
		`{"`+MetadataKeyVariesByProvider+`":"true"}`, now)

	response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: []string{"per-call-model"}})
	require.NoError(t, err)
	require.Len(t, response.Entries, 1)
	entry := response.Entries[0]

	require.Len(t, entry.Costs, 1)
	assert.True(t, entry.Costs[0].VariesByProvider)
	assert.Equal(t, VariesByProviderNote, entry.Costs[0].ProjectionNote)
	assert.InDelta(t, 1, *entry.Costs[0].AmountUSD, 1e-9)
	assert.True(t, entry.CostInverted)
	assert.False(t, entry.CostConfirmed)
	assert.Contains(t, entry.Statuses, CompareStatusCostInverted)
	assert.Contains(t, entry.Statuses, CompareStatusVariesByProvider)

	require.Len(t, response.Alerts, 1)
	assert.Equal(t, AlertCostInversion, response.Alerts[0].Code)
	assert.Equal(t, "per-call-model", response.Alerts[0].CanonicalModelName)
}

func TestCompareUpstreamPricesRequestValidation(t *testing.T) {
	setupCompareTestDB(t)
	setupCompareSaleConfig(t)

	negative := float64(-1)
	_, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{
		Usage: &dto.UpstreamPriceUsageVector{PromptTokens: &negative},
	})
	require.ErrorContains(t, err, "usage dimension")

	tooLarge := dto.MaxCompareUsageTokens * 2
	_, err = CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{
		Usage: &dto.UpstreamPriceUsageVector{CompletionTokens: &tooLarge},
	})
	require.ErrorContains(t, err, "usage dimension")

	tooMany := make([]string, dto.MaxCompareModelsRequested+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("m%d", i)
	}
	_, err = CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: tooMany})
	require.ErrorContains(t, err, "at most")
}

// TestCompareUpstreamPricesGroupSelection covers the §21 Q4 decision: the
// comparison group defaults to "default" and an administrator may pick another.
func TestCompareUpstreamPricesGroupSelection(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	channel := &model.Channel{Name: "c", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	seedCatalogSnapshot(t, db, "cost", RoleSupplierCost, &channel.Id, "tiered-model",
		`tier("base", p * 1 + c * 5)`, FormulaKindTokenExprV1, "", common.GetTimestamp())

	response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{
		Models: []string{"tiered-model"},
		Group:  "vip",
	})
	require.NoError(t, err)
	assert.Equal(t, "vip", response.Group)
	assert.Equal(t, 0.8, response.GroupRatio)
	assert.True(t, response.GroupRatioConfigured)
	require.Len(t, response.Entries, 1)
	require.NotNil(t, response.Entries[0].SaleBaseUSD)
	require.NotNil(t, response.Entries[0].SaleProjectedUSD)
	assert.InDelta(t, 18, *response.Entries[0].SaleBaseUSD, 1e-9)
	assert.InDelta(t, 14.4, *response.Entries[0].SaleProjectedUSD, 1e-9)

	unknownGroup, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{
		Models: []string{"tiered-model"},
		Group:  "not-configured",
	})
	require.NoError(t, err)
	assert.False(t, unknownGroup.GroupRatioConfigured)
	assert.Equal(t, float64(1), unknownGroup.GroupRatio)
}

func floatPtr(value float64) *float64 {
	return &value
}

// TestCompareSourcePriceCarriesSnapshotTimestamps pins that a comparison row
// reports the underlying snapshot's own fetched_at and effective_at, so a
// caller can label observation age and vendor effective date (spec §8.3)
// without a second full catalog request.
func TestCompareSourcePriceCarriesSnapshotTimestamps(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()
	effectiveAt := now - 86400

	channel := &model.Channel{Name: "timestamp-channel", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	source := seedCatalogSnapshot(t, db, "timestamp-cost", RoleSupplierCost, &channel.Id, "timestamp-model",
		`tier("base", p * 1 + c * 2)`, FormulaKindTokenExprV1, "", now)
	require.NoError(t, db.Model(&model.PriceSnapshot{}).Where("source_id = ?", source.Id).
		Update("effective_at", effectiveAt).Error)

	response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: []string{"timestamp-model"}})
	require.NoError(t, err)
	require.Len(t, response.Entries, 1)
	require.Len(t, response.Entries[0].Costs, 1)

	cost := response.Entries[0].Costs[0]
	assert.Equal(t, now, cost.FetchedAt)
	assert.Equal(t, now, cost.LastSeenAt)
	require.NotNil(t, cost.EffectiveAt)
	assert.Equal(t, effectiveAt, *cost.EffectiveAt)
}
