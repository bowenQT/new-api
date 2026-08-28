/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Shortest per-source scheduling interval, mirroring
 * `upstreamprice.MinScheduleIntervalSeconds` (spec §8.4). The server enforces
 * this; the form validates it too so the admin sees the rule before submitting.
 */
export const MIN_SCHEDULE_INTERVAL_SECONDS = 6 * 60 * 60

/** Default interval offered for a newly scheduled source. */
export const DEFAULT_SCHEDULE_INTERVAL_SECONDS = 24 * 60 * 60

/** `upstreamprice.MaxCompareModels` — the comparison response cap. */
export const MAX_COMPARE_MODELS = 500

/** `dto.MaxCompareModelFilterLength` — bound of the server-side model filter. */
export const MAX_COMPARE_MODEL_FILTER_LENGTH = 255

/** `dto.MaxCompareUsageTokens` — per-dimension bound of the usage vector. */
export const MAX_COMPARE_USAGE_TOKENS = 1e9

/** Default usage vector of the comparison, matching the backend defaults. */
export const DEFAULT_COMPARE_USAGE = {
  p: 1_000_000,
  c: 1_000_000,
  cr: 0,
  cc: 0,
}

export const DEFAULT_COMPARE_GROUP = 'default'

/**
 * The role that binds a source to a channel (spec §4.1). Only a
 * `supplier_cost` source may carry a channel id, and it must carry one.
 */
export const PRICE_ROLE_SUPPLIER_COST = 'supplier_cost'

/**
 * Display names of the registered adapters. The adapter contract itself
 * (roles, scopes, channel requirement, endpoint) comes from
 * `GET /api/upstream-price-sources/adapters`; only the product name is local,
 * because it is a brand and is rendered untranslated. An unknown key falls
 * back to the key.
 */
export const ADAPTER_LABELS: Record<string, string> = {
  vercel_gateway: 'Vercel AI Gateway',
  models_dev: 'models.dev',
  basellm: 'basellm llm-metadata',
}

/** i18n source keys for `PriceRole` (spec §4.1). */
export const ROLE_LABEL_KEYS: Record<string, string> = {
  supplier_cost: 'Supplier cost',
  vendor_list: 'Vendor list price',
  curated_reference: 'Third-party reference price',
}

/** i18n source keys for `PriceScope` (spec §4.2). */
export const SCOPE_LABEL_KEYS: Record<string, string> = {
  public: 'Public',
  account: 'Account',
  contract: 'Contract',
  regional: 'Regional',
  service_tier: 'Service tier',
  unknown: 'Unknown',
}

/**
 * `upstreamprice.AlertSourceConfigChanged`. The alert is the per-source view of
 * the same predicate the catalog entries carry as `source_config_changed`, so
 * the source list labels a source from it without joining the projection.
 */
export const ALERT_SOURCE_CONFIG_CHANGED = 'source_config_changed'

/** Catalog health alert codes (`service/upstreamprice/alerts.go`, spec §13). */
export const ALERT_LABEL_KEYS: Record<string, string> = {
  source_consecutive_failures: 'Source failed repeatedly',
  source_stale: 'Cost source is stale',
  coverage_drop: 'Model coverage dropped',
  cost_inversion: 'Cost exceeds the projected sale price',
  [ALERT_SOURCE_CONFIG_CHANGED]: 'Source configuration changed',
}

/** Run item / catalog entry status codes. */
export const ENTRY_STATUS_LABEL_KEYS: Record<string, string> = {
  current: 'Current',
  valid: 'Valid',
  missing: 'Missing upstream',
  unsupported: 'Unsupported',
  rejected: 'Rejected',
}

/**
 * Committed run status codes (`model.PriceSyncRunStatus*`). A commit that the
 * coverage gate refused, or that found no valid observation, still returns a
 * success envelope carrying `failed`, so the UI must read this field rather
 * than the envelope.
 */
export const RUN_STATUS_LABEL_KEYS: Record<string, string> = {
  succeeded: 'Succeeded',
  partial: 'Partially committed',
  failed: 'Failed',
}

/** Preview diff change codes. */
export const CHANGE_LABEL_KEYS: Record<string, string> = {
  new: 'New',
  changed: 'Changed',
  unchanged: 'Unchanged',
}

/** Sale billing modes reported by the comparison (spec §9.3). */
export const SALE_BILLING_MODE_LABEL_KEYS: Record<string, string> = {
  ratio: 'Ratio billing',
  per_call: 'Per-call price',
  tiered_expr: 'Tiered expression',
}

/** Projection outcome codes returned per sale price and per source price. */
export const PROJECTION_LABEL_KEYS: Record<string, string> = {
  ok: 'Projected',
  not_projectable: 'Not projectable',
  not_configured: 'Not configured',
}

/**
 * Factors deliberately excluded from the projection (spec §9.3). The backend
 * returns the codes; these are their labels.
 */
export const EXCLUDED_FACTOR_LABEL_KEYS: Record<string, string> = {
  group_group_ratio: 'Special group-combination ratios',
  request_billing_ratios: 'Per-request billing ratios',
  tool_call_surcharge: 'Tool call surcharge',
  other_ratios: 'Other ratios',
  image_and_audio_standalone_prices: 'Standalone image and audio prices',
  upstream_usage_semantic_differences: 'Upstream usage semantic differences',
}

export const priceCatalogQueryKeys = {
  sources: ['price-catalog', 'sources'] as const,
  adapters: ['price-catalog', 'adapters'] as const,
  currentPrices: ['price-catalog', 'current-prices'] as const,
  compare: (group: string, usageKey: string, modelsKey: string) =>
    ['price-catalog', 'compare', group, usageKey, modelsKey] as const,
  groups: ['price-catalog', 'groups'] as const,
  channels: ['price-catalog', 'channels'] as const,
}
