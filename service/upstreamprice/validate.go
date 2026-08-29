package upstreamprice

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"

	"gorm.io/gorm"
)

// Machine-readable warning codes recorded on run items (spec §7.3).
const (
	WarningInvalidPrice          = "invalid_price"
	WarningInvalidTiers          = "invalid_tiers"
	WarningTierThresholdMismatch = "tier_threshold_mismatch"
	WarningNoTokenPricing        = "no_token_pricing"
	WarningIncompleteTokenPrice  = "incomplete_token_pricing"
	WarningUnsupportedCurrency   = "unsupported_currency"
	WarningRoleScopeOutOfRange   = "role_scope_out_of_range"
	WarningExprValidationFailed  = "expr_validation_failed"
	WarningDuplicateModel        = "duplicate_model"
	WarningFieldTooLong          = "field_too_long"
	// WarningTieredPricingUnsupported marks a source model whose tiered
	// pricing changes the very dimensions Phase 2 normalizes, so recording
	// only the base tier would understate the price.
	WarningTieredPricingUnsupported = "tiered_pricing_unsupported"
)

// Structural length bounds enforced at validation time (defense in depth on
// top of the column types).
const (
	MaxSourceModelNameLength    = 255
	MaxCanonicalModelNameLength = 255
	MaxProviderLength           = 64
	MaxPriceExprLength          = 4096
	MaxMetadataBytes            = 4096
	MaxSourceRevisionLength     = 128
)

// Currency accepted in Phase 1 (spec §7.2).
const CurrencyUSD = "USD"

// ValidateTierBounds checks a tier list against the supported Phase 1 shapes
// under half-open [Min, Max) semantics (spec §6.2): either a single unbounded
// tier starting at 0 (flat-equivalent, returned threshold is nil), or exactly
// two tiers [0, T) + [T, ∞) with a consistent boundary (returned threshold is
// T). Every other shape — overlap, gap, open first tier, bounded last tier,
// or more than two tiers — fails closed.
func ValidateTierBounds(tiers []TierBound) (*int64, error) {
	switch len(tiers) {
	case 0:
		return nil, errors.New("empty tier list")
	case 1:
		tier := tiers[0]
		if tier.Min != 0 {
			return nil, fmt.Errorf("single tier must start at 0, got min=%d", tier.Min)
		}
		if tier.Max != nil {
			return nil, fmt.Errorf("single tier must be unbounded, got max=%d", *tier.Max)
		}
		return nil, nil
	case 2:
		first, second := tiers[0], tiers[1]
		if first.Min != 0 {
			return nil, fmt.Errorf("first tier must start at 0, got min=%d", first.Min)
		}
		if first.Max == nil {
			return nil, errors.New("first tier of a two-tier list must be bounded")
		}
		if second.Max != nil {
			return nil, fmt.Errorf("second tier must be unbounded, got max=%d", *second.Max)
		}
		threshold := *first.Max
		if threshold < 1 {
			return nil, fmt.Errorf("tier boundary must be >= 1, got %d", threshold)
		}
		if second.Min != threshold {
			return nil, fmt.Errorf("tiers do not cover contiguously: [0,%d) then [%d,∞)", threshold, second.Min)
		}
		return &threshold, nil
	default:
		return nil, fmt.Errorf("unsupported tier count %d (Phase 1 supports 1 or 2)", len(tiers))
	}
}

// exprSmokeVectors exercise flat and long-context branches, cache variables,
// and the zero vector. Coefficients are non-negative by construction, but the
// smoke test still guards against expressions that evaluate negative,
// NaN, or Inf (spec §8.2).
var exprSmokeVectors = []billingexpr.TokenParams{
	{},
	{P: 1000, C: 1000, Len: 1000, CR: 100, CC: 100},
	{P: 150000, C: 20000, Len: 199999, CR: 50000, CC: 10000},
	{P: 300000, C: 50000, Len: 300000, CR: 100000, CC: 20000},
	{P: 2000000, C: 2000000, Len: 2000000, CR: 1000000, CC: 1000000},
}

// ValidatePriceExpr compiles a generated price expression with the billing
// expression engine and smoke-tests it: every sample vector must produce a
// finite, non-negative result.
func ValidatePriceExpr(priceExpr string) error {
	if len(priceExpr) > MaxPriceExprLength {
		return fmt.Errorf("price expression exceeds %d bytes", MaxPriceExprLength)
	}
	if _, err := billingexpr.CompileFromCache(priceExpr); err != nil {
		return fmt.Errorf("price expression compile failed: %w", err)
	}
	for _, vector := range exprSmokeVectors {
		result, _, err := billingexpr.RunExpr(priceExpr, vector)
		if err != nil {
			return fmt.Errorf("price expression smoke run failed at {p=%g,len=%g}: %w", vector.P, vector.Len, err)
		}
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return fmt.Errorf("price expression produced non-finite result at {p=%g,len=%g}", vector.P, vector.Len)
		}
		if result < 0 {
			return fmt.Errorf("price expression produced negative result %g at {p=%g,len=%g}", result, vector.P, vector.Len)
		}
	}
	return nil
}

// ValidateNormalizedPrice performs the observation-level acceptance checks
// (spec §8.2): oversized fields, unknown currency, unknown formula kind,
// invalid role/scope, or a failing expression smoke test reject the
// observation.
func ValidateNormalizedPrice(price *NormalizedPrice) (string, error) {
	if price.SourceModelName == "" || len(price.SourceModelName) > MaxSourceModelNameLength {
		return WarningFieldTooLong, fmt.Errorf("source model name must be 1-%d bytes", MaxSourceModelNameLength)
	}
	if len(price.CanonicalModelName) > MaxCanonicalModelNameLength {
		return WarningFieldTooLong, fmt.Errorf("canonical model name exceeds %d bytes", MaxCanonicalModelNameLength)
	}
	if len(price.Provider) > MaxProviderLength {
		return WarningFieldTooLong, fmt.Errorf("provider exceeds %d bytes", MaxProviderLength)
	}
	if metadataJson, err := common.Marshal(price.Metadata); err != nil {
		return WarningInvalidPrice, err
	} else if len(metadataJson) > MaxMetadataBytes {
		return WarningFieldTooLong, fmt.Errorf("metadata exceeds %d bytes", MaxMetadataBytes)
	}
	if price.Currency != CurrencyUSD {
		return WarningUnsupportedCurrency, fmt.Errorf("unsupported currency %q (Phase 1 allows USD only)", price.Currency)
	}
	if price.FormulaKind != FormulaKindTokenExprV1 && price.FormulaKind != FormulaKindPerCallV1 {
		return WarningInvalidPrice, fmt.Errorf("unsupported formula kind %q", price.FormulaKind)
	}
	if !IsValidPriceRole(price.Role) || !IsValidPriceScope(price.Scope) {
		return WarningRoleScopeOutOfRange, fmt.Errorf("invalid role/scope %q/%q", price.Role, price.Scope)
	}
	if err := ValidatePriceExpr(price.PriceExpr); err != nil {
		return WarningExprValidationFailed, err
	}
	return "", nil
}

// MinScheduleIntervalSeconds is the shortest per-source scheduling interval
// (spec §8.4): public price endpoints must not be polled more than once every
// six hours.
const MinScheduleIntervalSeconds = int64(6 * 60 * 60)

// ValidatePriceSourceForWrite is the authoritative service-layer validation of
// source create/update requests (spec §7.1): role/channel combination rules,
// adapter membership, and settings shape. MySQL CHECK constraints are not
// relied on.
func ValidatePriceSourceForWrite(source *model.PriceSource) error {
	if source.Name == "" || len(source.Name) > 128 {
		return errors.New("source name must be 1-128 characters")
	}
	role := PriceRole(source.Role)
	scope := PriceScope(source.Scope)
	if !IsValidPriceRole(role) {
		return fmt.Errorf("invalid role %q", source.Role)
	}
	if !IsValidPriceScope(scope) {
		return fmt.Errorf("invalid scope %q", source.Scope)
	}

	config, err := SourceConfigFromModel(source)
	if err != nil {
		return err
	}
	if config.Settings.CoverageDropThreshold != nil {
		threshold := *config.Settings.CoverageDropThreshold
		if math.IsNaN(threshold) || threshold <= 0 || threshold > 1 {
			return errors.New("coverage_drop_threshold must be in (0, 1]")
		}
	}
	if config.Settings.StaleThresholdSeconds != nil && *config.Settings.StaleThresholdSeconds <= 0 {
		return errors.New("stale_threshold_seconds must be positive")
	}
	for from, to := range config.Settings.ModelMappings {
		if from == "" || len(from) > MaxSourceModelNameLength {
			return fmt.Errorf("model mapping key must be 1-%d bytes", MaxSourceModelNameLength)
		}
		if strings.TrimSpace(to) == "" || len(to) > MaxCanonicalModelNameLength {
			return fmt.Errorf("model mapping target for %q must be 1-%d bytes", from, MaxCanonicalModelNameLength)
		}
	}

	adapter, ok := GetAdapter(source.AdapterKey)
	if !ok {
		return fmt.Errorf("unknown adapter key %q", source.AdapterKey)
	}
	if !containsRole(adapter.AllowedRoles(), role) {
		return fmt.Errorf("adapter %q does not allow role %q", source.AdapterKey, source.Role)
	}
	if !containsScope(adapter.AllowedScopes(), scope) {
		return fmt.Errorf("adapter %q does not allow scope %q", source.AdapterKey, source.Scope)
	}
	if !adapter.Supports(config) {
		return fmt.Errorf("adapter %q does not support this source configuration", source.AdapterKey)
	}

	if source.ScheduleIntervalSeconds != 0 && source.ScheduleIntervalSeconds < MinScheduleIntervalSeconds {
		return fmt.Errorf("schedule_interval_seconds must be at least %d", MinScheduleIntervalSeconds)
	}
	if source.ScheduleEnabled && source.ScheduleIntervalSeconds < MinScheduleIntervalSeconds {
		return fmt.Errorf("scheduled sync requires schedule_interval_seconds of at least %d", MinScheduleIntervalSeconds)
	}

	if role == RoleSupplierCost {
		if source.ChannelId == nil {
			return errors.New("supplier_cost source must reference a channel")
		}
		channel, err := model.GetChannelById(*source.ChannelId, false)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("channel %d does not exist", *source.ChannelId)
			}
			return err
		}
		if channel.Status != common.ChannelStatusEnabled {
			return fmt.Errorf("channel %d is not enabled", *source.ChannelId)
		}
	} else if source.ChannelId != nil {
		return fmt.Errorf("%s source must not reference a channel", source.Role)
	}
	return nil
}
