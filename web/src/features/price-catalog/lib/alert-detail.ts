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
import type { TFunction } from 'i18next'

import {
  ALERT_COST_INVERSION,
  ALERT_COVERAGE_DROP,
  ALERT_PRICE_JUMP,
  ALERT_SOURCE_CONFIG_CHANGED,
  ALERT_SOURCE_CONSECUTIVE_FAILURES,
  ALERT_SOURCE_STALE,
  PRICE_JUMP_DIMENSION_EXPR_UNVERIFIED,
  PRICE_JUMP_DIMENSION_LABEL_KEYS,
} from '../constants'
import type { PriceAlert, PriceAlertParams } from '../types'
import { formatPercentRate } from './compare-format'

/** Alert durations are reported in seconds; the admin reads them in hours. */
function hoursOf(seconds: number): string {
  return (seconds / 3600).toFixed(1)
}

/**
 * Catalog amounts are USD per million tokens for a token price and USD per
 * request for a per-call one, and both can be small enough that two decimals
 * round them to zero, so the amount is shown with enough significant digits to
 * stay a number the admin can compare.
 */
function usdOf(amount: number): string {
  return `$${Number(amount.toPrecision(6))}`
}

/**
 * Localized detail of one price movement (spec §13). The dimension that could
 * not be measured gets its own wording: it states no rate, so rendering it in
 * the sentence the measured movements use would imply a magnitude that was
 * never established.
 */
function priceJumpDetail(t: TFunction, params: PriceAlertParams): string {
  const model = params.source_model_name ?? ''
  if (params.dimension === PRICE_JUMP_DIMENSION_EXPR_UNVERIFIED) {
    return t(
      'The price of {{model}} changed in a way this check could not measure. Review it manually.',
      { model }
    )
  }
  const dimension = t(
    PRICE_JUMP_DIMENSION_LABEL_KEYS[params.dimension ?? ''] ??
      params.dimension ??
      ''
  )
  if (params.from_zero === true && params.current_usd !== undefined) {
    return t(
      'The {{dimension}} price of {{model}} moved from zero to {{current}} at {{context}}.',
      {
        dimension,
        model,
        current: usdOf(params.current_usd),
        context: params.probe_context ?? '',
      }
    )
  }
  if (
    params.previous_usd === undefined ||
    params.current_usd === undefined ||
    params.change_rate === undefined
  ) {
    return ''
  }
  return t(
    'The {{dimension}} price of {{model}} moved from {{previous}} to {{current}} ({{rate}}) at {{context}}.',
    {
      dimension,
      model,
      previous: usdOf(params.previous_usd),
      current: usdOf(params.current_usd),
      rate: formatPercentRate(params.change_rate),
      context: params.probe_context ?? '',
    }
  )
}

/**
 * Localized detail sentence of one catalog health alert (spec §13).
 *
 * The message is built from the alert's structured `params` so it follows the
 * admin's language. The backend also sends an English `detail`, but that is
 * only a fallback for an alert carrying no params — an older backend, or a code
 * this UI does not know yet — because a localized page must not print an
 * English sentence when it can render the same facts itself.
 */
export function priceAlertDetail(t: TFunction, alert: PriceAlert): string {
  const params = alert.params
  if (params === undefined) return alert.detail

  if (
    alert.code === ALERT_SOURCE_CONSECUTIVE_FAILURES &&
    params.failure_count !== undefined
  ) {
    return t('The last {{failures}} sync runs failed.', {
      failures: params.failure_count,
    })
  }

  if (
    alert.code === ALERT_SOURCE_STALE &&
    params.age_seconds !== undefined &&
    params.threshold_seconds !== undefined
  ) {
    return t(
      'The last successful run is {{age}} hours old, past the {{threshold}} hour staleness threshold.',
      {
        age: hoursOf(params.age_seconds),
        threshold: hoursOf(params.threshold_seconds),
      }
    )
  }

  if (
    alert.code === ALERT_SOURCE_CONFIG_CHANGED &&
    params.run_id !== undefined
  ) {
    return t(
      'The source configuration changed after run #{{runId}}; its prices are not confirmed until the next successful sync.',
      { runId: params.run_id }
    )
  }

  if (
    alert.code === ALERT_COVERAGE_DROP &&
    params.previous_valid_count !== undefined &&
    params.valid_count !== undefined &&
    params.drop_threshold !== undefined
  ) {
    const values = {
      runId: params.run_id,
      previous: params.previous_valid_count,
      current: params.valid_count,
      gate: formatPercentRate(params.drop_threshold),
    }
    // A refused run committed nothing, which is a different fact from a drop
    // observed between two runs that both committed, so it gets its own wording.
    if (params.gate_refused === true && params.run_id !== undefined) {
      return t(
        'The coverage gate refused run #{{runId}}. Valid model coverage would have fallen from {{previous}} to {{current}} models, past the {{gate}} drop gate, so nothing was written.',
        values
      )
    }
    return t(
      'Valid model coverage fell from {{previous}} to {{current}} models, past the {{gate}} drop gate.',
      values
    )
  }

  if (alert.code === ALERT_PRICE_JUMP && params.dimension !== undefined) {
    // A movement missing the amounts it is built from cannot be described
    // locally; the backend sentence is then the only complete statement of it.
    return priceJumpDetail(t, params) || alert.detail
  }

  if (alert.code === ALERT_COST_INVERSION && params.group !== undefined) {
    return t(
      'The worst catalog cost exceeds the projected sale price for group {{group}}.',
      { group: params.group }
    )
  }

  return alert.detail
}
