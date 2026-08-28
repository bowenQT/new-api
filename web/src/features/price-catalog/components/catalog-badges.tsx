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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'

import { ROLE_LABEL_KEYS, SCOPE_LABEL_KEYS } from '../constants'
import type { PriceRole, PriceScope } from '../types'

/**
 * Role badge. `curated_reference` is always annotated as an unofficial
 * third-party compilation and `supplier_cost` as a purchase cost, so neither
 * can be read as a vendor's official list price (spec §6.3, §9.4).
 */
export function RoleBadge(props: { role: PriceRole }) {
  const { t } = useTranslation()
  const label = t(ROLE_LABEL_KEYS[props.role] ?? props.role)

  if (props.role === 'curated_reference') {
    return (
      <span className='inline-flex flex-wrap items-center gap-1'>
        <Badge variant='outline'>{label}</Badge>
        <Badge variant='warning'>
          {t('Unofficial compilation, not a vendor list price')}
        </Badge>
      </span>
    )
  }

  return <Badge variant='outline'>{label}</Badge>
}

export function ScopeBadge(props: { scope: PriceScope }) {
  const { t } = useTranslation()
  return (
    <Badge variant='secondary'>
      {t(SCOPE_LABEL_KEYS[props.scope] ?? props.scope)}
    </Badge>
  )
}

export type CatalogFlags = {
  stale?: boolean
  missing?: boolean
  orphaned?: boolean
  canonicalConflict?: boolean
  variesByProvider?: boolean
  costInverted?: boolean
}

/**
 * Integrity and freshness labels required on every catalog surface
 * (spec §11.1, §11.2). `varies_by_provider` carries the mandatory wording from
 * spec §6.2 so such an observation is never presented as a confirmed cost.
 */
export function CatalogFlagBadges(props: {
  flags: CatalogFlags
  className?: string
}) {
  const { t } = useTranslation()
  const flags = props.flags
  const hasAny =
    flags.stale ||
    flags.missing ||
    flags.orphaned ||
    flags.canonicalConflict ||
    flags.variesByProvider ||
    flags.costInverted

  if (!hasAny) return null

  return (
    <span className={props.className ?? 'inline-flex flex-wrap gap-1'}>
      {flags.costInverted && (
        <Badge variant='destructive'>
          {t('Cost exceeds the projected sale price')}
        </Badge>
      )}
      {flags.variesByProvider && (
        <Badge variant='warning'>
          {t('Prices differ across providers, cost is not confirmed')}
        </Badge>
      )}
      {flags.missing && (
        <Badge variant='warning'>{t('Missing upstream')}</Badge>
      )}
      {flags.stale && <Badge variant='warning'>{t('Stale')}</Badge>}
      {flags.orphaned && (
        <Badge variant='destructive'>{t('Channel deleted (orphaned)')}</Badge>
      )}
      {flags.canonicalConflict && (
        <Badge variant='destructive'>
          {t('Canonical model mapping conflict')}
        </Badge>
      )}
    </span>
  )
}
