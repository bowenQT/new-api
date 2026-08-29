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
import { api } from '@/lib/api'

import type {
  ApiEnvelope,
  PriceAdapterView,
  PriceCompareRequest,
  PriceCompareResponse,
  PricePreviewResponse,
  PriceSourceRequest,
  PriceSourceView,
  PriceSyncResponse,
  SourceAlertsResponse,
} from './types'

/**
 * Upstream price catalog admin API (root-only, spec §10, §12).
 *
 * Nothing in this module touches sale pricing: the catalog is a read-only
 * observation store plus its two-phase (preview → commit) sync.
 */

export async function listPriceSources(): Promise<
  ApiEnvelope<PriceSourceView[]>
> {
  const res = await api.get('/api/upstream-price-sources')
  return res.data
}

/**
 * The registered adapters with the role, scope, channel and endpoint contract
 * each one admits (spec §12). The source form is built from this response, so
 * the client never proposes a combination the server would refuse.
 */
export async function listPriceAdapters(): Promise<
  ApiEnvelope<PriceAdapterView[]>
> {
  const res = await api.get('/api/upstream-price-sources/adapters')
  return res.data
}

export async function createPriceSource(
  payload: PriceSourceRequest
): Promise<ApiEnvelope<PriceSourceView>> {
  const res = await api.post('/api/upstream-price-sources', payload)
  return res.data
}

export async function updatePriceSource(
  id: number,
  payload: PriceSourceRequest
): Promise<ApiEnvelope<PriceSourceView>> {
  const res = await api.put(`/api/upstream-price-sources/${id}`, payload)
  return res.data
}

/** Phase 1 of a sync: fetch + normalize + diff, persisting nothing (spec §8.1). */
export async function previewPriceSource(
  id: number
): Promise<ApiEnvelope<PricePreviewResponse>> {
  const res = await api.post(`/api/upstream-price-sources/${id}/preview`)
  return res.data
}

/**
 * Phase 2 of a sync. The preview token is the server's CAS handle over the
 * source config revision and base run; it must come from a preview response
 * and is never constructed on the client.
 */
export async function syncPriceSource(
  id: number,
  previewToken: string
): Promise<ApiEnvelope<PriceSyncResponse>> {
  const res = await api.post(`/api/upstream-price-sources/${id}/sync`, {
    preview_token: previewToken,
  })
  return res.data
}

/**
 * The source-level catalog health alerts on their own (spec §13). The source
 * list renders them without the current-price projection, which carries a row
 * per model and is far more than the alert bar needs.
 */
export async function listPriceSourceAlerts(): Promise<
  ApiEnvelope<SourceAlertsResponse>
> {
  const res = await api.get('/api/upstream-price-sources/alerts')
  return res.data
}

/**
 * Sale groups, used to pick the group whose ratio the comparison projects
 * with. This is the existing admin group list; the catalog never writes it.
 */
export async function listSaleGroups(): Promise<ApiEnvelope<string[]>> {
  const res = await api.get('/api/group')
  return res.data
}

export async function comparePrices(
  payload: PriceCompareRequest
): Promise<ApiEnvelope<PriceCompareResponse>> {
  const res = await api.post('/api/upstream-prices/compare', payload)
  return res.data
}
