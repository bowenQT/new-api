package billingexpr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunBaseExprNeutralizesRequestRules pins the projection contract: every
// instrumented request-rule factor evaluates as 1, no request probe runs, and
// expressions whose request dependency cannot be neutralized fail closed.
func TestRunBaseExprNeutralizesRequestRules(t *testing.T) {
	params := TokenParams{P: 1_000_000, C: 1_000_000, Len: 1_000_000}

	cases := []struct {
		name     string
		expr     string
		expected float64
	}{
		{
			name:     "plain expression is unaffected",
			expr:     `tier("base", p * 3 + c * 15)`,
			expected: 3_000_000 + 15_000_000,
		},
		{
			name:     "header factor neutralized to 1",
			expr:     `(tier("base", p * 5 + c * 25)) * (has(header("anthropic-beta"), "fast-mode") ? 6 : 1)`,
			expected: 5_000_000 + 25_000_000,
		},
		{
			name:     "param factor neutralized to 1",
			expr:     `(tier("base", p * 2)) * (param("service_tier") == "fast" ? 2.5 : 1)`,
			expected: 2_000_000,
		},
		{
			name:     "time factor neutralized to 1",
			expr:     `(tier("base", p * 4)) * (hour("UTC") >= 0 ? 3 : 1)`,
			expected: 4_000_000,
		},
		{
			name:     "several factors all neutralized",
			expr:     `(tier("base", p * 1)) * (hour("UTC") < 24 ? 2 : 1) * (header("x-fast") == "1" ? 4 : 1)`,
			expected: 1_000_000,
		},
		{
			name:     "tier condition still selects the long-context branch",
			expr:     `len <= 200000 ? tier("standard", p * 3) : tier("long_context", p * 6)`,
			expected: 6_000_000,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, trace, err := RunBaseExpr(testCase.expr, params)
			require.NoError(t, err)
			assert.Equal(t, testCase.expected, result)
			assert.NotEmpty(t, trace.MatchedTier)
		})
	}
}

func TestRunBaseExprFailsClosed(t *testing.T) {
	params := TokenParams{P: 1000, C: 1000, Len: 1000}

	cases := []struct {
		name string
		expr string
	}{
		{
			name: "probe outside an instrumented factor",
			expr: `tier("base", p * 3 + (hour("UTC") >= 9 ? 1000 : 0))`,
		},
		{
			name: "ternary fallback is not 1",
			expr: `(tier("base", p * 3)) * (header("x-fast") == "1" ? 2 : 1.5)`,
		},
		{
			name: "probe used directly as a multiplier",
			expr: `tier("base", p * 3 * float(hour("UTC")))`,
		},
		{
			name: "non-literal multiplier branch",
			expr: `(tier("base", p * 3)) * (param("n") == 1 ? p : 1)`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := RunBaseExpr(testCase.expr, params)
			require.ErrorIs(t, err, ErrRequestRuleNotProjectable)
		})
	}
}

func TestRunBaseExprRejectsInvalidExpressions(t *testing.T) {
	_, _, err := RunBaseExpr(`tier("base", p *`, TokenParams{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRequestRuleNotProjectable)

	_, _, err = RunBaseExpr(`_trace(0, true, 2)`, TokenParams{})
	require.Error(t, err)

	_, _, err = RunBaseExpr(`tier("base", p / 0 - c / 0)`, TokenParams{P: 1, C: 1})
	require.ErrorContains(t, err, "non-finite")
}

// TestRunExprUnchangedByProjection is the isolation regression required by the
// spec: projecting an expression must not alter how the same expression bills
// on the live path, in either evaluation order.
func TestRunExprUnchangedByProjection(t *testing.T) {
	const exprStr = `(tier("base", p * 5 + c * 25)) * (has(header("anthropic-beta"), "fast-mode") ? 6 : 1)`
	params := TokenParams{P: 1000, C: 1000, Len: 1000}
	matching := RequestInput{Headers: map[string]string{"anthropic-beta": "fast-mode-2026-02-01"}}

	InvalidateCache()
	beforeMatched, beforeTrace, err := RunExprWithRequest(exprStr, params, matching)
	require.NoError(t, err)
	beforeEmpty, _, err := RunExprWithRequest(exprStr, params, RequestInput{})
	require.NoError(t, err)

	projected, _, err := RunBaseExpr(exprStr, params)
	require.NoError(t, err)

	afterMatched, afterTrace, err := RunExprWithRequest(exprStr, params, matching)
	require.NoError(t, err)
	afterEmpty, _, err := RunExprWithRequest(exprStr, params, RequestInput{})
	require.NoError(t, err)

	assert.Equal(t, beforeMatched, afterMatched)
	assert.Equal(t, beforeEmpty, afterEmpty)
	assert.Equal(t, beforeTrace.RequestRules, afterTrace.RequestRules)
	assert.Equal(t, (5*1000+25*1000)*float64(6), afterMatched)
	assert.Equal(t, float64(5*1000+25*1000), projected)

	// Projection first, then billing: the caches must stay independent.
	InvalidateCache()
	_, _, err = RunBaseExpr(exprStr, params)
	require.NoError(t, err)
	reversedMatched, reversedTrace, err := RunExprWithRequest(exprStr, params, matching)
	require.NoError(t, err)
	assert.Equal(t, beforeMatched, reversedMatched)
	require.Len(t, reversedTrace.RequestRules, 1)
	assert.True(t, reversedTrace.RequestRules[0].Matched)
	assert.Equal(t, float64(6), reversedTrace.RequestRules[0].Multiplier)
}
