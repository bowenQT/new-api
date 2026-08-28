package upstreamprice

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testClaim(now time.Time) previewClaim {
	baseRun := 7
	return previewClaim{
		Version:        previewClaimVersion,
		SourceId:       3,
		ConfigRevision: 5,
		BaseRunId:      &baseRun,
		PreviewDigest:  "digest",
		GateVersion:    gateConfigVersion,
		GateThreshold:  "0.2",
		ExpiresAt:      now.Add(previewTokenTTL).Unix(),
	}
}

func TestPreviewTokenRoundTrip(t *testing.T) {
	now := time.Now()
	token, err := signPreviewClaim(testClaim(now))
	require.NoError(t, err)

	claim, err := verifyPreviewToken(token, now)
	require.NoError(t, err)
	assert.Equal(t, 3, claim.SourceId)
	assert.Equal(t, int64(5), claim.ConfigRevision)
	require.NotNil(t, claim.BaseRunId)
	assert.Equal(t, 7, *claim.BaseRunId)
	assert.Equal(t, "digest", claim.PreviewDigest)
}

func TestPreviewTokenExpiryRejected(t *testing.T) {
	now := time.Now()
	token, err := signPreviewClaim(testClaim(now))
	require.NoError(t, err)

	_, err = verifyPreviewToken(token, now.Add(previewTokenTTL+time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestPreviewTokenTamperRejected(t *testing.T) {
	now := time.Now()
	token, err := signPreviewClaim(testClaim(now))
	require.NoError(t, err)

	parts := strings.SplitN(token, ".", 2)
	require.Len(t, parts, 2)
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)

	// Flip the claimed source id inside the payload; the HMAC must fail.
	tamperedPayload := strings.Replace(string(payload), `"source_id":3`, `"source_id":4`, 1)
	require.NotEqual(t, string(payload), tamperedPayload)
	tampered := base64.RawURLEncoding.EncodeToString([]byte(tamperedPayload)) + "." + parts[1]
	_, err = verifyPreviewToken(tampered, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature mismatch")

	// A truncated signature must also fail.
	_, err = verifyPreviewToken(parts[0]+"."+parts[1][:len(parts[1])-2], now)
	require.Error(t, err)

	// Garbage tokens fail closed.
	_, err = verifyPreviewToken("not-a-token", now)
	require.Error(t, err)
}

func TestPreviewTokenClaimVersionRejected(t *testing.T) {
	now := time.Now()
	claim := testClaim(now)
	claim.Version = previewClaimVersion + 1
	token, err := signPreviewClaim(claim)
	require.NoError(t, err)

	_, err = verifyPreviewToken(token, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claim version")
}
