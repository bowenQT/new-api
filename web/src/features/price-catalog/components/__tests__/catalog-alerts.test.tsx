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
import { render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, describe, expect, test } from 'vitest'

import zhLocale from '@/i18n/locales/zh.json'

import type { PriceAlert } from '../../types'
import { CatalogAlerts } from '../catalog-alerts'

function coverageDropAlert(gateRefused: boolean): PriceAlert {
  return {
    code: 'coverage_drop',
    source_id: 1,
    source_name: 'Vercel gateway cost',
    detail: 'valid model coverage fell from 40 to 3 (gate 0.2000)',
    params: {
      run_id: 12,
      previous_valid_count: 40,
      valid_count: 3,
      drop_threshold: 0.2,
      gate_refused: gateRefused,
    },
  }
}

afterEach(async () => {
  await i18next.changeLanguage('en')
})

describe('catalog alerts', () => {
  test('states that nothing was written when the coverage gate refused the run', () => {
    render(<CatalogAlerts alerts={[coverageDropAlert(true)]} />)

    expect(
      screen.getByText(
        'The coverage gate refused run #12. Valid model coverage would have fallen from 40 to 3 models, past the 20.00% drop gate, so nothing was written.',
        { exact: false }
      )
    ).toBeInTheDocument()
  })

  test('reports a coverage drop between two committed runs without the refusal wording', () => {
    render(<CatalogAlerts alerts={[coverageDropAlert(false)]} />)

    expect(
      screen.getByText(
        'Valid model coverage fell from 40 to 3 models, past the 20.00% drop gate.',
        { exact: false }
      )
    ).toBeInTheDocument()
    expect(screen.queryByText(/refused/)).toBeNull()
  })

  test('renders the stale-source detail from its parameters rather than the backend sentence', () => {
    render(
      <CatalogAlerts
        alerts={[
          {
            code: 'source_stale',
            source_id: 1,
            source_name: 'Vercel gateway cost',
            detail:
              'last successful run is 180000 seconds old, threshold 86400 seconds',
            params: {
              run_id: 7,
              age_seconds: 180_000,
              threshold_seconds: 86_400,
            },
          },
        ]}
      />
    )

    expect(
      screen.getByText(
        'The last successful run is 50.0 hours old, past the 24.0 hour staleness threshold.',
        { exact: false }
      )
    ).toBeInTheDocument()
  })

  // An older backend sends no params at all; its English sentence is still the
  // only information available, so it must survive rather than disappear.
  test('falls back to the backend detail when the alert carries no parameters', () => {
    render(
      <CatalogAlerts
        alerts={[
          {
            code: 'cost_inversion',
            canonical_model_name: 'gpt-4o',
            detail:
              'worst cost exceeds the projected sale price for group "vip"',
          },
        ]}
      />
    )

    expect(
      screen.getByText(
        'worst cost exceeds the projected sale price for group "vip"',
        { exact: false }
      )
    ).toBeInTheDocument()
  })

  test('renders the alert detail in the active language', async () => {
    i18next.addResourceBundle(
      'zh',
      'translation',
      zhLocale.translation,
      true,
      true
    )
    await i18next.changeLanguage('zh')

    render(
      <CatalogAlerts
        alerts={[
          {
            code: 'cost_inversion',
            canonical_model_name: 'gpt-4o',
            detail:
              'worst cost exceeds the projected sale price for group "default"',
            params: { group: 'default' },
          },
        ]}
      />
    )

    expect(
      screen.getByText('分组 default 的最差成本已超过其预估售价。', {
        exact: false,
      })
    ).toBeInTheDocument()
    expect(screen.getByText('成本高于预估售价（成本倒挂）')).toBeInTheDocument()
  })
})
