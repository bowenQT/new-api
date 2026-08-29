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
 * Types mirroring the upstream price catalog DTOs
 * (`dto/upstream_price.go`, spec §10). Field names follow the backend JSON
 * exactly; do not rename them here.
 */

export interface ApiEnvelope<T> {
  success: boolean
  message?: string
  data?: T
}

export type PriceRole =
  | 'supplier_cost'
  | 'vendor_list'
  | 'curated_reference'
  | (string & {})

export type PriceScope =
  | 'public'
  | 'account'
  | 'contract'
  | 'regional'
  | 'service_tier'
  | 'unknown'
  | (string & {})

/** `dto.UpstreamPriceAdapterView` */
export interface PriceAdapterView {
  key: string
  allowed_roles: PriceRole[]
  allowed_scopes: PriceScope[]
  requires_channel: boolean
  endpoint: string
}

/**
 * `dto.UpstreamPriceSourceView`. `coverage_count`, `missing_count`, `stale` and
 * `last_success_finished_at` are the aggregates of the source's last successful
 * run (spec §8.3); they are always present, so a list view never has to derive
 * freshness from the current-price projection.
 */
export interface PriceSourceView {
  id: number
  name: string
  adapter_key: string
  role: PriceRole
  scope: PriceScope
  channel_id?: number | null
  enabled: boolean
  schedule_enabled: boolean
  schedule_interval_seconds: number
  settings: string
  config_revision: number
  last_success_run_id?: number | null
  last_success_at?: number | null
  last_success_finished_at?: number | null
  last_error_at?: number | null
  last_error_summary: string
  coverage_count: number
  missing_count: number
  stale: boolean
  orphaned: boolean
  created_time: number
  updated_time: number
}

/** `dto.UpstreamPriceSourceRequest` */
export interface PriceSourceRequest {
  name: string
  adapter_key: string
  role: PriceRole
  scope: PriceScope
  channel_id: number | null
  enabled: boolean
  schedule_enabled: boolean
  schedule_interval_seconds: number
  settings: string
}

/** `dto.UpstreamPricePreviewItem` */
export interface PricePreviewItem {
  source_model_name: string
  canonical_model_name?: string
  mapping_status?: string
  status: string
  change?: string
  warning_code?: string
  currency?: string
  formula_kind?: string
  price_expr?: string
  fingerprint?: string
  varies_by_provider?: boolean
  metadata?: Record<string, string>
}

/** `dto.UpstreamPricePreviewResponse` */
export interface PricePreviewResponse {
  source_id: number
  base_run_id?: number | null
  projected_run_status: string
  discovered_count: number
  valid_count: number
  unsupported_count: number
  rejected_count: number
  missing_count: number
  new_count: number
  changed_count: number
  unchanged_count: number
  coverage_drop_exceeded: boolean
  items: PricePreviewItem[]
  missing: string[]
  preview_token: string
  expires_at: number
}

/** `dto.UpstreamPriceSyncResponse` */
export interface PriceSyncResponse {
  run_id: number
  status: string
  discovered_count: number
  valid_count: number
  unsupported_count: number
  rejected_count: number
  missing_count: number
  new_snapshot_count: number
  idempotent_hit_count: number
  error_summary?: string
}

/**
 * `dto.UpstreamPriceAlertParams`. Only the fields documented for the alert's
 * own code are sent, so every field is optional here and a renderer must check
 * the ones it reads before using them.
 */
export interface PriceAlertParams {
  failure_count?: number
  run_id?: number
  age_seconds?: number
  threshold_seconds?: number
  previous_valid_count?: number
  valid_count?: number
  drop_threshold?: number
  /**
   * The coverage gate actually refused the run (it committed nothing), as
   * opposed to a drop observed between two runs that both committed.
   */
  gate_refused?: boolean
  group?: string
}

/**
 * `dto.UpstreamPriceAlert`. `detail` is the backend's English sentence; it is
 * only rendered when `params` is absent, because the localized message is built
 * from `params` (see `lib/alert-detail.ts`).
 */
export interface PriceAlert {
  code: string
  source_id?: number
  source_name?: string
  canonical_model_name?: string
  detail: string
  params?: PriceAlertParams
}

/** `dto.UpstreamCurrentPriceEntry` */
export interface CurrentPriceEntry {
  source_id: number
  source_name: string
  role: PriceRole
  scope: PriceScope
  channel_id?: number
  source_model_name: string
  canonical_model_name?: string
  mapping_status?: string
  provider?: string
  currency?: string
  formula_kind?: string
  price_expr?: string
  expr_version?: string
  effective_at?: number
  fetched_at?: number
  last_seen_at?: number
  fingerprint?: string
  snapshot_id?: number
  run_id: number
  run_finished_at?: number
  status: string
  warning_code?: string
  stale: boolean
  orphaned: boolean
  varies_by_provider: boolean
  canonical_conflict: boolean
  /**
   * The run behind this observation executed under a different source
   * configuration than the source carries now (spec §7.3, §9.2). The price is
   * still shown as evidence, but it is not a confirmed current cost until the
   * source syncs again.
   */
  source_config_changed: boolean
  metadata?: Record<string, string>
}

/** `dto.UpstreamCurrentPriceResponse` */
export interface CurrentPriceResponse {
  generated_at: number
  entries: CurrentPriceEntry[] | null
  alerts: PriceAlert[] | null
}

/** `dto.UpstreamPriceUsageVector` */
export interface PriceUsageVector {
  p: number
  c: number
  cr: number
  cc: number
}

/** `dto.UpstreamPriceCompareRequest` */
export interface PriceCompareRequest {
  models?: string[]
  /**
   * Case-insensitive substring over canonical model names, applied before the
   * response cap. Ignored when `models` names an explicit list (spec §10.3).
   */
  model_filter?: string
  group?: string
  usage?: PriceUsageVector
}

/** `dto.UpstreamPriceCompareSourcePrice` */
export interface PriceCompareSourcePrice {
  source_id: number
  source_name: string
  role: PriceRole
  scope: PriceScope
  channel_id?: number
  source_model_name: string
  formula_kind?: string
  status: string
  warning_code?: string
  amount_usd?: number
  projection: string
  projection_note?: string
  usable_for_margin: boolean
  stale: boolean
  orphaned: boolean
  varies_by_provider: boolean
  canonical_conflict: boolean
  /**
   * The run behind this cost executed under a different source configuration
   * than the source carries now. Such a cost never enters the margin and forces
   * `cost_confirmed: false` until the source syncs again.
   */
  source_config_changed: boolean
  snapshot_id?: number
  run_id?: number
  run_finished_at?: number
  last_seen_at?: number
  /**
   * Snapshot observation age and vendor effective date (spec §8.3). Both are
   * `omitempty`, so an unknown value arrives as `undefined` rather than 0.
   */
  fetched_at?: number
  effective_at?: number
}

/** `dto.UpstreamPriceCompareEntry` */
export interface PriceCompareEntry {
  canonical_model_name: string
  sale_billing_mode: string
  sale_projection: string
  sale_projection_note?: string
  sale_base_usd?: number
  sale_projected_usd?: number
  costs: PriceCompareSourcePrice[] | null
  references: PriceCompareSourcePrice[] | null
  min_cost_usd?: number
  max_cost_usd?: number
  worst_margin_usd?: number
  worst_margin_rate?: number
  cost_confirmed: boolean
  cost_inverted: boolean
  statuses?: string[]
}

/** `dto.UpstreamPriceCompareResponse` */
export interface PriceCompareResponse {
  generated_at: number
  group: string
  group_ratio: number
  group_ratio_configured: boolean
  usage: PriceUsageVector
  total_models: number
  truncated: boolean
  excluded_factors: string[] | null
  entries: PriceCompareEntry[] | null
  alerts: PriceAlert[] | null
}
