package upstreamprice

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jumpSnapshot(formulaKind, priceExpr string) *model.PriceSnapshot {
	return &model.PriceSnapshot{
		SourceModelName:    "openai/gpt-5.6-luna",
		CanonicalModelName: "gpt-5.6-luna",
		FormulaKind:        formulaKind,
		PriceExpr:          priceExpr,
	}
}

func jumpPrice(formulaKind, priceExpr string) *NormalizedPrice {
	return &NormalizedPrice{
		SourceModelName:    "openai/gpt-5.6-luna",
		CanonicalModelName: "gpt-5.6-luna",
		FormulaKind:        formulaKind,
		PriceExpr:          priceExpr,
	}
}

// TestEvaluatePriceJump is the acceptance table of the §13 price movement
// check: which upstream price changes are reported, along which dimension, and
// at what rate.
func TestEvaluatePriceJump(t *testing.T) {
	tests := []struct {
		name          string
		previous      *model.PriceSnapshot
		current       *NormalizedPrice
		threshold     float64
		wantDimension []string
		// wantRate is asserted on the first reported dimension when non-nil.
		wantRate     *float64
		wantFromZero bool
	}{
		{
			name:          "input coefficient multiplied tenfold",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 0.2 + c * 1.2)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 2 + c * 1.2)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionInput},
			wantRate:      float64Ptr(9),
		},
		{
			name:          "twenty percent stays under the default threshold",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 1 + c * 1)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 1.2 + c * 1)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: nil,
		},
		{
			name:          "a source threshold below the movement reports it",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 1 + c * 1)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 1.2 + c * 1)`),
			threshold:     0.1,
			wantDimension: []string{PriceJumpDimensionInput},
			wantRate:      float64Ptr(0.19999999999999998),
		},
		{
			name:          "only the long context tier moved",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `len <= 200000 ? tier("standard", p * 3) : tier("long_context", p * 6)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `len <= 200000 ? tier("standard", p * 3) : tier("long_context", p * 18)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionInput},
			wantRate:      float64Ptr(2),
		},
		{
			name:          "output coefficient moved inside the long context tier only",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long_context", p * 6 + c * 22.5)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long_context", p * 6 + c * 90)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionOutput},
			wantRate:      float64Ptr(3),
		},
		{
			name:          "cache dimensions are probed independently",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 1 + cr * 0.1 + cc * 1.25)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 1 + cr * 0.5 + cc * 5)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionCacheRead, PriceJumpDimensionCacheWrite},
		},
		{
			name:          "price fell to zero",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 2 + c * 4)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 0 + c * 4)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionInput},
			wantRate:      float64Ptr(1),
		},
		{
			name:          "price appeared out of zero",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 0 + c * 4)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 2 + c * 4)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionInput},
			wantFromZero:  true,
		},
		{
			name:          "per call price references usage",
			previous:      jumpSnapshot(FormulaKindPerCallV1, `tier("base", p * 0.000001)`),
			current:       jumpPrice(FormulaKindPerCallV1, `tier("base", p * 0.00001)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionPerCall},
			wantRate:      float64Ptr(9),
		},
		{
			name:          "per call flat price",
			previous:      jumpSnapshot(FormulaKindPerCallV1, `tier("base", p * 0 + 0.01)`),
			current:       jumpPrice(FormulaKindPerCallV1, `tier("base", p * 0 + 0.05)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionPerCall},
			wantRate:      float64Ptr(4),
		},
		{
			name: "formula kind changed but the USD amount did not move enough",
			// 2 USD per 1M prompt tokens, stated once as a token coefficient and
			// once as a per-call amount over the same million tokens.
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 2)`),
			current:       jumpPrice(FormulaKindPerCallV1, `tier("base", p * 0.0000022)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: nil,
		},
		{
			name:          "formula kind changed and the USD amount jumped",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 2)`),
			current:       jumpPrice(FormulaKindPerCallV1, `tier("base", p * 0.00002)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionPerCall},
			wantRate:      float64Ptr(9),
		},
		{
			name:          "an unmeasurable expression change is marked for review",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 0 + c * 1)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", c * 1)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: []string{PriceJumpDimensionExprUnverified},
		},
		{
			name:          "an identical expression is provably equivalent",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 1 + c * 1)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 1 + c * 1)`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: nil,
		},
		{
			name:          "a reformatted expression is provably equivalent",
			previous:      jumpSnapshot(FormulaKindTokenExprV1, `tier("base", p * 1 + c * 1)`),
			current:       jumpPrice(FormulaKindTokenExprV1, `tier("base",  (p*1) + (c*1))`),
			threshold:     DefaultPriceJumpThreshold,
			wantDimension: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := evaluatePriceJump(test.previous, test.current, test.threshold)
			dimensions := make([]string, 0, len(entries))
			for _, entry := range entries {
				dimensions = append(dimensions, entry.Dimension)
				assert.Equal(t, "openai/gpt-5.6-luna", entry.SourceModelName,
					"the source model name identifies which upstream entry moved")
				assert.Equal(t, "gpt-5.6-luna", entry.CanonicalModelName)
			}
			assert.ElementsMatch(t, test.wantDimension, dimensions)
			if test.wantRate != nil {
				require.NotEmpty(t, entries)
				require.NotNil(t, entries[0].ChangeRate)
				assert.InDelta(t, *test.wantRate, *entries[0].ChangeRate, 1e-9)
			}
			if test.wantFromZero {
				require.NotEmpty(t, entries)
				assert.True(t, entries[0].FromZero)
				assert.Nil(t, entries[0].ChangeRate, "a movement out of zero has no defined rate")
			}
		})
	}
}

func float64Ptr(value float64) *float64 { return &value }

// TestEvaluatePriceJumpTierBoundaryMoved is the case fixed sample points cannot
// see: both tier coefficients are unchanged, only the boundary between them
// moved, so any probe set that does not derive its context lengths from the two
// expressions themselves reports nothing at all.
func TestEvaluatePriceJumpTierBoundaryMoved(t *testing.T) {
	previous := jumpSnapshot(FormulaKindTokenExprV1,
		`len <= 200000 ? tier("standard", p * 3) : tier("long_context", p * 6)`)
	current := jumpPrice(FormulaKindTokenExprV1,
		`len <= 100000 ? tier("standard", p * 3) : tier("long_context", p * 6)`)

	entries := evaluatePriceJump(previous, current, DefaultPriceJumpThreshold)
	require.Len(t, entries, 1)
	assert.Equal(t, PriceJumpDimensionInput, entries[0].Dimension)
	require.NotNil(t, entries[0].ChangeRate)
	// Between the old and the new boundary the price doubles.
	assert.InDelta(t, 1.0, *entries[0].ChangeRate, 1e-9)
	require.NotNil(t, entries[0].PreviousUSD)
	require.NotNil(t, entries[0].CurrentUSD)
	assert.InDelta(t, 3.0, *entries[0].PreviousUSD, 1e-9)
	assert.InDelta(t, 6.0, *entries[0].CurrentUSD, 1e-9)
	assert.Contains(t, entries[0].ProbeContext, "len=",
		"the reported probe context must let an admin reproduce the projection")
}

// TestEvaluatePriceJumpNoBaseline pins that a model with no baseline snapshot —
// the first run of a source, or a model the source just started returning — is
// never reported: there is nothing for its price to have moved from.
func TestEvaluatePriceJumpNoBaseline(t *testing.T) {
	assert.Empty(t, evaluatePriceJump(nil,
		jumpPrice(FormulaKindTokenExprV1, `tier("base", p * 2)`), DefaultPriceJumpThreshold))
}

// TestEncodePriceJumpSummaryTruncationKeepsTotal pins the truncation contract:
// the stored summary carries the largest movements and still states how many
// were observed, so a bounded summary never reads as the whole story.
func TestEncodePriceJumpSummaryTruncationKeepsTotal(t *testing.T) {
	entries := make([]priceJumpEntry, 0, MaxPriceJumpEntries*3)
	for i := 0; i < MaxPriceJumpEntries*3; i++ {
		rate := float64(i)
		entries = append(entries, priceJumpEntry{
			SourceModelName: "model-" + string(rune('a'+i%26)),
			Dimension:       PriceJumpDimensionInput,
			ChangeRate:      &rate,
		})
	}
	summary := decodePriceJumpSummary(encodePriceJumpSummary(0.5, entries))

	assert.Equal(t, MaxPriceJumpEntries*3, summary.Total)
	assert.Len(t, summary.Entries, MaxPriceJumpEntries)
	assert.Equal(t, priceJumpSummaryVersion, summary.Version)
	assert.Equal(t, priceJumpProbeVersion, summary.ProbeVersion)
	assert.Equal(t, 0.5, summary.Threshold)
	require.NotNil(t, summary.Entries[0].ChangeRate)
	assert.Equal(t, float64(MaxPriceJumpEntries*3-1), *summary.Entries[0].ChangeRate,
		"truncation must keep the largest movements")
}

// TestEncodePriceJumpSummaryEmpty pins that no movement stores the empty string,
// which is also what a run written before the column existed reads back as, so
// alerting cannot tell the two apart and must treat both as "nothing to report".
func TestEncodePriceJumpSummaryEmpty(t *testing.T) {
	assert.Empty(t, encodePriceJumpSummary(0.5, nil))
	assert.Empty(t, decodePriceJumpSummary("").Entries)
	assert.Empty(t, decodePriceJumpSummary("{not json").Entries)
}

// TestPriceJumpThresholdValidation locks the accepted range of the per-source
// override. It deliberately extends past 1, because a change rate is not a
// fraction of a whole the way the coverage drop gate is.
func TestPriceJumpThresholdValidation(t *testing.T) {
	tests := []struct {
		settings string
		valid    bool
	}{
		{`{"price_jump_threshold":0.5}`, true},
		{`{"price_jump_threshold":9}`, true},
		{`{"price_jump_threshold":1000}`, true},
		{`{"price_jump_threshold":0}`, false},
		{`{"price_jump_threshold":-1}`, false},
		{`{"price_jump_threshold":1000.1}`, false},
	}
	for _, test := range tests {
		t.Run(test.settings, func(t *testing.T) {
			source := &model.PriceSource{
				Name:       "jump-source",
				AdapterKey: "test_adapter",
				Role:       string(RoleCuratedReference),
				Scope:      string(ScopePublic),
				Settings:   test.settings,
			}
			err := ValidatePriceSourceForWrite(source)
			if test.valid {
				// The adapter key is not registered in this package's tests, so a
				// valid threshold must fail on the adapter instead of the range.
				if err != nil {
					assert.Contains(t, err.Error(), "adapter")
				}
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "price_jump_threshold")
		})
	}
}

// TestPriceJumpThresholdSourceOverride pins that the settings value wins over
// the default and that an unset value falls back to it.
func TestPriceJumpThresholdSourceOverride(t *testing.T) {
	settings, err := ParseSourceSettings(`{"price_jump_threshold":2.5}`)
	require.NoError(t, err)
	assert.Equal(t, 2.5, priceJumpThreshold(SourceConfig{Settings: settings}))
	assert.Equal(t, DefaultPriceJumpThreshold, priceJumpThreshold(SourceConfig{}))
}
