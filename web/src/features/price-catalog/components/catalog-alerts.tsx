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

import { ALERT_LABEL_KEYS } from '../constants'
import type { PriceAlert } from '../types'

/**
 * Catalog health alerts (spec §13). They are derived on read by the backend
 * and shown in the admin UI only; they are deliberately not routed to any
 * notification channel.
 */
export function CatalogAlerts(props: { alerts: PriceAlert[] }) {
  const { t } = useTranslation()

  if (props.alerts.length === 0) return null

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
              <span className='text-muted-foreground'> — {alert.detail}</span>
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  )
}
