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
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type {
  PricePreviewResponse,
  PriceSourceView,
  PriceSyncResponse,
} from '../../types'
import { PriceSourceSyncDialog } from '../price-source-sync-dialog'
import {
  mockApi,
  renderWithQueryClient,
  resetCatalogTestEnv,
} from './test-utils'

const source: PriceSourceView = {
  id: 42,
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
  last_success_run_id: null,
  last_success_at: null,
  last_success_finished_at: null,
  last_error_at: null,
  last_error_summary: '',
  coverage_count: 0,
  missing_count: 0,
  stale: false,
  orphaned: false,
  created_time: 1,
  updated_time: 2,
}

const preview: PricePreviewResponse = {
  source_id: 42,
  base_run_id: null,
  projected_run_status: 'succeeded',
  discovered_count: 2,
  valid_count: 2,
  unsupported_count: 0,
  rejected_count: 0,
  missing_count: 0,
  new_count: 1,
  changed_count: 1,
  unchanged_count: 0,
  coverage_drop_exceeded: false,
  price_jump_count: 0,
  items: [
    {
      source_model_name: 'openai/gpt-4o',
      canonical_model_name: 'gpt-4o',
      status: 'valid',
      change: 'new',
      varies_by_provider: true,
    },
  ],
  missing: [],
  preview_token: 'preview-token-1',
  expires_at: 1_700_000_600,
}

const committedRun: PriceSyncResponse = {
  run_id: 9,
  status: 'succeeded',
  discovered_count: 2,
  valid_count: 2,
  unsupported_count: 0,
  rejected_count: 0,
  missing_count: 0,
  new_snapshot_count: 1,
  idempotent_hit_count: 1,
  price_jump_count: 0,
}

function renderDialog(
  onSynced: () => void = () => undefined,
  sourceOverrides: Partial<PriceSourceView> = {}
) {
  return renderWithQueryClient(
    <PriceSourceSyncDialog
      open
      onOpenChange={() => undefined}
      source={{ ...source, ...sourceOverrides }}
      onSynced={onSynced}
    />
  )
}

/** Runs the preview, then the commit the preview token unlocks. */
async function commitPreviewedSync() {
  fireEvent.click(screen.getByRole('button', { name: 'Run preview' }))
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Confirm and sync' })
    ).toBeEnabled()
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm and sync' }))
  await screen.findByText('Run #9')
}

afterEach(resetCatalogTestEnv)

describe('price source sync dialog', () => {
  test('offers no commit before a preview has been run', () => {
    mockApi('post', async () => {
      throw new Error('no request expected')
    })

    renderDialog()

    expect(
      screen.getByText('Run a preview to see what a sync would change.')
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Confirm and sync' })
    ).toBeDisabled()
  })

  test('commits with the token the preview returned and then requires a fresh preview', async () => {
    const calls: { url: string; data: unknown }[] = []
    mockApi('post', async (url, data) => {
      calls.push({ url, data })
      if (url.endsWith('/preview')) {
        return { data: { success: true, data: preview } }
      }
      return {
        data: {
          success: true,
          data: {
            run_id: 9,
            status: 'succeeded',
            discovered_count: 2,
            valid_count: 2,
            unsupported_count: 0,
            rejected_count: 0,
            missing_count: 0,
            new_snapshot_count: 1,
            idempotent_hit_count: 1,
          },
        },
      }
    })
    let syncedCount = 0

    renderDialog(() => {
      syncedCount += 1
    })
    fireEvent.click(screen.getByRole('button', { name: 'Run preview' }))

    expect(await screen.findByText('gpt-4o')).toBeInTheDocument()
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Confirm and sync' })
      ).toBeEnabled()
    )
    fireEvent.click(screen.getByRole('button', { name: 'Confirm and sync' }))

    expect(await screen.findByText('Run #9')).toBeInTheDocument()
    expect(calls).toEqual([
      { url: '/api/upstream-price-sources/42/preview', data: undefined },
      {
        url: '/api/upstream-price-sources/42/sync',
        data: { preview_token: 'preview-token-1' },
      },
    ])
    expect(syncedCount).toBe(1)
    // The token is single use, so the committed dialog must not offer a second
    // commit until another preview has been produced.
    expect(
      screen.getByRole('button', { name: 'Confirm and sync' })
    ).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Run preview' })).toBeEnabled()
  })

  test('labels a previewed model whose price varies across providers', async () => {
    mockApi('post', async () => ({ data: { success: true, data: preview } }))

    renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Run preview' }))

    expect(
      await screen.findByText(
        'Prices differ across providers, cost is not confirmed'
      )
    ).toBeInTheDocument()
  })

  test('warns about a previewed model priced along dimensions the catalog does not normalize', async () => {
    mockApi('post', async () => ({
      data: {
        success: true,
        data: {
          ...preview,
          items: [
            {
              source_model_name: 'openai/gpt-4o',
              canonical_model_name: 'gpt-4o',
              status: 'valid',
              change: 'new',
              metadata: { unsupported_dimensions: 'fast,regional' },
            },
          ],
        },
      },
    }))

    renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Run preview' }))

    expect(
      await screen.findByText(
        'Unnormalized source pricing dimensions: fast, regional'
      )
    ).toBeInTheDocument()
  })

  test('shows the empty preview message when the fetch discovered no model', async () => {
    mockApi('post', async () => ({
      data: {
        success: true,
        data: {
          ...preview,
          discovered_count: 0,
          valid_count: 0,
          new_count: 0,
          changed_count: 0,
          items: [],
        },
      },
    }))

    renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Run preview' }))

    expect(
      await screen.findByText('The preview returned no model rows.')
    ).toBeInTheDocument()
  })

  // A refused or partial commit still comes back inside a success envelope, so
  // the dialog has to read the run status rather than the envelope.
  test('reports a gate-refused commit as a failure that wrote nothing', async () => {
    mockApi('post', async (url) => {
      if (url.endsWith('/preview')) {
        return { data: { success: true, data: preview } }
      }
      return {
        data: {
          success: true,
          data: {
            ...committedRun,
            status: 'failed',
            valid_count: 1,
            new_snapshot_count: 0,
            error_summary:
              'coverage drop gate refused commit: valid 1 vs baseline 40',
          },
        },
      }
    })

    renderDialog()
    await commitPreviewedSync()

    expect(
      await screen.findByText('The sync run failed and nothing was written')
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Model coverage fell further than the configured gate allows, so the server refused this commit. The previous prices are kept.'
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'coverage drop gate refused commit: valid 1 vs baseline 40'
      )
    ).toBeInTheDocument()
    expect(screen.getByText('Failed')).toBeInTheDocument()
  })

  test('reports a commit with no valid observation as a failure', async () => {
    mockApi('post', async (url) => {
      if (url.endsWith('/preview')) {
        return { data: { success: true, data: preview } }
      }
      return {
        data: {
          success: true,
          data: {
            ...committedRun,
            status: 'failed',
            valid_count: 0,
            new_snapshot_count: 0,
            error_summary: 'no valid observations',
          },
        },
      }
    })

    renderDialog()
    await commitPreviewedSync()

    expect(
      await screen.findByText(
        'The fetch returned no valid observation, so no snapshot was committed. The previous prices are kept.'
      )
    ).toBeInTheDocument()
  })

  test('warns that a partial commit left some models out', async () => {
    mockApi('post', async (url) => {
      if (url.endsWith('/preview')) {
        return { data: { success: true, data: preview } }
      }
      return {
        data: {
          success: true,
          data: { ...committedRun, status: 'partial', rejected_count: 1 },
        },
      }
    })

    renderDialog()
    await commitPreviewedSync()

    expect(
      await screen.findByText('Some models were left out')
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'The valid observations were committed. Unsupported, rejected, and missing models were not, and their previous snapshots are kept.'
      )
    ).toBeInTheDocument()
    expect(screen.getByText('Partially committed')).toBeInTheDocument()
    expect(
      screen.queryByText('The sync run failed and nothing was written')
    ).toBeNull()
  })

  // The server refuses a commit for a disabled source, so the dialog must not
  // offer the button that would produce that error.
  test('refuses the commit for a disabled source while keeping the preview', async () => {
    mockApi('post', async () => ({ data: { success: true, data: preview } }))

    renderDialog(() => undefined, { enabled: false })

    expect(screen.getByText('Source is disabled')).toBeInTheDocument()
    expect(
      screen.getByText(
        'This source is disabled. Preview stays available for diagnosis, but the server refuses to commit a sync until the source is enabled again.'
      )
    ).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Run preview' }))

    expect(await screen.findByText('gpt-4o')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Confirm and sync' })
    ).toBeDisabled()
  })

  test('keeps the commit unavailable when the preview failed', async () => {
    mockApi('post', async () => ({
      data: { success: false, message: 'upstream unreachable' },
    }))

    renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Run preview' }))

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Run preview' })).toBeEnabled()
    )
    expect(
      screen.getByRole('button', { name: 'Confirm and sync' })
    ).toBeDisabled()
    expect(
      screen.getByText('Run a preview to see what a sync would change.')
    ).toBeInTheDocument()
  })
})
