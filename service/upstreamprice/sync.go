package upstreamprice

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Hardened fetch HTTP client (spec §6.2 / §12)
// ---------------------------------------------------------------------------

// MaxFetchResponseBytes bounds the decompressed response body size; larger
// responses fail explicitly. Adapters needing a test-only override carry an
// unexported field seeded from this constant.
const MaxFetchResponseBytes = int64(16 << 20) // 16 MiB

const (
	fetchConnectTimeout = 10 * time.Second
	fetchTotalTimeout   = 60 * time.Second
)

// ErrRedirectRefused is returned when an upstream price endpoint answers with
// a redirect; catalog fetches never follow redirects.
var ErrRedirectRefused = errors.New("upstream price fetch: redirects are refused")

// Bounded fetch retry policy (spec §8.2: retry/backoff is defined by this
// service): at most 2 retries after the initial attempt, exponential backoff,
// always bounded by the request context. Only transport errors and the
// retryable status codes below are retried; redirect refusals, context
// cancellation, other status codes, and parse errors are never retried.
const maxFetchAttempts = 3

var fetchRetryBackoffBase = 100 * time.Millisecond

func isRetryableFetchStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// cancelOnCloseBody releases the overall fetch deadline when the caller
// finishes reading the response body.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// DoCatalogRequest executes one catalog fetch with the bounded retry policy
// under a single overall deadline: retries, backoff waits, and the body read
// of the returned response all share one fetchTotalTimeout budget.
// buildRequest is called once per attempt with the deadline-bound context so
// every attempt gets a fresh request.
func DoCatalogRequest(ctx context.Context, client *http.Client, buildRequest func(ctx context.Context) (*http.Request, error)) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTotalTimeout)
	var lastErr error
	for attempt := 0; attempt < maxFetchAttempts; attempt++ {
		if attempt > 0 {
			backoff := fetchRetryBackoffBase << (attempt - 1)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				cancel()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		request, err := buildRequest(ctx)
		if err != nil {
			cancel()
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			if errors.Is(err, ErrRedirectRefused) || ctx.Err() != nil {
				cancel()
				return nil, err
			}
			lastErr = err
			continue
		}
		if isRetryableFetchStatus(response.StatusCode) && attempt < maxFetchAttempts-1 {
			_ = response.Body.Close()
			lastErr = fmt.Errorf("upstream returned http %d", response.StatusCode)
			continue
		}
		// The deadline stays armed until the caller closes the body, so slow
		// body reads cannot escape the total budget.
		response.Body = &cancelOnCloseBody{ReadCloser: response.Body, cancel: cancel}
		return response, nil
	}
	cancel()
	return nil, lastErr
}

// ValidatePinnedEndpoint refuses to fetch from anything but the adapter's own
// pinned public catalog URL: https only, and an exact host match so a
// suffix-forged domain such as "models.dev.evil.example" is rejected (spec
// §12). label names the adapter in the error text. allowTestEndpoint is the
// package-internal escape hatch adapters expose to their own tests so a fixture
// can be served from httptest; production constructors never set it.
func ValidatePinnedEndpoint(label string, endpoint string, host string, allowTestEndpoint bool) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid %s endpoint: %w", label, err)
	}
	if allowTestEndpoint {
		return nil
	}
	if parsed.Scheme != "https" || parsed.Hostname() != host {
		return fmt.Errorf("%s adapter only accepts %s", label, endpoint)
	}
	return nil
}

// ReadBoundedCatalogBody reads a catalog response body under an explicit size
// bound, reading one byte past it so an oversized body is detected rather than
// silently truncated into a parseable prefix. maxResponseBytes falls back to
// MaxFetchResponseBytes when the adapter declares no limit of its own.
//
// The returned byte count is what the run records as its response size: it is
// reported for an oversized body too, and stays zero when the read itself
// failed. Callers wrap the returned error with their own adapter prefix; it
// never carries any part of the response.
func ReadBoundedCatalogBody(body io.Reader, maxResponseBytes int64) ([]byte, int64, error) {
	if maxResponseBytes <= 0 {
		maxResponseBytes = MaxFetchResponseBytes
	}
	read, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read failed: %w", err)
	}
	if int64(len(read)) > maxResponseBytes {
		return nil, int64(len(read)), fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return read, int64(len(read)), nil
}

// NewCatalogHTTPClient builds the hardened HTTP client all price adapters
// must use: redirects are rejected outright, and connection plus total
// timeouts are enforced. It intentionally does not reuse the ratio_sync
// client construction, which sets no CheckRedirect.
func NewCatalogHTTPClient() *http.Client {
	return &http.Client{
		Timeout: fetchTotalTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return ErrRedirectRefused
		},
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: fetchConnectTimeout,
			}).DialContext,
			TLSHandshakeTimeout:   fetchConnectTimeout,
			ResponseHeaderTimeout: fetchConnectTimeout,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
		},
	}
}

// ---------------------------------------------------------------------------
// Preview token (spec §8.1 / §12)
// ---------------------------------------------------------------------------

const (
	previewClaimVersion = 1
	previewTokenTTL     = 10 * time.Minute
	// gateConfigVersion identifies the validation/gate rule set bound into
	// the preview token claim.
	gateConfigVersion = 1
)

// DefaultCoverageDropThreshold refuses a commit whose valid-model count drops
// by more than this fraction versus the last successful run (spec §8.2);
// overridable per source via settings.coverage_drop_threshold.
const DefaultCoverageDropThreshold = 0.2

// previewTokenKey is generated per process and never persisted. Preview and
// commit must therefore reach the same instance; commit re-fetches and
// re-validates regardless, so safety does not depend on this key (spec §8.1).
var previewTokenKey []byte

func init() {
	previewTokenKey = make([]byte, 32)
	if _, err := rand.Read(previewTokenKey); err != nil {
		panic(fmt.Sprintf("upstreamprice: cannot initialize preview token key: %v", err))
	}
}

type previewClaim struct {
	Version        int    `json:"version"`
	SourceId       int    `json:"source_id"`
	ConfigRevision int64  `json:"config_revision"`
	BaseRunId      *int   `json:"base_run_id"`
	PreviewDigest  string `json:"preview_digest"`
	GateVersion    int    `json:"gate_version"`
	GateThreshold  string `json:"gate_threshold"`
	ExpiresAt      int64  `json:"expires_at"`
}

func signPreviewClaim(claim previewClaim) (string, error) {
	payload, err := common.Marshal(claim)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, previewTokenKey)
	mac.Write(payload)
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func verifyPreviewToken(token string, now time.Time) (*previewClaim, error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return nil, errors.New("malformed preview token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		return nil, errors.New("malformed preview token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return nil, errors.New("malformed preview token")
	}
	mac := hmac.New(sha256.New, previewTokenKey)
	mac.Write(payload)
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(signature, expected) != 1 {
		return nil, errors.New("preview token signature mismatch, re-preview required")
	}
	claim := &previewClaim{}
	if err := common.Unmarshal(payload, claim); err != nil {
		return nil, errors.New("malformed preview token")
	}
	if claim.Version != previewClaimVersion {
		return nil, errors.New("preview token claim version mismatch, re-preview required")
	}
	if now.Unix() >= claim.ExpiresAt {
		return nil, errors.New("preview token expired, re-preview required")
	}
	return claim, nil
}

// ---------------------------------------------------------------------------
// Sync plan (shared by preview and commit)
// ---------------------------------------------------------------------------

const (
	changeNew       = "new"
	changeChanged   = "changed"
	changeUnchanged = "unchanged"
)

// boundSourceModelIdentity returns the storable identity for a source model
// name. Names within MaxSourceModelNameLength bytes pass through unchanged;
// longer names fail closed to a bounded diagnostic identity — the first 200
// bytes (cut at a UTF-8 boundary) plus "#" plus the first 12 hex characters
// of the full name's SHA-256 — so the oversized original never reaches
// storage or log summaries, while distinct originals stay distinguishable.
func boundSourceModelIdentity(name string) (string, bool) {
	if len(name) <= MaxSourceModelNameLength {
		return name, false
	}
	digest := sha256.Sum256([]byte(name))
	return common.TruncateUTF8(name, 200) + "#" + hex.EncodeToString(digest[:6]), true
}

type plannedItem struct {
	SourceModelName string
	Status          string
	WarningCode     string
	Change          string
	Price           *NormalizedPrice
}

type syncPlan struct {
	Source               SourceConfig
	AdapterKey           string
	Meta                 FetchMeta
	Items                []plannedItem // valid + unsupported + rejected, sorted by model name
	Missing              []string      // sorted
	BaseRunId            *int
	BaseValidCount       int
	ValidCount           int
	UnsupportedCount     int
	RejectedCount        int
	NewCount             int
	ChangedCount         int
	UnchangedCount       int
	CoverageDropExceeded bool
	GateThreshold        float64
	// PriceJumps are the price movements this plan measured against the
	// baseline run (spec §13). They are evidence recorded on the run and never
	// influence Status: a price movement alerts, it does not refuse a commit.
	PriceJumps         []priceJumpEntry
	PriceJumpThreshold float64
	Status             string
	StartedAt          int64
	DurationMs         int64
}

func coverageDropThreshold(config SourceConfig) float64 {
	if config.Settings.CoverageDropThreshold != nil {
		return *config.Settings.CoverageDropThreshold
	}
	return DefaultCoverageDropThreshold
}

// baseState is the last successful run of a source, as far as planning the
// next one needs it.
type baseState struct {
	RunId *int
	Run   *model.PriceSyncRun
	// Items are the baseline run items in id order, the manifest a 304 answer
	// replays.
	Items []*model.PriceSyncRunItem
	// Fingerprints and Snapshots are keyed by source model name and cover the
	// valid items only.
	Fingerprints map[string]string
	Snapshots    map[string]*model.PriceSnapshot
	// Historical holds every model name the baseline run saw, in any status, so
	// missing markers persist across runs.
	Historical map[string]bool
	ValidCount int
}

// loadBaseState reads the last successful run of a source. A source that never
// committed one returns an empty state rather than an error.
func loadBaseState(source *model.PriceSource) (*baseState, error) {
	base := &baseState{
		Fingerprints: map[string]string{},
		Snapshots:    map[string]*model.PriceSnapshot{},
		Historical:   map[string]bool{},
	}
	if source.LastSuccessRunId == nil {
		return base, nil
	}
	baseRunId := *source.LastSuccessRunId
	run, err := model.GetPriceSyncRunById(baseRunId)
	if err != nil {
		return nil, err
	}
	items, err := model.GetPriceSyncRunItems(baseRunId)
	if err != nil {
		return nil, err
	}
	base.RunId = source.LastSuccessRunId
	base.Run = run
	base.Items = items
	validSnapshotIds := make([]int, 0, len(items))
	modelBySnapshotId := make(map[int]string, len(items))
	for _, item := range items {
		base.Historical[item.SourceModelName] = true
		if item.Status == model.PriceSyncItemStatusValid && item.SnapshotId != nil {
			base.ValidCount++
			validSnapshotIds = append(validSnapshotIds, *item.SnapshotId)
			modelBySnapshotId[*item.SnapshotId] = item.SourceModelName
		}
	}
	snapshots, err := model.GetPriceSnapshotsByIds(validSnapshotIds)
	if err != nil {
		return nil, err
	}
	for _, snapshot := range snapshots {
		if modelName, ok := modelBySnapshotId[snapshot.Id]; ok {
			base.Fingerprints[modelName] = snapshot.Fingerprint
			base.Snapshots[modelName] = snapshot
		}
	}
	return base, nil
}

// conditionalFetchRevision returns the baseline source revision to send as
// If-None-Match, or "" when the fetch must download the full representation.
//
// A 304 answer is replayed from the baseline run without re-normalizing
// anything, so a conditional request is only safe while the baseline is still
// an exact statement of what a full fetch would produce today. Two conditions
// establish that, and either one failing forces the full path:
//
//   - the configuration the baseline ran under still matches the source's
//     current configuration (the §7.3 digest). A changed model mapping, role,
//     scope, channel, or adapter re-normalizes into different snapshots, and
//     replaying the old ones would re-confirm them under the new configuration
//     and silently clear the source_config_changed alert.
//   - every baseline valid snapshot carries the current FingerprintVersion. A
//     bumped version means today's canonical payload differs, so a full fetch
//     of the same bytes would produce new snapshots; replaying would keep the
//     older-version ones current instead. Any change to normalization
//     semantics must therefore bump FingerprintVersion (spec §7.2) for this
//     gate to hold.
func conditionalFetchRevision(config SourceConfig, base *baseState) string {
	if base.Run == nil || base.Run.SourceRevision == "" {
		return ""
	}
	if priceSourceConfigChanged(config, base.Run) {
		return ""
	}
	// A baseline whose snapshot rows cannot all be read back is not replayable.
	if base.ValidCount == 0 || len(base.Snapshots) != base.ValidCount {
		return ""
	}
	for _, snapshot := range base.Snapshots {
		if snapshot.FingerprintVersion != FingerprintVersion {
			return ""
		}
	}
	return base.Run.SourceRevision
}

// buildSyncPlan runs fetch → normalize → validate → diff for one source. It
// never writes anything.
func buildSyncPlan(ctx context.Context, source *model.PriceSource) (*syncPlan, error) {
	config, err := SourceConfigFromModel(source)
	if err != nil {
		return nil, err
	}
	adapter, err := FindAdapterForSource(config)
	if err != nil {
		return nil, err
	}
	base, err := loadBaseState(source)
	if err != nil {
		return nil, err
	}

	ifNoneMatch := ""
	conditionalFetcher, supportsConditional := adapter.(ConditionalFetcher)
	if supportsConditional {
		ifNoneMatch = conditionalFetchRevision(config, base)
	}

	startedAt := common.GetTimestamp()
	fetchStart := time.Now()
	var (
		observations []Observation
		meta         FetchMeta
	)
	if ifNoneMatch != "" {
		observations, meta, err = conditionalFetcher.FetchConditional(ctx, config, ifNoneMatch)
	} else {
		observations, meta, err = adapter.Fetch(ctx, config)
	}
	durationMs := time.Since(fetchStart).Milliseconds()
	if err != nil {
		return &syncPlan{
			Source:     config,
			AdapterKey: adapter.Key(),
			Meta:       meta,
			BaseRunId:  base.RunId,
			StartedAt:  startedAt,
			DurationMs: durationMs,
			Status:     model.PriceSyncRunStatusFailed,
		}, fmt.Errorf("fetch failed for source %d via adapter %s: %w", source.Id, adapter.Key(), err)
	}
	// NotModified is only honoured for a request this function made
	// conditional, so an adapter cannot put the engine on the replay path
	// without a baseline to replay.
	if ifNoneMatch != "" && meta.NotModified {
		return buildNotModifiedPlan(config, adapter.Key(), meta, base, startedAt, durationMs), nil
	}

	plan := &syncPlan{
		Source:             config,
		AdapterKey:         adapter.Key(),
		Meta:               meta,
		BaseRunId:          base.RunId,
		BaseValidCount:     base.ValidCount,
		GateThreshold:      coverageDropThreshold(config),
		PriceJumpThreshold: priceJumpThreshold(config),
		StartedAt:          startedAt,
		DurationMs:         durationMs,
	}

	// Every model identity is bounded BEFORE any planned/skipped item is
	// formed, so oversized names never reach storage or logs. Duplicate
	// (bounded) names are then handled deterministically and
	// order-independently: any model appearing more than once (in whatever
	// valid/skipped combination) becomes exactly one rejected item and never
	// stores a snapshot.
	type planEntry struct {
		name    string
		bounded bool
		skipped *SkippedModel
		obs     *Observation
	}
	entries := make([]planEntry, 0, len(observations)+len(meta.Skipped))
	for i := range meta.Skipped {
		name, bounded := boundSourceModelIdentity(meta.Skipped[i].SourceModelName)
		entries = append(entries, planEntry{name: name, bounded: bounded, skipped: &meta.Skipped[i]})
	}
	for i := range observations {
		name, bounded := boundSourceModelIdentity(observations[i].SourceModelName)
		entries = append(entries, planEntry{name: name, bounded: bounded, obs: &observations[i]})
	}
	nameCounts := make(map[string]int, len(entries))
	for _, entry := range entries {
		nameCounts[entry.name]++
	}
	seen := make(map[string]bool, len(nameCounts))
	for name, count := range nameCounts {
		if count > 1 {
			seen[name] = true
			plan.Items = append(plan.Items, plannedItem{
				SourceModelName: name,
				Status:          model.PriceSyncItemStatusRejected,
				WarningCode:     WarningDuplicateModel,
			})
		}
	}
	for _, entry := range entries {
		if seen[entry.name] {
			continue
		}
		seen[entry.name] = true
		if entry.bounded {
			// Oversized model identity: fail closed to a bounded diagnostic
			// item; the original value is discarded.
			plan.Items = append(plan.Items, plannedItem{
				SourceModelName: entry.name,
				Status:          model.PriceSyncItemStatusRejected,
				WarningCode:     WarningFieldTooLong,
			})
			continue
		}
		if entry.skipped != nil {
			plan.Items = append(plan.Items, plannedItem{
				SourceModelName: entry.name,
				Status:          entry.skipped.Status,
				WarningCode:     entry.skipped.WarningCode,
			})
			continue
		}
		obs := *entry.obs
		item := plannedItem{SourceModelName: obs.SourceModelName}
		normalized, err := NormalizeObservation(obs, config, adapter)
		if err != nil {
			item.Status = model.PriceSyncItemStatusRejected
			item.WarningCode = WarningRoleScopeOutOfRange
			plan.Items = append(plan.Items, item)
			continue
		}
		if warningCode, err := ValidateNormalizedPrice(normalized); err != nil {
			item.Status = model.PriceSyncItemStatusRejected
			item.WarningCode = warningCode
			plan.Items = append(plan.Items, item)
			continue
		}
		item.Status = model.PriceSyncItemStatusValid
		item.Price = normalized
		if baseFingerprint, ok := base.Fingerprints[obs.SourceModelName]; !ok {
			item.Change = changeNew
		} else if baseFingerprint != normalized.Fingerprint {
			item.Change = changeChanged
			// A changed fingerprint is the only case where a price can have
			// moved: a new model has nothing to move from, and an unchanged
			// one is byte-identical to its baseline snapshot.
			plan.PriceJumps = append(plan.PriceJumps,
				evaluatePriceJump(base.Snapshots[obs.SourceModelName], normalized, plan.PriceJumpThreshold)...)
		} else {
			item.Change = changeUnchanged
		}
		plan.Items = append(plan.Items, item)
	}

	for name := range base.Historical {
		if !seen[name] {
			plan.Missing = append(plan.Missing, name)
		}
	}
	plan.deriveRunOutcome()
	return plan, nil
}

// deriveRunOutcome sorts a plan's manifest and derives the per-status counts,
// the coverage-drop verdict, and the run status from it (spec §8.2). The full
// fetch path and the 304 replay path both end here, so a replayed baseline is
// graded by exactly the same rules — a partial baseline replays as partial.
func (plan *syncPlan) deriveRunOutcome() {
	sort.Slice(plan.Items, func(i, j int) bool {
		return plan.Items[i].SourceModelName < plan.Items[j].SourceModelName
	})
	sort.Strings(plan.Missing)
	for _, item := range plan.Items {
		switch item.Status {
		case model.PriceSyncItemStatusValid:
			plan.ValidCount++
			switch item.Change {
			case changeNew:
				plan.NewCount++
			case changeChanged:
				plan.ChangedCount++
			default:
				plan.UnchangedCount++
			}
		case model.PriceSyncItemStatusUnsupported:
			plan.UnsupportedCount++
		default:
			plan.RejectedCount++
		}
	}

	if plan.BaseValidCount > 0 {
		drop := 1 - float64(plan.ValidCount)/float64(plan.BaseValidCount)
		plan.CoverageDropExceeded = drop > plan.GateThreshold
	}

	switch {
	case plan.ValidCount == 0:
		plan.Status = model.PriceSyncRunStatusFailed
	case plan.CoverageDropExceeded:
		plan.Status = model.PriceSyncRunStatusFailed
	case plan.UnsupportedCount+plan.RejectedCount+len(plan.Missing) > 0:
		plan.Status = model.PriceSyncRunStatusPartial
	default:
		plan.Status = model.PriceSyncRunStatusSucceeded
	}
}

// buildNotModifiedPlan replays the baseline run for a 304 answer: the upstream
// representation is unchanged, so every baseline item is re-affirmed exactly as
// it was recorded and nothing is fetched, parsed, or re-normalized. The valid
// items carry their baseline snapshots, which the commit transaction resolves
// through the same fingerprint idempotency as any other run, so they keep the
// same snapshot ids and only their last_seen evidence advances.
func buildNotModifiedPlan(config SourceConfig, adapterKey string, meta FetchMeta, base *baseState, startedAt int64, durationMs int64) *syncPlan {
	// A 304 carries no body, so the source-level evidence a full fetch reports
	// from the payload is copied from the baseline run instead of invented.
	meta.ResponseBytes = 0
	meta.Skipped = nil
	meta.Discovered = base.Run.DiscoveredCount
	if meta.SourceRevision == "" {
		meta.SourceRevision = base.Run.SourceRevision
	}
	// A 304 re-affirms the baseline snapshots byte for byte, so every replayed
	// item is unchanged and no price can have moved. The replayed run therefore
	// carries no movement summary — not an empty one standing in for an
	// unevaluated question, but the correct answer that nothing moved.
	plan := &syncPlan{
		Source:             config,
		AdapterKey:         adapterKey,
		Meta:               meta,
		BaseRunId:          base.RunId,
		BaseValidCount:     base.ValidCount,
		GateThreshold:      coverageDropThreshold(config),
		PriceJumpThreshold: priceJumpThreshold(config),
		StartedAt:          startedAt,
		DurationMs:         durationMs,
	}
	for _, item := range base.Items {
		switch item.Status {
		case model.PriceSyncItemStatusValid:
			snapshot := base.Snapshots[item.SourceModelName]
			if snapshot == nil {
				// conditionalFetchRevision refuses a baseline whose snapshots
				// cannot all be read, so this cannot be reached; dropping the
				// item keeps the coverage gate as the fallback authority.
				continue
			}
			plan.Items = append(plan.Items, plannedItem{
				SourceModelName: item.SourceModelName,
				Status:          model.PriceSyncItemStatusValid,
				Change:          changeUnchanged,
				Price:           replayPriceFromSnapshot(snapshot),
			})
		case model.PriceSyncItemStatusMissing:
			plan.Missing = append(plan.Missing, item.SourceModelName)
		default:
			plan.Items = append(plan.Items, plannedItem{
				SourceModelName: item.SourceModelName,
				Status:          item.Status,
				WarningCode:     item.WarningCode,
			})
		}
	}
	plan.deriveRunOutcome()
	return plan
}

// replayPriceFromSnapshot rebuilds the normalized price a baseline snapshot was
// written from. Every field is read back from the stored row, including the
// fingerprint, so a 304 replay re-affirms the recorded observation instead of
// recomputing it.
func replayPriceFromSnapshot(snapshot *model.PriceSnapshot) *NormalizedPrice {
	price := &NormalizedPrice{
		SourceModelName:    snapshot.SourceModelName,
		CanonicalModelName: snapshot.CanonicalModelName,
		MappingStatus:      snapshot.MappingStatus,
		Role:               PriceRole(snapshot.Role),
		Scope:              PriceScope(snapshot.Scope),
		Provider:           snapshot.Provider,
		Currency:           snapshot.Currency,
		FormulaKind:        snapshot.FormulaKind,
		PriceExpr:          snapshot.PriceExpr,
		ExprVersion:        snapshot.ExprVersion,
		EffectiveAt:        snapshot.EffectiveAt,
		Fingerprint:        snapshot.Fingerprint,
	}
	if snapshot.Metadata != "" {
		metadata := map[string]string{}
		if err := common.UnmarshalJsonStr(snapshot.Metadata, &metadata); err == nil {
			price.Metadata = metadata
		}
	}
	return price
}

// ---------------------------------------------------------------------------
// Preview digest
// ---------------------------------------------------------------------------

type previewDigestItem struct {
	Model       string `json:"model"`
	Status      string `json:"status"`
	Change      string `json:"change,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	WarningCode string `json:"warning_code,omitempty"`
}

type previewDigestPayload struct {
	SourceId       int                 `json:"source_id"`
	ConfigRevision int64               `json:"config_revision"`
	BaseRunId      *int                `json:"base_run_id"`
	GateVersion    int                 `json:"gate_version"`
	GateThreshold  string              `json:"gate_threshold"`
	Discovered     int                 `json:"discovered"`
	Items          []previewDigestItem `json:"items"`
	Missing        []string            `json:"missing"`
}

func computePreviewDigest(plan *syncPlan) (string, error) {
	payload := previewDigestPayload{
		SourceId:       plan.Source.Id,
		ConfigRevision: plan.Source.ConfigRevision,
		BaseRunId:      plan.BaseRunId,
		GateVersion:    gateConfigVersion,
		GateThreshold:  strconv.FormatFloat(plan.GateThreshold, 'f', -1, 64),
		Discovered:     plan.Meta.Discovered,
		Items:          make([]previewDigestItem, 0, len(plan.Items)),
		Missing:        append([]string{}, plan.Missing...),
	}
	for _, item := range plan.Items {
		digestItem := previewDigestItem{
			Model:       item.SourceModelName,
			Status:      item.Status,
			Change:      item.Change,
			WarningCode: item.WarningCode,
		}
		if item.Price != nil {
			digestItem.Fingerprint = item.Price.Fingerprint
		}
		payload.Items = append(payload.Items, digestItem)
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// ---------------------------------------------------------------------------
// Preview / Commit entry points
// ---------------------------------------------------------------------------

// PreviewPriceSource fetches, normalizes, validates, and diffs a source
// without writing anything, then issues the short-lived HMAC preview token
// required by commit (spec §8.1). Orphaned and disabled sources may preview
// for diagnostics.
func PreviewPriceSource(ctx context.Context, sourceId int) (*dto.UpstreamPricePreviewResponse, error) {
	source, err := model.GetPriceSourceById(sourceId)
	if err != nil {
		return nil, err
	}
	plan, err := buildSyncPlan(ctx, source)
	if err != nil {
		return nil, err
	}
	digest, err := computePreviewDigest(plan)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(previewTokenTTL).Unix()
	token, err := signPreviewClaim(previewClaim{
		Version:        previewClaimVersion,
		SourceId:       plan.Source.Id,
		ConfigRevision: plan.Source.ConfigRevision,
		BaseRunId:      plan.BaseRunId,
		PreviewDigest:  digest,
		GateVersion:    gateConfigVersion,
		GateThreshold:  strconv.FormatFloat(plan.GateThreshold, 'f', -1, 64),
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		return nil, err
	}

	response := &dto.UpstreamPricePreviewResponse{
		SourceId:             plan.Source.Id,
		BaseRunId:            plan.BaseRunId,
		ProjectedRunStatus:   plan.Status,
		DiscoveredCount:      plan.Meta.Discovered,
		ValidCount:           plan.ValidCount,
		UnsupportedCount:     plan.UnsupportedCount,
		RejectedCount:        plan.RejectedCount,
		MissingCount:         len(plan.Missing),
		NewCount:             plan.NewCount,
		ChangedCount:         plan.ChangedCount,
		UnchangedCount:       plan.UnchangedCount,
		CoverageDropExceeded: plan.CoverageDropExceeded,
		PriceJumpCount:       len(plan.PriceJumps),
		Missing:              append([]string{}, plan.Missing...),
		PreviewToken:         token,
		ExpiresAt:            expiresAt,
	}
	for _, item := range plan.Items {
		view := dto.UpstreamPricePreviewItem{
			SourceModelName: item.SourceModelName,
			Status:          item.Status,
			Change:          item.Change,
			WarningCode:     item.WarningCode,
		}
		if item.Price != nil {
			view.CanonicalModelName = item.Price.CanonicalModelName
			view.MappingStatus = item.Price.MappingStatus
			view.Currency = item.Price.Currency
			view.FormulaKind = item.Price.FormulaKind
			view.PriceExpr = item.Price.PriceExpr
			view.Fingerprint = item.Price.Fingerprint
			view.Metadata = item.Price.Metadata
			view.VariesByProvider = item.Price.Metadata[MetadataKeyVariesByProvider] == "true"
		}
		response.Items = append(response.Items, view)
	}
	return response, nil
}

// MetadataKeyVariesByProvider marks observations whose upstream price differs
// per routed provider; such prices are saved but must never be shown as a
// confirmed cost (spec §6.2).
const MetadataKeyVariesByProvider = "varies_by_provider"

// MetadataKeyUnsupportedDimensions lists source pricing dimensions Phase 1
// does not normalize (service tiers, regional, fast, media, ...), so a saved
// price never silently claims completeness (spec §6.2).
const MetadataKeyUnsupportedDimensions = "unsupported_dimensions"

// SyncPriceSource is the commit phase (spec §8.1): it re-fetches and fully
// recomputes the plan server-side, verifies the preview token claim, and only
// then writes the run, run items, and snapshots in one CAS-guarded
// transaction. Client-supplied price content is never accepted.
func SyncPriceSource(ctx context.Context, sourceId int, previewToken string) (*dto.UpstreamPriceSyncResponse, error) {
	source, err := model.GetPriceSourceById(sourceId)
	if err != nil {
		return nil, err
	}
	if err := checkSourceRunnableForCommit(source); err != nil {
		return nil, err
	}

	claim, err := verifyPreviewToken(previewToken, time.Now())
	if err != nil {
		return nil, err
	}
	if claim.SourceId != source.Id {
		return nil, errors.New("preview token belongs to a different source")
	}
	if claim.GateVersion != gateConfigVersion {
		return nil, errors.New("gate configuration changed, re-preview required")
	}
	if claim.ConfigRevision != source.ConfigRevision {
		return nil, model.ErrPriceSyncConflict
	}

	plan, planErr := buildSyncPlan(ctx, source)
	if planErr != nil {
		return nil, recordSyncPlanFailure(ctx, source, plan, planErr)
	}
	if !model.IntPtrEqual(claim.BaseRunId, plan.BaseRunId) {
		return nil, model.ErrPriceSyncConflict
	}
	if claim.GateThreshold != strconv.FormatFloat(plan.GateThreshold, 'f', -1, 64) {
		return nil, errors.New("gate configuration changed, re-preview required")
	}
	digest, err := computePreviewDigest(plan)
	if err != nil {
		return nil, err
	}
	if digest != claim.PreviewDigest {
		return nil, errors.New("source content changed since preview, re-preview required")
	}

	return commitSyncPlan(ctx, source, plan, claim.ConfigRevision, claim.BaseRunId)
}

// SyncPriceSourceWithoutPreview is the unattended commit path used by the
// scheduled task (spec §8.4). There is no human preview, but the fetch,
// normalization, validation, and coverage/change gates are identical, and the
// same CAS-guarded commit transaction writes the result. It never touches sale
// pricing.
//
// Every failure on this path is recorded, including the ones that produce no
// plan: a refused preflight, an unusable adapter or settings, a base-state read
// error, and a commit transaction that rolled back. The scheduler backs off by
// the source's own interval from the last attempt, so a failure that left no
// timestamp would make the source retry on every wake (spec §8.4). The manual
// preview/commit path deliberately keeps its existing semantics and stamps
// nothing for a refusal that never ran.
//
// The one scheduled failure that is neither stamped nor recorded is a CAS
// conflict: it means an admin changed the source configuration while this fetch
// was running, so nothing about the new configuration failed.
func SyncPriceSourceWithoutPreview(ctx context.Context, source *model.PriceSource) (*dto.UpstreamPriceSyncResponse, error) {
	if err := checkSourceRunnableForCommit(source); err != nil {
		return nil, recordScheduledAttemptFailure(ctx, source, err)
	}
	plan, planErr := buildSyncPlan(ctx, source)
	if planErr != nil {
		if plan == nil {
			return nil, recordScheduledAttemptFailure(ctx, source, planErr)
		}
		return nil, recordSyncPlanFailure(ctx, source, plan, planErr)
	}
	response, commitErr := commitSyncPlan(ctx, source, plan, plan.Source.ConfigRevision, plan.BaseRunId)
	if commitErr != nil {
		if errors.Is(commitErr, model.ErrPriceSyncConflict) {
			// The source configuration changed while this fetch was running, so
			// the CAS refused a commit computed under the superseded
			// configuration. Backing the source off here would delay the new
			// configuration's first real sync by a full interval, and counting a
			// consecutive failure would blame the new configuration for a
			// conflict it did not cause. The next wake re-plans under the
			// current configuration and its CAS passes, so this cannot loop.
			return nil, commitErr
		}
		return nil, recordScheduledAttemptFailure(ctx, source, commitErr)
	}
	return response, nil
}

// recordScheduledAttemptFailure records a scheduled attempt that failed before
// any plan existed and returns the original cause.
//
// It writes a lightweight failed run carrying no items. That run is what makes
// the attempt visible: a pre-plan failure — a disabled channel, an unavailable
// adapter, corrupt settings — used to update only last_error_at, so a source
// failing this way forever never reached the consecutive-failure alert, which
// counts failed run rows (spec §13). The run write stamps the backoff timestamp
// as part of the same transaction; if it cannot be written, the timestamp is
// still stamped on its own, because the scheduler's due check depends on it.
func recordScheduledAttemptFailure(ctx context.Context, source *model.PriceSource, cause error) error {
	run := model.PriceSyncRun{
		Status:               model.PriceSyncRunStatusFailed,
		AdapterKey:           source.AdapterKey,
		StartedAt:            common.GetTimestamp(),
		SourceConfigRevision: source.ConfigRevision,
		ErrorSummary:         cause.Error(),
	}
	if _, err := model.RecordFailedPriceSyncRun(source.Id, run); err != nil {
		common.SysError(fmt.Sprintf("upstream price source %d failure run could not be recorded: %v", source.Id, err))
		if stampErr := model.RecordPriceSourceFailure(source.Id, cause.Error()); stampErr != nil {
			common.SysError(fmt.Sprintf("upstream price source %d failure timestamp could not be recorded: %v", source.Id, stampErr))
		}
		return cause
	}
	LogCatalogAlertsAfterWrite(ctx, source.Id, nil)
	return cause
}

// checkSourceRunnableForCommit is the pre-fetch gate shared by the manual and
// the scheduled commit paths. The authoritative orphan/disabled check runs
// again inside the commit transaction under the row lock.
func checkSourceRunnableForCommit(source *model.PriceSource) error {
	if !source.Enabled {
		return errors.New("price source is disabled; commit refused")
	}
	return checkSupplierChannelForCommit(source)
}

// recordSyncPlanFailure persists a failed run for a fetch that never produced
// a plan result, then returns the original failure.
func recordSyncPlanFailure(ctx context.Context, source *model.PriceSource, plan *syncPlan, planErr error) error {
	if plan == nil {
		return planErr
	}
	recordRun := buildRunFromPlan(plan, planErr.Error())
	if _, recordErr := model.RecordFailedPriceSyncRun(source.Id, recordRun); recordErr != nil {
		return fmt.Errorf("fetch failed (%v) and failed run could not be recorded: %w", planErr, recordErr)
	}
	LogCatalogAlertsAfterWrite(ctx, source.Id, nil)
	return planErr
}

// commitSyncPlan writes one prepared plan through the CAS-guarded commit
// transaction and summarizes the resulting run.
func commitSyncPlan(ctx context.Context, source *model.PriceSource, plan *syncPlan, expectedConfigRevision int64, expectedBaseRunId *int) (*dto.UpstreamPriceSyncResponse, error) {
	errorSummary := ""
	if plan.Status == model.PriceSyncRunStatusFailed {
		if plan.ValidCount == 0 {
			errorSummary = "no valid observations"
		} else if plan.CoverageDropExceeded {
			errorSummary = fmt.Sprintf("coverage drop gate refused commit: valid %d vs baseline %d", plan.ValidCount, plan.BaseValidCount)
		}
	}
	commit := &model.PriceSyncCommit{
		SourceId:               source.Id,
		ExpectedConfigRevision: expectedConfigRevision,
		ExpectedBaseRunId:      expectedBaseRunId,
		Run:                    buildRunFromPlan(plan, errorSummary),
		Items:                  buildCommitItems(plan),
	}
	run, err := model.CommitPriceSync(commit)
	if err != nil {
		return nil, err
	}
	// A run that persisted snapshots changed the current cost of the models it
	// made valid, so those are the models whose cost inversion is re-checked.
	LogCatalogAlertsAfterWrite(ctx, source.Id, committedCanonicalModels(plan, run.Status))
	return &dto.UpstreamPriceSyncResponse{
		RunId:              run.Id,
		Status:             run.Status,
		DiscoveredCount:    run.DiscoveredCount,
		ValidCount:         run.ValidCount,
		UnsupportedCount:   run.UnsupportedCount,
		RejectedCount:      run.RejectedCount,
		MissingCount:       run.MissingCount,
		NewSnapshotCount:   run.NewSnapshotCount,
		IdempotentHitCount: run.IdempotentHitCount,
		ErrorSummary:       run.ErrorSummary,
		PriceJumpCount:     len(plan.PriceJumps),
	}, nil
}

// committedCanonicalModels lists, in sorted order, the canonical model names a
// committed run made current. A run that persisted no snapshots returns none.
func committedCanonicalModels(plan *syncPlan, runStatus string) []string {
	if runStatus != model.PriceSyncRunStatusSucceeded && runStatus != model.PriceSyncRunStatusPartial {
		return nil
	}
	seen := make(map[string]bool, len(plan.Items))
	names := make([]string, 0, len(plan.Items))
	for _, item := range plan.Items {
		if item.Status != model.PriceSyncItemStatusValid || item.Price == nil {
			continue
		}
		name := item.Price.CanonicalModelName
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildRunFromPlan(plan *syncPlan, errorSummary string) model.PriceSyncRun {
	coverageDropExceeded := plan.CoverageDropExceeded
	return model.PriceSyncRun{
		Status:     plan.Status,
		AdapterKey: plan.AdapterKey,
		StartedAt:  plan.StartedAt,
		DurationMs: plan.DurationMs,
		HttpStatus: plan.Meta.HTTPStatus,
		// The revision the fetch actually ran under, not the row value at
		// transaction time.
		SourceConfigRevision: plan.Source.ConfigRevision,
		ResponseBytes:        plan.Meta.ResponseBytes,
		SourceConfigDigest:   sourceConfigDigest(plan.Source),
		SourceRevision:       common.TruncateUTF8(plan.Meta.SourceRevision, MaxSourceRevisionLength),
		DiscoveredCount:      plan.Meta.Discovered,
		ValidCount:           plan.ValidCount,
		UnsupportedCount:     plan.UnsupportedCount,
		RejectedCount:        plan.RejectedCount,
		MissingCount:         len(plan.Missing),
		ErrorSummary:         errorSummary,
		CoverageDropExceeded: &coverageDropExceeded,
		PriceJumpSummary:     encodePriceJumpSummary(plan.PriceJumpThreshold, plan.PriceJumps),
	}
}

func buildCommitItems(plan *syncPlan) []model.PriceSyncCommitItem {
	items := make([]model.PriceSyncCommitItem, 0, len(plan.Items)+len(plan.Missing))
	for _, item := range plan.Items {
		commitItem := model.PriceSyncCommitItem{
			SourceModelName: item.SourceModelName,
			Status:          item.Status,
			WarningCode:     item.WarningCode,
		}
		if item.Status == model.PriceSyncItemStatusValid && item.Price != nil {
			commitItem.Snapshot = snapshotFromNormalizedPrice(plan.Source.Id, item.Price)
		}
		items = append(items, commitItem)
	}
	for _, missingModel := range plan.Missing {
		items = append(items, model.PriceSyncCommitItem{
			SourceModelName: missingModel,
			Status:          model.PriceSyncItemStatusMissing,
		})
	}
	return items
}

func snapshotFromNormalizedPrice(sourceId int, price *NormalizedPrice) *model.PriceSnapshot {
	metadataJson := ""
	if len(price.Metadata) > 0 {
		if data, err := common.Marshal(price.Metadata); err == nil {
			metadataJson = string(data)
		}
	}
	return &model.PriceSnapshot{
		SourceId:           sourceId,
		SourceModelName:    price.SourceModelName,
		CanonicalModelName: price.CanonicalModelName,
		Role:               string(price.Role),
		Scope:              string(price.Scope),
		Provider:           price.Provider,
		MappingStatus:      price.MappingStatus,
		Currency:           price.Currency,
		FormulaKind:        price.FormulaKind,
		PriceExpr:          price.PriceExpr,
		ExprVersion:        price.ExprVersion,
		EffectiveAt:        price.EffectiveAt,
		Fingerprint:        price.Fingerprint,
		FingerprintVersion: FingerprintVersion,
		Metadata:           metadataJson,
	}
}

// sourceConfigDigest hashes the non-secret source configuration for run
// evidence (spec §7.3). Settings are already non-secret by contract.
func sourceConfigDigest(config SourceConfig) string {
	payload := struct {
		AdapterKey string         `json:"adapter_key"`
		Role       string         `json:"role"`
		Scope      string         `json:"scope"`
		ChannelId  *int           `json:"channel_id"`
		Settings   SourceSettings `json:"settings"`
	}{
		AdapterKey: config.AdapterKey,
		Role:       string(config.Role),
		Scope:      string(config.Scope),
		ChannelId:  config.ChannelId,
		Settings:   config.Settings,
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// priceSourceConfigChanged reports whether the source configuration a run
// actually executed under still matches the source's current configuration
// (spec §7.3, §9.2). Only the price-semantic fields participate, because
// sourceConfigDigest covers adapter_key, role, scope, channel_id, and the
// non-secret settings and nothing else: toggling enabled, schedule_enabled, or
// schedule_interval_seconds does not invalidate an observation, while pointing
// the source at a different channel or adapter does.
//
// A run without a digest is treated as changed. The catalog and comparison
// paths fail closed on the answer — a cost whose configuration moved is never
// reported as a confirmed current cost — so "unknown" must not read as "still
// valid".
func priceSourceConfigChanged(config SourceConfig, run *model.PriceSyncRun) bool {
	if run == nil {
		return true
	}
	return run.SourceConfigDigest != sourceConfigDigest(config)
}

// checkSupplierChannelForCommit is the pre-transaction fast fail for commit:
// a supplier_cost source must reference an existing, enabled channel. The
// same check is repeated authoritatively inside the commit transaction.
func checkSupplierChannelForCommit(source *model.PriceSource) error {
	if PriceRole(source.Role) != RoleSupplierCost {
		return nil
	}
	if source.ChannelId == nil {
		return model.ErrPriceSourceOrphaned
	}
	channel, err := model.GetChannelById(*source.ChannelId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ErrPriceSourceOrphaned
		}
		return err
	}
	if channel.Status != common.ChannelStatusEnabled {
		return model.ErrPriceSourceChannelDisabled
	}
	return nil
}

// IsPriceSourceOrphaned reports whether a supplier_cost source references a
// channel that no longer exists (spec §7.1). Reference sources are never
// orphaned.
func IsPriceSourceOrphaned(source *model.PriceSource) (bool, error) {
	if PriceRole(source.Role) != RoleSupplierCost || source.ChannelId == nil {
		return false, nil
	}
	_, err := model.GetChannelById(*source.ChannelId, false)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	return false, err
}
