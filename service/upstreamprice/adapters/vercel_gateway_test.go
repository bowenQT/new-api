package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newVercelAdapterForTest builds an adapter pinned to an arbitrary endpoint.
// Only tests can do this; production constructors always use the canonical
// endpoint.
func newVercelAdapterForTest(key, endpoint string) *VercelGatewayAdapter {
	return &VercelGatewayAdapter{
		key:               key,
		endpoint:          endpoint,
		client:            upstreamprice.NewCatalogHTTPClient(),
		maxResponseBytes:  upstreamprice.MaxFetchResponseBytes,
		allowTestEndpoint: true,
	}
}

func serveFixture(t *testing.T) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile("testdata/vercel_models_sample.json")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"fixture-v1"`)
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)
	return server
}

func vercelSourceConfig(adapterKey string) upstreamprice.SourceConfig {
	channelId := 1
	return upstreamprice.SourceConfig{
		Id:         1,
		Name:       "vercel",
		AdapterKey: adapterKey,
		Role:       upstreamprice.RoleSupplierCost,
		Scope:      upstreamprice.ScopePublic,
		ChannelId:  &channelId,
	}
}

func TestVercelHostExactMatch(t *testing.T) {
	assert.True(t, isCanonicalVercelHost("ai-gateway.vercel.sh"))
	// Forged suffix and prefix domains must be refused.
	assert.False(t, isCanonicalVercelHost("ai-gateway.vercel.sh.evil.example"))
	assert.False(t, isCanonicalVercelHost("evil-ai-gateway.vercel.sh"))
	assert.False(t, isCanonicalVercelHost("sub.ai-gateway.vercel.sh"))
	assert.False(t, isCanonicalVercelHost(""))

	// A production-mode adapter pointed at a forged host fails before any
	// network I/O.
	forged := &VercelGatewayAdapter{
		key:      VercelGatewayAdapterKey,
		endpoint: "https://ai-gateway.vercel.sh.evil.example/v1/models",
		client:   upstreamprice.NewCatalogHTTPClient(),
	}
	_, _, err := forged.Fetch(context.Background(), vercelSourceConfig(VercelGatewayAdapterKey))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only accepts")

	// Plain http on the canonical host is refused too.
	insecure := &VercelGatewayAdapter{
		key:      VercelGatewayAdapterKey,
		endpoint: "http://ai-gateway.vercel.sh/v1/models",
		client:   upstreamprice.NewCatalogHTTPClient(),
	}
	_, _, err = insecure.Fetch(context.Background(), vercelSourceConfig(VercelGatewayAdapterKey))
	require.Error(t, err)
}

func TestVercelFixtureNormalization(t *testing.T) {
	server := serveFixture(t)
	adapter := newVercelAdapterForTest("vercel_fixture_parse", server.URL)

	observations, meta, err := adapter.Fetch(context.Background(), vercelSourceConfig("vercel_fixture_parse"))
	require.NoError(t, err)

	assert.Equal(t, 8, meta.Discovered)
	assert.Equal(t, http.StatusOK, meta.HTTPStatus)
	assert.Equal(t, `"fixture-v1"`, meta.SourceRevision)
	assert.Positive(t, meta.ResponseBytes)

	byModel := map[string]upstreamprice.Observation{}
	for _, obs := range observations {
		byModel[obs.SourceModelName] = obs
	}
	require.Len(t, byModel, 6)

	// Flat pricing without cache keys.
	qwen := byModel["alibaba/qwen-3-14b"]
	assert.Equal(t, `tier("base", p * 0.12 + c * 0.24)`, qwen.PriceExpr)
	assert.Equal(t, upstreamprice.RoleSupplierCost, qwen.Role)
	assert.Equal(t, upstreamprice.ScopePublic, qwen.Scope)
	assert.Equal(t, "alibaba", qwen.Provider)
	assert.Equal(t, upstreamprice.CurrencyUSD, qwen.Currency)
	assert.Equal(t, upstreamprice.FormulaKindTokenExprV1, qwen.FormulaKind)
	assert.Empty(t, qwen.Metadata)

	// Flat pricing with cache read/write.
	flash := byModel["alibaba/qwen3.5-flash"]
	assert.Equal(t, `tier("base", p * 0.1 + c * 0.4 + cr * 0.001 + cc * 0.125)`, flash.PriceExpr)

	// Long-context tiers on all four dimensions; Vercel boundary 200001 with
	// half-open [min,max) semantics maps to `len <= 200000` (spec §6.2).
	sonnet := byModel["anthropic/claude-sonnet-4"]
	assert.Equal(t,
		`len <= 200000 ? tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75) : tier("long_context", p * 6 + c * 22.5 + cr * 0.6 + cc * 7.5)`,
		sonnet.PriceExpr)
	assert.Empty(t, sonnet.Metadata)

	// varies_by_provider is saved but flagged; service/regional/fast/search
	// dimensions are reported as unsupported, never guessed.
	sol := byModel["openai/gpt-5.6-sol"]
	assert.Equal(t,
		`len <= 271999 ? tier("standard", p * 2 + c * 10 + cr * 0.2 + cc * 2.5) : tier("long_context", p * 4 + c * 15 + cr * 0.4 + cc * 5)`,
		sol.PriceExpr)
	assert.Equal(t, "true", sol.Metadata[upstreamprice.MetadataKeyVariesByProvider])
	assert.Equal(t, "fast,regional,service_tiers,web_search", sol.Metadata[upstreamprice.MetadataKeyUnsupportedDimensions])

	luna := byModel["openai/gpt-5.6-luna"]
	assert.Equal(t,
		`len <= 271999 ? tier("standard", p * 0.2 + c * 1.2 + cr * 0.02 + cc * 0.25) : tier("long_context", p * 0.4 + c * 1.8 + cr * 0.04 + cc * 0.5)`,
		luna.PriceExpr)
	_, hasVaries := luna.Metadata[upstreamprice.MetadataKeyVariesByProvider]
	assert.False(t, hasVaries, "luna has no varies_by_provider flag")
	assert.Equal(t, "fast,regional,service_tiers,web_search", luna.Metadata[upstreamprice.MetadataKeyUnsupportedDimensions])

	// Token pricing plus an audio-only dimension stays valid with a warning
	// dimension recorded, so completeness is never silently claimed.
	transcribe := byModel["google/gemini-3.5-transcribe"]
	assert.Equal(t, `tier("base", p * 2 + c * 12)`, transcribe.PriceExpr)
	assert.Equal(t, "audio_input_token_cost", transcribe.Metadata[upstreamprice.MetadataKeyUnsupportedDimensions])

	// Video-only and image-only pricing cannot be normalized: unsupported.
	skippedByModel := map[string]upstreamprice.SkippedModel{}
	for _, skipped := range meta.Skipped {
		skippedByModel[skipped.SourceModelName] = skipped
	}
	require.Len(t, skippedByModel, 2)
	video := skippedByModel["alibaba/wan-v2.5-t2v-preview"]
	assert.Equal(t, model.PriceSyncItemStatusUnsupported, video.Status)
	assert.Equal(t, upstreamprice.WarningNoTokenPricing, video.WarningCode)
	image := skippedByModel["bfl/flux-kontext-max"]
	assert.Equal(t, model.PriceSyncItemStatusUnsupported, image.Status)
	assert.Equal(t, upstreamprice.WarningNoTokenPricing, image.WarningCode)
}

func rawPricing(t *testing.T, pricing string) vercelModel {
	t.Helper()
	return vercelModel{Id: "test/model", OwnedBy: "test", Pricing: json.RawMessage(pricing)}
}

func TestVercelTierFailClosed(t *testing.T) {
	tests := []struct {
		name        string
		pricing     string
		wantStatus  string
		wantWarning string
	}{
		{
			name: "threshold mismatch across dimensions",
			pricing: `{
				"input_tiers": [{"cost":"0.000002","min":0,"max":200001},{"cost":"0.000004","min":200001}],
				"output_tiers": [{"cost":"0.00001","min":0,"max":272000},{"cost":"0.000015","min":272000}]
			}`,
			wantStatus:  model.PriceSyncItemStatusRejected,
			wantWarning: upstreamprice.WarningTierThresholdMismatch,
		},
		{
			name: "overlapping tiers",
			pricing: `{
				"input_tiers": [{"cost":"0.000002","min":0,"max":200001},{"cost":"0.000004","min":100000}],
				"output": "0.00001"
			}`,
			wantStatus:  model.PriceSyncItemStatusRejected,
			wantWarning: upstreamprice.WarningInvalidTiers,
		},
		{
			name: "gap between tiers",
			pricing: `{
				"input_tiers": [{"cost":"0.000002","min":0,"max":100000},{"cost":"0.000004","min":200000}],
				"output": "0.00001"
			}`,
			wantStatus:  model.PriceSyncItemStatusRejected,
			wantWarning: upstreamprice.WarningInvalidTiers,
		},
		{
			name: "unclosed coverage with bounded last tier",
			pricing: `{
				"input_tiers": [{"cost":"0.000002","min":0,"max":100000},{"cost":"0.000004","min":100000,"max":200000}],
				"output": "0.00001"
			}`,
			wantStatus:  model.PriceSyncItemStatusRejected,
			wantWarning: upstreamprice.WarningInvalidTiers,
		},
		{
			name: "three tiers unsupported shape",
			pricing: `{
				"input_tiers": [{"cost":"1","min":0,"max":10},{"cost":"2","min":10,"max":20},{"cost":"3","min":20}],
				"output": "0.00001"
			}`,
			wantStatus:  model.PriceSyncItemStatusRejected,
			wantWarning: upstreamprice.WarningInvalidTiers,
		},
		{
			name: "negative tier cost",
			pricing: `{
				"input_tiers": [{"cost":"-0.000002","min":0,"max":200001},{"cost":"0.000004","min":200001}],
				"output": "0.00001"
			}`,
			wantStatus:  model.PriceSyncItemStatusRejected,
			wantWarning: upstreamprice.WarningInvalidPrice,
		},
		{
			name:        "negative flat price",
			pricing:     `{"input": "-0.000002", "output": "0.00001"}`,
			wantStatus:  model.PriceSyncItemStatusRejected,
			wantWarning: upstreamprice.WarningInvalidPrice,
		},
		{
			name:        "input without output fails closed",
			pricing:     `{"input": "0.000002"}`,
			wantStatus:  model.PriceSyncItemStatusUnsupported,
			wantWarning: upstreamprice.WarningIncompleteTokenPrice,
		},
		{
			name:        "no pricing at all",
			pricing:     `{}`,
			wantStatus:  model.PriceSyncItemStatusUnsupported,
			wantWarning: upstreamprice.WarningNoTokenPricing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, skipped := normalizeVercelModel(rawPricing(t, tt.pricing))
			require.Nil(t, obs)
			require.NotNil(t, skipped)
			assert.Equal(t, tt.wantStatus, skipped.Status)
			assert.Equal(t, tt.wantWarning, skipped.WarningCode)
		})
	}
}

func TestVercelSingleTierEqualsFlat(t *testing.T) {
	obs, skipped := normalizeVercelModel(rawPricing(t, `{
		"input_tiers": [{"cost":"0.000002","min":0}],
		"output": "0.00001"
	}`))
	require.Nil(t, skipped)
	require.NotNil(t, obs)
	assert.Equal(t, `tier("base", p * 2 + c * 10)`, obs.PriceExpr)
}
