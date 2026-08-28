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

import type { PriceSourceView } from '../../types'
import { PriceSourcesPanel } from '../price-sources-panel'

type ApiMethod = (url: string, config?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
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
 * Serves the two GETs the panel makes. The current-price projection is served
 * empty on purpose: coverage, missing and staleness must come from the source
 * list, never from a join against the catalog entries.
 */
function installApi(sources: PriceSourceView[] | Error) {
  apiClient.get = async (url) => {
    if (url === '/api/upstream-price-sources') {
      if (sources instanceof Error) throw sources
      return { data: { success: true, data: sources } }
    }
    if (url === '/api/upstream-prices/current') {
      return {
        data: {
          success: true,
          data: { generated_at: 1_700_000_000, entries: [], alerts: [] },
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
