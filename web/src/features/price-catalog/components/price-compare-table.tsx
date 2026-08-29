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
import { ChevronDown, ChevronRight } from 'lucide-react'
import { Fragment, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  PROJECTION_LABEL_KEYS,
  SALE_BILLING_MODE_LABEL_KEYS,
} from '../constants'
import { formatMarginRate, formatUsdAmount } from '../lib/compare-format'
import type { PriceCompareEntry, PriceCompareSourcePrice } from '../types'
import {
  CatalogFlagBadges,
  RoleBadge,
  UnsupportedDimensionsBadge,
} from './catalog-badges'

type Props = {
  entries: PriceCompareEntry[]
}

function SourcePriceLines(props: {
  prices: PriceCompareSourcePrice[]
  emptyLabel: string
}) {
  if (props.prices.length === 0) {
    return <span className='text-muted-foreground'>{props.emptyLabel}</span>
  }
  return (
    <div className='space-y-1'>
      {props.prices.map((price) => (
        <div key={`${price.source_id}-${price.source_model_name}`}>
          <span className='tabular-nums'>
            {formatUsdAmount(price.amount_usd)}
          </span>
          <span className='text-muted-foreground'> · {price.source_name}</span>
        </div>
      ))}
    </div>
  )
}

/**
 * Model-level comparison table (spec §11.2). Every amount is a limited-basis
 * estimate; the table never offers an action that writes sale pricing.
 */
export function PriceCompareTable(props: Props) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const toggle = (model: string) => {
    setExpanded((previous) => {
      const next = new Set(previous)
      if (next.has(model)) next.delete(model)
      else next.add(model)
      return next
    })
  }

  return (
    <div className='overflow-x-auto rounded-md border'>
      <Table className='min-w-[1320px]'>
        <TableHeader>
          <TableRow className='bg-muted/40 hover:bg-muted/40'>
            <TableHead className='h-9 w-[260px] px-3 text-xs'>
              {t('Model')}
            </TableHead>
            <TableHead className='h-9 w-[160px] text-xs'>
              {t('Current sale base price')}
            </TableHead>
            <TableHead className='h-9 w-[150px] text-xs'>
              {t('Vendor list price')}
            </TableHead>
            <TableHead className='h-9 w-[190px] text-xs'>
              {t('Third-party reference price')}
            </TableHead>
            <TableHead className='h-9 w-[210px] text-xs'>
              {t('Channel costs')}
            </TableHead>
            <TableHead className='h-9 w-[160px] text-xs'>
              {t('Lowest / highest cost')}
            </TableHead>
            <TableHead className='h-9 w-[150px] text-xs'>
              {t('Projected sale price')}
            </TableHead>
            <TableHead className='h-9 w-[170px] pr-3 text-xs'>
              {t('Worst-case margin')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.entries.map((entry) => {
            const model = entry.canonical_model_name
            const costs = entry.costs ?? []
            const references = entry.references ?? []
            const isExpanded = expanded.has(model)
            const detailRows = [...costs, ...references]

            return (
              <Fragment key={model}>
                <TableRow
                  className={cn(
                    'hover:bg-muted/30',
                    entry.cost_inverted && 'bg-destructive/5'
                  )}
                >
                  <TableCell className='px-3 py-3 align-top'>
                    <div className='space-y-1'>
                      <div className='flex items-start gap-1'>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-sm'
                          onClick={() => toggle(model)}
                          aria-expanded={isExpanded}
                          aria-label={
                            isExpanded
                              ? t('Hide source detail')
                              : t('Show source detail')
                          }
                          disabled={detailRows.length === 0}
                        >
                          {isExpanded ? (
                            <ChevronDown
                              className='size-3.5'
                              aria-hidden='true'
                            />
                          ) : (
                            <ChevronRight
                              className='size-3.5'
                              aria-hidden='true'
                            />
                          )}
                        </Button>
                        <span className='font-mono text-xs break-all'>
                          {model}
                        </span>
                      </div>
                      <CatalogFlagBadges
                        flags={{
                          costInverted: entry.cost_inverted,
                          variesByProvider: costs.some(
                            (cost) => cost.varies_by_provider
                          ),
                          stale: costs.some((cost) => cost.stale),
                          orphaned: costs.some((cost) => cost.orphaned),
                          canonicalConflict: costs.some(
                            (cost) => cost.canonical_conflict
                          ),
                          missing: costs.some(
                            (cost) => cost.status === 'missing'
                          ),
                          sourceConfigChanged: costs.some(
                            (cost) => cost.source_config_changed
                          ),
                        }}
                        className='flex flex-wrap gap-1'
                      />
                      {costs.length === 0 && (
                        <Badge variant='outline'>
                          {t('No catalog cost for this model')}
                        </Badge>
                      )}
                    </div>
                  </TableCell>

                  <TableCell className='py-3 align-top text-xs'>
                    <div className='tabular-nums'>
                      {formatUsdAmount(entry.sale_base_usd)}
                    </div>
                    <div className='text-muted-foreground mt-0.5'>
                      {t(
                        SALE_BILLING_MODE_LABEL_KEYS[entry.sale_billing_mode] ??
                          entry.sale_billing_mode
                      )}
                    </div>
                    {entry.sale_projection !== 'ok' && (
                      <div className='text-warning mt-0.5'>
                        {t(
                          PROJECTION_LABEL_KEYS[entry.sale_projection] ??
                            entry.sale_projection
                        )}
                        {entry.sale_projection_note
                          ? ` · ${entry.sale_projection_note}`
                          : ''}
                      </div>
                    )}
                  </TableCell>

                  <TableCell className='text-muted-foreground py-3 align-top text-xs'>
                    {t('No vendor source configured')}
                  </TableCell>

                  <TableCell className='py-3 align-top text-xs'>
                    <SourcePriceLines
                      prices={references}
                      emptyLabel={t('None')}
                    />
                    {references.length > 0 && (
                      <Badge variant='warning' className='mt-1'>
                        {t('Unofficial compilation, not a vendor list price')}
                      </Badge>
                    )}
                  </TableCell>

                  <TableCell className='py-3 align-top text-xs'>
                    <SourcePriceLines prices={costs} emptyLabel={t('None')} />
                  </TableCell>

                  <TableCell className='py-3 align-top text-xs tabular-nums'>
                    <div>{formatUsdAmount(entry.min_cost_usd)}</div>
                    <div className='text-muted-foreground'>
                      {formatUsdAmount(entry.max_cost_usd)}
                    </div>
                    {!entry.cost_confirmed && costs.length > 0 && (
                      <div className='text-warning mt-0.5'>
                        {t('Cost not confirmed')}
                      </div>
                    )}
                  </TableCell>

                  <TableCell className='py-3 align-top text-xs tabular-nums'>
                    {formatUsdAmount(entry.sale_projected_usd)}
                  </TableCell>

                  <TableCell className='py-3 pr-3 align-top text-xs tabular-nums'>
                    <div
                      className={cn(
                        entry.worst_margin_usd !== undefined &&
                          entry.worst_margin_usd < 0 &&
                          'text-destructive font-medium'
                      )}
                    >
                      {formatUsdAmount(entry.worst_margin_usd)}
                    </div>
                    <div className='text-muted-foreground'>
                      {formatMarginRate(entry.worst_margin_rate)}
                    </div>
                  </TableCell>
                </TableRow>

                {isExpanded && detailRows.length > 0 && (
                  <TableRow className='bg-muted/20 hover:bg-muted/20'>
                    <TableCell colSpan={8} className='px-3 py-3'>
                      <div className='space-y-2'>
                        {detailRows.map((price) => {
                          return (
                            <div
                              key={`${price.source_id}-${price.source_model_name}`}
                              className='bg-card rounded-md border p-3 text-xs'
                            >
                              <div className='flex flex-wrap items-center gap-2'>
                                <span className='font-medium'>
                                  {price.source_name}
                                </span>
                                <RoleBadge role={price.role} />
                                {price.channel_id === undefined ? null : (
                                  <Badge variant='secondary'>
                                    {t('Channel #{{id}}', {
                                      id: price.channel_id,
                                    })}
                                  </Badge>
                                )}
                                <span className='tabular-nums'>
                                  {formatUsdAmount(price.amount_usd)}
                                </span>
                                {price.usable_for_margin ? null : (
                                  <Badge variant='outline'>
                                    {t('Excluded from the margin')}
                                  </Badge>
                                )}
                              </div>

                              <CatalogFlagBadges
                                flags={{
                                  stale: price.stale,
                                  orphaned: price.orphaned,
                                  variesByProvider: price.varies_by_provider,
                                  canonicalConflict: price.canonical_conflict,
                                  missing: price.status === 'missing',
                                  sourceConfigChanged:
                                    price.source_config_changed,
                                }}
                                className='mt-1 flex flex-wrap gap-1'
                              />

                              <UnsupportedDimensionsBadge
                                dimensions={price.unsupported_dimensions}
                                className='mt-1'
                              />

                              {price.projection !== 'ok' && (
                                <p className='text-warning mt-1'>
                                  {t(
                                    PROJECTION_LABEL_KEYS[price.projection] ??
                                      price.projection
                                  )}
                                  {price.projection_note
                                    ? ` · ${price.projection_note}`
                                    : ''}
                                </p>
                              )}
                              {price.warning_code ? (
                                <p className='text-muted-foreground mt-1'>
                                  {t('Warning: {{code}}', {
                                    code: price.warning_code,
                                  })}
                                </p>
                              ) : null}

                              <dl className='text-muted-foreground mt-2 grid grid-cols-1 gap-x-4 gap-y-1 sm:grid-cols-2 lg:grid-cols-4'>
                                <div>
                                  <dt className='inline'>
                                    {t('Last successful run finished')}:{' '}
                                  </dt>
                                  <dd className='inline'>
                                    {formatTimestampToDate(
                                      price.run_finished_at
                                    )}
                                  </dd>
                                </div>
                                <div>
                                  <dt className='inline'>
                                    {t('Last seen at')}:{' '}
                                  </dt>
                                  <dd className='inline'>
                                    {formatTimestampToDate(price.last_seen_at)}
                                  </dd>
                                </div>
                                <div>
                                  <dt className='inline'>
                                    {t('Fetched at')}:{' '}
                                  </dt>
                                  <dd className='inline'>
                                    {formatTimestampToDate(price.fetched_at)}
                                  </dd>
                                </div>
                                <div>
                                  <dt className='inline'>
                                    {t('Effective at')}:{' '}
                                  </dt>
                                  <dd className='inline'>
                                    {formatTimestampToDate(price.effective_at)}
                                  </dd>
                                </div>
                              </dl>
                              <p className='text-muted-foreground mt-1 font-mono'>
                                {price.source_model_name}
                              </p>
                            </div>
                          )
                        })}
                      </div>
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            )
          })}

          {props.entries.length === 0 && (
            <TableRow>
              <TableCell
                colSpan={8}
                className='text-muted-foreground py-10 text-center text-sm'
              >
                {t('No model matches the current filter.')}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}
