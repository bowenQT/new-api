package upstreamprice

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidatePinnedEndpoint pins the endpoint gate every HTTP-backed adapter
// shares (spec §12): https only, an exact host match so no forged suffix or
// prefix domain passes, and a test-only bypass that production constructors
// never set.
func TestValidatePinnedEndpoint(t *testing.T) {
	const host = "models.dev"
	cases := []struct {
		name              string
		endpoint          string
		allowTestEndpoint bool
		wantError         string
	}{
		{name: "the pinned endpoint", endpoint: "https://models.dev/api.json"},
		{name: "plain http", endpoint: "http://models.dev/api.json", wantError: "only accepts"},
		{name: "forged suffix host", endpoint: "https://models.dev.evil.example/api.json", wantError: "only accepts"},
		{name: "forged prefix host", endpoint: "https://evil-models.dev/api.json", wantError: "only accepts"},
		{name: "subdomain", endpoint: "https://api.models.dev/api.json", wantError: "only accepts"},
		{name: "unparseable url", endpoint: "https://[::1/api.json", wantError: "invalid models_dev endpoint"},
		{
			name:              "a test endpoint is accepted only under the test bypass",
			endpoint:          "http://127.0.0.1:8080/api.json",
			allowTestEndpoint: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidatePinnedEndpoint("models_dev", testCase.endpoint, host, testCase.allowTestEndpoint)
			if testCase.wantError == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantError)
		})
	}
}

// failingReader fails after handing out a prefix, which is what a truncated
// upstream response looks like to the body reader.
type failingReader struct {
	prefix string
	read   bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		n := copy(p, r.prefix)
		return n, nil
	}
	return 0, errors.New("connection reset")
}

// TestReadBoundedCatalogBody pins the body bound every adapter shares: a body
// one byte past the limit is refused rather than silently truncated into a
// parseable prefix, the refused size is still reported so the run records what
// the source actually sent, and a failed read reports no size at all.
func TestReadBoundedCatalogBody(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		max       int64
		wantBytes int64
		wantBody  string
		wantError string
	}{
		{name: "under the limit", body: "abcd", max: 8, wantBytes: 4, wantBody: "abcd"},
		{name: "exactly at the limit", body: "abcd", max: 4, wantBytes: 4, wantBody: "abcd"},
		{name: "one byte over the limit", body: "abcde", max: 4, wantBytes: 5, wantError: "response exceeds 4 bytes"},
		{name: "no limit falls back to the catalog default", body: "abcd", max: 0, wantBytes: 4, wantBody: "abcd"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body, readBytes, err := ReadBoundedCatalogBody(strings.NewReader(testCase.body), testCase.max)
			assert.Equal(t, testCase.wantBytes, readBytes)
			if testCase.wantError == "" {
				require.NoError(t, err)
				assert.Equal(t, testCase.wantBody, string(body))
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantError)
			assert.Nil(t, body)
		})
	}

	t.Run("a failed read reports no size", func(t *testing.T) {
		body, readBytes, err := ReadBoundedCatalogBody(&failingReader{prefix: "partial"}, 1024)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read failed: connection reset")
		assert.Zero(t, readBytes)
		assert.Nil(t, body)
	})
}
