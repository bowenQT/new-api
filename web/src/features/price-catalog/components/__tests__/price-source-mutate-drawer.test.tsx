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
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, describe, expect, test } from 'vitest'

import type { PriceAdapterView, PriceSourceView } from '../../types'
import { PriceSourceMutateDrawer } from '../price-source-mutate-drawer'
import {
  mockApi,
  renderWithQueryClient,
  resetCatalogTestEnv,
} from './test-utils'

// The adapter endpoint is interpolated into a label, so this instance mirrors
// the app's `escapeValue: false` (src/i18n/config.ts); the shared test setup
// would otherwise HTML-escape every slash of the URL.
const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
  interpolation: { escapeValue: false },
})

const VERCEL: PriceAdapterView = {
  key: 'vercel_gateway',
  allowed_roles: ['supplier_cost'],
  allowed_scopes: ['public'],
  endpoint: 'https://ai-gateway.vercel.sh/v1/models',
}

const MODELS_DEV: PriceAdapterView = {
  key: 'models_dev',
  allowed_roles: ['curated_reference'],
  allowed_scopes: ['unknown'],
  endpoint: 'https://models.dev/api.json',
}

/**
 * An adapter that admits several roles: the channel field must then follow the
 * selected role, the only authority on the requirement.
 */
const MIXED: PriceAdapterView = {
  key: 'mixed_feed',
  allowed_roles: ['supplier_cost', 'curated_reference'],
  allowed_scopes: ['public', 'contract'],
  endpoint: 'https://example.invalid/prices.json',
}

function existingSource(
  overrides: Partial<PriceSourceView> = {}
): PriceSourceView {
  return {
    id: 5,
    name: 'Mixed feed',
    adapter_key: 'mixed_feed',
    role: 'curated_reference',
    scope: 'contract',
    channel_id: null,
    enabled: true,
    schedule_enabled: false,
    schedule_interval_seconds: 86_400,
    settings: '',
    config_revision: 1,
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
    ...overrides,
  }
}

function installAdapters(adapters: PriceAdapterView[] | Error) {
  mockApi('get', async (url) => {
    if (url !== '/api/upstream-price-sources/adapters') {
      throw new Error(`Unexpected GET ${url}`)
    }
    if (adapters instanceof Error) throw adapters
    return { data: { success: true, data: adapters } }
  })
}

function drawerTree(source?: PriceSourceView, open = true) {
  return (
    <I18nextProvider i18n={i18n}>
      <PriceSourceMutateDrawer
        open={open}
        onOpenChange={() => undefined}
        source={source}
        onSaved={() => undefined}
      />
    </I18nextProvider>
  )
}

function renderDrawer(source?: PriceSourceView) {
  const rendered = renderWithQueryClient(drawerTree(source))
  return {
    ...rendered,
    setOpen: (open: boolean) => rendered.rerender(drawerTree(source, open)),
  }
}

function nameInput(): HTMLInputElement {
  return screen.getByPlaceholderText(
    'e.g. Vercel gateway cost'
  ) as HTMLInputElement
}

/**
 * The form only reflects the adapter contract once the registry has answered,
 * and the endpoint line is the first thing that depends on it.
 */
async function waitForAdapter(endpoint: string): Promise<void> {
  await screen.findByText(`Fixed endpoint: ${endpoint}`)
}

function labelText(text: string): HTMLElement | null {
  return (
    [...document.querySelectorAll('label')].find(
      (candidate) => candidate.textContent?.trim() === text
    ) ?? null
  )
}

afterEach(resetCatalogTestEnv)

describe('price source mutate drawer', () => {
  test('announces that adapters are loading before the registry responds', () => {
    mockApi('get', () => new Promise(() => undefined))

    renderDrawer()

    expect(
      screen.getByText('Loading the registered adapters...')
    ).toBeInTheDocument()
    expect(screen.queryByText(/^Fixed endpoint:/)).toBeNull()
  })

  test('reports a failure to load the adapter registry', async () => {
    installAdapters(new Error('boom'))

    renderDrawer()

    expect(
      await screen.findByText('We could not load adapters.')
    ).toBeInTheDocument()
  })

  test('reports an empty adapter registry instead of offering a choice', async () => {
    installAdapters([])

    renderDrawer()

    expect(
      await screen.findByText('No adapter is registered on this server.')
    ).toBeInTheDocument()
    expect(screen.queryByText(/^Fixed endpoint:/)).toBeNull()
  })

  test('takes the endpoint, role and scope of a new source from the adapter registry', async () => {
    installAdapters([VERCEL, MODELS_DEV])

    renderDrawer()

    expect(
      await screen.findByText(
        'Fixed endpoint: https://ai-gateway.vercel.sh/v1/models'
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Role and scope are fixed by the adapter and cannot be chosen freely.'
      )
    ).toBeInTheDocument()
    expect(screen.getByText(/Supplier cost/)).toBeInTheDocument()
    expect(labelText('Channel ID')).not.toBeNull()
  })

  test('asks for a channel when the selected role is supplier cost even though the adapter does not require one', async () => {
    installAdapters([MIXED])

    renderDrawer(existingSource({ role: 'supplier_cost', channel_id: 12 }))

    await waitForAdapter(MIXED.endpoint)
    expect(
      screen.getByText(
        'This adapter admits more than one combination. Only the values listed here are accepted.'
      )
    ).toBeInTheDocument()
    expect(labelText('Role')).not.toBeNull()
    expect(labelText('Scope')).not.toBeNull()
    expect(labelText('Channel ID')).not.toBeNull()
  })

  test('hides the channel field and warns about compilations for a curated reference role', async () => {
    installAdapters([MIXED])

    renderDrawer(existingSource())

    expect(
      await screen.findByText(
        'This is a third-party compilation, not an official vendor price.'
      )
    ).toBeInTheDocument()
    expect(labelText('Channel ID')).toBeNull()
  })

  test('keeps what the admin typed before the adapter registry answered', async () => {
    let publishAdapters: (adapters: PriceAdapterView[]) => void = () =>
      undefined
    const registry = new Promise<PriceAdapterView[]>((resolve) => {
      publishAdapters = resolve
    })
    mockApi('get', async (url) => {
      if (url !== '/api/upstream-price-sources/adapters') {
        throw new Error(`Unexpected GET ${url}`)
      }
      return { data: { success: true, data: await registry } }
    })

    renderDrawer()
    // The drawer is interactive while the registry is still in flight.
    fireEvent.change(nameInput(), { target: { value: 'Gateway cost' } })
    publishAdapters([VERCEL, MODELS_DEV])

    await waitForAdapter(VERCEL.endpoint)
    expect(nameInput()).toHaveValue('Gateway cost')
    expect(screen.getByText(/Supplier cost/)).toBeInTheDocument()
  })

  test('clears an abandoned draft when the drawer is closed and opened again', async () => {
    installAdapters([VERCEL, MODELS_DEV])

    const drawer = renderDrawer()
    await waitForAdapter(VERCEL.endpoint)
    fireEvent.change(nameInput(), { target: { value: 'Abandoned draft' } })

    drawer.setOpen(false)
    drawer.setOpen(true)

    await waitForAdapter(VERCEL.endpoint)
    expect(nameInput()).toHaveValue('')
  })

  test('saves a curated reference source with its own role and scope and without a channel', async () => {
    installAdapters([MIXED])
    const payloads: Record<string, unknown>[] = []
    mockApi('put', async (_url, data) => {
      payloads.push(data as Record<string, unknown>)
      return { data: { success: true, data: {} } }
    })

    renderDrawer(existingSource())
    await waitForAdapter(MIXED.endpoint)
    expect(labelText('Channel ID')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(payloads).toHaveLength(1))
    expect(payloads[0]).toMatchObject({
      adapter_key: 'mixed_feed',
      role: 'curated_reference',
      scope: 'contract',
      channel_id: null,
    })
  })
})
