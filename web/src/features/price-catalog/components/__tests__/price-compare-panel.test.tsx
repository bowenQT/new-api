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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import { api } from '@/lib/api'

import type { PriceCompareResponse } from '../../types'
import { PriceComparePanel } from '../price-compare-panel'

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post
let queryClient: QueryClient | null = null
let requestedUrls: string[] = []

const comparison: PriceCompareResponse = {
  generated_at: 1_700_000_000,
  group: 'default',
  group_ratio: 1,
  group_ratio_configured: true,
  usage: { p: 1_000_000, c: 1_000_000, cr: 0, cc: 0 },
  total_models: 1,
  truncated: false,
  excluded_factors: ['tool_call_surcharge'],
  entries: [
    {
      canonical_model_name: 'gpt-4o',
      sale_billing_mode: 'ratio',
      sale_projection: 'ok',
      sale_base_usd: 5,
      sale_projected_usd: 5,
      costs: [
        {
          source_id: 1,
          source_name: 'Vercel gateway cost',
          role: 'supplier_cost',
          scope: 'public',
          source_model_name: 'openai/gpt-4o',
          status: 'current',
          amount_usd: 4,
          projection: 'ok',
          usable_for_margin: true,
          stale: false,
          orphaned: false,
          varies_by_provider: false,
          canonical_conflict: false,
          fetched_at: 1_700_000_000,
        },
      ],
      references: null,
      min_cost_usd: 4,
      max_cost_usd: 4,
      worst_margin_usd: 1,
      worst_margin_rate: 0.2,
      cost_confirmed: true,
      cost_inverted: false,
    },
  ],
  alerts: [],
}

function installApi(compare: PriceCompareResponse | Error) {
  requestedUrls = []
  apiClient.get = async (url) => {
    requestedUrls.push(url)
    if (url === '/api/group') {
      return { data: { success: true, data: ['default', 'vip'] } }
    }
    throw new Error(`Unexpected GET ${url}`)
  }
  apiClient.post = async (url) => {
    requestedUrls.push(url)
    if (url !== '/api/upstream-prices/compare') {
      throw new Error(`Unexpected POST ${url}`)
    }
    if (compare instanceof Error) throw compare
    return { data: { success: true, data: compare } }
  }
}

function renderPanel() {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <PriceComparePanel />
    </QueryClientProvider>
  )
}

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
  queryClient?.clear()
  queryClient = null
})

describe('price comparison panel', () => {
  test('shows no comparison table while the comparison request is pending', () => {
    apiClient.get = async () => ({ data: { success: true, data: [] } })
    apiClient.post = () => new Promise(() => undefined)

    renderPanel()

    expect(screen.queryByRole('table')).toBeNull()
    expect(
      screen.queryByText('No model matches the current filter.')
    ).toBeNull()
  })

  test('shows a retryable error state when the comparison request fails', async () => {
    installApi(new Error('boom'))

    renderPanel()

    expect(
      await screen.findByText('We could not compare catalog prices.')
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
  })

  test('renders the comparison without a second full catalog request', async () => {
    installApi(comparison)

    renderPanel()

    expect(await screen.findByText('gpt-4o')).toBeInTheDocument()
    expect(requestedUrls).not.toContain('/api/upstream-prices/current')
    expect(
      requestedUrls.filter((url) => url === '/api/upstream-prices/compare')
    ).toHaveLength(1)
  })

  test('keeps the usage vector apply action disabled until the draft changes', async () => {
    installApi(comparison)

    renderPanel()
    await screen.findByText('gpt-4o')

    const apply = screen.getByRole('button', { name: 'Apply usage vector' })
    expect(apply).toBeDisabled()

    fireEvent.change(screen.getByLabelText('Prompt tokens (p)'), {
      target: { value: '2000000' },
    })

    await waitFor(() => expect(apply).toBeEnabled())
  })

  test('refuses to apply a usage dimension outside the accepted bound', async () => {
    installApi(comparison)

    renderPanel()
    await screen.findByText('gpt-4o')

    fireEvent.change(screen.getByLabelText('Completion tokens (c)'), {
      target: { value: '2000000000' },
    })

    expect(
      await screen.findByText(
        'Each dimension must be between 0 and 1000000000.'
      )
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Apply usage vector' })
    ).toBeDisabled()
  })

  test('lists the factors the projection deliberately excludes', async () => {
    installApi(comparison)

    renderPanel()

    expect(
      await screen.findByText(/Excluded from this projection/)
    ).toHaveTextContent('Tool call surcharge')
  })
})
