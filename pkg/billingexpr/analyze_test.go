package billingexpr

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTierBoundaries locks the contract offline probe derivation depends on:
// which comparison literals are reported, against which variable, and in what
// order. A boundary that goes unreported is a tier switch nobody probes.
func TestTierBoundaries(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want map[string][]float64
	}{
		{
			name: "flat expression states no boundary",
			expr: `tier("base", p * 2.5 + c * 15)`,
			want: map[string][]float64{},
		},
		{
			name: "long context tier",
			expr: `len <= 200000 ? tier("standard", p * 3) : tier("long_context", p * 6)`,
			want: map[string][]float64{"len": {200000}},
		},
		{
			name: "boundary stated with the literal on the left",
			expr: `272000 < len ? tier("long_context", p * 6) : tier("standard", p * 3)`,
			want: map[string][]float64{"len": {272000}},
		},
		{
			name: "boundaries on several variables are reported apart",
			expr: `len <= 100000 && c > 4096 ? tier("a", p * 1) : tier("b", p * 2)`,
			want: map[string][]float64{"len": {100000}, "c": {4096}},
		},
		{
			name: "repeated boundaries are deduplicated and sorted",
			expr: `len <= 200000 ? (len <= 1000 ? tier("a", p * 1) : tier("b", p * 2)) : tier("c", p * 3)`,
			want: map[string][]float64{"len": {1000, 200000}},
		},
		{
			name: "comparisons that are not against a token variable are ignored",
			expr: `tier("base", p * 2) * (header("x-tier") == "fast" ? 2 : 1)`,
			want: map[string][]float64{},
		},
		{
			name: "an uncompilable expression reports nothing",
			expr: `tier("base", p *`,
			want: map[string][]float64{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, TierBoundaries(test.expr))
		})
	}
}

// TestTierBoundariesAreCapped keeps a generated or hostile expression from
// making a caller's probe set grow without limit, while still reporting the
// smallest boundaries, which are the ones realistic usage actually crosses.
func TestTierBoundariesAreCapped(t *testing.T) {
	conditions := make([]string, 0, MaxTierBoundariesPerVariable*3)
	for i := 1; i <= MaxTierBoundariesPerVariable*3; i++ {
		conditions = append(conditions, fmt.Sprintf("len <= %d", i*1000))
	}
	expr := fmt.Sprintf(`(%s) ? tier("a", p * 1) : tier("b", p * 2)`, strings.Join(conditions, " || "))

	boundaries := TierBoundaries(expr)
	require.Len(t, boundaries["len"], MaxTierBoundariesPerVariable)
	assert.Equal(t, float64(1000), boundaries["len"][0])
}

// TestCanonicalForm pins the one-directional equivalence proof: expressions
// that differ only in formatting must prove equal, and expressions that compute
// differently must never be reported as the same program.
func TestCanonicalForm(t *testing.T) {
	spaced, ok := CanonicalForm(`tier("base", p * 1 + c * 1)`)
	require.True(t, ok)
	compact, ok := CanonicalForm(`tier("base",(p*1)+(c*1))`)
	require.True(t, ok)
	assert.Equal(t, spaced, compact, "formatting must not defeat the equivalence proof")

	different, ok := CanonicalForm(`tier("base", p * 2 + c * 1)`)
	require.True(t, ok)
	assert.NotEqual(t, spaced, different)

	_, ok = CanonicalForm(`tier("base", p *`)
	assert.False(t, ok, "an uncompilable expression can never be proven equivalent to anything")
	_, ok = CanonicalForm("")
	assert.False(t, ok)
}
