package upstreamprice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoCatalogRequestSharedOverallDeadline: every retry attempt runs under
// one shared deadline established outside the retry loop, so backoff and
// retries can never exceed the total fetch budget.
func TestDoCatalogRequestSharedOverallDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	start := time.Now()
	var deadlines []time.Time
	response, err := DoCatalogRequest(context.Background(), NewCatalogHTTPClient(), func(ctx context.Context) (*http.Request, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "every attempt must carry the overall deadline")
		deadlines = append(deadlines, deadline)
		return http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	})
	// The final attempt hands back the 503 response for the caller to report.
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.NoError(t, response.Body.Close())

	require.Len(t, deadlines, maxFetchAttempts, "503 is retried up to the attempt budget")
	// All attempts share the same deadline (one WithTimeout outside the
	// loop), and it sits within the total budget from the start.
	assert.Equal(t, deadlines[0], deadlines[1])
	assert.Equal(t, deadlines[0], deadlines[2])
	assert.WithinDuration(t, start.Add(fetchTotalTimeout), deadlines[0], 2*time.Second)
}

// TestDoCatalogRequestHonorsCallerCancellation: a cancelled caller context
// stops the fetch immediately with a context error instead of burning the
// retry budget.
func TestDoCatalogRequestHonorsCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DoCatalogRequest(ctx, NewCatalogHTTPClient(), func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
