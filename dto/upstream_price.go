package dto

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Request/response DTOs for the upstream price catalog
// (docs/downstream/upstream-price-catalog-spec.md §10). These carry only
// shape-level validation; the authoritative business validation (adapter
// membership, role/channel combination rules) lives in service/upstreamprice.

// UpstreamPriceSourceRequest creates or updates a price source. Pointer
// fields distinguish "absent" from explicit zero values.
type UpstreamPriceSourceRequest struct {
	Name                    string  `json:"name"`
	AdapterKey              string  `json:"adapter_key"`
	Role                    string  `json:"role"`
	Scope                   string  `json:"scope"`
	ChannelId               *int    `json:"channel_id"`
	Enabled                 *bool   `json:"enabled"`
	ScheduleEnabled         *bool   `json:"schedule_enabled"`
	ScheduleIntervalSeconds *int64  `json:"schedule_interval_seconds"`
	Settings                *string `json:"settings"`
}

const maxSourceSettingsBytes = 65535

func (r *UpstreamPriceSourceRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("name is required")
	}
	if len(r.Name) > 128 {
		return errors.New("name must be at most 128 characters")
	}
	if strings.TrimSpace(r.AdapterKey) == "" {
		return errors.New("adapter_key is required")
	}
	if len(r.AdapterKey) > 64 {
		return errors.New("adapter_key must be at most 64 characters")
	}
	if strings.TrimSpace(r.Role) == "" {
		return errors.New("role is required")
	}
	if strings.TrimSpace(r.Scope) == "" {
		return errors.New("scope is required")
	}
	if r.ChannelId != nil && *r.ChannelId <= 0 {
		return errors.New("channel_id must be positive")
	}
	if r.ScheduleIntervalSeconds != nil && *r.ScheduleIntervalSeconds < 0 {
		return errors.New("schedule_interval_seconds must not be negative")
	}
	if r.Settings != nil && len(*r.Settings) > maxSourceSettingsBytes {
		return errors.New("settings too large")
	}
	return nil
}

// UpstreamPriceSyncRequest carries the preview token required by commit.
type UpstreamPriceSyncRequest struct {
	PreviewToken string `json:"preview_token"`
}

func (r *UpstreamPriceSyncRequest) Validate() error {
	if strings.TrimSpace(r.PreviewToken) == "" {
		return errors.New("preview_token is required")
	}
	return nil
}

// UpstreamPriceSourceView is the admin-facing projection of a price source.
type UpstreamPriceSourceView struct {
	Id                      int    `json:"id"`
	Name                    string `json:"name"`
	AdapterKey              string `json:"adapter_key"`
	Role                    string `json:"role"`
	Scope                   string `json:"scope"`
	ChannelId               *int   `json:"channel_id"`
	Enabled                 bool   `json:"enabled"`
	ScheduleEnabled         bool   `json:"schedule_enabled"`
	ScheduleIntervalSeconds int64  `json:"schedule_interval_seconds"`
	Settings                string `json:"settings"`
	ConfigRevision          int64  `json:"config_revision"`
	LastSuccessRunId        *int   `json:"last_success_run_id"`
	LastSuccessAt           *int64 `json:"last_success_at"`
	LastErrorAt             *int64 `json:"last_error_at"`
	LastErrorSummary        string `json:"last_error_summary"`
	Orphaned                bool   `json:"orphaned"`
	CreatedTime             int64  `json:"created_time"`
	UpdatedTime             int64  `json:"updated_time"`
}

// UpstreamPricePreviewItem is one model row of a preview diff.
type UpstreamPricePreviewItem struct {
	SourceModelName    string            `json:"source_model_name"`
	CanonicalModelName string            `json:"canonical_model_name,omitempty"`
	MappingStatus      string            `json:"mapping_status,omitempty"`
	Status             string            `json:"status"`
	Change             string            `json:"change,omitempty"`
	WarningCode        string            `json:"warning_code,omitempty"`
	Currency           string            `json:"currency,omitempty"`
	FormulaKind        string            `json:"formula_kind,omitempty"`
	PriceExpr          string            `json:"price_expr,omitempty"`
	Fingerprint        string            `json:"fingerprint,omitempty"`
	VariesByProvider   bool              `json:"varies_by_provider,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// UpstreamPricePreviewResponse is the preview phase result (spec §8.1): it
// never persists anything and returns the short-lived preview token commit
// requires.
type UpstreamPricePreviewResponse struct {
	SourceId             int                        `json:"source_id"`
	BaseRunId            *int                       `json:"base_run_id"`
	ProjectedRunStatus   string                     `json:"projected_run_status"`
	DiscoveredCount      int                        `json:"discovered_count"`
	ValidCount           int                        `json:"valid_count"`
	UnsupportedCount     int                        `json:"unsupported_count"`
	RejectedCount        int                        `json:"rejected_count"`
	MissingCount         int                        `json:"missing_count"`
	NewCount             int                        `json:"new_count"`
	ChangedCount         int                        `json:"changed_count"`
	UnchangedCount       int                        `json:"unchanged_count"`
	CoverageDropExceeded bool                       `json:"coverage_drop_exceeded"`
	Items                []UpstreamPricePreviewItem `json:"items"`
	Missing              []string                   `json:"missing"`
	PreviewToken         string                     `json:"preview_token"`
	ExpiresAt            int64                      `json:"expires_at"`
}

// UpstreamPriceSyncResponse summarizes one committed (or gate-refused) run.
type UpstreamPriceSyncResponse struct {
	RunId              int    `json:"run_id"`
	Status             string `json:"status"`
	DiscoveredCount    int    `json:"discovered_count"`
	ValidCount         int    `json:"valid_count"`
	UnsupportedCount   int    `json:"unsupported_count"`
	RejectedCount      int    `json:"rejected_count"`
	MissingCount       int    `json:"missing_count"`
	NewSnapshotCount   int    `json:"new_snapshot_count"`
	IdempotentHitCount int    `json:"idempotent_hit_count"`
	ErrorSummary       string `json:"error_summary,omitempty"`
}

// UpstreamCurrentPriceEntry is one row of the current price catalog with its
// freshness and integrity labels (spec §8.3, §11.2).
type UpstreamCurrentPriceEntry struct {
	SourceId           int               `json:"source_id"`
	SourceName         string            `json:"source_name"`
	Role               string            `json:"role"`
	Scope              string            `json:"scope"`
	ChannelId          *int              `json:"channel_id,omitempty"`
	SourceModelName    string            `json:"source_model_name"`
	CanonicalModelName string            `json:"canonical_model_name,omitempty"`
	MappingStatus      string            `json:"mapping_status,omitempty"`
	Provider           string            `json:"provider,omitempty"`
	Currency           string            `json:"currency,omitempty"`
	FormulaKind        string            `json:"formula_kind,omitempty"`
	PriceExpr          string            `json:"price_expr,omitempty"`
	ExprVersion        string            `json:"expr_version,omitempty"`
	EffectiveAt        *int64            `json:"effective_at,omitempty"`
	FetchedAt          int64             `json:"fetched_at,omitempty"`
	LastSeenAt         int64             `json:"last_seen_at,omitempty"`
	Fingerprint        string            `json:"fingerprint,omitempty"`
	SnapshotId         int               `json:"snapshot_id,omitempty"`
	RunId              int               `json:"run_id"`
	RunFinishedAt      *int64            `json:"run_finished_at,omitempty"`
	Status             string            `json:"status"`
	WarningCode        string            `json:"warning_code,omitempty"`
	Stale              bool              `json:"stale"`
	Orphaned           bool              `json:"orphaned"`
	VariesByProvider   bool              `json:"varies_by_provider"`
	CanonicalConflict  bool              `json:"canonical_conflict"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// UpstreamCurrentPriceResponse is the current catalog projection.
type UpstreamCurrentPriceResponse struct {
	GeneratedAt int64                       `json:"generated_at"`
	Entries     []UpstreamCurrentPriceEntry `json:"entries"`
	Alerts      []UpstreamPriceAlert        `json:"alerts"`
}

// UpstreamPriceAlert is one catalog health signal (spec §13). Alerts are shown
// in the admin UI and written to the backend log; they are never routed to a
// notification channel.
type UpstreamPriceAlert struct {
	Code               string `json:"code"`
	SourceId           int    `json:"source_id,omitempty"`
	SourceName         string `json:"source_name,omitempty"`
	CanonicalModelName string `json:"canonical_model_name,omitempty"`
	Detail             string `json:"detail"`
}

// UpstreamPriceUsageVector is the limited-dimension usage basis of one price
// comparison (spec §9.3). Pointer fields distinguish an absent dimension from
// an explicit zero.
type UpstreamPriceUsageVector struct {
	PromptTokens        *float64 `json:"p"`
	CompletionTokens    *float64 `json:"c"`
	CacheReadTokens     *float64 `json:"cr"`
	CacheCreationTokens *float64 `json:"cc"`
}

// UpstreamPriceAppliedUsage echoes the usage vector actually used, so a caller
// always sees the basis of the returned amounts.
type UpstreamPriceAppliedUsage struct {
	PromptTokens        float64 `json:"p"`
	CompletionTokens    float64 `json:"c"`
	CacheReadTokens     float64 `json:"cr"`
	CacheCreationTokens float64 `json:"cc"`
}

// MaxCompareModelsRequested caps how many model names one comparison request
// may name explicitly.
const MaxCompareModelsRequested = 500

// MaxCompareUsageTokens bounds every usage dimension. The comparison is a
// display-only projection, but the bound keeps hostile input from producing
// meaningless or non-finite amounts.
const MaxCompareUsageTokens = float64(1e9)

// UpstreamPriceCompareRequest asks for a cost / sale-price / margin comparison
// (spec §9.2, §10.3). An empty Models list compares every canonical model the
// catalog currently knows.
type UpstreamPriceCompareRequest struct {
	Models []string                  `json:"models"`
	Group  string                    `json:"group"`
	Usage  *UpstreamPriceUsageVector `json:"usage"`
}

func (r *UpstreamPriceCompareRequest) Validate() error {
	if len(r.Models) > MaxCompareModelsRequested {
		return fmt.Errorf("at most %d models may be compared per request", MaxCompareModelsRequested)
	}
	for _, name := range r.Models {
		if strings.TrimSpace(name) == "" {
			return errors.New("model names must not be empty")
		}
		if len(name) > 255 {
			return errors.New("model name must be at most 255 characters")
		}
	}
	if len(r.Group) > 64 {
		return errors.New("group must be at most 64 characters")
	}
	if r.Usage == nil {
		return nil
	}
	for name, value := range map[string]*float64{
		"p":  r.Usage.PromptTokens,
		"c":  r.Usage.CompletionTokens,
		"cr": r.Usage.CacheReadTokens,
		"cc": r.Usage.CacheCreationTokens,
	} {
		if value == nil {
			continue
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			return fmt.Errorf("usage dimension %q must be a finite number", name)
		}
		if *value < 0 || *value > MaxCompareUsageTokens {
			return fmt.Errorf("usage dimension %q must be within [0, %g]", name, MaxCompareUsageTokens)
		}
	}
	return nil
}

// UpstreamPriceCompareSourcePrice is one source's projected price for a model.
type UpstreamPriceCompareSourcePrice struct {
	SourceId          int      `json:"source_id"`
	SourceName        string   `json:"source_name"`
	Role              string   `json:"role"`
	Scope             string   `json:"scope"`
	ChannelId         *int     `json:"channel_id,omitempty"`
	SourceModelName   string   `json:"source_model_name"`
	FormulaKind       string   `json:"formula_kind,omitempty"`
	Status            string   `json:"status"`
	WarningCode       string   `json:"warning_code,omitempty"`
	AmountUSD         *float64 `json:"amount_usd,omitempty"`
	Projection        string   `json:"projection"`
	ProjectionNote    string   `json:"projection_note,omitempty"`
	UsableForMargin   bool     `json:"usable_for_margin"`
	Stale             bool     `json:"stale"`
	Orphaned          bool     `json:"orphaned"`
	VariesByProvider  bool     `json:"varies_by_provider"`
	CanonicalConflict bool     `json:"canonical_conflict"`
	SnapshotId        int      `json:"snapshot_id,omitempty"`
	RunId             int      `json:"run_id,omitempty"`
	RunFinishedAt     *int64   `json:"run_finished_at,omitempty"`
	LastSeenAt        int64    `json:"last_seen_at,omitempty"`
}

// UpstreamPriceCompareEntry is one model's comparison row (spec §11.2).
// Reference prices are reported separately and never enter the margin.
type UpstreamPriceCompareEntry struct {
	CanonicalModelName string                            `json:"canonical_model_name"`
	SaleBillingMode    string                            `json:"sale_billing_mode"`
	SaleProjection     string                            `json:"sale_projection"`
	SaleProjectionNote string                            `json:"sale_projection_note,omitempty"`
	SaleBaseUSD        *float64                          `json:"sale_base_usd,omitempty"`
	SaleProjectedUSD   *float64                          `json:"sale_projected_usd,omitempty"`
	Costs              []UpstreamPriceCompareSourcePrice `json:"costs"`
	References         []UpstreamPriceCompareSourcePrice `json:"references"`
	MinCostUSD         *float64                          `json:"min_cost_usd,omitempty"`
	MaxCostUSD         *float64                          `json:"max_cost_usd,omitempty"`
	WorstMarginUSD     *float64                          `json:"worst_margin_usd,omitempty"`
	WorstMarginRate    *float64                          `json:"worst_margin_rate,omitempty"`
	CostConfirmed      bool                              `json:"cost_confirmed"`
	CostInverted       bool                              `json:"cost_inverted"`
	Statuses           []string                          `json:"statuses,omitempty"`
}

// UpstreamPriceCompareResponse is the estimate-only comparison result. It
// writes no state and changes no billing configuration.
type UpstreamPriceCompareResponse struct {
	GeneratedAt          int64                       `json:"generated_at"`
	Group                string                      `json:"group"`
	GroupRatio           float64                     `json:"group_ratio"`
	GroupRatioConfigured bool                        `json:"group_ratio_configured"`
	Usage                UpstreamPriceAppliedUsage   `json:"usage"`
	TotalModels          int                         `json:"total_models"`
	Truncated            bool                        `json:"truncated"`
	ExcludedFactors      []string                    `json:"excluded_factors"`
	Entries              []UpstreamPriceCompareEntry `json:"entries"`
	Alerts               []UpstreamPriceAlert        `json:"alerts"`
}
