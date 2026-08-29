package upstreamprice

import (
	"fmt"
	"slices"
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

	// The run records the configuration it ran under, exactly as the commit
	// path does; the catalog compares it against the source's current
	// configuration.
	config, err := SourceConfigFromModel(source)
	require.NoError(t, err)
	run := &model.PriceSyncRun{
		SourceId:           source.Id,
		Status:             model.PriceSyncRunStatusSucceeded,
		AdapterKey:         "test_adapter",
		StartedAt:          finishedAt,
		FinishedAt:         &finishedAt,
		ValidCount:         1,
		SourceConfigDigest: sourceConfigDigest(config),
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
			wantAmount: float64Ptr((1_000_000 + 500_000*0.1 + 1_000_000*4) * 1.5 / common.QuotaPerUnit),
		},
		{
			name:       "per-call model price is the USD amount",
			modelName:  "per-call-model",
			wantMode:   SaleBillingModePerCall,
			wantStatus: ProjectionOK,
			wantAmount: float64Ptr(0.04),
		},
		{
			name:       "tiered expression divides by one million",
			modelName:  "tiered-model",
			wantMode:   SaleBillingModeTieredExpr,
			wantStatus: ProjectionOK,
			wantAmount: float64Ptr((1_000_000*3 + 1_000_000*15) / 1_000_000),
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

// TestCompareUpstreamPricesStaleAlertBasis pins the comparison's alerting
// contract: its source alerts are the ones evaluated against the timestamp it
// reports as GeneratedAt, over the sources its own catalog projection covered.
// The margins around the threshold are wide enough that a clock tick during the
// request cannot flip the expected verdict; the exact one-second boundary is
// pinned deterministically in TestCatalogStaleBoundaryIsSharedByEntriesAndAlerts.
func TestCompareUpstreamPricesStaleAlertBasis(t *testing.T) {
	cases := []struct {
		name      string
		age       int64
		wantStale bool
	}{
		{"an hour inside the threshold", DefaultManualStaleThresholdSeconds - 3600, false},
		{"an hour past the threshold", DefaultManualStaleThresholdSeconds + 3600, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupCompareTestDB(t)
			setupCompareSaleConfig(t)
			channel := &model.Channel{Name: "c", Key: "k", Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(channel).Error)
			seedCatalogSnapshot(t, db, "cost", RoleSupplierCost, &channel.Id, "tiered-model",
				`tier("base", p * 1 + c * 5)`, FormulaKindTokenExprV1, "", common.GetTimestamp()-testCase.age)

			response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: []string{"tiered-model"}})
			require.NoError(t, err)

			sources, err := model.GetAllPriceSources()
			require.NoError(t, err)
			expected, err := EvaluateSourceAlerts(sources, response.GeneratedAt)
			require.NoError(t, err)
			assert.Equal(t, expected, response.Alerts,
				"the comparison must report exactly the alerts of its own generation timestamp")

			assert.Equal(t, testCase.wantStale, slices.Contains(alertCodeList(response.Alerts), AlertSourceStale))
			require.Len(t, response.Entries, 1)
			assert.Equal(t, testCase.wantStale, slices.Contains(response.Entries[0].Statuses, CompareStatusStale))
		})
	}
}

// TestCompareUpstreamPricesFailsClosedOnChangedSourceConfig is the §9.2 /
// §10.3 fail-closed contract: a cost observed under one source configuration
// must not be presented as the confirmed current cost of a source that has
// since been pointed somewhere else. The stale observation stays visible as
// evidence but leaves the min/max range, the margin, and cost_confirmed until
// a run under the current configuration replaces it.
func TestCompareUpstreamPricesFailsClosedOnChangedSourceConfig(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	now := common.GetTimestamp()

	original := &model.Channel{Name: "original", Key: "k1", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(original).Error)
	replacement := &model.Channel{Name: "replacement", Key: "k2", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(replacement).Error)

	source := seedCatalogSnapshot(t, db, "cost", RoleSupplierCost, &original.Id, "tiered-model",
		`tier("base", p * 1 + c * 5)`, FormulaKindTokenExprV1, "", now)

	compareEntry := func() dto.UpstreamPriceCompareEntry {
		t.Helper()
		response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: []string{"tiered-model"}})
		require.NoError(t, err)
		require.Len(t, response.Entries, 1)
		return response.Entries[0]
	}

	confirmed := compareEntry()
	require.Len(t, confirmed.Costs, 1)
	assert.True(t, confirmed.Costs[0].UsableForMargin)
	assert.False(t, confirmed.Costs[0].SourceConfigChanged)
	assert.True(t, confirmed.CostConfirmed)
	require.NotNil(t, confirmed.MaxCostUSD)
	assert.InDelta(t, 6, *confirmed.MaxCostUSD, 1e-9)
	assert.NotContains(t, confirmed.Statuses, CompareStatusSourceConfigChanged)

	// Toggling the scheduling switches is not a price-semantic change.
	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).Updates(map[string]interface{}{
		"schedule_enabled":          true,
		"schedule_interval_seconds": MinScheduleIntervalSeconds,
	}).Error)
	assert.True(t, compareEntry().CostConfirmed)

	// Repointing the source at another channel is: the observed cost belongs
	// to the old channel and must not be attributed to the new one.
	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).
		Update("channel_id", replacement.Id).Error)

	changed := compareEntry()
	require.Len(t, changed.Costs, 1)
	assert.True(t, changed.Costs[0].SourceConfigChanged)
	assert.False(t, changed.Costs[0].UsableForMargin)
	assert.False(t, changed.CostConfirmed)
	assert.Nil(t, changed.MinCostUSD)
	assert.Nil(t, changed.MaxCostUSD)
	assert.Nil(t, changed.WorstMarginUSD)
	assert.Nil(t, changed.WorstMarginRate)
	assert.Contains(t, changed.Statuses, CompareStatusSourceConfigChanged)

	// A successful run under the current configuration restores the cost; the
	// commit path records the configuration the fetch actually ran under.
	current, err := model.GetPriceSourceById(source.Id)
	require.NoError(t, err)
	config, err := SourceConfigFromModel(current)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.PriceSyncRun{}).Where("source_id = ?", source.Id).
		Update("source_config_digest", sourceConfigDigest(config)).Error)

	restored := compareEntry()
	assert.True(t, restored.CostConfirmed)
	assert.False(t, restored.Costs[0].SourceConfigChanged)
	require.NotNil(t, restored.MaxCostUSD)
	assert.InDelta(t, 6, *restored.MaxCostUSD, 1e-9)
	assert.NotContains(t, restored.Statuses, CompareStatusSourceConfigChanged)
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

// TestCompareSourcePriceCarriesUnsupportedDimensions pins the §6.2 label the
// comparison view needs: the one metadata key naming the pricing dimensions
// this catalog does not normalize is exposed on the source price, and the rest
// of the snapshot metadata is not.
func TestCompareSourcePriceCarriesUnsupportedDimensions(t *testing.T) {
	db := setupCompareTestDB(t)
	now := common.GetTimestamp()

	channel := &model.Channel{Name: "dimensions-channel", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	metadata := `{"` + MetadataKeyUnsupportedDimensions + `":"fast,regional","provider_slug":"secret"}`
	seedCatalogSnapshot(t, db, "dimensions-cost", RoleSupplierCost, &channel.Id, "dimensions-model",
		`tier("base", p * 1 + c * 2)`, FormulaKindTokenExprV1, metadata, now)

	response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{Models: []string{"dimensions-model"}})
	require.NoError(t, err)
	require.Len(t, response.Entries, 1)
	require.Len(t, response.Entries[0].Costs, 1)
	assert.Equal(t, "fast,regional", response.Entries[0].Costs[0].UnsupportedDimensions)

	encoded, err := common.Marshal(response.Entries[0].Costs[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "secret")
}

// TestResolveUsagePartialVector pins the §10.3 usage contract: the default
// vector stands in only for a caller that named no usage at all. Naming one
// dimension describes the whole request, so the rest are zero rather than
// silently carrying a million default tokens into the margin.
func TestResolveUsagePartialVector(t *testing.T) {
	value := func(v float64) *float64 { return &v }
	cases := []struct {
		name      string
		requested *dto.UpstreamPriceUsageVector
		want      appliedUsage
	}{
		{
			name:      "absent vector uses the documented default",
			requested: nil,
			want:      appliedUsage{P: defaultComparePromptTokens, C: defaultCompareCompletionTokens},
		},
		{
			name:      "partial vector zeroes the dimensions it omits",
			requested: &dto.UpstreamPriceUsageVector{PromptTokens: value(1000)},
			want:      appliedUsage{P: 1000},
		},
		{
			name:      "explicit zero stays zero",
			requested: &dto.UpstreamPriceUsageVector{CompletionTokens: value(0)},
			want:      appliedUsage{},
		},
		{
			name: "full vector is applied verbatim",
			requested: &dto.UpstreamPriceUsageVector{
				PromptTokens:        value(10),
				CompletionTokens:    value(20),
				CacheReadTokens:     value(30),
				CacheCreationTokens: value(40),
			},
			want: appliedUsage{P: 10, C: 20, CR: 30, CC: 40},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, resolveUsage(testCase.requested))
		})
	}
}

// TestCompareUpstreamPricesEchoesPartialUsage proves the echoed vector follows
// the same semantics, so the response always states the basis of its amounts.
func TestCompareUpstreamPricesEchoesPartialUsage(t *testing.T) {
	setupCompareTestDB(t)
	setupCompareSaleConfig(t)

	completion := float64(2000)
	response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{
		Models: []string{"ratio-model"},
		Usage:  &dto.UpstreamPriceUsageVector{CompletionTokens: &completion},
	})
	require.NoError(t, err)
	assert.Equal(t, float64(0), response.Usage.PromptTokens)
	assert.Equal(t, completion, response.Usage.CompletionTokens)
	assert.Equal(t, float64(0), response.Usage.CacheReadTokens)
	assert.Equal(t, float64(0), response.Usage.CacheCreationTokens)

	// 2000 completion tokens at completion ratio 4 and model ratio 1.5.
	require.Len(t, response.Entries, 1)
	require.NotNil(t, response.Entries[0].SaleBaseUSD)
	assert.InDelta(t, 2000*4*1.5/common.QuotaPerUnit, *response.Entries[0].SaleBaseUSD, 1e-9)
}

// TestSelectCompareModelsFilter pins the §10.3 filter contract: the substring
// filter narrows the catalog before the MaxCompareModels cap, so a catalog
// larger than the cap stays searchable end to end.
func TestSelectCompareModelsFilter(t *testing.T) {
	catalog := make(map[string][]dto.UpstreamCurrentPriceEntry)
	for i := 0; i < MaxCompareModels+120; i++ {
		catalog[fmt.Sprintf("gpt-%04d", i)] = nil
	}
	for i := 0; i < 3; i++ {
		catalog[fmt.Sprintf("Claude-Sonnet-%d", i)] = nil
	}

	cases := []struct {
		name          string
		requested     []string
		filter        string
		wantCount     int
		wantTotal     int
		wantTruncated bool
		wantFirst     string
	}{
		{
			name:          "empty filter compares the whole catalog up to the cap",
			wantCount:     MaxCompareModels,
			wantTotal:     MaxCompareModels + 123,
			wantTruncated: true,
			wantFirst:     "Claude-Sonnet-0",
		},
		{
			name:          "a filter matching more than the cap still truncates",
			filter:        "gpt-",
			wantCount:     MaxCompareModels,
			wantTotal:     MaxCompareModels + 120,
			wantTruncated: true,
			wantFirst:     "gpt-0000",
		},
		{
			name:      "a filter narrowing below the cap returns every match",
			filter:    "sonnet",
			wantCount: 3,
			wantTotal: 3,
			wantFirst: "Claude-Sonnet-0",
		},
		{
			name:      "the filter is trimmed and case-insensitive",
			filter:    "  SONNET-2 ",
			wantCount: 1,
			wantTotal: 1,
			wantFirst: "Claude-Sonnet-2",
		},
		{
			name:      "a filter matching nothing returns no model",
			filter:    "gemini",
			wantCount: 0,
			wantTotal: 0,
		},
		{
			name:      "an explicit model list is honored verbatim and ignores the filter",
			requested: []string{"gpt-0001", "gemini-not-in-catalog"},
			filter:    "sonnet",
			wantCount: 2,
			wantTotal: 2,
			wantFirst: "gpt-0001",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			names, total, truncated := selectCompareModels(testCase.requested, testCase.filter, catalog)
			assert.Len(t, names, testCase.wantCount)
			assert.Equal(t, testCase.wantTotal, total)
			assert.Equal(t, testCase.wantTruncated, truncated)
			if testCase.wantFirst != "" {
				require.NotEmpty(t, names)
				assert.Equal(t, testCase.wantFirst, names[0])
			}
		})
	}
}

// TestCompareUpstreamPricesModelFilter pins that the filter reaches the API
// response, so narrowing a truncated catalog is a server-side operation.
func TestCompareUpstreamPricesModelFilter(t *testing.T) {
	db := setupCompareTestDB(t)
	setupCompareSaleConfig(t)
	now := common.GetTimestamp()
	channel := &model.Channel{Name: "filter-channel", Key: "k", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	seedCatalogSnapshot(t, db, "tiered-cost", RoleSupplierCost, &channel.Id, "tiered-model",
		`tier("base", p * 1 + c * 5)`, FormulaKindTokenExprV1, "", now)
	seedCatalogSnapshot(t, db, "ratio-cost", RoleSupplierCost, &channel.Id, "ratio-model",
		`tier("base", p * 1 + c * 5)`, FormulaKindTokenExprV1, "", now)

	response, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{ModelFilter: "TIERED"})
	require.NoError(t, err)
	require.Len(t, response.Entries, 1)
	assert.Equal(t, "tiered-model", response.Entries[0].CanonicalModelName)
	assert.Equal(t, 1, response.TotalModels)
	assert.False(t, response.Truncated)

	tooLong, err := CompareUpstreamPrices(&dto.UpstreamPriceCompareRequest{
		ModelFilter: strings.Repeat("m", dto.MaxCompareModelFilterLength+1),
	})
	require.ErrorContains(t, err, "model_filter")
	assert.Nil(t, tooLong)
}
