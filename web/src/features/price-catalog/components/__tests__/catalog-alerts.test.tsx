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
import i18next, { createInstance } from 'i18next'
import type { ReactElement } from 'react'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, describe, expect, test } from 'vitest'

import zhLocale from '@/i18n/locales/zh.json'

import type { PriceAlert } from '../../types'
import { CatalogAlerts } from '../catalog-alerts'

// Source model names carry slashes, so this instance mirrors the app's
// `escapeValue: false` (src/i18n/config.ts); the shared test setup would
// otherwise HTML-escape every one of them.
const unescapedI18n = createInstance()
await unescapedI18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
  interpolation: { escapeValue: false },
})

function renderUnescaped(element: ReactElement) {
  return render(
    <I18nextProvider i18n={unescapedI18n}>{element}</I18nextProvider>
  )
}

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

  test('describes a price movement from its parameters, naming the source model', () => {
    renderUnescaped(
      <CatalogAlerts
        alerts={[
          {
            code: 'price_jump',
            source_id: 1,
            source_name: 'Vercel gateway cost',
            canonical_model_name: 'gpt-5.6-luna',
            detail: 'run 12 moved the input price of "openai/gpt-5.6-luna"',
            params: {
              run_id: 12,
              source_model_name: 'openai/gpt-5.6-luna',
              dimension: 'input',
              probe_context: 'p=1000000,len=1000',
              previous_usd: 0.2,
              current_usd: 2,
              change_rate: 9,
              jump_threshold: 0.5,
              jump_count: 1,
              reported_count: 1,
            },
          },
        ]}
      />
    )

    expect(
      screen.getByText(
        'The Input price of openai/gpt-5.6-luna moved from $0.2 to $2 (900.00%) at p=1000000,len=1000.',
        { exact: false }
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText('Upstream price changed sharply')
    ).toBeInTheDocument()
    expect(screen.queryByText(/not listed/)).toBeNull()
  })

  // The fail-closed dimension states no rate, so it must not be rendered in the
  // sentence measured movements use — that would imply a magnitude nobody
  // established.
  test('asks for a manual review when the movement could not be measured', () => {
    renderUnescaped(
      <CatalogAlerts
        alerts={[
          {
            code: 'price_jump',
            source_id: 1,
            source_name: 'Vercel gateway cost',
            detail:
              'run 12 changed the price in a way this check could not measure',
            params: {
              run_id: 12,
              source_model_name: 'openai/gpt-5.6-luna',
              dimension: 'expr_unverified',
              jump_count: 1,
              reported_count: 1,
            },
          },
        ]}
      />
    )

    expect(
      screen.getByText(
        'The price of openai/gpt-5.6-luna changed in a way this check could not measure. Review it manually.',
        { exact: false }
      )
    ).toBeInTheDocument()
    expect(screen.queryByText(/%/)).toBeNull()
  })

  // A run that repriced a whole source stores only its largest movements. The
  // list has to say so once, or the sample reads as the whole change.
  test('states how many recorded price movements the list leaves out', () => {
    renderUnescaped(
      <CatalogAlerts
        alerts={[0, 1].map((index) => ({
          code: 'price_jump',
          source_id: 1,
          source_name: 'Vercel gateway cost',
          detail: 'moved',
          params: {
            run_id: 12,
            source_model_name: `vendor/model-${index}`,
            dimension: 'input',
            probe_context: 'p=1000000,len=1000',
            previous_usd: 1,
            current_usd: 4,
            change_rate: 3,
            jump_count: 137,
            reported_count: 2,
          },
        }))}
      />
    )

    expect(
      screen.getByText(
        '135 further price change(s) were recorded but are not listed.'
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
