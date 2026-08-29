package upstreamprice

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func int64Ptr(v int64) *int64 { return &v }

func TestValidateTierBounds(t *testing.T) {
	tests := []struct {
		name          string
		tiers         []TierBound
		wantThreshold *int64
		wantErr       bool
	}{
		{
			name:          "two-tier half-open contiguous",
			tiers:         []TierBound{{Cost: "0.000003", Min: 0, Max: int64Ptr(200001)}, {Cost: "0.000006", Min: 200001}},
			wantThreshold: int64Ptr(200001),
		},
		{
			name:          "single unbounded tier is flat-equivalent",
			tiers:         []TierBound{{Cost: "0.000003", Min: 0}},
			wantThreshold: nil,
		},
		{
			name:    "empty list rejected",
			tiers:   nil,
			wantErr: true,
		},
		{
			name:    "gap rejected",
			tiers:   []TierBound{{Cost: "1", Min: 0, Max: int64Ptr(100)}, {Cost: "2", Min: 200}},
			wantErr: true,
		},
		{
			name:    "overlap rejected",
			tiers:   []TierBound{{Cost: "1", Min: 0, Max: int64Ptr(200)}, {Cost: "2", Min: 100}},
			wantErr: true,
		},
		{
			name:    "first tier not starting at zero rejected",
			tiers:   []TierBound{{Cost: "1", Min: 10, Max: int64Ptr(100)}, {Cost: "2", Min: 100}},
			wantErr: true,
		},
		{
			name:    "bounded second tier rejected as not closed",
			tiers:   []TierBound{{Cost: "1", Min: 0, Max: int64Ptr(100)}, {Cost: "2", Min: 100, Max: int64Ptr(200)}},
			wantErr: true,
		},
		{
			name:    "unbounded first tier rejected",
			tiers:   []TierBound{{Cost: "1", Min: 0}, {Cost: "2", Min: 100}},
			wantErr: true,
		},
		{
			name:    "single bounded tier rejected",
			tiers:   []TierBound{{Cost: "1", Min: 0, Max: int64Ptr(100)}},
			wantErr: true,
		},
		{
			name:    "three tiers unsupported",
			tiers:   []TierBound{{Cost: "1", Min: 0, Max: int64Ptr(100)}, {Cost: "2", Min: 100, Max: int64Ptr(200)}, {Cost: "3", Min: 200}},
			wantErr: true,
		},
		{
			name:    "zero threshold rejected",
			tiers:   []TierBound{{Cost: "1", Min: 0, Max: int64Ptr(0)}, {Cost: "2", Min: 0}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			threshold, err := ValidateTierBounds(tt.tiers)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantThreshold == nil {
				assert.Nil(t, threshold)
			} else {
				require.NotNil(t, threshold)
				assert.Equal(t, *tt.wantThreshold, *threshold)
			}
		})
	}
}

func TestValidatePriceExpr(t *testing.T) {
	require.NoError(t, ValidatePriceExpr(`tier("base", p * 0.12 + c * 0.24)`))
	require.NoError(t, ValidatePriceExpr(
		`len <= 200000 ? tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75) : tier("long_context", p * 6 + c * 22.5 + cr * 0.6 + cc * 7.5)`))

	// Syntax errors fail at compile.
	require.Error(t, ValidatePriceExpr(`p * `))
	// Negative results are refused by the smoke test.
	require.Error(t, ValidatePriceExpr(`p * -1`))
	// Non-finite results are refused by the smoke test.
	require.Error(t, ValidatePriceExpr(`p * 1e308 * 1e308`))
}

func TestValidateNormalizedPrice(t *testing.T) {
	valid := &NormalizedPrice{
		SourceModelName:    "openai/gpt-5.6-luna",
		CanonicalModelName: "gpt-5.6-luna",
		MappingStatus:      MappingStatusDefault,
		Role:               RoleSupplierCost,
		Scope:              ScopePublic,
		Currency:           CurrencyUSD,
		FormulaKind:        FormulaKindTokenExprV1,
		PriceExpr:          `tier("base", p * 0.2 + c * 1.2)`,
		ExprVersion:        ExprVersionV1,
	}
	warningCode, err := ValidateNormalizedPrice(valid)
	require.NoError(t, err)
	assert.Empty(t, warningCode)

	badCurrency := *valid
	badCurrency.Currency = "EUR"
	warningCode, err = ValidateNormalizedPrice(&badCurrency)
	require.Error(t, err)
	assert.Equal(t, WarningUnsupportedCurrency, warningCode)

	badKind := *valid
	badKind.FormulaKind = "media_seconds_v1"
	warningCode, err = ValidateNormalizedPrice(&badKind)
	require.Error(t, err)
	assert.Equal(t, WarningInvalidPrice, warningCode)

	badExpr := *valid
	badExpr.PriceExpr = `p * -5`
	warningCode, err = ValidateNormalizedPrice(&badExpr)
	require.Error(t, err)
	assert.Equal(t, WarningExprValidationFailed, warningCode)
}

func TestValidateNormalizedPriceLengthBounds(t *testing.T) {
	base := NormalizedPrice{
		SourceModelName:    "openai/gpt-5.6-luna",
		CanonicalModelName: "gpt-5.6-luna",
		MappingStatus:      MappingStatusDefault,
		Role:               RoleSupplierCost,
		Scope:              ScopePublic,
		Currency:           CurrencyUSD,
		FormulaKind:        FormulaKindTokenExprV1,
		PriceExpr:          `tier("base", p * 0.2 + c * 1.2)`,
		ExprVersion:        ExprVersionV1,
	}

	tooLongName := base
	tooLongName.SourceModelName = strings.Repeat("m", MaxSourceModelNameLength+1)
	warningCode, err := ValidateNormalizedPrice(&tooLongName)
	require.Error(t, err)
	assert.Equal(t, WarningFieldTooLong, warningCode)

	emptyName := base
	emptyName.SourceModelName = ""
	warningCode, err = ValidateNormalizedPrice(&emptyName)
	require.Error(t, err)
	assert.Equal(t, WarningFieldTooLong, warningCode)

	tooLongCanonical := base
	tooLongCanonical.CanonicalModelName = strings.Repeat("m", MaxCanonicalModelNameLength+1)
	warningCode, err = ValidateNormalizedPrice(&tooLongCanonical)
	require.Error(t, err)
	assert.Equal(t, WarningFieldTooLong, warningCode)

	tooLongProvider := base
	tooLongProvider.Provider = strings.Repeat("p", MaxProviderLength+1)
	warningCode, err = ValidateNormalizedPrice(&tooLongProvider)
	require.Error(t, err)
	assert.Equal(t, WarningFieldTooLong, warningCode)

	hugeMetadata := base
	hugeMetadata.Metadata = map[string]string{"unsupported_dimensions": strings.Repeat("x", MaxMetadataBytes)}
	warningCode, err = ValidateNormalizedPrice(&hugeMetadata)
	require.Error(t, err)
	assert.Equal(t, WarningFieldTooLong, warningCode)

	hugeExpr := base
	hugeExpr.PriceExpr = "p * 1 + " + strings.Repeat("0", MaxPriceExprLength)
	warningCode, err = ValidateNormalizedPrice(&hugeExpr)
	require.Error(t, err)
	assert.Equal(t, WarningExprValidationFailed, warningCode)
	require.Error(t, ValidatePriceExpr(hugeExpr.PriceExpr))
}

func TestParseSourceSettingsStrict(t *testing.T) {
	// Unknown fields — credential- or endpoint-shaped ones in particular —
	// are refused, never silently ignored.
	for _, hostile := range []string{
		`{"api_key":"sk-secret"}`,
		`{"url":"https://evil.example/v1/models"}`,
		`{"host":"evil.example"}`,
		`{"authorization":"Bearer sk-secret"}`,
		`{"endpoint":"https://evil.example"}`,
		`{"model_mappings":{},"extra":1}`,
	} {
		_, err := ParseSourceSettings(hostile)
		require.Error(t, err, hostile)
		assert.Contains(t, err.Error(), "not allowed")
	}

	// Valid settings round-trip through canonicalization with equivalent
	// fields, regardless of client formatting.
	raw := ` {"stale_threshold_seconds": 3600, "model_mappings": {"openai/x": "x"}, "coverage_drop_threshold": 0.5} `
	parsed, err := ParseSourceSettings(raw)
	require.NoError(t, err)
	require.NotNil(t, parsed.CoverageDropThreshold)
	assert.Equal(t, 0.5, *parsed.CoverageDropThreshold)
	require.NotNil(t, parsed.StaleThresholdSeconds)
	assert.EqualValues(t, 3600, *parsed.StaleThresholdSeconds)
	assert.Equal(t, map[string]string{"openai/x": "x"}, parsed.ModelMappings)

	canonical, err := CanonicalSourceSettingsJSON(raw)
	require.NoError(t, err)
	reparsed, err := ParseSourceSettings(canonical)
	require.NoError(t, err)
	assert.Equal(t, parsed, reparsed, "canonical form must preserve every field")

	// Empty settings stay empty.
	canonical, err = CanonicalSourceSettingsJSON("  ")
	require.NoError(t, err)
	assert.Empty(t, canonical)
}

// TestCanonicalSettingsSizeRecheckedAfterEscaping: JSON HTML escaping can
// inflate the canonical form ("<" becomes "<"), so the byte cap is
// re-checked on the serialized output, not just on the raw client input.
func TestCanonicalSettingsSizeRecheckedAfterEscaping(t *testing.T) {
	// Raw input ~14KB, canonical form >65535 bytes after escaping.
	value := strings.Repeat("<", 13000)
	raw := `{"model_mappings":{"vendor/model":"` + value + `"}}`
	require.Less(t, len(raw), dto.MaxSourceSettingsBytes, "raw input itself stays under the cap")

	_, err := CanonicalSourceSettingsJSON(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceed")

	// A same-shape input without escaping amplification passes.
	okRaw := `{"model_mappings":{"vendor/model":"plain"}}`
	canonical, err := CanonicalSourceSettingsJSON(okRaw)
	require.NoError(t, err)
	assert.NotEmpty(t, canonical)
}

// TestValidatePriceSourceRoleChannelRules pins the single authority for the
// role×channel combination (spec §7.1). The adapter under test admits both
// roles and accepts any source of its own key, so the verdict can only come
// from the role: supplier_cost requires an enabled channel, every other role
// refuses one. This is the rule the admin UI mirrors, and the reason no
// adapter flag reports a channel requirement.
func TestValidatePriceSourceRoleChannelRules(t *testing.T) {
	db := setupCompareTestDB(t)

	adapterKey := "validate_role_channel_test"
	require.NoError(t, RegisterAdapter(&fakeAdapter{
		key:    adapterKey,
		roles:  []PriceRole{RoleSupplierCost, RoleCuratedReference},
		scopes: []PriceScope{ScopePublic},
	}))

	enabled := &model.Channel{Name: "enabled", Key: "k-enabled", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(enabled).Error)
	disabled := &model.Channel{Name: "disabled", Key: "k-disabled", Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, db.Create(disabled).Error)
	missingChannelId := enabled.Id + disabled.Id + 1000

	tests := []struct {
		name      string
		role      PriceRole
		channelId *int
		wantErr   string
	}{
		{name: "supplier cost with an enabled channel is accepted", role: RoleSupplierCost, channelId: &enabled.Id},
		{name: "supplier cost without a channel is refused", role: RoleSupplierCost, wantErr: "must reference a channel"},
		{name: "supplier cost with a disabled channel is refused", role: RoleSupplierCost, channelId: &disabled.Id, wantErr: "is not enabled"},
		{name: "supplier cost with an unknown channel is refused", role: RoleSupplierCost, channelId: &missingChannelId, wantErr: "does not exist"},
		{name: "curated reference without a channel is accepted", role: RoleCuratedReference},
		{name: "curated reference with a channel is refused", role: RoleCuratedReference, channelId: &enabled.Id, wantErr: "must not reference a channel"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			source := &model.PriceSource{
				Name:       "role channel fixture",
				AdapterKey: adapterKey,
				Role:       string(testCase.role),
				Scope:      string(ScopePublic),
				ChannelId:  testCase.channelId,
			}
			err := ValidatePriceSourceForWrite(source)
			if testCase.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantErr)
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	assert.Equal(t, "abc", common.TruncateUTF8("abc", 10))
	assert.Equal(t, "ab", common.TruncateUTF8("abcd", 2))
	// 3-byte runes: cutting at 4 bytes must back off to the rune boundary.
	assert.Equal(t, "配", common.TruncateUTF8("配置错", 4))
	assert.Equal(t, "", common.TruncateUTF8("配", 2))
}
