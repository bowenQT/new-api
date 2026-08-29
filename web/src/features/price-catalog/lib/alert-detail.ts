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
  ALERT_SOURCE_CONFIG_CHANGED,
  ALERT_SOURCE_CONSECUTIVE_FAILURES,
  ALERT_SOURCE_STALE,
} from '../constants'
import type { PriceAlert } from '../types'

/** Alert durations are reported in seconds; the admin reads them in hours. */
function hoursOf(seconds: number): string {
  return (seconds / 3600).toFixed(1)
}

/** The coverage gate is a drop fraction; it is shown as a percentage. */
function percentOf(fraction: number): string {
  return `${(fraction * 100).toFixed(2)}%`
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
      gate: percentOf(params.drop_threshold),
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

  if (alert.code === ALERT_COST_INVERSION && params.group !== undefined) {
    return t(
      'The worst catalog cost exceeds the projected sale price for group {{group}}.',
      { group: params.group }
    )
  }

  return alert.detail
}
