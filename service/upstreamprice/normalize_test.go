package upstreamprice

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDecimalString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"plain", "0.00000012", "0.00000012", false},
		{"integer", "3", "3", false},
		{"trailing zeros trimmed", "1.50", "1.5", false},
		{"zero allowed", "0", "0", false},
		{"negative rejected", "-0.01", "", true},
		{"nan rejected", "NaN", "", true},
		{"inf rejected", "Inf", "", true},
		{"garbage rejected", "abc", "", true},
		{"empty rejected", "", "", true},
		{"exponent notation rejected", "1e6", "", true},
		{"huge exponent rejected before parsing", "1e2000000000", "", true},
		{"uppercase exponent rejected", "1E6", "", true},
		{"over 64 chars rejected", "0." + strings.Repeat("1", 63), "", true},
		{"too many integer digits rejected", "1234567890123", "", true},
		{"12 integer digits accepted", "123456789012", "123456789012", false},
		{"too many fraction digits rejected", "0.1234567890123456789", "", true},
		{"18 fraction digits accepted", "0.123456789012345678", "0.123456789012345678", false},
		{"multiple dots rejected", "1.2.3", "", true},
		{"lone dot rejected", ".", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeDecimalString(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPerMillionTokenCoefficient(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"0.00000012", "0.12"},
		{"0.000003", "3"},
		{"0.0000225", "22.5"},
		{"0.000000001", "0.001"},
		{"0.00001", "10"},
		{"0", "0"},
	}
	for _, tt := range tests {
		got, err := PerMillionTokenCoefficient(tt.input)
		require.NoError(t, err, tt.input)
		assert.Equal(t, tt.want, got, tt.input)
	}
	_, err := PerMillionTokenCoefficient("-0.000001")
	require.Error(t, err)

	// Exactly 1 USD/token is the accepted upper bound; anything above is
	// treated as corrupt source data.
	got, err := PerMillionTokenCoefficient("1")
	require.NoError(t, err)
	assert.Equal(t, "1000000", got)
	_, err = PerMillionTokenCoefficient("1.000001")
	require.Error(t, err)
	_, err = PerMillionTokenCoefficient("1.5")
	require.Error(t, err)
	_, err = PerMillionTokenCoefficient("999999999999")
	require.Error(t, err)
}

func TestMapCanonicalModelName(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		explicit   map[string]string
		want       string
		wantStatus string
	}{
		{"default strips one prefix", "openai/gpt-5.6-luna", nil, "gpt-5.6-luna", MappingStatusDefault},
		{"only one level stripped", "a/b/c", nil, "b/c", MappingStatusDefault},
		{"explicit overrides default", "openai/gpt-5.6-luna", map[string]string{"openai/gpt-5.6-luna": "custom-luna"}, "custom-luna", MappingStatusExplicit},
		{"no prefix keeps name unmapped", "gpt-5.6-luna", nil, "gpt-5.6-luna", MappingStatusUnmapped},
		{"leading slash unmapped", "/model", nil, "/model", MappingStatusUnmapped},
		{"trailing slash unmapped", "openai/", nil, "openai/", MappingStatusUnmapped},
		{"empty explicit target unmapped", "openai/x", map[string]string{"openai/x": " "}, "openai/x", MappingStatusUnmapped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, status := MapCanonicalModelName(tt.source, tt.explicit)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantStatus, status)
		})
	}
}

type fakeAdapter struct {
	key    string
	roles  []PriceRole
	scopes []PriceScope
}

func (f *fakeAdapter) Key() string                 { return f.key }
func (f *fakeAdapter) Supports(SourceConfig) bool  { return true }
func (f *fakeAdapter) AllowedRoles() []PriceRole   { return f.roles }
func (f *fakeAdapter) AllowedScopes() []PriceScope { return f.scopes }
func (f *fakeAdapter) Fetch(context.Context, SourceConfig) ([]Observation, FetchMeta, error) {
	return nil, FetchMeta{}, nil
}

func supplierCostAdapter() *fakeAdapter {
	return &fakeAdapter{
		key:    "fake",
		roles:  []PriceRole{RoleSupplierCost},
		scopes: []PriceScope{ScopePublic},
	}
}

func supplierCostSource() SourceConfig {
	return SourceConfig{Id: 1, AdapterKey: "fake", Role: RoleSupplierCost, Scope: ScopePublic}
}

func TestResolveObservationRoleScope(t *testing.T) {
	adapter := supplierCostAdapter()
	source := supplierCostSource()

	role, scope, err := ResolveObservationRoleScope(Observation{}, source, adapter)
	require.NoError(t, err)
	assert.Equal(t, RoleSupplierCost, role)
	assert.Equal(t, ScopePublic, scope)

	role, scope, err = ResolveObservationRoleScope(
		Observation{Role: RoleSupplierCost, Scope: ScopePublic}, source, adapter)
	require.NoError(t, err)
	assert.Equal(t, RoleSupplierCost, role)
	assert.Equal(t, ScopePublic, scope)

	// Observation exceeding the source declaration is rejected, never
	// silently overridden.
	_, _, err = ResolveObservationRoleScope(Observation{Role: RoleVendorList}, source, adapter)
	require.Error(t, err)
	_, _, err = ResolveObservationRoleScope(Observation{Scope: ScopeAccount}, source, adapter)
	require.Error(t, err)

	// Source default outside the adapter's allowed set is rejected too.
	vendorSource := supplierCostSource()
	vendorSource.Role = RoleVendorList
	_, _, err = ResolveObservationRoleScope(Observation{}, vendorSource, adapter)
	require.Error(t, err)
}

func baseObservation() Observation {
	return Observation{
		Provider:        "openai",
		SourceModelName: "openai/gpt-5.6-luna",
		Currency:        CurrencyUSD,
		FormulaKind:     FormulaKindTokenExprV1,
		PriceExpr:       `tier("base", p * 0.2 + c * 1.2)`,
	}
}

func TestFingerprintIdempotenceAndSensitivity(t *testing.T) {
	adapter := supplierCostAdapter()
	source := supplierCostSource()

	first, err := NormalizeObservation(baseObservation(), source, adapter)
	require.NoError(t, err)
	second, err := NormalizeObservation(baseObservation(), source, adapter)
	require.NoError(t, err)
	assert.Equal(t, first.Fingerprint, second.Fingerprint, "identical content must produce identical fingerprints")
	assert.Len(t, first.Fingerprint, 64)
	assert.Equal(t, "gpt-5.6-luna", first.CanonicalModelName)
	assert.Equal(t, MappingStatusDefault, first.MappingStatus)

	// A mapping change alone must change the fingerprint even though the
	// price numbers are identical (spec §7.2/§7.5).
	mappedSource := supplierCostSource()
	mappedSource.Settings.ModelMappings = map[string]string{"openai/gpt-5.6-luna": "luna-mapped"}
	mapped, err := NormalizeObservation(baseObservation(), mappedSource, adapter)
	require.NoError(t, err)
	assert.Equal(t, "luna-mapped", mapped.CanonicalModelName)
	assert.Equal(t, MappingStatusExplicit, mapped.MappingStatus)
	assert.NotEqual(t, first.Fingerprint, mapped.Fingerprint)

	// Semantic metadata changes the fingerprint.
	flagged := baseObservation()
	flagged.Metadata = map[string]string{MetadataKeyVariesByProvider: "true"}
	flaggedPrice, err := NormalizeObservation(flagged, source, adapter)
	require.NoError(t, err)
	assert.NotEqual(t, first.Fingerprint, flaggedPrice.Fingerprint)

	// Expression change changes the fingerprint.
	repriced := baseObservation()
	repriced.PriceExpr = `tier("base", p * 0.3 + c * 1.2)`
	repricedPrice, err := NormalizeObservation(repriced, source, adapter)
	require.NoError(t, err)
	assert.NotEqual(t, first.Fingerprint, repricedPrice.Fingerprint)
}
