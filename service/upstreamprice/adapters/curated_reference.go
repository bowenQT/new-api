package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"
)

// Curated reference price adapters (spec §6.3, Phase 2).
//
// models.dev and basellm/llm-metadata publish the same provider-keyed catalog
// shape, and basellm is a filtered derivative of models.dev, so one parser
// serves both. Both are third-party compilations: they are recorded as
// `curated_reference`, never as a vendor's official list price, and their
// scope is `unknown` because neither source proves who can obtain the price
// (spec §4.2). They are reference prices only and must never be linked to a
// channel or presented as a confirmed cost.
const (
	// ModelsDevAdapterKey identifies the models.dev reference adapter.
	ModelsDevAdapterKey = "models_dev"
	// BaseLLMAdapterKey identifies the basellm/llm-metadata reference adapter.
	BaseLLMAdapterKey = "basellm"

	modelsDevHost     = "models.dev"
	modelsDevEndpoint = "https://models.dev/api.json"

	baseLLMHost     = "basellm.github.io"
	baseLLMEndpoint = "https://basellm.github.io/llm-metadata/api/all.json"
)

func init() {
	upstreamprice.MustRegisterAdapter(NewModelsDevAdapter())
	upstreamprice.MustRegisterAdapter(NewBaseLLMAdapter())
}

// CuratedReferenceAdapter fetches one third-party curated price catalog. The
// endpoint is pinned per adapter and cannot be overridden through source
// settings.
type CuratedReferenceAdapter struct {
	key      string
	host     string
	endpoint string
	client   *http.Client
	// maxResponseBytes bounds the decompressed response size; production
	// constructors always seed it from upstreamprice.MaxFetchResponseBytes.
	maxResponseBytes int64
	// allowTestEndpoint is only settable from package-internal tests so the
	// fixture can be served from httptest.
	allowTestEndpoint bool
}

func NewModelsDevAdapter() *CuratedReferenceAdapter {
	return newCuratedReferenceAdapter(ModelsDevAdapterKey, modelsDevHost, modelsDevEndpoint)
}

func NewBaseLLMAdapter() *CuratedReferenceAdapter {
	return newCuratedReferenceAdapter(BaseLLMAdapterKey, baseLLMHost, baseLLMEndpoint)
}

func newCuratedReferenceAdapter(key, host, endpoint string) *CuratedReferenceAdapter {
	return &CuratedReferenceAdapter{
		key:              key,
		host:             host,
		endpoint:         endpoint,
		client:           upstreamprice.NewCatalogHTTPClient(),
		maxResponseBytes: upstreamprice.MaxFetchResponseBytes,
	}
}

func (a *CuratedReferenceAdapter) Key() string { return a.key }

func (a *CuratedReferenceAdapter) AllowedRoles() []upstreamprice.PriceRole {
	return []upstreamprice.PriceRole{upstreamprice.RoleCuratedReference}
}

func (a *CuratedReferenceAdapter) AllowedScopes() []upstreamprice.PriceScope {
	return []upstreamprice.PriceScope{upstreamprice.ScopeUnknown}
}

func (a *CuratedReferenceAdapter) Supports(source upstreamprice.SourceConfig) bool {
	// Reference sources must never be attributed to a channel (spec §6.3).
	return source.AdapterKey == a.key && source.ChannelId == nil
}

func (a *CuratedReferenceAdapter) Endpoint() string {
	return a.endpoint
}

func (a *CuratedReferenceAdapter) validateEndpoint() error {
	parsed, err := url.Parse(a.endpoint)
	if err != nil {
		return fmt.Errorf("invalid %s endpoint: %w", a.key, err)
	}
	if a.allowTestEndpoint {
		return nil
	}
	// Exact host match only, so a suffix-forged domain such as
	// "models.dev.evil.example" is refused.
	if parsed.Scheme != "https" || parsed.Hostname() != a.host {
		return fmt.Errorf("%s adapter only accepts %s", a.key, a.endpoint)
	}
	return nil
}

func (a *CuratedReferenceAdapter) Fetch(ctx context.Context, source upstreamprice.SourceConfig) ([]upstreamprice.Observation, upstreamprice.FetchMeta, error) {
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
		// Public catalogs: no credential is ever attached (spec §12).
		return request, nil
	})
	if err != nil {
		return nil, meta, fmt.Errorf("%s fetch failed: %w", a.key, err)
	}
	defer response.Body.Close()
	meta.HTTPStatus = response.StatusCode
	meta.SourceRevision = response.Header.Get("ETag")
	if response.StatusCode != http.StatusOK {
		return nil, meta, fmt.Errorf("%s fetch: http %d", a.key, response.StatusCode)
	}

	maxResponseBytes := a.maxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = upstreamprice.MaxFetchResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, meta, fmt.Errorf("%s fetch: read failed: %w", a.key, err)
	}
	meta.ResponseBytes = int64(len(body))
	if int64(len(body)) > maxResponseBytes {
		return nil, meta, fmt.Errorf("%s fetch: response exceeds %d bytes", a.key, maxResponseBytes)
	}

	observations, skipped, discovered, err := parseCuratedCatalog(body)
	if err != nil {
		return nil, meta, fmt.Errorf("%s fetch: %w", a.key, err)
	}
	meta.Discovered = discovered
	meta.Skipped = skipped
	return observations, meta, nil
}

// ---------------------------------------------------------------------------
// Curated catalog payload
// ---------------------------------------------------------------------------

type curatedProviderEntry struct {
	Id     string                     `json:"id"`
	Models map[string]json.RawMessage `json:"models"`
}

type curatedModelEntry struct {
	Id   string          `json:"id"`
	Cost json.RawMessage `json:"cost"`
}

// curatedCostComponents maps the cost keys Phase 2 normalizes onto their
// expression variables. Component order fixes the term order in generated
// expressions.
var curatedCostComponents = []struct {
	key      string
	variable string
}{
	{"input", "p"},
	{"output", "c"},
	{"cache_read", "cr"},
	{"cache_write", "cc"},
}

// curatedTieredCostKeys change the price of the very dimensions above, so a
// model carrying one is reported unsupported instead of being recorded at its
// base tier only.
var curatedTieredCostKeys = []string{"tiers", "context_over_200k"}

func curatedHandledCostKey(key string) bool {
	for _, component := range curatedCostComponents {
		if component.key == key {
			return true
		}
	}
	return false
}

// parseCuratedCatalog walks the provider-keyed catalog deterministically and
// converts every model with usable flat token pricing into an Observation.
func parseCuratedCatalog(body []byte) ([]upstreamprice.Observation, []upstreamprice.SkippedModel, int, error) {
	var providers map[string]json.RawMessage
	if err := common.Unmarshal(body, &providers); err != nil {
		return nil, nil, 0, fmt.Errorf("invalid JSON: %w", err)
	}
	providerIds := make([]string, 0, len(providers))
	for providerId := range providers {
		providerIds = append(providerIds, providerId)
	}
	sort.Strings(providerIds)

	observations := make([]upstreamprice.Observation, 0, len(providerIds))
	skipped := make([]upstreamprice.SkippedModel, 0)
	discovered := 0
	for _, providerKey := range providerIds {
		var provider curatedProviderEntry
		if err := common.Unmarshal(providers[providerKey], &provider); err != nil {
			// A malformed provider block is not attributable to a model, so it
			// is reported once under the provider key.
			skipped = append(skipped, *skippedModel(providerKey, model.PriceSyncItemStatusRejected, upstreamprice.WarningInvalidPrice))
			continue
		}
		providerId := provider.Id
		if providerId == "" {
			providerId = providerKey
		}
		modelKeys := make([]string, 0, len(provider.Models))
		for modelKey := range provider.Models {
			modelKeys = append(modelKeys, modelKey)
		}
		sort.Strings(modelKeys)

		for _, modelKey := range modelKeys {
			discovered++
			var entry curatedModelEntry
			sourceModelName := providerId + "/" + modelKey
			if err := common.Unmarshal(provider.Models[modelKey], &entry); err != nil {
				skipped = append(skipped, *skippedModel(sourceModelName, model.PriceSyncItemStatusRejected, upstreamprice.WarningInvalidPrice))
				continue
			}
			if entry.Id != "" {
				sourceModelName = providerId + "/" + entry.Id
			}
			observation, skip := normalizeCuratedModel(providerId, sourceModelName, entry)
			if skip != nil {
				skipped = append(skipped, *skip)
				continue
			}
			observations = append(observations, *observation)
		}
	}
	return observations, skipped, discovered, nil
}

// normalizeCuratedModel converts one catalog model into an Observation.
// Coefficients are already USD per 1M tokens in both sources. Values are read
// as raw JSON tokens so the source's exact decimal text reaches the decimal
// validator; anything that is not plain decimal notation is refused.
func normalizeCuratedModel(providerId string, sourceModelName string, entry curatedModelEntry) (*upstreamprice.Observation, *upstreamprice.SkippedModel) {
	if len(entry.Cost) == 0 {
		return nil, skippedModel(sourceModelName, model.PriceSyncItemStatusUnsupported, upstreamprice.WarningNoTokenPricing)
	}
	var cost map[string]json.RawMessage
	if err := common.Unmarshal(entry.Cost, &cost); err != nil {
		return nil, skippedModel(sourceModelName, model.PriceSyncItemStatusRejected, upstreamprice.WarningInvalidPrice)
	}
	if len(cost) == 0 {
		return nil, skippedModel(sourceModelName, model.PriceSyncItemStatusUnsupported, upstreamprice.WarningNoTokenPricing)
	}
	for _, tieredKey := range curatedTieredCostKeys {
		if _, ok := cost[tieredKey]; ok {
			return nil, skippedModel(sourceModelName, model.PriceSyncItemStatusUnsupported, upstreamprice.WarningTieredPricingUnsupported)
		}
	}

	unsupportedDimensions := make([]string, 0)
	for key := range cost {
		if !curatedHandledCostKey(key) {
			unsupportedDimensions = append(unsupportedDimensions, key)
		}
	}
	sort.Strings(unsupportedDimensions)

	terms := make([]string, 0, len(curatedCostComponents))
	hasInput := false
	hasOutput := false
	for _, component := range curatedCostComponents {
		raw, ok := cost[component.key]
		if !ok {
			continue
		}
		coefficient, err := upstreamprice.MillionTokenCoefficient(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, skippedModel(sourceModelName, model.PriceSyncItemStatusRejected, upstreamprice.WarningInvalidPrice)
		}
		switch component.variable {
		case "p":
			hasInput = true
		case "c":
			hasOutput = true
		}
		terms = append(terms, component.variable+" * "+coefficient)
	}
	if !hasInput || !hasOutput {
		// Pricing only one side would silently price the other at zero.
		return nil, skippedModel(sourceModelName, model.PriceSyncItemStatusUnsupported, upstreamprice.WarningIncompleteTokenPrice)
	}

	metadata := map[string]string{}
	if len(unsupportedDimensions) > 0 {
		metadata[upstreamprice.MetadataKeyUnsupportedDimensions] = strings.Join(unsupportedDimensions, ",")
	}

	return &upstreamprice.Observation{
		Role:            upstreamprice.RoleCuratedReference,
		Scope:           upstreamprice.ScopeUnknown,
		Provider:        providerId,
		SourceModelName: sourceModelName,
		Currency:        upstreamprice.CurrencyUSD,
		FormulaKind:     upstreamprice.FormulaKindTokenExprV1,
		PriceExpr:       `tier("base", ` + strings.Join(terms, " + ") + `)`,
		Metadata:        metadata,
	}, nil
}
