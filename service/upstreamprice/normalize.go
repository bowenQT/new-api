package upstreamprice

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
)

// FingerprintVersion identifies the fingerprint algorithm and canonical
// payload layout. It is part of the canonical payload itself (spec §7.2), so
// bumping it changes every fingerprint.
const FingerprintVersion = "fp1"

// ExprVersionV1 is the billing expression version recorded on snapshots.
// Bodies without a version prefix are evaluated as v1 by pkg/billingexpr.
const ExprVersionV1 = "v1"

// Mapping status values recorded on snapshots (spec §7.5).
const (
	MappingStatusDefault  = "mapped_default"
	MappingStatusExplicit = "mapped_explicit"
	MappingStatusUnmapped = "unmapped"
)

// Decimal input bounds enforced BEFORE shopspring parsing so hostile inputs
// (huge exponents, absurd digit counts) never reach the arbitrary-precision
// parser (spec §12 defense in depth).
const (
	maxDecimalStringLength   = 64
	maxDecimalIntegerDigits  = 12
	maxDecimalFractionDigits = 18
)

// checkDecimalShape enforces plain decimal syntax: an optional sign, digits,
// at most one dot, no exponent notation, and bounded digit counts. Rejecting
// 'e'/'E' up front keeps inputs like "1e2000000000" away from the decimal
// parser entirely.
func checkDecimalShape(value string) error {
	body := value
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" {
		return fmt.Errorf("invalid decimal string %q", value)
	}
	integerPart := body
	fractionPart := ""
	if dot := strings.IndexByte(body, '.'); dot >= 0 {
		integerPart, fractionPart = body[:dot], body[dot+1:]
	}
	if integerPart == "" && fractionPart == "" {
		return fmt.Errorf("invalid decimal string %q", value)
	}
	for _, part := range []string{integerPart, fractionPart} {
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return fmt.Errorf("invalid decimal string %q: only plain decimal notation is accepted", value)
			}
		}
	}
	if len(integerPart) > maxDecimalIntegerDigits {
		return fmt.Errorf("decimal string %q exceeds %d integer digits", value, maxDecimalIntegerDigits)
	}
	if len(fractionPart) > maxDecimalFractionDigits {
		return fmt.Errorf("decimal string %q exceeds %d fraction digits", value, maxDecimalFractionDigits)
	}
	return nil
}

// NormalizeDecimalString parses a decimal amount string and returns its
// canonical representation. Negative amounts, exponent notation, oversized
// digit counts, and non-decimal input (including NaN/Inf spellings) are
// rejected before parsing.
func NormalizeDecimalString(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("empty decimal string")
	}
	if len(trimmed) > maxDecimalStringLength {
		return "", fmt.Errorf("decimal string exceeds %d characters", maxDecimalStringLength)
	}
	if err := checkDecimalShape(trimmed); err != nil {
		return "", err
	}
	parsed, err := decimal.NewFromString(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid decimal string %q: %w", value, err)
	}
	if parsed.IsNegative() {
		return "", fmt.Errorf("negative price %q rejected", value)
	}
	return parsed.String(), nil
}

// maxPerTokenPriceUSD bounds a single-token price: anything above 1 USD per
// token is treated as corrupt source data and rejected.
var maxPerTokenPriceUSD = decimal.NewFromInt(1)

// PerMillionTokenCoefficient converts a USD-per-token decimal string into the
// USD-per-1M-tokens coefficient used by token_expr_v1 expressions. The
// conversion shifts the decimal exponent, so no float rounding is involved.
func PerMillionTokenCoefficient(perTokenCost string) (string, error) {
	normalized, err := NormalizeDecimalString(perTokenCost)
	if err != nil {
		return "", err
	}
	parsed, err := decimal.NewFromString(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid decimal string %q: %w", perTokenCost, err)
	}
	if parsed.GreaterThan(maxPerTokenPriceUSD) {
		return "", fmt.Errorf("per-token price %q exceeds 1 USD and is rejected", perTokenCost)
	}
	return parsed.Shift(6).String(), nil
}

// maxPerMillionTokenPriceUSD bounds a USD-per-1M-tokens coefficient. It is the
// same ceiling as maxPerTokenPriceUSD expressed in the other unit, so both
// source conventions are rejected at the same real price.
var maxPerMillionTokenPriceUSD = decimal.NewFromInt(1_000_000)

// MillionTokenCoefficient validates a source price that is already quoted in
// USD per 1M tokens and returns its canonical decimal string. Sources quoting
// USD per token must use PerMillionTokenCoefficient instead.
func MillionTokenCoefficient(perMillionCost string) (string, error) {
	normalized, err := NormalizeDecimalString(perMillionCost)
	if err != nil {
		return "", err
	}
	parsed, err := decimal.NewFromString(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid decimal string %q: %w", perMillionCost, err)
	}
	if parsed.GreaterThan(maxPerMillionTokenPriceUSD) {
		return "", fmt.Errorf("per-1M-token price %q exceeds %s USD and is rejected", perMillionCost, maxPerMillionTokenPriceUSD.String())
	}
	return normalized, nil
}

// truncateUTF8 cuts a string to at most max bytes without splitting a UTF-8
// sequence.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// MapCanonicalModelName resolves the unified model name (spec §7.5): an
// explicit mapping wins, otherwise one "provider/" prefix level is stripped;
// names that cannot be mapped keep the original name and are marked unmapped.
func MapCanonicalModelName(sourceModelName string, explicit map[string]string) (string, string) {
	if target, ok := explicit[sourceModelName]; ok {
		if strings.TrimSpace(target) == "" {
			return sourceModelName, MappingStatusUnmapped
		}
		return target, MappingStatusExplicit
	}
	slash := strings.Index(sourceModelName, "/")
	if slash <= 0 || slash == len(sourceModelName)-1 {
		return sourceModelName, MappingStatusUnmapped
	}
	return sourceModelName[slash+1:], MappingStatusDefault
}

// TierBound is one half-open [Min, Max) price tier in source token units. A
// nil Max means the tier is unbounded above.
type TierBound struct {
	Cost string
	Min  int64
	Max  *int64
}

// NormalizedPrice is a fully resolved, fingerprinted observation ready for
// validation and persistence.
type NormalizedPrice struct {
	SourceModelName    string
	CanonicalModelName string
	MappingStatus      string
	Role               PriceRole
	Scope              PriceScope
	Provider           string
	Currency           string
	FormulaKind        string
	PriceExpr          string
	ExprVersion        string
	EffectiveAt        *int64
	Metadata           map[string]string
	Fingerprint        string
}

// NormalizeObservation applies the role/scope authority algorithm, the model
// name mapping, and computes the versioned canonical fingerprint.
func NormalizeObservation(obs Observation, source SourceConfig, adapter Adapter) (*NormalizedPrice, error) {
	role, scope, err := ResolveObservationRoleScope(obs, source, adapter)
	if err != nil {
		return nil, err
	}
	canonical := obs.CanonicalModelName
	mappingStatus := MappingStatusExplicit
	if canonical == "" {
		canonical, mappingStatus = MapCanonicalModelName(obs.SourceModelName, source.Settings.ModelMappings)
	}
	normalized := &NormalizedPrice{
		SourceModelName:    obs.SourceModelName,
		CanonicalModelName: canonical,
		MappingStatus:      mappingStatus,
		Role:               role,
		Scope:              scope,
		Provider:           obs.Provider,
		Currency:           obs.Currency,
		FormulaKind:        obs.FormulaKind,
		PriceExpr:          obs.PriceExpr,
		ExprVersion:        ExprVersionV1,
		Metadata:           obs.Metadata,
	}
	if obs.EffectiveAt != nil {
		unix := obs.EffectiveAt.Unix()
		normalized.EffectiveAt = &unix
	}
	fingerprint, err := ComputeFingerprint(normalized)
	if err != nil {
		return nil, err
	}
	normalized.Fingerprint = fingerprint
	return normalized, nil
}

type fingerprintMetadataEntry struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

// canonicalPricePayload covers every semantic field of a snapshot (spec §7.2):
// a change in any of them, including the model mapping result, produces a new
// fingerprint and therefore a new snapshot.
type canonicalPricePayload struct {
	FingerprintVersion string                     `json:"fingerprint_version"`
	Role               string                     `json:"role"`
	Scope              string                     `json:"scope"`
	Provider           string                     `json:"provider"`
	SourceModel        string                     `json:"source_model"`
	CanonicalModel     string                     `json:"canonical_model"`
	MappingStatus      string                     `json:"mapping_status"`
	Currency           string                     `json:"currency"`
	FormulaKind        string                     `json:"formula_kind"`
	PriceExpr          string                     `json:"price_expr"`
	ExprVersion        string                     `json:"expr_version"`
	EffectiveAt        *int64                     `json:"effective_at"`
	Metadata           []fingerprintMetadataEntry `json:"metadata"`
}

// ComputeFingerprint hashes the versioned canonical payload with SHA-256.
// Metadata entries are sorted by key so map iteration order never leaks into
// the fingerprint.
func ComputeFingerprint(price *NormalizedPrice) (string, error) {
	payload := canonicalPricePayload{
		FingerprintVersion: FingerprintVersion,
		Role:               string(price.Role),
		Scope:              string(price.Scope),
		Provider:           price.Provider,
		SourceModel:        price.SourceModelName,
		CanonicalModel:     price.CanonicalModelName,
		MappingStatus:      price.MappingStatus,
		Currency:           price.Currency,
		FormulaKind:        price.FormulaKind,
		PriceExpr:          price.PriceExpr,
		ExprVersion:        price.ExprVersion,
		EffectiveAt:        price.EffectiveAt,
		Metadata:           sortedMetadata(price.Metadata),
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func sortedMetadata(metadata map[string]string) []fingerprintMetadataEntry {
	if len(metadata) == 0 {
		return []fingerprintMetadataEntry{}
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]fingerprintMetadataEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, fingerprintMetadataEntry{Key: key, Value: metadata[key]})
	}
	return entries
}
