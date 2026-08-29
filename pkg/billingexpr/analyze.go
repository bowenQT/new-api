package billingexpr

import (
	"math"
	"sort"

	"github.com/expr-lang/expr/ast"
)

// Static expression analysis.
//
// Offline tooling that compares two versions of the same price needs to know
// where an expression's behaviour can change discontinuously, so it can probe
// those points instead of guessing at them. That is a compile-time question
// about the expression's own syntax tree, so it is answered here with the same
// AST machinery the request-rule detection uses, rather than by string
// matching on the stored expression.
//
// Nothing here runs on a billing path.

// analyzedVariables are the token dimensions a boundary may be stated against.
// Identifiers outside this set (helper functions, request probes, the tier
// callback) carry no usage-vector meaning and are ignored.
var analyzedVariables = map[string]bool{
	"p":     true,
	"c":     true,
	"len":   true,
	"cr":    true,
	"cc":    true,
	"cc1h":  true,
	"img":   true,
	"img_o": true,
	"ai":    true,
	"ao":    true,
}

// MaxTierBoundariesPerVariable bounds how many boundaries are reported for one
// variable, so a hostile or generated expression cannot make a caller's probe
// set grow without limit.
const MaxTierBoundariesPerVariable = 4

// maxAnalyzedBoundary rejects comparison literals too large to be a token
// count. Anything beyond it is a sentinel or corrupt input, not a tier edge.
const maxAnalyzedBoundary = float64(1e12)

// TierBoundaries reports, per token variable, the ascending numeric literals
// the expression compares that variable against — the points where a tiered
// expression switches branches. An expression that cannot be compiled, or that
// states no comparison at all, returns an empty map.
//
// Only order and equality comparisons against a bare variable count. The
// result is deduplicated, sorted ascending, and capped at
// MaxTierBoundariesPerVariable entries per variable (the smallest ones, which
// are the tier edges reachable by realistic usage).
func TierBoundaries(exprStr string) map[string][]float64 {
	boundaries := map[string][]float64{}
	if exprStr == "" {
		return boundaries
	}
	prog, err := CompileFromCache(exprStr)
	if err != nil {
		return boundaries
	}
	found := map[string]map[float64]bool{}
	ast.Find(prog.Node(), func(node ast.Node) bool {
		binary, ok := node.(*ast.BinaryNode)
		if !ok {
			return false
		}
		switch binary.Operator {
		case "<", "<=", ">", ">=", "==", "!=":
		default:
			return false
		}
		variable, value, ok := comparisonBoundary(binary)
		if !ok {
			return false
		}
		if found[variable] == nil {
			found[variable] = map[float64]bool{}
		}
		found[variable][value] = true
		return false
	})
	for variable, values := range found {
		list := make([]float64, 0, len(values))
		for value := range values {
			list = append(list, value)
		}
		sort.Float64s(list)
		if len(list) > MaxTierBoundariesPerVariable {
			list = list[:MaxTierBoundariesPerVariable]
		}
		boundaries[variable] = list
	}
	return boundaries
}

// comparisonBoundary matches one side of a comparison against an analyzed
// variable and the other against a usable numeric literal.
func comparisonBoundary(binary *ast.BinaryNode) (string, float64, bool) {
	if variable, ok := analyzedVariable(binary.Left); ok {
		if value, ok := analyzedBoundaryNumber(binary.Right); ok {
			return variable, value, true
		}
		return "", 0, false
	}
	if variable, ok := analyzedVariable(binary.Right); ok {
		if value, ok := analyzedBoundaryNumber(binary.Left); ok {
			return variable, value, true
		}
	}
	return "", 0, false
}

func analyzedVariable(node ast.Node) (string, bool) {
	identifier, ok := node.(*ast.IdentifierNode)
	if !ok || !analyzedVariables[identifier.Value] {
		return "", false
	}
	return identifier.Value, true
}

// analyzedBoundaryNumber accepts only finite, non-negative literals within the
// token-count range: a negative or absurd boundary is never reachable by a
// usage vector, so probing it would waste evaluations without proving anything.
func analyzedBoundaryNumber(node ast.Node) (float64, bool) {
	value, ok := requestRuleNumber(node)
	if !ok {
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maxAnalyzedBoundary {
		return 0, false
	}
	return value, true
}

// CanonicalForm returns the printed form of an expression's compiled syntax
// tree, so two expressions that differ only in whitespace, redundant
// parentheses, or literal formatting produce the same string. It is the only
// equivalence proof this package offers: two expressions with the same
// canonical form always compute the same result, while a different canonical
// form proves nothing either way.
//
// The second return value is false when the expression cannot be compiled, in
// which case no equivalence may be assumed.
func CanonicalForm(exprStr string) (string, bool) {
	if exprStr == "" {
		return "", false
	}
	prog, err := CompileFromCache(exprStr)
	if err != nil {
		return "", false
	}
	return prog.Node().String(), true
}
