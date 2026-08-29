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
import { render, type RenderResult } from '@testing-library/react'
import type { ReactNode } from 'react'

import { api } from '@/lib/api'

/** One axios verb, under the shape a catalog test replaces it with. */
export type ApiMethod = (
  url: string,
  data?: unknown
) => Promise<{ data: unknown }>

type MockableApi = { get: ApiMethod; post: ApiMethod; put: ApiMethod }

const apiClient = api as unknown as MockableApi
const replacedMethods = new Map<keyof MockableApi, ApiMethod>()
const renderedClients: QueryClient[] = []

/**
 * Serves one axios verb from `impl` for the rest of the test. The real method
 * is remembered on the first replacement, so `resetCatalogTestEnv` restores the
 * shared client however many times a test replaced it.
 */
export function mockApi(method: keyof MockableApi, impl: ApiMethod): void {
  if (!replacedMethods.has(method)) {
    replacedMethods.set(method, apiClient[method])
  }
  apiClient[method] = impl
}

/**
 * Renders a catalog panel under its own query client. Retries are off so a
 * failing request reaches the error state within the test rather than being
 * retried past it, and the client is returned for the tests that assert on
 * cache state. `rerender` keeps that same client, so a test can change a prop
 * without the component remounting against a cold cache.
 */
export function renderWithQueryClient(
  ui: ReactNode
): RenderResult & { queryClient: QueryClient } {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  renderedClients.push(queryClient)
  const rendered = render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>
  )
  return {
    queryClient,
    ...rendered,
    rerender: (next: ReactNode) =>
      rendered.rerender(
        <QueryClientProvider client={queryClient}>{next}</QueryClientProvider>
      ),
  }
}

/**
 * Restores every replaced axios verb and clears every client rendered by this
 * test. Register it as the `afterEach` of a catalog test file, so no test
 * inherits another one's stubbed API or cached queries.
 */
export function resetCatalogTestEnv(): void {
  for (const [method, original] of replacedMethods) {
    apiClient[method] = original
  }
  replacedMethods.clear()
  for (const client of renderedClients) {
    client.clear()
  }
  renderedClients.length = 0
}
