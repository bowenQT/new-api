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
	"sort"
	"strconv"
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
	dot := -1
	for i := range token {
		if token[i] == '.' {
			dot = i
			break
		}
	}
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
	return truncateUTF8(name, 200) + "#" + hex.EncodeToString(digest[:6]), true
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
	Status               string
	StartedAt            int64
	DurationMs           int64
}

func coverageDropThreshold(config SourceConfig) float64 {
	if config.Settings.CoverageDropThreshold != nil {
		return *config.Settings.CoverageDropThreshold
	}
	return DefaultCoverageDropThreshold
}

// loadBaseState reads the last successful run of a source: its id, per-model
// current fingerprints (valid items only), the set of historical model names
// (all statuses, so missing markers persist across runs), and the valid count.
func loadBaseState(source *model.PriceSource) (*int, map[string]string, map[string]bool, int, error) {
	if source.LastSuccessRunId == nil {
		return nil, map[string]string{}, map[string]bool{}, 0, nil
	}
	baseRunId := *source.LastSuccessRunId
	items, err := model.GetPriceSyncRunItems(baseRunId)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	historical := make(map[string]bool, len(items))
	validSnapshotIds := make([]int, 0, len(items))
	snapshotIdByModel := make(map[int]string, len(items))
	validCount := 0
	for _, item := range items {
		historical[item.SourceModelName] = true
		if item.Status == model.PriceSyncItemStatusValid && item.SnapshotId != nil {
			validCount++
			validSnapshotIds = append(validSnapshotIds, *item.SnapshotId)
			snapshotIdByModel[*item.SnapshotId] = item.SourceModelName
		}
	}
	fingerprints := make(map[string]string, len(validSnapshotIds))
	snapshots, err := model.GetPriceSnapshotsByIds(validSnapshotIds)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	for _, snapshot := range snapshots {
		if modelName, ok := snapshotIdByModel[snapshot.Id]; ok {
			fingerprints[modelName] = snapshot.Fingerprint
		}
	}
	return source.LastSuccessRunId, fingerprints, historical, validCount, nil
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
	baseRunId, baseFingerprints, historical, baseValidCount, err := loadBaseState(source)
	if err != nil {
		return nil, err
	}

	startedAt := common.GetTimestamp()
	fetchStart := time.Now()
	observations, meta, err := adapter.Fetch(ctx, config)
	durationMs := time.Since(fetchStart).Milliseconds()
	if err != nil {
		return &syncPlan{
			Source:     config,
			AdapterKey: adapter.Key(),
			Meta:       meta,
			BaseRunId:  baseRunId,
			StartedAt:  startedAt,
			DurationMs: durationMs,
			Status:     model.PriceSyncRunStatusFailed,
		}, fmt.Errorf("fetch failed for source %d via adapter %s: %w", source.Id, adapter.Key(), err)
	}

	plan := &syncPlan{
		Source:         config,
		AdapterKey:     adapter.Key(),
		Meta:           meta,
		BaseRunId:      baseRunId,
		BaseValidCount: baseValidCount,
		GateThreshold:  coverageDropThreshold(config),
		StartedAt:      startedAt,
		DurationMs:     durationMs,
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
		if baseFingerprint, ok := baseFingerprints[obs.SourceModelName]; !ok {
			item.Change = changeNew
		} else if baseFingerprint != normalized.Fingerprint {
			item.Change = changeChanged
		} else {
			item.Change = changeUnchanged
		}
		plan.Items = append(plan.Items, item)
	}

	sort.Slice(plan.Items, func(i, j int) bool {
		return plan.Items[i].SourceModelName < plan.Items[j].SourceModelName
	})
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

	for name := range historical {
		if !seen[name] {
			plan.Missing = append(plan.Missing, name)
		}
	}
	sort.Strings(plan.Missing)

	if baseValidCount > 0 {
		drop := 1 - float64(plan.ValidCount)/float64(baseValidCount)
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
	return plan, nil
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
		return nil, recordSyncPlanFailure(source, plan, planErr)
	}
	if !intPtrEqual(claim.BaseRunId, plan.BaseRunId) {
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

	return commitSyncPlan(source, plan, claim.ConfigRevision, claim.BaseRunId)
}

// SyncPriceSourceWithoutPreview is the unattended commit path used by the
// scheduled task (spec §8.4). There is no human preview, but the fetch,
// normalization, validation, and coverage/change gates are identical, and the
// same CAS-guarded commit transaction writes the result. It never touches sale
// pricing.
func SyncPriceSourceWithoutPreview(ctx context.Context, source *model.PriceSource) (*dto.UpstreamPriceSyncResponse, error) {
	if err := checkSourceRunnableForCommit(source); err != nil {
		return nil, err
	}
	plan, planErr := buildSyncPlan(ctx, source)
	if planErr != nil {
		return nil, recordSyncPlanFailure(source, plan, planErr)
	}
	return commitSyncPlan(source, plan, plan.Source.ConfigRevision, plan.BaseRunId)
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
func recordSyncPlanFailure(source *model.PriceSource, plan *syncPlan, planErr error) error {
	if plan == nil {
		return planErr
	}
	recordRun := buildRunFromPlan(plan, planErr.Error())
	if _, recordErr := model.RecordFailedPriceSyncRun(source.Id, recordRun); recordErr != nil {
		return fmt.Errorf("fetch failed (%v) and failed run could not be recorded: %w", planErr, recordErr)
	}
	return planErr
}

// commitSyncPlan writes one prepared plan through the CAS-guarded commit
// transaction and summarizes the resulting run.
func commitSyncPlan(source *model.PriceSource, plan *syncPlan, expectedConfigRevision int64, expectedBaseRunId *int) (*dto.UpstreamPriceSyncResponse, error) {
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
	}, nil
}

func buildRunFromPlan(plan *syncPlan, errorSummary string) model.PriceSyncRun {
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
		SourceRevision:       truncateUTF8(plan.Meta.SourceRevision, MaxSourceRevisionLength),
		DiscoveredCount:      plan.Meta.Discovered,
		ValidCount:           plan.ValidCount,
		UnsupportedCount:     plan.UnsupportedCount,
		RejectedCount:        plan.RejectedCount,
		MissingCount:         len(plan.Missing),
		ErrorSummary:         errorSummary,
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

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
