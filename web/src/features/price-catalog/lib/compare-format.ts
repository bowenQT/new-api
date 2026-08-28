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
 * Catalog amounts are always USD by contract (`CurrencyUSD`, spec §7.2), and
 * the comparison is an estimate rather than a charge, so they are rendered as
 * plain USD instead of going through the site's display-currency conversion.
 */
const usdFormatter = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 6,
})

/** Renders an optional USD amount; an absent amount is an em dash, never 0. */
export function formatUsdAmount(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '—'
  }
  return usdFormatter.format(value)
}

/**
 * Renders a margin rate. The backend omits the rate when the projected sale
 * price is 0 (spec §9.2), so an absent rate must stay blank and must never be
 * shown as infinity.
 */
export function formatMarginRate(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '—'
  }
  return `${(value * 100).toFixed(2)}%`
}

/** Stable cache key for one usage vector. */
export function usageVectorKey(usage: {
  p: number
  c: number
  cr: number
  cc: number
}): string {
  return `${usage.p}/${usage.c}/${usage.cr}/${usage.cc}`
}

/**
 * Key joining a comparison source price back to its catalog entry, which is
 * where `fetched_at` and `effective_at` live (spec §8.3).
 */
export function catalogEntryKey(
  sourceId: number,
  sourceModelName: string
): string {
  return `${sourceId}::${sourceModelName}`
}
