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
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import { api } from '@/lib/api'

import { priceCatalogQueryKeys } from '../../constants'
import type { PriceAlert, PriceSourceView } from '../../types'
import { PriceSourcesPanel } from '../price-sources-panel'

type ApiMethod = (url: string, config?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; put: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put
let queryClient: QueryClient | null = null

function priceSource(
  overrides: Partial<PriceSourceView> = {}
): PriceSourceView {
  return {
    id: 1,
    name: 'Vercel gateway cost',
    adapter_key: 'vercel_gateway',
    role: 'supplier_cost',
    scope: 'public',
    channel_id: 12,
    enabled: true,
    schedule_enabled: false,
    schedule_interval_seconds: 86_400,
    settings: '',
    config_revision: 3,
    last_success_run_id: 7,
    last_success_at: 1_700_000_000,
    last_success_finished_at: 1_700_000_600,
    last_error_at: null,
    last_error_summary: '',
    coverage_count: 3,
    missing_count: 2,
    stale: true,
    orphaned: false,
    created_time: 1,
    updated_time: 2,
    ...overrides,
  }
}

/**
 * Serves the two GETs the panel makes: the source list, which is the only
 * authority for coverage, missing count and staleness, and the source-alerts
 * endpoint. Any other URL throws, so a request for the current-price
 * projection fails the test rather than being silently served.
 */
function installApi(
  sources: PriceSourceView[] | Error,
  alerts: PriceAlert[] = []
) {
  apiClient.get = async (url) => {
    if (url === '/api/upstream-price-sources') {
      if (sources instanceof Error) throw sources
      return { data: { success: true, data: sources } }
    }
    if (url === '/api/upstream-price-sources/alerts') {
      return {
        data: {
          success: true,
          data: { generated_at: 1_700_000_000, alerts },
        },
      }
    }
    throw new Error(`Unexpected GET ${url}`)
  }
}

function renderPanel() {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <PriceSourcesPanel />
    </QueryClientProvider>
  )
}

async function rowOf(name: string): Promise<HTMLElement> {
  const cell = await screen.findByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`Expected a table row for "${name}"`)
  return row
}

afterEach(() => {
  apiClient.get = originalGet
  apiClient.put = originalPut
  queryClient?.clear()
  queryClient = null
})

describe('price sources panel', () => {
  test('marks the list busy and shows no source rows while the sources request is pending', async () => {
    apiClient.get = () => new Promise(() => undefined)

    const { container } = renderPanel()

    expect(container.querySelector('[aria-busy="true"]')).not.toBeNull()
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByText('Vercel gateway cost')).toBeNull()
  })

  test('shows the empty message when no source is registered', async () => {
    installApi([])

    renderPanel()

    expect(
      await screen.findByText('No price source has been registered yet.')
    ).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
  })

  test('shows a retryable error state when the sources request fails', async () => {
    installApi(new Error('boom'))

    renderPanel()

    expect(
      await screen.findByText('We could not load price sources.')
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  test('reads coverage, missing count and staleness from the source list rather than the catalog projection', async () => {
    installApi([priceSource()])

    renderPanel()

    const row = within(await rowOf('Vercel gateway cost'))
    expect(row.getByText('3 models')).toBeInTheDocument()
    expect(row.getByText('2 missing')).toBeInTheDocument()
    expect(row.getByText('Stale')).toBeInTheDocument()
    expect(row.getByText('Missing upstream')).toBeInTheDocument()
  })

  // The health alerts have their own endpoint; rendering the source list must
  // not project the whole current-price catalog to reach them.
  test('reads the alerts from the source-alerts endpoint and never requests the current-price projection', async () => {
    installApi([priceSource()])
    const served = apiClient.get
    const requested: string[] = []
    apiClient.get = async (url, config) => {
      requested.push(url)
      return served(url, config)
    }

    renderPanel()

    await rowOf('Vercel gateway cost')
    await waitFor(() =>
      expect(requested).toContain('/api/upstream-price-sources/alerts')
    )
    expect(requested).not.toContain('/api/upstream-prices/current')
  })

  test('labels a source whose configuration changed after its last successful run', async () => {
    installApi(
      [priceSource({ stale: false, missing_count: 0 })],
      [
        {
          code: 'source_config_changed',
          source_id: 1,
          source_name: 'Vercel gateway cost',
          detail:
            'source configuration changed after run 7; its prices are not confirmed until the next successful sync',
          params: { run_id: 7 },
        },
      ]
    )

    renderPanel()

    const row = within(await rowOf('Vercel gateway cost'))
    expect(
      row.getByText(
        'Source configuration changed, cost is not confirmed until the next sync'
      )
    ).toBeInTheDocument()
    // The per-source line is rendered from the alert parameters, so the
    // backend's English sentence never reaches a localized page.
    expect(
      row.getByText(
        'The source configuration changed after run #7; its prices are not confirmed until the next successful sync.'
      )
    ).toBeInTheDocument()
  })

  test('reports a source whose last successful run never finished as never run', async () => {
    installApi([
      priceSource({
        id: 2,
        name: 'Never finished',
        last_success_at: 1_700_000_000,
        last_success_finished_at: null,
        last_success_run_id: null,
      }),
    ])

    renderPanel()

    const row = within(await rowOf('Never finished'))
    expect(row.getByText('Never')).toBeInTheDocument()
    expect(row.queryByText(/^Run #/)).toBeNull()
  })

  // A comparison is derived from the sources, so a cached one is stale the
  // moment a source changes.
  test('invalidates the cached comparisons after a source is toggled', async () => {
    installApi([priceSource()])
    apiClient.put = async () => ({ data: { success: true } })

    renderPanel()
    const client = queryClient
    if (!client) throw new Error('Expected a query client')
    const compareKey = priceCatalogQueryKeys.compare('default', '1/1/0/0', '')
    client.setQueryData(compareKey, { generated_at: 1, entries: [] })

    const row = within(await rowOf('Vercel gateway cost'))
    fireEvent.click(row.getByLabelText('Enabled'))

    await waitFor(() =>
      expect(client.getQueryState(compareKey)?.isInvalidated).toBe(true)
    )
  })

  test('disables scheduling and refuses a commit for an orphaned source', async () => {
    installApi([
      priceSource({
        id: 3,
        name: 'Orphaned cost',
        orphaned: true,
        schedule_enabled: false,
      }),
    ])

    renderPanel()

    const row = within(await rowOf('Orphaned cost'))
    expect(row.getByLabelText('Scheduled sync')).toHaveAttribute(
      'aria-disabled',
      'true'
    )
    expect(row.getByLabelText('Enabled')).not.toHaveAttribute(
      'aria-disabled',
      'true'
    )
    expect(
      row.getByText('Scheduling is unavailable while the source is orphaned.')
    ).toBeInTheDocument()

    fireEvent.click(row.getByRole('button', { name: 'Preview / sync' }))

    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByText('Channel deleted (orphaned)')).toBeInTheDocument()
    await waitFor(() =>
      expect(
        dialog.getByRole('button', { name: 'Confirm and sync' })
      ).toBeDisabled()
    )
    expect(dialog.getByRole('button', { name: 'Run preview' })).toBeEnabled()
  })
})
