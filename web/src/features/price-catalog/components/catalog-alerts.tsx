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
import { TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import { ALERT_LABEL_KEYS, ALERT_PRICE_JUMP } from '../constants'
import { priceAlertDetail } from '../lib/alert-detail'
import type { PriceAlert } from '../types'

/**
 * How many price movements this batch of alerts leaves unlisted (spec §13). A
 * run that repriced a whole source stores only its largest movements, so
 * showing twenty of them without saying so would present a sample as the whole
 * change. The count is stated once for the list rather than repeated on every
 * movement, which describes the same fact twenty times over.
 */
function unlistedPriceJumpCount(alerts: PriceAlert[]): number {
  let unlisted = 0
  const countedRuns = new Set<number>()
  for (const alert of alerts) {
    const params = alert.params
    if (
      alert.code !== ALERT_PRICE_JUMP ||
      params?.run_id === undefined ||
      params.jump_count === undefined ||
      params.reported_count === undefined ||
      countedRuns.has(params.run_id)
    ) {
      continue
    }
    countedRuns.add(params.run_id)
    unlisted += Math.max(0, params.jump_count - params.reported_count)
  }
  return unlisted
}

/**
 * Catalog health alerts (spec §13). They are derived on read by the backend
 * and shown in the admin UI only; they are deliberately not routed to any
 * notification channel.
 */
export function CatalogAlerts(props: { alerts: PriceAlert[] }) {
  const { t } = useTranslation()

  if (props.alerts.length === 0) return null

  const unlistedPriceJumps = unlistedPriceJumpCount(props.alerts)

  return (
    <Alert variant='destructive'>
      <TriangleAlert aria-hidden='true' />
      <AlertTitle>
        {t('{{count}} catalog health alert(s)', {
          count: props.alerts.length,
        })}
      </AlertTitle>
      <AlertDescription>
        <ul className='mt-1 space-y-1'>
          {props.alerts.map((alert) => (
            <li
              key={`${alert.code}-${alert.source_id ?? 0}-${alert.canonical_model_name ?? ''}-${alert.detail}`}
            >
              <span className='font-medium'>
                {t(ALERT_LABEL_KEYS[alert.code] ?? alert.code)}
              </span>
              {alert.source_name ? ` · ${alert.source_name}` : ''}
              {alert.canonical_model_name
                ? ` · ${alert.canonical_model_name}`
                : ''}
              <span className='text-muted-foreground'>
                {' — '}
                {priceAlertDetail(t, alert)}
              </span>
            </li>
          ))}
          {unlistedPriceJumps > 0 && (
            <li className='text-muted-foreground'>
              {t(
                '{{count}} further price change(s) were recorded but are not listed.',
                { count: unlistedPriceJumps }
              )}
            </li>
          )}
        </ul>
      </AlertDescription>
    </Alert>
  )
}
