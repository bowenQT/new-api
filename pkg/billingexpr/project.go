package billingexpr

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"
)

// Base-expression projection.
//
// Offline tooling (the upstream price catalog comparison in particular) needs
// the price a model would charge with every request-conditional multiplier
// neutralized, evaluated outside any request. Running the normal program with
// an empty request cannot do that: `header("x") == ""` can be true on an empty
// request and `hour`/`weekday` still resolve against the wall clock.
//
// The projection therefore compiles a second program in which every
// instrumented request-rule factor (see requestRuleFactor) is replaced by the
// literal 1 before evaluation, so no request probe is ever called. If the
// expression still references a request probe after that substitution, the
// factor is not of the neutralizable shape and the projection fails closed.
//
// This path is entirely separate from the billing programs: it uses its own
// cache, and RunExpr / RunExprByHash / ComputeTieredQuota keep their exact
// current semantics.

// ErrRequestRuleNotProjectable reports an expression whose request-dependent
// factors cannot be safely neutralized, so no base price can be projected.
var ErrRequestRuleNotProjectable = errors.New("expression references request rules that cannot be neutralized")

// maxProjectionResult bounds a projected base-expression result. Coefficients
// are USD per 1M tokens, so anything beyond this magnitude is corrupt input
// rather than a price.
const maxProjectionResult = 1e15

// baseExprPatcher replaces every instrumented request-rule factor with the
// literal 1. It rejects the reserved trace identifiers for the same reason the
// tracing patcher does: stored expressions must not call them.
type baseExprPatcher struct {
	restrictedIdentifier string
}

func (p *baseExprPatcher) Visit(node *ast.Node) {
	if identifier, ok := (*node).(*ast.IdentifierNode); ok {
		switch identifier.Value {
		case requestRuleTraceFunction, requestRuleTraceIntFunction:
			p.restrictedIdentifier = identifier.Value
		}
		return
	}
	conditional, _, ok := requestRuleFactor(*node)
	if !ok {
		return
	}
	if requestRuleFactorIsInteger(conditional) {
		ast.Patch(node, &ast.IntegerNode{Value: 1})
		return
	}
	ast.Patch(node, &ast.FloatNode{Value: 1})
}

// projectionEntry caches one compiled projection program, or the sticky reason
// the expression cannot be projected. Both outcomes are deterministic for a
// given expression, so caching the refusal avoids recompiling it per query.
type projectionEntry struct {
	prog *vm.Program
	err  error
}

var (
	projectionMu    sync.RWMutex
	projectionCache = make(map[string]*projectionEntry, 32)
)

func compileBaseExprByHash(exprStr, hash string) (*vm.Program, error) {
	projectionMu.RLock()
	if entry, ok := projectionCache[hash]; ok {
		projectionMu.RUnlock()
		return entry.prog, entry.err
	}
	projectionMu.RUnlock()

	entry := &projectionEntry{}
	version, body := ParseExprVersion(exprStr)
	patcher := &baseExprPatcher{}
	prog, err := expr.Compile(body, expr.Env(getCompileEnv(version)), expr.Patch(patcher), expr.AsFloat64())
	switch {
	case patcher.restrictedIdentifier != "":
		entry.err = fmt.Errorf("base expr compile error: identifier %q is reserved for internal use", patcher.restrictedIdentifier)
	case err != nil:
		entry.err = fmt.Errorf("base expr compile error: %w", err)
	case usesRequestProbe(prog.Node()):
		entry.err = ErrRequestRuleNotProjectable
	default:
		entry.prog = prog
	}

	projectionMu.Lock()
	if len(projectionCache) >= maxCacheSize {
		projectionCache = make(map[string]*projectionEntry, 32)
	}
	projectionCache[hash] = entry
	projectionMu.Unlock()
	return entry.prog, entry.err
}

// RunBaseExpr evaluates an expression with every instrumented request-rule
// factor forced to 1 and no request context at all. It returns
// ErrRequestRuleNotProjectable when the expression still depends on a request
// probe after neutralization, and rejects NaN, Inf, and out-of-range results.
//
// It never runs on a billing path: the result is a display-only projection.
func RunBaseExpr(exprStr string, params TokenParams) (float64, TraceResult, error) {
	prog, err := compileBaseExprByHash(exprStr, ExprHashString(exprStr))
	if err != nil {
		return 0, TraceResult{}, err
	}
	result, trace, err := runProgram(prog, nil, params, RequestInput{})
	if err != nil {
		return 0, trace, err
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, trace, errors.New("base expression produced a non-finite result")
	}
	if result < -maxProjectionResult || result > maxProjectionResult {
		return 0, trace, fmt.Errorf("base expression result %g is out of the projectable range", result)
	}
	return result, trace, nil
}
