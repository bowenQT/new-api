package billingexpr

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"
)

const maxCacheSize = 256

// DefaultExprVersion is used when an expression string has no version prefix.
const DefaultExprVersion = 1

const (
	requestRuleTraceFunction    = "_trace"
	requestRuleTraceIntFunction = "_trace_int"
)

// ParseExprVersion extracts the version tag and body from an expression string.
// Format: "v1:tier(...)" → version=1, body="tier(...)".
// No prefix defaults to DefaultExprVersion.
func ParseExprVersion(exprStr string) (version int, body string) {
	if strings.HasPrefix(exprStr, "v1:") {
		return 1, exprStr[3:]
	}
	return DefaultExprVersion, exprStr
}

// requestRulePatcher adds trace side effects to existing request multipliers
// without changing the stored expression or its numeric result.
type requestRulePatcher struct {
	requestRules         []RequestRuleTrace
	restrictedIdentifier string
}

func (p *requestRulePatcher) Visit(node *ast.Node) {
	if identifier, ok := (*node).(*ast.IdentifierNode); ok {
		switch identifier.Value {
		case requestRuleTraceFunction, requestRuleTraceIntFunction:
			p.restrictedIdentifier = identifier.Value
		}
		return
	}

	conditional, multiplier, ok := requestRuleFactor(*node)
	if !ok {
		return
	}

	ruleIndex := len(p.requestRules)
	p.requestRules = append(p.requestRules, RequestRuleTrace{
		Cond:       conditional.Cond.String(),
		Multiplier: multiplier,
	})

	traceFunction := requestRuleTraceFunction
	var multiplierNode ast.Node = &ast.FloatNode{Value: multiplier}
	if requestRuleFactorIsInteger(conditional) {
		traceFunction = requestRuleTraceIntFunction
		multiplierNode = conditional.Exp1
	}

	ast.Patch(node, &ast.CallNode{
		Callee: &ast.IdentifierNode{Value: traceFunction},
		Arguments: []ast.Node{
			&ast.IntegerNode{Value: ruleIndex},
			conditional.Cond,
			multiplierNode,
		},
	})
}

// requestRuleFactor reports whether a node is a request-conditional multiplier
// of the exact instrumented shape `<request-probe condition> ? <numeric
// literal> : 1`. It is the single definition of that shape, shared by the
// tracing patcher and the base-expression projection patcher, so the two can
// never disagree about which factors are neutralizable.
func requestRuleFactor(node ast.Node) (*ast.ConditionalNode, float64, bool) {
	conditional, ok := node.(*ast.ConditionalNode)
	if !ok || !conditional.Ternary || !usesRequestProbe(conditional.Cond) {
		return nil, 0, false
	}
	multiplier, multiplierOK := requestRuleNumber(conditional.Exp1)
	fallback, fallbackOK := requestRuleNumber(conditional.Exp2)
	if !multiplierOK || !fallbackOK || fallback != 1 {
		return nil, 0, false
	}
	return conditional, multiplier, true
}

// requestRuleFactorIsInteger reports whether both branches of an instrumented
// factor are integer literals, which requires an integer-preserving
// replacement so operators such as `%` keep a valid operand.
func requestRuleFactorIsInteger(conditional *ast.ConditionalNode) bool {
	if _, multiplierIsInt := conditional.Exp1.(*ast.IntegerNode); !multiplierIsInt {
		return false
	}
	_, fallbackIsInt := conditional.Exp2.(*ast.IntegerNode)
	return fallbackIsInt
}

func requestRuleNumber(node ast.Node) (float64, bool) {
	switch value := node.(type) {
	case *ast.IntegerNode:
		return float64(value.Value), true
	case *ast.FloatNode:
		return value.Value, true
	default:
		return 0, false
	}
}

func usesRequestProbe(node ast.Node) bool {
	return ast.Find(node, func(node ast.Node) bool {
		identifier, ok := node.(*ast.IdentifierNode)
		if !ok {
			return false
		}
		switch identifier.Value {
		case "param", "header", "hour", "minute", "weekday", "month", "day":
			return true
		default:
			return false
		}
	}) != nil
}

type cachedEntry struct {
	prog         *vm.Program
	usedVars     map[string]bool
	requestRules []RequestRuleTrace
	version      int
}

var (
	cacheMu sync.RWMutex
	cache   = make(map[string]*cachedEntry, 64)
)

// compileEnvPrototypeV1 is the v1 type-checking prototype used at compile time.
var compileEnvPrototypeV1 = map[string]interface{}{
	"p":          float64(0),
	"c":          float64(0),
	"len":        float64(0),
	"cr":         float64(0),
	"cc":         float64(0),
	"cc1h":       float64(0),
	"img":        float64(0),
	"img_o":      float64(0),
	"ai":         float64(0),
	"ao":         float64(0),
	"tier":       func(string, float64) float64 { return 0 },
	"_trace":     func(int, bool, float64) float64 { return 1 },
	"_trace_int": func(int, bool, int) int { return 1 },
	"header":     func(string) string { return "" },
	"param":      func(string) interface{} { return nil },
	"has":        func(interface{}, string) bool { return false },
	"hour":       func(string) int { return 0 },
	"minute":     func(string) int { return 0 },
	"weekday":    func(string) int { return 0 },
	"month":      func(string) int { return 0 },
	"day":        func(string) int { return 0 },
	"max":        math.Max,
	"min":        math.Min,
	"abs":        math.Abs,
	"ceil":       math.Ceil,
	"floor":      math.Floor,
}

func getCompileEnv(version int) map[string]interface{} {
	switch version {
	default:
		return compileEnvPrototypeV1
	}
}

// CompileFromCache compiles an expression string, using a cached program when
// available. The cache is keyed by the SHA-256 hex digest of the expression.
func CompileFromCache(exprStr string) (*vm.Program, error) {
	return compileFromCacheByHash(exprStr, ExprHashString(exprStr))
}

// CompileFromCacheByHash is like CompileFromCache but accepts a pre-computed
// hash, useful when the caller already has the BillingSnapshot.ExprHash.
func CompileFromCacheByHash(exprStr, hash string) (*vm.Program, error) {
	return compileFromCacheByHash(exprStr, hash)
}

func compileFromCacheByHash(exprStr, hash string) (*vm.Program, error) {
	entry, err := compileEntryFromCacheByHash(exprStr, hash)
	if err != nil {
		return nil, err
	}
	return entry.prog, nil
}

func compileEntryFromCacheByHash(exprStr, hash string) (*cachedEntry, error) {
	cacheMu.RLock()
	if entry, ok := cache[hash]; ok {
		cacheMu.RUnlock()
		return entry, nil
	}
	cacheMu.RUnlock()

	version, body := ParseExprVersion(exprStr)
	patcher := &requestRulePatcher{}
	prog, err := expr.Compile(body, expr.Env(getCompileEnv(version)), expr.Patch(patcher), expr.AsFloat64())
	if patcher.restrictedIdentifier != "" {
		return nil, fmt.Errorf("expr compile error: identifier %q is reserved for internal use", patcher.restrictedIdentifier)
	}
	if err != nil {
		return nil, fmt.Errorf("expr compile error: %w", err)
	}

	entry := &cachedEntry{
		prog:         prog,
		usedVars:     extractUsedVars(prog),
		requestRules: patcher.requestRules,
		version:      version,
	}
	cacheMu.Lock()
	if len(cache) >= maxCacheSize {
		cache = make(map[string]*cachedEntry, 64)
	}
	cache[hash] = entry
	cacheMu.Unlock()

	return entry, nil
}

// ExprVersion returns the version of a cached expression. Returns DefaultExprVersion
// if the expression hasn't been compiled yet or is empty.
func ExprVersion(exprStr string) int {
	if exprStr == "" {
		return DefaultExprVersion
	}
	hash := ExprHashString(exprStr)
	cacheMu.RLock()
	if entry, ok := cache[hash]; ok {
		cacheMu.RUnlock()
		return entry.version
	}
	cacheMu.RUnlock()
	v, _ := ParseExprVersion(exprStr)
	return v
}

func extractUsedVars(prog *vm.Program) map[string]bool {
	vars := make(map[string]bool)
	node := prog.Node()
	ast.Find(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.IdentifierNode); ok {
			switch id.Value {
			case requestRuleTraceFunction, requestRuleTraceIntFunction:
				return false
			}
			vars[id.Value] = true
		}
		return false
	})
	return vars
}

// UsedVars returns the set of identifier names referenced by an expression.
// The result is cached alongside the compiled program. Returns nil for empty input.
func UsedVars(exprStr string) map[string]bool {
	if exprStr == "" {
		return nil
	}
	hash := ExprHashString(exprStr)
	cacheMu.RLock()
	if entry, ok := cache[hash]; ok {
		cacheMu.RUnlock()
		return entry.usedVars
	}
	cacheMu.RUnlock()

	// Compile (and cache) to populate usedVars
	if _, err := compileFromCacheByHash(exprStr, hash); err != nil {
		return nil
	}
	cacheMu.RLock()
	entry, ok := cache[hash]
	cacheMu.RUnlock()
	if ok {
		return entry.usedVars
	}
	return nil
}

// InvalidateCache clears the compiled-expression caches, both the billing
// programs and the base-expression projection programs.
// Called when billing rules are updated.
func InvalidateCache() {
	cacheMu.Lock()
	cache = make(map[string]*cachedEntry, 64)
	cacheMu.Unlock()

	projectionMu.Lock()
	projectionCache = make(map[string]*projectionEntry, 32)
	projectionMu.Unlock()
}
