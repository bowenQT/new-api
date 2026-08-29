// Package upstreamprice implements the vendor-agnostic upstream price catalog
// (docs/downstream/upstream-price-catalog-spec.md). It only records price
// observations; it never reads or writes any sale-pricing configuration.
package upstreamprice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// PriceRole classifies what a price observation represents (spec §4.1).
type PriceRole string

const (
	RoleSupplierCost     PriceRole = "supplier_cost"
	RoleVendorList       PriceRole = "vendor_list"
	RoleCuratedReference PriceRole = "curated_reference"
)

// PriceScope states who can obtain the observed price (spec §4.2).
type PriceScope string

const (
	ScopePublic      PriceScope = "public"
	ScopeAccount     PriceScope = "account"
	ScopeContract    PriceScope = "contract"
	ScopeRegional    PriceScope = "regional"
	ScopeServiceTier PriceScope = "service_tier"
	ScopeUnknown     PriceScope = "unknown"
)

// FormulaKind values supported in Phase 1 (spec §6.1).
const (
	FormulaKindTokenExprV1 = "token_expr_v1" // coefficients are USD / 1M tokens
	FormulaKindPerCallV1   = "per_call_v1"   // expression result is USD per request
)

func IsValidPriceRole(role PriceRole) bool {
	switch role {
	case RoleSupplierCost, RoleVendorList, RoleCuratedReference:
		return true
	}
	return false
}

func IsValidPriceScope(scope PriceScope) bool {
	switch scope {
	case ScopePublic, ScopeAccount, ScopeContract, ScopeRegional, ScopeServiceTier, ScopeUnknown:
		return true
	}
	return false
}

// SourceSettings is the non-secret JSON configuration stored on a PriceSource.
// It must never contain credentials, endpoints, schemes, or hosts (spec §12).
type SourceSettings struct {
	// ModelMappings maps source_model_name to an explicit canonical model
	// name, overriding the default provider-prefix stripping rule (spec §7.5).
	ModelMappings map[string]string `json:"model_mappings,omitempty"`
	// CoverageDropThreshold overrides the default commit gate for coverage
	// drops, expressed as a fraction in (0, 1] (spec §8.2).
	CoverageDropThreshold *float64 `json:"coverage_drop_threshold,omitempty"`
	// StaleThresholdSeconds overrides the staleness threshold for manual
	// sources (spec §8.3).
	StaleThresholdSeconds *int64 `json:"stale_threshold_seconds,omitempty"`
	// PriceJumpThreshold overrides the per-source price-movement alert
	// threshold, expressed as a change rate in (0, 1000] (spec §13). Unlike a
	// coverage drop, a price change rate is not a fraction of a whole: a ten
	// fold increase is 9.0, so the range deliberately extends past 1.
	PriceJumpThreshold *float64 `json:"price_jump_threshold,omitempty"`
}

// SourceConfig is the adapter-facing view of a PriceSource.
type SourceConfig struct {
	Id             int
	Name           string
	AdapterKey     string
	Role           PriceRole
	Scope          PriceScope
	ChannelId      *int
	ConfigRevision int64
	Settings       SourceSettings
}

// allowedSourceSettingsFields is the strict whitelist of settings keys. Any
// other key — in particular anything credential- or endpoint-shaped — is
// refused outright (spec §12: settings must never carry secrets, schemes, or
// hosts).
var allowedSourceSettingsFields = map[string]bool{
	"model_mappings":          true,
	"coverage_drop_threshold": true,
	"stale_threshold_seconds": true,
	"price_jump_threshold":    true,
}

// ParseSourceSettings strictly parses a settings JSON string: unknown fields
// are rejected instead of ignored.
func ParseSourceSettings(jsonStr string) (SourceSettings, error) {
	settings := SourceSettings{}
	if strings.TrimSpace(jsonStr) == "" {
		return settings, nil
	}
	var raw map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(jsonStr, &raw); err != nil {
		return settings, fmt.Errorf("invalid price source settings JSON: %w", err)
	}
	for key := range raw {
		if !allowedSourceSettingsFields[key] {
			return settings, fmt.Errorf("settings field %q is not allowed", key)
		}
	}
	if err := common.UnmarshalJsonStr(jsonStr, &settings); err != nil {
		return settings, fmt.Errorf("invalid price source settings JSON: %w", err)
	}
	return settings, nil
}

// MaxSourceSettingsBytes caps the canonical serialized settings. It matches
// the request DTO limit; the canonical form is re-checked because JSON HTML
// escaping can inflate the client's raw input.
const MaxSourceSettingsBytes = 65535

// CanonicalSourceSettingsJSON re-serializes validated settings so the stored
// value is the canonical form of the parsed structure, never the client's raw
// JSON. Empty input stays empty; a canonical form over MaxSourceSettingsBytes
// is refused.
func CanonicalSourceSettingsJSON(jsonStr string) (string, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return "", nil
	}
	settings, err := ParseSourceSettings(jsonStr)
	if err != nil {
		return "", err
	}
	data, err := common.Marshal(settings)
	if err != nil {
		return "", err
	}
	if len(data) > MaxSourceSettingsBytes {
		return "", fmt.Errorf("canonical settings exceed %d bytes", MaxSourceSettingsBytes)
	}
	return string(data), nil
}

// SourceConfigFromModel converts a persisted PriceSource, strictly parsing
// its settings JSON.
func SourceConfigFromModel(source *model.PriceSource) (SourceConfig, error) {
	config := SourceConfig{
		Id:             source.Id,
		Name:           source.Name,
		AdapterKey:     source.AdapterKey,
		Role:           PriceRole(source.Role),
		Scope:          PriceScope(source.Scope),
		ChannelId:      source.ChannelId,
		ConfigRevision: source.ConfigRevision,
	}
	settings, err := ParseSourceSettings(source.Settings)
	if err != nil {
		return SourceConfig{}, err
	}
	config.Settings = settings
	return config, nil
}

// Observation is the vendor-agnostic intermediate price object (spec §6.1).
// Role and Scope may be left empty; the authoritative resolution algorithm in
// ResolveObservationRoleScope then falls back to the source declaration.
type Observation struct {
	Role               PriceRole
	Scope              PriceScope
	Provider           string
	SourceModelName    string
	CanonicalModelName string
	Currency           string
	FormulaKind        string
	PriceExpr          string
	EffectiveAt        *time.Time
	Metadata           map[string]string
}

// SkippedModel reports a source model the adapter discovered but could not
// normalize (unsupported) or had to refuse (rejected), with a machine-readable
// warning code (spec §8.2).
type SkippedModel struct {
	SourceModelName string
	Status          string // model.PriceSyncItemStatusUnsupported or ...Rejected
	WarningCode     string
}

// FetchMeta carries source-level fetch evidence, persisted onto the
// PriceSyncRun (spec §7.3).
type FetchMeta struct {
	SourceRevision string // ETag, version number, or source update time
	HTTPStatus     int
	ResponseBytes  int64 // decompressed size actually read
	Discovered     int   // total models the source returned
	Skipped        []SkippedModel
	// NotModified marks a conditional fetch the upstream answered with 304:
	// the representation is unchanged, no body was read or parsed, and no
	// observations are returned. The sync engine then replays the baseline run
	// instead of normalizing anything. It is only ever honoured for a request
	// the engine itself made conditional.
	NotModified bool
}

// Adapter is the uniform per-vendor contract (spec §6.1). Every adapter must
// declare its allowed role/scope sets; observations outside those sets are
// rejected, never silently overridden.
type Adapter interface {
	Key() string
	// Supports answers adapter-identity questions only: whether this adapter
	// is the one serving the source. Whether a role may carry a channel is
	// decided exclusively by ValidatePriceSourceForWrite, so the rule has a
	// single authority and an adapter cannot pre-empt it with a different
	// verdict.
	Supports(source SourceConfig) bool
	Fetch(ctx context.Context, source SourceConfig) ([]Observation, FetchMeta, error)
	AllowedRoles() []PriceRole
	AllowedScopes() []PriceScope
}

// EndpointReporter is the optional capability of adapters that fetch from a
// pinned public URL. Endpoint is provenance shown to admins, never a
// configurable value, and must never carry credentials or query parameters
// derived from a source (spec §12). An adapter without a fixed URL — a
// database-backed or manually fed one, say — simply does not implement this
// interface; it does not implement it and return an empty string.
type EndpointReporter interface {
	Endpoint() string
}

// ConditionalFetcher is the optional capability of adapters whose upstream
// supports HTTP conditional requests. The sync engine calls FetchConditional
// instead of Fetch when it holds the source revision (ETag) of the baseline
// run and that baseline is still an exact statement of what a full fetch would
// produce today; ifNoneMatch is that revision.
//
// An implementation must set FetchMeta.NotModified only for an actual 304
// answer to the conditional request it sent, and must not read, size-check, or
// parse a body in that case. If the revision is unusable as a validator it
// must fall back to an unconditional request rather than put a malformed
// header on the wire. An adapter that is not HTTP-backed, or whose upstream
// publishes no validator, simply does not implement this interface.
type ConditionalFetcher interface {
	FetchConditional(ctx context.Context, source SourceConfig, ifNoneMatch string) ([]Observation, FetchMeta, error)
}

// ResolveObservationRoleScope implements the single authoritative role/scope
// algorithm from spec §6.1: an empty observation value takes the source's
// default declaration; a non-empty value that differs from the source
// declaration, or a resolved value outside the adapter's allowed set, rejects
// the observation.
func ResolveObservationRoleScope(obs Observation, source SourceConfig, adapter Adapter) (PriceRole, PriceScope, error) {
	role := obs.Role
	if role == "" {
		role = source.Role
	} else if role != source.Role {
		return "", "", fmt.Errorf("observation role %q exceeds source declaration %q", role, source.Role)
	}
	if !containsRole(adapter.AllowedRoles(), role) {
		return "", "", fmt.Errorf("role %q not allowed by adapter %q", role, adapter.Key())
	}

	scope := obs.Scope
	if scope == "" {
		scope = source.Scope
	} else if scope != source.Scope {
		return "", "", fmt.Errorf("observation scope %q exceeds source declaration %q", scope, source.Scope)
	}
	if !containsScope(adapter.AllowedScopes(), scope) {
		return "", "", fmt.Errorf("scope %q not allowed by adapter %q", scope, adapter.Key())
	}
	return role, scope, nil
}

func containsRole(roles []PriceRole, role PriceRole) bool {
	for _, candidate := range roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func containsScope(scopes []PriceScope, scope PriceScope) bool {
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}
