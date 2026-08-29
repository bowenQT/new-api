package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCuratedAdapterForTest(key, endpoint string) *CuratedReferenceAdapter {
	return &CuratedReferenceAdapter{
		key:               key,
		host:              "127.0.0.1",
		endpoint:          endpoint,
		client:            upstreamprice.NewCatalogHTTPClient(),
		maxResponseBytes:  upstreamprice.MaxFetchResponseBytes,
		allowTestEndpoint: true,
	}
}

func serveCuratedFixture(t *testing.T, fixtureName string) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile("testdata/" + fixtureName)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"curated-fixture-v1"`)
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)
	return server
}

func curatedReferenceSourceConfig(adapterKey string) upstreamprice.SourceConfig {
	return upstreamprice.SourceConfig{
		Id:         1,
		Name:       "curated",
		AdapterKey: adapterKey,
		Role:       upstreamprice.RoleCuratedReference,
		Scope:      upstreamprice.ScopeUnknown,
	}
}

// TestCuratedReferenceEndpointPinning proves the production adapters only
// accept their exact canonical host, so no source setting can redirect the
// fetch (spec §12).
func TestCuratedReferenceEndpointPinning(t *testing.T) {
	for _, adapter := range []*CuratedReferenceAdapter{NewModelsDevAdapter(), NewBaseLLMAdapter()} {
		require.NoError(t, upstreamprice.ValidatePinnedEndpoint(adapter.key, adapter.endpoint, adapter.host, adapter.allowTestEndpoint))

		for _, hostile := range []string{
			"https://" + adapter.host + ".evil.example/api.json",
			"http://" + adapter.host + "/api.json",
			"https://evil.example/api.json",
		} {
			require.Error(t, upstreamprice.ValidatePinnedEndpoint(adapter.key, hostile, adapter.host, adapter.allowTestEndpoint), hostile)
		}
	}
}

// TestCuratedReferenceRoleScopeAndChannel pins spec §6.3 / §4.2: these sources
// are curated references with an unprovable scope, so curated_reference is the
// only role they admit — and that role is what refuses a channel, in
// ValidatePriceSourceForWrite. Supports answers adapter identity only, so it
// does not re-decide the channel rule (a curated source can never be persisted
// with a channel in the first place).
func TestCuratedReferenceRoleScopeAndChannel(t *testing.T) {
	adapter := NewModelsDevAdapter()
	assert.Equal(t, []upstreamprice.PriceRole{upstreamprice.RoleCuratedReference}, adapter.AllowedRoles())
	assert.Equal(t, []upstreamprice.PriceScope{upstreamprice.ScopeUnknown}, adapter.AllowedScopes())

	assert.True(t, adapter.Supports(curatedReferenceSourceConfig(ModelsDevAdapterKey)))
	assert.False(t, adapter.Supports(curatedReferenceSourceConfig(BaseLLMAdapterKey)),
		"an adapter must not serve another adapter's source")
}

// TestCuratedReferenceFetchNormalization is the parser contract against a
// models.dev-shaped fixture: flat token prices become an auditable expression
// in USD per 1M tokens, unsupported dimensions are labeled rather than
// guessed, tiered pricing fails closed, and bad prices are rejected.
func TestCuratedReferenceFetchNormalization(t *testing.T) {
	server := serveCuratedFixture(t, "curated_models_dev_sample.json")
	adapter := newCuratedAdapterForTest(ModelsDevAdapterKey, server.URL)

	observations, meta, err := adapter.Fetch(context.Background(), curatedReferenceSourceConfig(ModelsDevAdapterKey))
	require.NoError(t, err)
	assert.Equal(t, 7, meta.Discovered)
	assert.Equal(t, `"curated-fixture-v1"`, meta.SourceRevision)

	byModel := make(map[string]upstreamprice.Observation, len(observations))
	for _, observation := range observations {
		byModel[observation.SourceModelName] = observation
	}
	require.Len(t, observations, 2)

	sonnet := byModel["anthropic/claude-sonnet-4-5"]
	assert.Equal(t, upstreamprice.RoleCuratedReference, sonnet.Role)
	assert.Equal(t, upstreamprice.ScopeUnknown, sonnet.Scope)
	assert.Equal(t, "anthropic", sonnet.Provider)
	assert.Equal(t, upstreamprice.CurrencyUSD, sonnet.Currency)
	assert.Equal(t, upstreamprice.FormulaKindTokenExprV1, sonnet.FormulaKind)
	assert.Equal(t, `tier("base", p * 3 + c * 15 + cr * 0.3 + cc * 3.75)`, sonnet.PriceExpr)
	assert.Empty(t, sonnet.Metadata[upstreamprice.MetadataKeyUnsupportedDimensions])

	mini := byModel["openai/gpt-4o-mini"]
	assert.Equal(t, `tier("base", p * 0.15 + c * 0.6)`, mini.PriceExpr)
	assert.Equal(t, "input_audio,reasoning", mini.Metadata[upstreamprice.MetadataKeyUnsupportedDimensions])

	skippedByModel := make(map[string]upstreamprice.SkippedModel, len(meta.Skipped))
	for _, skipped := range meta.Skipped {
		skippedByModel[skipped.SourceModelName] = skipped
	}
	require.Len(t, meta.Skipped, 5)

	assert.Equal(t, model.PriceSyncItemStatusUnsupported, skippedByModel["anthropic/claude-opus-tiered"].Status)
	assert.Equal(t, upstreamprice.WarningTieredPricingUnsupported, skippedByModel["anthropic/claude-opus-tiered"].WarningCode)
	assert.Equal(t, upstreamprice.WarningTieredPricingUnsupported, skippedByModel["openai/gpt-legacy-context"].WarningCode)
	assert.Equal(t, model.PriceSyncItemStatusUnsupported, skippedByModel["openai/embedding-only"].Status)
	assert.Equal(t, upstreamprice.WarningIncompleteTokenPrice, skippedByModel["openai/embedding-only"].WarningCode)
	assert.Equal(t, upstreamprice.WarningNoTokenPricing, skippedByModel["openai/no-cost-model"].WarningCode)
	assert.Equal(t, model.PriceSyncItemStatusRejected, skippedByModel["openai/negative-price"].Status)
	assert.Equal(t, upstreamprice.WarningInvalidPrice, skippedByModel["openai/negative-price"].WarningCode)

	// Every accepted observation must survive the shared validation pipeline.
	for _, observation := range observations {
		normalized, err := upstreamprice.NormalizeObservation(observation, curatedReferenceSourceConfig(ModelsDevAdapterKey), adapter)
		require.NoError(t, err)
		warningCode, err := upstreamprice.ValidateNormalizedPrice(normalized)
		require.NoError(t, err)
		assert.Empty(t, warningCode)
	}
}

// TestCuratedReferenceBaseLLMFixture proves the same parser serves the
// basellm catalog, which is a filtered derivative of the same shape.
func TestCuratedReferenceBaseLLMFixture(t *testing.T) {
	server := serveCuratedFixture(t, "curated_basellm_sample.json")
	adapter := newCuratedAdapterForTest(BaseLLMAdapterKey, server.URL)

	observations, meta, err := adapter.Fetch(context.Background(), curatedReferenceSourceConfig(BaseLLMAdapterKey))
	require.NoError(t, err)
	assert.Equal(t, 1, meta.Discovered)
	assert.Empty(t, meta.Skipped)
	require.Len(t, observations, 1)
	assert.Equal(t, "minimax/MiniMax-M2", observations[0].SourceModelName)
	assert.Equal(t, `tier("base", p * 0.3 + c * 1.2 + cr * 0.06)`, observations[0].PriceExpr)
}

// TestCuratedReferenceConditionalFetch is the adapter half of the ETag
// contract: a conditional fetch sends If-None-Match, a 304 answer is reported
// as NotModified with no body read, and the validator the run keeps is the one
// we asked about when the reply omits it.
func TestCuratedReferenceConditionalFetch(t *testing.T) {
	fixture, err := os.ReadFile("testdata/curated_models_dev_sample.json")
	require.NoError(t, err)

	const etag = `"curated-fixture-v1"`
	var (
		received     []string
		echoETagOn30 = true
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == etag {
			if echoETagOn30 {
				w.Header().Set("ETag", etag)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", etag)
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)

	adapter := newCuratedAdapterForTest(ModelsDevAdapterKey, server.URL)
	config := curatedReferenceSourceConfig(ModelsDevAdapterKey)

	observations, meta, err := adapter.Fetch(context.Background(), config)
	require.NoError(t, err)
	require.Len(t, observations, 2)
	assert.Equal(t, etag, meta.SourceRevision)
	assert.False(t, meta.NotModified)
	assert.Positive(t, meta.ResponseBytes)

	observations, meta, err = adapter.FetchConditional(context.Background(), config, etag)
	require.NoError(t, err)
	assert.Empty(t, observations)
	assert.True(t, meta.NotModified)
	assert.Equal(t, http.StatusNotModified, meta.HTTPStatus)
	assert.Zero(t, meta.ResponseBytes)
	assert.Zero(t, meta.Discovered)
	assert.Equal(t, etag, meta.SourceRevision)

	// A 304 that omits the validator keeps the one the request carried, so the
	// next run can still ask conditionally.
	echoETagOn30 = false
	_, meta, err = adapter.FetchConditional(context.Background(), config, etag)
	require.NoError(t, err)
	assert.True(t, meta.NotModified)
	assert.Equal(t, etag, meta.SourceRevision)

	// A revision that is not a well-formed entity-tag — a value truncated to
	// the source_revision column width, say — degrades to a full fetch instead
	// of putting a malformed header on the wire.
	received = nil
	observations, meta, err = adapter.FetchConditional(context.Background(), config, `"truncated-without-closing-quote`)
	require.NoError(t, err)
	require.Len(t, observations, 2)
	assert.False(t, meta.NotModified)
	assert.Equal(t, []string{""}, received)
}

// TestCuratedReferenceRejectsNonDecimalPrices: source prices reach the decimal
// validator as their exact JSON text, so exponent notation and non-numeric
// values fail closed rather than being coerced.
func TestCuratedReferenceRejectsNonDecimalPrices(t *testing.T) {
	cases := []struct {
		name string
		cost string
	}{
		{name: "exponent notation", cost: `{"input": 1e-5, "output": 2}`},
		{name: "string price", cost: `{"input": "3", "output": 15}`},
		{name: "absurd magnitude", cost: `{"input": 99999999, "output": 2}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observation, skipped := normalizeCuratedModel("vendor", "vendor/model", curatedModelEntry{
				Id:   "model",
				Cost: []byte(testCase.cost),
			})
			assert.Nil(t, observation)
			require.NotNil(t, skipped)
			assert.Equal(t, model.PriceSyncItemStatusRejected, skipped.Status)
			assert.Equal(t, upstreamprice.WarningInvalidPrice, skipped.WarningCode)
		})
	}
}
