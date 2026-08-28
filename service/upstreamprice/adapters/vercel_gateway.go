// Package adapters holds concrete price source adapters. Each adapter
// registers itself with the upstreamprice registry at init time.
package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"
)

const (
	// VercelGatewayAdapterKey identifies the Vercel AI Gateway cost adapter.
	VercelGatewayAdapterKey = "vercel_gateway"

	vercelCanonicalHost = "ai-gateway.vercel.sh"
	vercelEndpoint      = "https://ai-gateway.vercel.sh/v1/models"
)

func init() {
	upstreamprice.MustRegisterAdapter(NewVercelGatewayAdapter())
}

// VercelGatewayAdapter fetches the public Vercel AI Gateway model catalog
// (spec §6.2). The endpoint is a public directory: no credentials are ever
// sent. Only the exact canonical host is accepted; the endpoint cannot be
// overridden through source settings.
type VercelGatewayAdapter struct {
	key      string
	endpoint string
	client   *http.Client
	// maxResponseBytes bounds the decompressed response size; production
	// constructors always seed it from upstreamprice.MaxFetchResponseBytes,
	// package-internal tests may inject a smaller limit.
	maxResponseBytes int64
	// allowTestEndpoint is only settable from package-internal tests so the
	// fixture can be served from httptest; production constructors always
	// pin the canonical endpoint.
	allowTestEndpoint bool
}

func NewVercelGatewayAdapter() *VercelGatewayAdapter {
	return &VercelGatewayAdapter{
		key:              VercelGatewayAdapterKey,
		endpoint:         vercelEndpoint,
		client:           upstreamprice.NewCatalogHTTPClient(),
		maxResponseBytes: upstreamprice.MaxFetchResponseBytes,
	}
}

func (a *VercelGatewayAdapter) Key() string {
	return a.key
}

func (a *VercelGatewayAdapter) AllowedRoles() []upstreamprice.PriceRole {
	return []upstreamprice.PriceRole{upstreamprice.RoleSupplierCost}
}

func (a *VercelGatewayAdapter) AllowedScopes() []upstreamprice.PriceScope {
	return []upstreamprice.PriceScope{upstreamprice.ScopePublic}
}

func (a *VercelGatewayAdapter) Supports(source upstreamprice.SourceConfig) bool {
	return source.AdapterKey == a.key
}

func (a *VercelGatewayAdapter) Endpoint() string {
	return a.endpoint
}

// isCanonicalVercelHost accepts only the exact gateway host, refusing forged
// suffix domains such as "ai-gateway.vercel.sh.evil.example".
func isCanonicalVercelHost(host string) bool {
	return host == vercelCanonicalHost
}

func (a *VercelGatewayAdapter) validateEndpoint() error {
	parsed, err := url.Parse(a.endpoint)
	if err != nil {
		return fmt.Errorf("invalid vercel endpoint: %w", err)
	}
	if a.allowTestEndpoint {
		return nil
	}
	if parsed.Scheme != "https" || !isCanonicalVercelHost(parsed.Hostname()) {
		return fmt.Errorf("vercel adapter only accepts %s", vercelEndpoint)
	}
	return nil
}

func (a *VercelGatewayAdapter) Fetch(ctx context.Context, source upstreamprice.SourceConfig) ([]upstreamprice.Observation, upstreamprice.FetchMeta, error) {
	meta := upstreamprice.FetchMeta{}
	if err := a.validateEndpoint(); err != nil {
		return nil, meta, err
	}
	response, err := upstreamprice.DoCatalogRequest(ctx, a.client, func(requestCtx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, a.endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		// The catalog is public: never attach channel credentials (spec §6.2).
		return request, nil
	})
	if err != nil {
		return nil, meta, fmt.Errorf("vercel fetch failed: %w", err)
	}
	defer response.Body.Close()
	meta.HTTPStatus = response.StatusCode
	meta.SourceRevision = response.Header.Get("ETag")
	if response.StatusCode != http.StatusOK {
		// Error summaries stay sanitized: status only, never response body.
		return nil, meta, fmt.Errorf("vercel fetch: http %d", response.StatusCode)
	}

	maxResponseBytes := a.maxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = upstreamprice.MaxFetchResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, meta, fmt.Errorf("vercel fetch: read failed: %w", err)
	}
	meta.ResponseBytes = int64(len(body))
	if int64(len(body)) > maxResponseBytes {
		return nil, meta, fmt.Errorf("vercel fetch: response exceeds %d bytes", maxResponseBytes)
	}

	var payload vercelModelsResponse
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, meta, fmt.Errorf("vercel fetch: invalid JSON: %w", err)
	}
	meta.Discovered = len(payload.Data)

	observations := make([]upstreamprice.Observation, 0, len(payload.Data))
	for _, vercelModel := range payload.Data {
		obs, skipped := normalizeVercelModel(vercelModel)
		if skipped != nil {
			meta.Skipped = append(meta.Skipped, *skipped)
			continue
		}
		observations = append(observations, *obs)
	}
	return observations, meta, nil
}

// ---------------------------------------------------------------------------
// Vercel payload DTOs
// ---------------------------------------------------------------------------

type vercelModelsResponse struct {
	Data []vercelModel `json:"data"`
}

type vercelModel struct {
	Id      string          `json:"id"`
	OwnedBy string          `json:"owned_by"`
	Pricing json.RawMessage `json:"pricing"`
}

type vercelTier struct {
	Cost string `json:"cost"`
	Min  *int64 `json:"min"`
	Max  *int64 `json:"max"`
}

// Pricing keys Phase 1 normalizes (spec §6.2): flat and long-context tiers of
// input/output/cache read/cache write. Every other key is reported as an
// unsupported dimension, never guessed into a default cost.
var vercelHandledPricingKeys = map[string]bool{
	"input":                   true,
	"output":                  true,
	"input_cache_read":        true,
	"input_cache_write":       true,
	"input_tiers":             true,
	"output_tiers":            true,
	"input_cache_read_tiers":  true,
	"input_cache_write_tiers": true,
	"varies_by_provider":      true,
}

// vercelPriceComponent maps one pricing dimension to its expression variable.
type vercelPriceComponent struct {
	flatKey  string
	tiersKey string
	variable string
}

// Component order fixes the term order inside generated expressions.
var vercelPriceComponents = []vercelPriceComponent{
	{"input", "input_tiers", "p"},
	{"output", "output_tiers", "c"},
	{"input_cache_read", "input_cache_read_tiers", "cr"},
	{"input_cache_write", "input_cache_write_tiers", "cc"},
}

type parsedComponent struct {
	variable string
	// baseCost / longCost are USD-per-1M-token coefficients as canonical
	// decimal strings. longCost is empty for flat components.
	baseCost string
	longCost string
}

func skippedModel(name, status, warningCode string) *upstreamprice.SkippedModel {
	return &upstreamprice.SkippedModel{
		SourceModelName: name,
		Status:          status,
		WarningCode:     warningCode,
	}
}

// normalizeVercelModel converts one catalog entry into an Observation, or
// reports why it was skipped. Tier inconsistencies fail closed as rejected;
// models without normalizable token pricing are unsupported.
func normalizeVercelModel(entry vercelModel) (*upstreamprice.Observation, *upstreamprice.SkippedModel) {
	if entry.Id == "" {
		return nil, skippedModel("(unnamed)", model.PriceSyncItemStatusRejected, upstreamprice.WarningInvalidPrice)
	}
	if len(entry.Pricing) == 0 {
		return nil, skippedModel(entry.Id, model.PriceSyncItemStatusUnsupported, upstreamprice.WarningNoTokenPricing)
	}
	var pricing map[string]json.RawMessage
	if err := common.Unmarshal(entry.Pricing, &pricing); err != nil {
		return nil, skippedModel(entry.Id, model.PriceSyncItemStatusRejected, upstreamprice.WarningInvalidPrice)
	}

	unsupportedDimensions := make([]string, 0)
	for key := range pricing {
		if !vercelHandledPricingKeys[key] {
			unsupportedDimensions = append(unsupportedDimensions, key)
		}
	}
	sort.Strings(unsupportedDimensions)

	variesByProvider := false
	if raw, ok := pricing["varies_by_provider"]; ok {
		if err := common.Unmarshal(raw, &variesByProvider); err != nil {
			return nil, skippedModel(entry.Id, model.PriceSyncItemStatusRejected, upstreamprice.WarningInvalidPrice)
		}
	}

	components := make([]parsedComponent, 0, len(vercelPriceComponents))
	var sharedThreshold *int64
	for _, component := range vercelPriceComponents {
		parsed, threshold, warningCode, err := parseVercelComponent(pricing, component)
		if err != nil {
			return nil, skippedModel(entry.Id, model.PriceSyncItemStatusRejected, warningCode)
		}
		if parsed == nil {
			continue
		}
		if threshold != nil {
			if sharedThreshold == nil {
				sharedThreshold = threshold
			} else if *sharedThreshold != *threshold {
				return nil, skippedModel(entry.Id, model.PriceSyncItemStatusRejected, upstreamprice.WarningTierThresholdMismatch)
			}
		}
		components = append(components, *parsed)
	}

	if len(components) == 0 {
		return nil, skippedModel(entry.Id, model.PriceSyncItemStatusUnsupported, upstreamprice.WarningNoTokenPricing)
	}
	hasInput := false
	hasOutput := false
	for _, component := range components {
		if component.variable == "p" {
			hasInput = true
		}
		if component.variable == "c" {
			hasOutput = true
		}
	}
	if !hasInput || !hasOutput {
		// A cost expression missing the input or output side would silently
		// price that side at zero; fail closed instead (spec §4.3).
		return nil, skippedModel(entry.Id, model.PriceSyncItemStatusUnsupported, upstreamprice.WarningIncompleteTokenPrice)
	}

	priceExpr := buildVercelPriceExpr(components, sharedThreshold)

	metadata := map[string]string{}
	if variesByProvider {
		metadata[upstreamprice.MetadataKeyVariesByProvider] = "true"
	}
	if len(unsupportedDimensions) > 0 {
		metadata[upstreamprice.MetadataKeyUnsupportedDimensions] = strings.Join(unsupportedDimensions, ",")
	}

	provider := entry.OwnedBy
	if provider == "" {
		if slash := strings.Index(entry.Id, "/"); slash > 0 {
			provider = entry.Id[:slash]
		}
	}

	return &upstreamprice.Observation{
		Role:            upstreamprice.RoleSupplierCost,
		Scope:           upstreamprice.ScopePublic,
		Provider:        provider,
		SourceModelName: entry.Id,
		Currency:        upstreamprice.CurrencyUSD,
		FormulaKind:     upstreamprice.FormulaKindTokenExprV1,
		PriceExpr:       priceExpr,
		Metadata:        metadata,
	}, nil
}

// parseVercelComponent extracts one pricing dimension. Tiers take priority
// over the flat value (Vercel duplicates the base tier as the flat price).
// Returns nil parsed when the dimension is absent.
func parseVercelComponent(pricing map[string]json.RawMessage, component vercelPriceComponent) (*parsedComponent, *int64, string, error) {
	if rawTiers, ok := pricing[component.tiersKey]; ok {
		var tiers []vercelTier
		if err := common.Unmarshal(rawTiers, &tiers); err != nil {
			return nil, nil, upstreamprice.WarningInvalidTiers, err
		}
		bounds := make([]upstreamprice.TierBound, 0, len(tiers))
		for _, tier := range tiers {
			bound := upstreamprice.TierBound{Cost: tier.Cost, Max: tier.Max}
			if tier.Min != nil {
				bound.Min = *tier.Min
			}
			bounds = append(bounds, bound)
		}
		threshold, err := upstreamprice.ValidateTierBounds(bounds)
		if err != nil {
			return nil, nil, upstreamprice.WarningInvalidTiers, err
		}
		baseCost, err := upstreamprice.PerMillionTokenCoefficient(bounds[0].Cost)
		if err != nil {
			return nil, nil, upstreamprice.WarningInvalidPrice, err
		}
		parsed := &parsedComponent{variable: component.variable, baseCost: baseCost}
		if threshold != nil {
			longCost, err := upstreamprice.PerMillionTokenCoefficient(bounds[1].Cost)
			if err != nil {
				return nil, nil, upstreamprice.WarningInvalidPrice, err
			}
			parsed.longCost = longCost
		}
		return parsed, threshold, "", nil
	}

	rawFlat, ok := pricing[component.flatKey]
	if !ok {
		return nil, nil, "", nil
	}
	var flat string
	if err := common.Unmarshal(rawFlat, &flat); err != nil {
		return nil, nil, upstreamprice.WarningInvalidPrice, err
	}
	baseCost, err := upstreamprice.PerMillionTokenCoefficient(flat)
	if err != nil {
		return nil, nil, upstreamprice.WarningInvalidPrice, err
	}
	return &parsedComponent{variable: component.variable, baseCost: baseCost}, nil, "", nil
}

// buildVercelPriceExpr renders the audited billing expression. Vercel tiers
// are half-open [min, max) in source token units, so a boundary of 200001
// becomes the billingexpr condition `len <= 200000` (spec §6.2).
func buildVercelPriceExpr(components []parsedComponent, threshold *int64) string {
	baseTerms := make([]string, 0, len(components))
	longTerms := make([]string, 0, len(components))
	for _, component := range components {
		baseTerms = append(baseTerms, component.variable+" * "+component.baseCost)
		longCost := component.longCost
		if longCost == "" {
			longCost = component.baseCost
		}
		longTerms = append(longTerms, component.variable+" * "+longCost)
	}
	base := strings.Join(baseTerms, " + ")
	if threshold == nil {
		return `tier("base", ` + base + `)`
	}
	condition := strconv.FormatInt(*threshold-1, 10)
	long := strings.Join(longTerms, " + ")
	return `len <= ` + condition + ` ? tier("standard", ` + base + `) : tier("long_context", ` + long + `)`
}
