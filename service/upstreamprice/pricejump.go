package upstreamprice

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

// Price movement detection (spec §13).
//
// A fingerprint change tells us a source model's recorded price is not the one
// the baseline run recorded, but not by how much: the fingerprint covers the
// whole canonical payload and the expression is opaque to it. The magnitude is
// therefore measured the same way the §9.3 comparison measures a cost — by
// projecting both expressions into USD under identical usage vectors — rather
// than by parsing coefficients out of the expression text, so flat, tiered, and
// per-call prices are all compared on one scale and a future formula kind needs
// no new comparator.
//
// This is evidence, not a gate: the movement is recorded on the run and raises
// an alert, and the commit proceeds exactly as it would have.

const (
	// DefaultPriceJumpThreshold is the change rate above which a projected
	// price movement is reported (spec §13). 0.5 means "more than 50%".
	DefaultPriceJumpThreshold = 0.5
	// MaxPriceJumpThreshold bounds settings.price_jump_threshold. A change rate
	// is not a fraction of a whole — a tenfold increase is 9.0 — so the range
	// extends far past 1, while still refusing a value that could never fire.
	MaxPriceJumpThreshold = float64(1000)
)

// priceJumpProbeVersion identifies the probe construction rules below. It is
// recorded on every summary so a stored movement is always readable against the
// rules that produced it.
const priceJumpProbeVersion = 1

// priceJumpSummaryVersion identifies the stored summary layout.
const priceJumpSummaryVersion = 1

// Movement dimensions reported on a summary entry and on an alert.
const (
	PriceJumpDimensionInput      = "input"
	PriceJumpDimensionOutput     = "output"
	PriceJumpDimensionCacheRead  = "cache_read"
	PriceJumpDimensionCacheWrite = "cache_write"
	// PriceJumpDimensionPerCall is the single dimension of a per-call price.
	// Per-call expressions are still probed with the non-zero usage vectors
	// below, because the expression validator does not forbid a per-call price
	// from referencing usage variables and a `p * k` shape must not escape
	// detection; the four probes collapse into this one dimension because a
	// per-call amount is charged once per request, not per token category.
	PriceJumpDimensionPerCall = "per_call"
	// PriceJumpDimensionExprUnverified marks a fingerprint change whose probes
	// showed no movement at all and whose expressions could not be proven
	// equivalent. It carries no change rate and is never a threshold breach; it
	// is the fail-closed outcome that keeps an unexplained price change visible
	// instead of silently passing it.
	PriceJumpDimensionExprUnverified = "expr_unverified"
)

// priceJumpUnitTokens is the magnitude of the non-zero dimension in a unit
// vector probe. It equals the token_expr_v1 coefficient unit (USD per 1M
// tokens), so a flat expression projects to exactly its own coefficient and a
// reported movement reads as the movement of that coefficient.
const priceJumpUnitTokens = float64(1_000_000)

// priceJumpBaseContexts are the two context lengths every probe set starts
// from: one comfortably below any published long-context tier boundary and one
// above it. Boundaries extracted from the expressions themselves are added on
// top, so a tier edge that moved is probed directly rather than hopefully.
var priceJumpBaseContexts = []float64{1_000, 1_000_000}

// maxPriceJumpProbes bounds the probe set of a single model.
const maxPriceJumpProbes = 256

// MaxPriceJumpEntries bounds how many movements one run's summary carries. A
// source-wide repricing reports the largest movements and states the full count
// alongside them, so a truncated summary is never mistaken for the whole story.
const MaxPriceJumpEntries = 20

// maxPriceJumpSummaryBytes bounds the serialized summary written to the run.
const maxPriceJumpSummaryBytes = 8192

// priceJumpDimension binds a reported dimension to the usage variable whose
// probe isolates it.
type priceJumpDimension struct {
	name     string
	variable string
}

var tokenPriceJumpDimensions = []priceJumpDimension{
	{PriceJumpDimensionInput, "p"},
	{PriceJumpDimensionOutput, "c"},
	{PriceJumpDimensionCacheRead, "cr"},
	{PriceJumpDimensionCacheWrite, "cc"},
}

// priceJumpEntry is one reported movement of one source model along one
// dimension. Both the source model name and the canonical name are carried:
// several source models can map to one canonical model, so the canonical name
// alone cannot tell an admin which upstream entry moved.
type priceJumpEntry struct {
	SourceModelName    string   `json:"source_model_name"`
	CanonicalModelName string   `json:"canonical_model_name,omitempty"`
	Dimension          string   `json:"dimension"`
	ProbeContext       string   `json:"probe_context,omitempty"`
	PreviousUSD        *float64 `json:"previous_usd,omitempty"`
	CurrentUSD         *float64 `json:"current_usd,omitempty"`
	ChangeRate         *float64 `json:"change_rate,omitempty"`
	// FromZero marks a price that was zero and is not any more. The rate is
	// undefined there, but the movement is exactly the kind of bad data the
	// threshold exists to catch, so it is always reported.
	FromZero bool `json:"from_zero,omitempty"`
}

// priceJumpSummary is the bounded JSON value stored on PriceSyncRun.
type priceJumpSummary struct {
	Version      int     `json:"version"`
	ProbeVersion int     `json:"probe_version"`
	Threshold    float64 `json:"threshold"`
	// Total is the number of movements observed, which exceeds len(Entries)
	// when the summary was truncated.
	Total   int              `json:"total"`
	Entries []priceJumpEntry `json:"entries"`
}

// priceJumpThreshold resolves the source's configured change-rate threshold.
func priceJumpThreshold(config SourceConfig) float64 {
	if config.Settings.PriceJumpThreshold != nil {
		return *config.Settings.PriceJumpThreshold
	}
	return DefaultPriceJumpThreshold
}

// evaluatePriceJump measures the movement between a baseline snapshot and the
// price about to replace it, and returns the dimensions whose movement exceeds
// the threshold. A model with no baseline snapshot is new and returns nothing.
//
// When every probe agrees the price did not move, the fingerprint change still
// has to be explained: either the two expressions are provably the same program
// (the change came from a non-price field of the canonical payload) or the
// movement could not be verified, which is reported as
// PriceJumpDimensionExprUnverified rather than passed silently.
func evaluatePriceJump(previous *model.PriceSnapshot, current *NormalizedPrice, threshold float64) []priceJumpEntry {
	if previous == nil || current == nil {
		return nil
	}
	if priceExprProvablyEquivalent(previous, current) {
		// The fingerprint moved on a non-price field of the canonical payload —
		// a remapped canonical name, new metadata, a new effective_at. There is
		// no price movement to measure and nothing to review.
		return nil
	}
	entries := make([]priceJumpEntry, 0, len(tokenPriceJumpDimensions))
	measured := false
	for _, probe := range buildPriceJumpProbes(previous, current) {
		previousUSD, err := projectPriceExpr(previous.FormulaKind, previous.PriceExpr, probe.params)
		if err != nil {
			continue
		}
		// A formula-kind change is compared in USD under the same usage vector
		// (spec §6.1): each side converts by its own kind's unit rule, so the
		// amounts stay comparable and a kind change that did not move the price
		// raises nothing.
		currentUSD, err := projectPriceExpr(current.FormulaKind, current.PriceExpr, probe.params)
		if err != nil {
			continue
		}
		rate, fromZero, moved := priceMovement(previousUSD, currentUSD)
		if moved {
			measured = true
		}
		if !moved || (!fromZero && rate <= threshold) {
			continue
		}
		entry := priceJumpEntry{
			SourceModelName:    current.SourceModelName,
			CanonicalModelName: current.CanonicalModelName,
			Dimension:          probe.dimension,
			ProbeContext:       probe.context,
			PreviousUSD:        &previousUSD,
			CurrentUSD:         &currentUSD,
			FromZero:           fromZero,
		}
		if !fromZero {
			entry.ChangeRate = common.GetPointer(rate)
		}
		entries = keepWorstPerDimension(entries, entry)
	}
	if len(entries) > 0 {
		return entries
	}
	if measured {
		// The probes did see the price move, just not past the threshold. The
		// fingerprint change is accounted for, including a formula-kind change
		// whose USD delta under the same usage vector stayed small (spec §6.1).
		return nil
	}
	// Fail closed: the fingerprint changed, the expressions are not provably the
	// same program, and no probe measured any difference at all. That can mean
	// the change is genuinely price-neutral, or that the probes missed it — the
	// two are indistinguishable from here, so the run is marked for review. The
	// entry states no rate and carries its own dimension, so it reads as the
	// note it is and never as a threshold breach.
	return []priceJumpEntry{{
		SourceModelName:    current.SourceModelName,
		CanonicalModelName: current.CanonicalModelName,
		Dimension:          PriceJumpDimensionExprUnverified,
	}}
}

// keepWorstPerDimension keeps one entry per dimension — the probe that moved
// the most — so a tier edge probed at several points reports once, with the
// context that produced the worst movement.
func keepWorstPerDimension(entries []priceJumpEntry, candidate priceJumpEntry) []priceJumpEntry {
	for i := range entries {
		if entries[i].Dimension != candidate.Dimension {
			continue
		}
		if priceJumpSeverity(candidate) > priceJumpSeverity(entries[i]) {
			entries[i] = candidate
		}
		return entries
	}
	return append(entries, candidate)
}

// priceJumpSeverity orders entries for truncation: a price that appeared out of
// zero outranks any finite rate, and an unverifiable expression ranks below
// every measured movement because it states no magnitude.
func priceJumpSeverity(entry priceJumpEntry) float64 {
	switch {
	case entry.FromZero:
		return math.Inf(1)
	case entry.ChangeRate != nil:
		return *entry.ChangeRate
	default:
		return -1
	}
}

// priceMovement reports the change rate between two projected amounts. Two
// zeroes are not a movement; a price appearing out of zero has no defined rate
// but is always reported; a price falling to zero is a rate of 1.
func priceMovement(previous, current float64) (rate float64, fromZero bool, moved bool) {
	if previous == current {
		return 0, false, false
	}
	if previous == 0 {
		return 0, true, true
	}
	rate = math.Abs(current-previous) / math.Abs(previous)
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, false, false
	}
	return rate, false, true
}

// priceExprProvablyEquivalent reports whether the two prices are the same
// program stated differently. It is deliberately one-directional: a true answer
// proves the price did not change, while a false answer proves nothing and
// therefore fails closed into a review entry.
func priceExprProvablyEquivalent(previous *model.PriceSnapshot, current *NormalizedPrice) bool {
	if previous.FormulaKind != current.FormulaKind {
		return false
	}
	previousForm, ok := billingexpr.CanonicalForm(previous.PriceExpr)
	if !ok {
		return false
	}
	currentForm, ok := billingexpr.CanonicalForm(current.PriceExpr)
	if !ok {
		return false
	}
	return previousForm == currentForm
}

// priceJumpProbe is one evaluation point applied identically to both
// expressions.
type priceJumpProbe struct {
	dimension string
	context   string
	params    billingexpr.TokenParams
}

// buildPriceJumpProbes derives the evaluation points for one comparison.
//
// The fixed part is a unit vector per dimension under two context lengths. The
// derived part comes from both expressions' own tier boundaries: every literal
// either expression compares a usage variable against is probed at the
// boundary and on both sides of it, so a tier edge that moved — or a branch
// only one of the two expressions has — is evaluated where it actually
// switches, instead of being missed between two fixed sample points.
func buildPriceJumpProbes(previous *model.PriceSnapshot, current *NormalizedPrice) []priceJumpProbe {
	boundaries := map[string][]float64{}
	for _, expression := range []string{previous.PriceExpr, current.PriceExpr} {
		for variable, values := range billingexpr.TierBoundaries(expression) {
			boundaries[variable] = append(boundaries[variable], values...)
		}
	}
	contexts := probePoints(priceJumpBaseContexts, boundaries["len"])

	dimensions := tokenPriceJumpDimensions
	perCall := current.FormulaKind == FormulaKindPerCallV1
	probes := make([]priceJumpProbe, 0, len(dimensions)*len(contexts))
	for _, dimension := range dimensions {
		name := dimension.name
		if perCall {
			name = PriceJumpDimensionPerCall
		}
		for _, magnitude := range probePoints([]float64{priceJumpUnitTokens}, boundaries[dimension.variable]) {
			for _, context := range contexts {
				if len(probes) >= maxPriceJumpProbes {
					return probes
				}
				probes = append(probes, priceJumpProbe{
					dimension: name,
					context:   probeContextLabel(dimension.variable, magnitude, context),
					params:    probeParams(dimension.variable, magnitude, context),
				})
			}
		}
	}
	return probes
}

// probePoints expands a set of tier boundaries into evaluation points: the
// boundary itself and one unit on either side of it, which is where a
// half-open tier condition changes its answer. Results are deduplicated and
// sorted so a probe set is a deterministic function of the two expressions.
func probePoints(base []float64, boundaries []float64) []float64 {
	seen := make(map[float64]bool, len(base)+len(boundaries)*3)
	points := make([]float64, 0, len(base)+len(boundaries)*3)
	add := func(value float64) {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || seen[value] {
			return
		}
		seen[value] = true
		points = append(points, value)
	}
	for _, value := range base {
		add(value)
	}
	for _, boundary := range boundaries {
		add(boundary - 1)
		add(boundary)
		add(boundary + 1)
	}
	sort.Float64s(points)
	return points
}

// probeParams builds the usage vector of one probe. The context length is set
// independently of the token magnitude rather than derived from it: a probe is
// a synthetic evaluation point applied to both expressions alike, and pinning
// len separately is what lets an output-only or cache-only coefficient be
// measured inside a long-context branch at all.
func probeParams(variable string, magnitude float64, context float64) billingexpr.TokenParams {
	params := billingexpr.TokenParams{Len: context}
	switch variable {
	case "p":
		params.P = magnitude
	case "c":
		params.C = magnitude
	case "cr":
		params.CR = magnitude
	case "cc":
		params.CC = magnitude
	}
	return params
}

func probeContextLabel(variable string, magnitude float64, context float64) string {
	return fmt.Sprintf("%s=%s,len=%s", variable,
		strconv.FormatFloat(magnitude, 'f', -1, 64),
		strconv.FormatFloat(context, 'f', -1, 64))
}

// encodePriceJumpSummary serializes a run's movements. Entries are ordered by
// severity and truncated, but Total always states how many were observed, so a
// truncated summary reports its own truncation. No movement serializes to the
// empty string, which is also what rows written before the column existed read
// back as.
func encodePriceJumpSummary(threshold float64, entries []priceJumpEntry) string {
	if len(entries) == 0 {
		return ""
	}
	ordered := append([]priceJumpEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := priceJumpSeverity(ordered[i]), priceJumpSeverity(ordered[j])
		if left != right {
			return left > right
		}
		if ordered[i].SourceModelName != ordered[j].SourceModelName {
			return ordered[i].SourceModelName < ordered[j].SourceModelName
		}
		return ordered[i].Dimension < ordered[j].Dimension
	})
	summary := priceJumpSummary{
		Version:      priceJumpSummaryVersion,
		ProbeVersion: priceJumpProbeVersion,
		Threshold:    threshold,
		Total:        len(ordered),
	}
	kept := ordered
	if len(kept) > MaxPriceJumpEntries {
		kept = kept[:MaxPriceJumpEntries]
	}
	for {
		summary.Entries = kept
		data, err := common.Marshal(summary)
		if err != nil {
			return ""
		}
		if len(data) <= maxPriceJumpSummaryBytes || len(kept) <= 1 {
			return string(data)
		}
		kept = kept[:len(kept)/2]
	}
}

// decodePriceJumpSummary parses a stored summary. An empty or unparsable value
// yields no movements: alerting must not fail because one run carries a value
// it cannot read.
func decodePriceJumpSummary(raw string) priceJumpSummary {
	summary := priceJumpSummary{}
	if raw == "" {
		return summary
	}
	if err := common.UnmarshalJsonStr(raw, &summary); err != nil {
		common.SysError(fmt.Sprintf("upstream price jump summary could not be parsed: %v", err))
		return priceJumpSummary{}
	}
	return summary
}
