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
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import type { PriceCompareEntry, PriceCompareSourcePrice } from '../../types'
import { PriceCompareTable } from '../price-compare-table'

function costPrice(
  overrides: Partial<PriceCompareSourcePrice> = {}
): PriceCompareSourcePrice {
  return {
    source_id: 1,
    source_name: 'Vercel gateway cost',
    role: 'supplier_cost',
    scope: 'public',
    channel_id: 12,
    source_model_name: 'openai/gpt-4o',
    status: 'current',
    amount_usd: 4,
    projection: 'ok',
    usable_for_margin: true,
    stale: false,
    orphaned: false,
    varies_by_provider: false,
    canonical_conflict: false,
    source_config_changed: false,
    run_id: 7,
    ...overrides,
  }
}

function entry(overrides: Partial<PriceCompareEntry> = {}): PriceCompareEntry {
  return {
    canonical_model_name: 'gpt-4o',
    sale_billing_mode: 'ratio',
    sale_projection: 'ok',
    sale_base_usd: 5,
    sale_projected_usd: 5,
    costs: [costPrice()],
    references: null,
    min_cost_usd: 4,
    max_cost_usd: 4,
    worst_margin_usd: 1,
    worst_margin_rate: 0.2,
    cost_confirmed: true,
    cost_inverted: false,
    ...overrides,
  }
}

function definitionValue(term: string): string {
  const dt = [...document.querySelectorAll('dt')].find((candidate) =>
    candidate.textContent?.startsWith(term)
  )
  const dd = dt?.parentElement?.querySelector('dd')
  if (!dd) throw new Error(`Expected a value for "${term}"`)
  return dd.textContent ?? ''
}

function expandSourceDetail(): void {
  fireEvent.click(screen.getByRole('button', { name: 'Show source detail' }))
}

describe('price comparison table', () => {
  test('shows the empty message when the filter matches no model', () => {
    render(<PriceCompareTable entries={[]} />)

    expect(
      screen.getByText('No model matches the current filter.')
    ).toBeInTheDocument()
  })

  test('disables the source detail toggle for a model with no source price', () => {
    render(
      <PriceCompareTable entries={[entry({ costs: null, references: null })]} />
    )

    expect(
      screen.getByRole('button', { name: 'Show source detail' })
    ).toBeDisabled()
    expect(
      screen.getByText('No catalog cost for this model')
    ).toBeInTheDocument()
  })

  test('warns that a cost varying across providers is not confirmed', () => {
    render(
      <PriceCompareTable
        entries={[
          entry({
            costs: [costPrice({ varies_by_provider: true })],
            cost_confirmed: false,
          }),
        ]}
      />
    )

    expect(
      screen.getAllByText(
        'Prices differ across providers, cost is not confirmed'
      ).length
    ).toBeGreaterThan(0)
    expect(screen.getByText('Cost not confirmed')).toBeInTheDocument()
  })

  test('labels a cost observed under an older source configuration and keeps it unconfirmed', () => {
    render(
      <PriceCompareTable
        entries={[
          entry({
            costs: [
              costPrice({
                source_config_changed: true,
                usable_for_margin: false,
              }),
            ],
            cost_confirmed: false,
            min_cost_usd: undefined,
            max_cost_usd: undefined,
            worst_margin_usd: undefined,
            worst_margin_rate: undefined,
          }),
        ]}
      />
    )

    expect(
      screen.getByText(
        'Source configuration changed, cost is not confirmed until the next sync'
      )
    ).toBeInTheDocument()
    expect(screen.getByText('Cost not confirmed')).toBeInTheDocument()

    expandSourceDetail()

    expect(
      screen.getAllByText(
        'Source configuration changed, cost is not confirmed until the next sync'
      )
    ).toHaveLength(2)
    expect(screen.getByText('Excluded from the margin')).toBeInTheDocument()
  })

  test('labels a model whose cost exceeds the projected sale price', () => {
    render(
      <PriceCompareTable
        entries={[
          entry({
            cost_inverted: true,
            sale_projected_usd: 3,
            costs: [costPrice({ amount_usd: 6 })],
            min_cost_usd: 6,
            max_cost_usd: 6,
            worst_margin_usd: -3,
            worst_margin_rate: -1,
          }),
        ]}
      />
    )

    expect(
      screen.getByText('Cost exceeds the projected sale price')
    ).toBeInTheDocument()
    expect(screen.getByText('-$3.00')).toBeInTheDocument()
    expect(screen.getByText('-100.00%')).toBeInTheDocument()
  })

  test('renders an em dash instead of a rate when the projected sale price is zero', () => {
    render(
      <PriceCompareTable
        entries={[
          entry({
            sale_base_usd: 0,
            sale_projected_usd: 0,
            worst_margin_usd: -4,
            worst_margin_rate: undefined,
          }),
        ]}
      />
    )

    expect(screen.getByText('—')).toBeInTheDocument()
    expect(screen.queryByText('Infinity%')).toBeNull()
  })

  test('takes the observation age and the vendor effective date from the compared price itself', () => {
    render(
      <PriceCompareTable
        entries={[
          entry({
            costs: [
              costPrice({ fetched_at: 1_700_000_000, effective_at: undefined }),
            ],
          }),
        ]}
      />
    )
    expandSourceDetail()

    expect(definitionValue('Fetched at')).not.toBe('-')
    // `effective_at` is omitempty, so an absent vendor date must stay blank
    // rather than being shown as the epoch.
    expect(definitionValue('Effective at')).toBe('-')
  })

  test('expands and collapses the source detail of one model', () => {
    render(<PriceCompareTable entries={[entry()]} />)

    const toggle = screen.getByRole('button', { name: 'Show source detail' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('openai/gpt-4o')).toBeNull()

    fireEvent.click(toggle)

    const collapse = screen.getByRole('button', { name: 'Hide source detail' })
    expect(collapse).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('openai/gpt-4o')).toBeInTheDocument()

    fireEvent.click(collapse)

    expect(
      screen.getByRole('button', { name: 'Show source detail' })
    ).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('openai/gpt-4o')).toBeNull()
  })
})
