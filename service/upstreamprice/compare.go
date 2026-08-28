package upstreamprice

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// Cost / sale-price / margin comparison (spec §9.2, §9.3, §10.3).
//
// This is a limited-dimension baseline estimate, not an equivalent of the
// online billing path: it projects the three existing billing modes onto one
// usage vector in USD and reports the resulting estimated margin. It reads
// sale pricing configuration and writes nothing at all.

// DefaultCompareGroup is the sale group used when the caller names none
// (spec §21 Q4).
const DefaultCompareGroup = "default"

// MaxCompareModels caps how many models one response describes. Catalogs with
// more canonical models return the first page of names in sort order and set
// Truncated, so a caller can narrow the request.
const MaxCompareModels = 500

// Default usage vector: one million prompt and completion tokens, no cache.
// It is echoed back on every response so the basis of the amounts is explicit.
const (
	defaultComparePromptTokens     = float64(1_000_000)
	defaultCompareCompletionTokens = float64(1_000_000)
)

// maxCompareUSD bounds every projected amount. Anything beyond it is treated
// as corrupt configuration rather than a price.
const maxCompareUSD = float64(1e12)

// Projection outcome of one sale price or one source price.
const (
	ProjectionOK             = "ok"
	ProjectionNotProjectable = "not_projectable"
	ProjectionNotConfigured  = "not_configured"
)

// Sale billing modes as reported by the comparison.
const (
	SaleBillingModeRatio      = "ratio"
	SaleBillingModePerCall    = "per_call"
	SaleBillingModeTieredExpr = "tiered_expr"
)

// Entry status labels (spec §11.2).
const (
	CompareStatusStale             = "stale"
	CompareStatusMissing           = "missing"
	CompareStatusOrphaned          = "orphaned"
	CompareStatusCanonicalConflict = "canonical_conflict"
	CompareStatusVariesByProvider  = "varies_by_provider"
	CompareStatusCostInverted      = "cost_inverted"
	CompareStatusNoCatalogPrice    = "no_catalog_price"
	CompareStatusSaleNotProjected  = "sale_not_projectable"
	CompareStatusCostNotProjected  = "cost_not_projectable"
)

// RequestRuleProjectionNote is shown for tiered expressions whose
// request-dependent factors cannot be neutralized (spec §9.3).
const RequestRuleProjectionNote = "含请求规则，无法投影"

// VariesByProviderNote is the mandatory label for observations whose upstream
// price differs per routed provider (spec §6.2).
const VariesByProviderNote = "多 provider 价格不一，成本不确定"

// compareExcludedFactors is the explicit exclusion list of the projection
// contract (spec §9.3). It is returned with every comparison so the estimate
// is never mistaken for the online charge.
var compareExcludedFactors = []string{
	"group_group_ratio",
	"request_billing_ratios",
	"tool_call_surcharge",
	"other_ratios",
	"image_and_audio_standalone_prices",
	"upstream_usage_semantic_differences",
}

type appliedUsage struct {
	P  float64
	C  float64
	CR float64
	CC float64
}

func (u appliedUsage) tokenParams() billingexpr.TokenParams {
	return billingexpr.TokenParams{
		P: u.P,
		C: u.C,
		// len is the full input context length, so tier conditions match the
		// billing engine's semantics even when cache tokens are priced apart.
		Len: u.P + u.CR + u.CC,
		CR:  u.CR,
		CC:  u.CC,
	}
}

func resolveUsage(requested *dto.UpstreamPriceUsageVector) appliedUsage {
	usage := appliedUsage{P: defaultComparePromptTokens, C: defaultCompareCompletionTokens}
	if requested == nil {
		return usage
	}
	if requested.PromptTokens != nil {
		usage.P = *requested.PromptTokens
	}
	if requested.CompletionTokens != nil {
		usage.C = *requested.CompletionTokens
	}
	if requested.CacheReadTokens != nil {
		usage.CR = *requested.CacheReadTokens
	}
	if requested.CacheCreationTokens != nil {
		usage.CC = *requested.CacheCreationTokens
	}
	return usage
}

// boundedUSD rejects non-finite and out-of-range amounts so a corrupt
// expression or ratio can never surface as a price.
func boundedUSD(value float64) (float64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	if value < -maxCompareUSD || value > maxCompareUSD {
		return 0, false
	}
	return value, true
}

// CompareUpstreamPrices projects the current sale price, every catalog cost,
// and the resulting estimated margin for the requested models onto one usage
// vector (spec §9.2, §9.3).
func CompareUpstreamPrices(request *dto.UpstreamPriceCompareRequest) (*dto.UpstreamPriceCompareResponse, error) {
	if request == nil {
		return nil, errors.New("nil compare request")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}

	group := strings.TrimSpace(request.Group)
	if group == "" {
		group = DefaultCompareGroup
	}
	groupRatio := float64(1)
	groupConfigured := ratio_setting.ContainsGroupRatio(group)
	if groupConfigured {
		groupRatio = ratio_setting.GetGroupRatio(group)
	}
	if _, ok := boundedUSD(groupRatio); !ok || groupRatio < 0 {
		return nil, fmt.Errorf("group %q has an unusable group ratio", group)
	}

	usage := resolveUsage(request.Usage)
	params := usage.tokenParams()

	catalog, err := GetCurrentUpstreamPrices(nil)
	if err != nil {
		return nil, err
	}
	pricesByModel := make(map[string][]dto.UpstreamCurrentPriceEntry)
	for _, entry := range catalog.Entries {
		if entry.CanonicalModelName == "" {
			continue
		}
		pricesByModel[entry.CanonicalModelName] = append(pricesByModel[entry.CanonicalModelName], entry)
	}

	modelNames, total, truncated := selectCompareModels(request.Models, pricesByModel)

	response := &dto.UpstreamPriceCompareResponse{
		GeneratedAt:          common.GetTimestamp(),
		Group:                group,
		GroupRatio:           groupRatio,
		GroupRatioConfigured: groupConfigured,
		Usage: dto.UpstreamPriceAppliedUsage{
			PromptTokens:        usage.P,
			CompletionTokens:    usage.C,
			CacheReadTokens:     usage.CR,
			CacheCreationTokens: usage.CC,
		},
		TotalModels:     total,
		Truncated:       truncated,
		ExcludedFactors: append([]string{}, compareExcludedFactors...),
		Entries:         make([]dto.UpstreamPriceCompareEntry, 0, len(modelNames)),
		Alerts:          make([]dto.UpstreamPriceAlert, 0),
	}
	for _, modelName := range modelNames {
		entry := buildCompareEntry(modelName, pricesByModel[modelName], params, usage, groupRatio)
		if entry.CostInverted {
			response.Alerts = append(response.Alerts, dto.UpstreamPriceAlert{
				Code:               AlertCostInversion,
				CanonicalModelName: modelName,
				Detail:             fmt.Sprintf("worst cost exceeds the projected sale price for group %q", group),
			})
		}
		response.Entries = append(response.Entries, entry)
	}

	sources, err := model.GetAllPriceSources()
	if err != nil {
		return nil, err
	}
	sourceAlerts, err := EvaluateSourceAlerts(sources, response.GeneratedAt)
	if err != nil {
		return nil, err
	}
	response.Alerts = append(response.Alerts, sourceAlerts...)
	return response, nil
}

// selectCompareModels resolves the requested model set. An explicit list is
// honored verbatim (deduplicated) so a caller can ask about a model the
// catalog does not cover; an empty list compares every canonical model in the
// catalog, capped at MaxCompareModels in sort order.
func selectCompareModels(requested []string, pricesByModel map[string][]dto.UpstreamCurrentPriceEntry) ([]string, int, bool) {
	names := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	if len(requested) > 0 {
		for _, name := range requested {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			names = append(names, trimmed)
		}
		return names, len(names), false
	}
	for name := range pricesByModel {
		names = append(names, name)
	}
	sort.Strings(names)
	total := len(names)
	if total > MaxCompareModels {
		return names[:MaxCompareModels], total, true
	}
	return names, total, false
}

func buildCompareEntry(modelName string, catalogEntries []dto.UpstreamCurrentPriceEntry, params billingexpr.TokenParams, usage appliedUsage, groupRatio float64) dto.UpstreamPriceCompareEntry {
	entry := dto.UpstreamPriceCompareEntry{
		CanonicalModelName: modelName,
		Costs:              make([]dto.UpstreamPriceCompareSourcePrice, 0, len(catalogEntries)),
		References:         make([]dto.UpstreamPriceCompareSourcePrice, 0),
		CostConfirmed:      true,
	}

	mode, saleBase, saleStatus, saleNote := projectSalePrice(modelName, params, usage)
	entry.SaleBillingMode = mode
	entry.SaleProjection = saleStatus
	entry.SaleProjectionNote = saleNote
	if saleBase != nil {
		base := *saleBase
		projected, ok := boundedUSD(base * groupRatio)
		if ok {
			entry.SaleBaseUSD = &base
			entry.SaleProjectedUSD = &projected
		} else {
			entry.SaleProjection = ProjectionNotProjectable
			entry.SaleProjectionNote = "projected sale price is out of range"
		}
	}

	statuses := make(map[string]bool)
	if entry.SaleProjection != ProjectionOK {
		statuses[CompareStatusSaleNotProjected] = true
	}
	if len(catalogEntries) == 0 {
		statuses[CompareStatusNoCatalogPrice] = true
	}

	var minCost, maxCost *float64
	for _, catalogEntry := range catalogEntries {
		price := projectCatalogEntry(catalogEntry, params)
		if price.Stale {
			statuses[CompareStatusStale] = true
		}
		if price.Orphaned {
			statuses[CompareStatusOrphaned] = true
		}
		if price.VariesByProvider {
			statuses[CompareStatusVariesByProvider] = true
		}
		if price.CanonicalConflict {
			statuses[CompareStatusCanonicalConflict] = true
		}
		if price.Status == CatalogStatusMissing {
			statuses[CompareStatusMissing] = true
		}
		if price.Projection == ProjectionNotProjectable {
			statuses[CompareStatusCostNotProjected] = true
		}
		if PriceRole(price.Role) != RoleSupplierCost {
			// Vendor list and curated reference prices are reported separately
			// and never enter the margin (spec §9.2).
			price.UsableForMargin = false
			entry.References = append(entry.References, price)
			continue
		}
		if price.UsableForMargin && price.AmountUSD != nil {
			amount := *price.AmountUSD
			if minCost == nil || amount < *minCost {
				value := amount
				minCost = &value
			}
			if maxCost == nil || amount > *maxCost {
				value := amount
				maxCost = &value
			}
			if price.Stale || price.VariesByProvider || price.Orphaned || price.CanonicalConflict {
				entry.CostConfirmed = false
			}
		} else {
			entry.CostConfirmed = false
		}
		entry.Costs = append(entry.Costs, price)
	}
	if len(entry.Costs) == 0 {
		entry.CostConfirmed = false
	}
	entry.MinCostUSD = minCost
	entry.MaxCostUSD = maxCost

	// Worst margin uses the highest usable cost; the rate is undefined when the
	// projected sale price is zero (spec §9.2).
	if entry.SaleProjectedUSD != nil && maxCost != nil {
		sale := *entry.SaleProjectedUSD
		if margin, ok := boundedUSD(sale - *maxCost); ok {
			entry.WorstMarginUSD = &margin
			if sale != 0 {
				if rate, ok := boundedUSD(margin / sale); ok {
					entry.WorstMarginRate = &rate
				}
			}
			if *maxCost > sale {
				entry.CostInverted = true
				statuses[CompareStatusCostInverted] = true
			}
		}
	}

	entry.Statuses = sortedStatusList(statuses)
	return entry
}

func sortedStatusList(statuses map[string]bool) []string {
	list := make([]string, 0, len(statuses))
	for status := range statuses {
		list = append(list, status)
	}
	sort.Strings(list)
	return list
}

// projectCatalogEntry converts one catalog row into a projected USD amount
// under the comparison's usage vector. Only rows that are current or stale
// observations carry a margin-usable amount: a model the source stopped
// returning must not be asserted as a current cost.
func projectCatalogEntry(catalogEntry dto.UpstreamCurrentPriceEntry, params billingexpr.TokenParams) dto.UpstreamPriceCompareSourcePrice {
	price := dto.UpstreamPriceCompareSourcePrice{
		SourceId:          catalogEntry.SourceId,
		SourceName:        catalogEntry.SourceName,
		Role:              catalogEntry.Role,
		Scope:             catalogEntry.Scope,
		ChannelId:         catalogEntry.ChannelId,
		SourceModelName:   catalogEntry.SourceModelName,
		FormulaKind:       catalogEntry.FormulaKind,
		Status:            catalogEntry.Status,
		WarningCode:       catalogEntry.WarningCode,
		Stale:             catalogEntry.Stale,
		Orphaned:          catalogEntry.Orphaned,
		VariesByProvider:  catalogEntry.VariesByProvider,
		CanonicalConflict: catalogEntry.CanonicalConflict,
		SnapshotId:        catalogEntry.SnapshotId,
		RunId:             catalogEntry.RunId,
		RunFinishedAt:     catalogEntry.RunFinishedAt,
		LastSeenAt:        catalogEntry.LastSeenAt,
		FetchedAt:         catalogEntry.FetchedAt,
		EffectiveAt:       catalogEntry.EffectiveAt,
		Projection:        ProjectionNotConfigured,
	}
	if catalogEntry.VariesByProvider {
		price.ProjectionNote = VariesByProviderNote
	}
	if catalogEntry.PriceExpr == "" {
		return price
	}

	amount, err := projectPriceExpr(catalogEntry.FormulaKind, catalogEntry.PriceExpr, params)
	if err != nil {
		price.Projection = ProjectionNotProjectable
		if errors.Is(err, billingexpr.ErrRequestRuleNotProjectable) {
			price.ProjectionNote = RequestRuleProjectionNote
		} else {
			price.ProjectionNote = err.Error()
		}
		return price
	}
	price.Projection = ProjectionOK
	price.AmountUSD = &amount
	price.UsableForMargin = catalogEntry.Status == CatalogStatusCurrent
	return price
}

// projectPriceExpr evaluates one catalog price expression as USD under the
// given usage vector. token_expr_v1 coefficients are USD per 1M tokens;
// per_call_v1 results are already USD per request (spec §6.1).
func projectPriceExpr(formulaKind string, priceExpr string, params billingexpr.TokenParams) (float64, error) {
	raw, _, err := billingexpr.RunBaseExpr(priceExpr, params)
	if err != nil {
		return 0, err
	}
	var amount float64
	switch formulaKind {
	case FormulaKindTokenExprV1:
		amount = raw / 1_000_000
	case FormulaKindPerCallV1:
		amount = raw
	default:
		return 0, fmt.Errorf("unsupported formula kind %q", formulaKind)
	}
	bounded, ok := boundedUSD(amount)
	if !ok {
		return 0, errors.New("projected amount is out of range")
	}
	return bounded, nil
}

// projectSalePrice projects the model's current sale base price (before the
// group ratio) in USD, mirroring how the online path selects a billing mode:
// a tiered expression wins, then a per-call ModelPrice, otherwise ratios.
func projectSalePrice(modelName string, params billingexpr.TokenParams, usage appliedUsage) (mode string, base *float64, status string, note string) {
	if billing_setting.GetBillingMode(modelName) == billing_setting.BillingModeTieredExpr {
		mode = SaleBillingModeTieredExpr
		exprStr, ok := billing_setting.GetBillingExpr(modelName)
		if !ok || strings.TrimSpace(exprStr) == "" {
			return mode, nil, ProjectionNotConfigured, "tiered billing is enabled but no expression is configured"
		}
		raw, _, err := billingexpr.RunBaseExpr(exprStr, params)
		if err != nil {
			if errors.Is(err, billingexpr.ErrRequestRuleNotProjectable) {
				return mode, nil, ProjectionNotProjectable, RequestRuleProjectionNote
			}
			return mode, nil, ProjectionNotProjectable, err.Error()
		}
		amount, ok := boundedUSD(raw / 1_000_000)
		if !ok {
			return mode, nil, ProjectionNotProjectable, "projected sale price is out of range"
		}
		return mode, &amount, ProjectionOK, ""
	}

	if modelPrice, ok := ratio_setting.GetModelPrice(modelName, false); ok {
		mode = SaleBillingModePerCall
		amount, valid := boundedUSD(modelPrice)
		if !valid {
			return mode, nil, ProjectionNotProjectable, "configured model price is out of range"
		}
		return mode, &amount, ProjectionOK, ""
	}

	mode = SaleBillingModeRatio
	modelRatio, configured, _ := ratio_setting.GetModelRatio(modelName)
	if !configured {
		return mode, nil, ProjectionNotConfigured, "no model ratio or price is configured"
	}
	if common.QuotaPerUnit <= 0 {
		return mode, nil, ProjectionNotProjectable, "QuotaPerUnit is not configured"
	}
	completionRatio := ratio_setting.GetCompletionRatio(modelName)
	cacheRatio, _ := ratio_setting.GetCacheRatio(modelName)
	cacheCreationRatio, _ := ratio_setting.GetCreateCacheRatio(modelName)

	// Weighted quota before the group ratio, then USD = quota / QuotaPerUnit
	// (QuotaPerUnit is the USD -> quota multiplier, spec §9.3). Dimensions the
	// usage vector does not provide stay out of the sum, and p is already the
	// input part that is not priced separately, so cr / cc are never counted
	// twice.
	weighted := usage.P +
		usage.CR*cacheRatio +
		usage.CC*cacheCreationRatio +
		usage.C*completionRatio
	amount, ok := boundedUSD(weighted * modelRatio / common.QuotaPerUnit)
	if !ok {
		return mode, nil, ProjectionNotProjectable, "projected sale price is out of range"
	}
	return mode, &amount, ProjectionOK, ""
}
