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
import { useQuery } from '@tanstack/react-query'
import { Info, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import { comparePrices, getCurrentPrices, listSaleGroups } from '../api'
import {
  DEFAULT_COMPARE_GROUP,
  DEFAULT_COMPARE_USAGE,
  EXCLUDED_FACTOR_LABEL_KEYS,
  MAX_COMPARE_USAGE_TOKENS,
  priceCatalogQueryKeys,
} from '../constants'
import { usageVectorKey } from '../lib/compare-format'
import type { PriceUsageVector } from '../types'
import { CatalogAlerts } from './catalog-alerts'
import { PriceCompareTable } from './price-compare-table'

const USAGE_FIELDS: { key: keyof PriceUsageVector; labelKey: string }[] = [
  { key: 'p', labelKey: 'Prompt tokens (p)' },
  { key: 'c', labelKey: 'Completion tokens (c)' },
  { key: 'cr', labelKey: 'Cache read tokens (cr)' },
  { key: 'cc', labelKey: 'Cache creation tokens (cc)' },
]

/**
 * Price comparison (spec §11.2). Everything on this page is a limited-basis
 * estimate derived from the catalog and the current sale configuration; it
 * writes nothing and offers no "apply as sale price" action.
 */
export function PriceComparePanel() {
  const { t } = useTranslation()
  const [draftUsage, setDraftUsage] = useState<PriceUsageVector>(
    DEFAULT_COMPARE_USAGE
  )
  const [appliedUsage, setAppliedUsage] = useState<PriceUsageVector>(
    DEFAULT_COMPARE_USAGE
  )
  const [group, setGroup] = useState(DEFAULT_COMPARE_GROUP)
  const [modelFilter, setModelFilter] = useState('')

  const groupsQuery = useQuery({
    queryKey: priceCatalogQueryKeys.groups,
    queryFn: async () => {
      const res = await listSaleGroups()
      if (!res.success) {
        throw new Error(res.message || t('We could not load groups.'))
      }
      return res.data ?? []
    },
    retry: false,
    staleTime: 5 * 60 * 1000,
  })

  const compareQuery = useQuery({
    queryKey: priceCatalogQueryKeys.compare(
      group,
      usageVectorKey(appliedUsage),
      ''
    ),
    queryFn: async () => {
      const res = await comparePrices({ group, usage: appliedUsage })
      if (!res.success || !res.data) {
        throw new Error(
          res.message || t('We could not compare catalog prices.')
        )
      }
      return res.data
    },
    retry: false,
  })

  // `fetched_at` and `effective_at` are only carried by the catalog entries,
  // so the evidence shown per source row is joined in from there (spec §8.3).
  const catalogQuery = useQuery({
    queryKey: priceCatalogQueryKeys.currentPrices,
    queryFn: async () => {
      const res = await getCurrentPrices()
      if (!res.success || !res.data) {
        throw new Error(
          res.message || t('We could not load the price catalog.')
        )
      }
      return res.data
    },
    retry: false,
  })

  const entries = useMemo(() => {
    const all = compareQuery.data?.entries ?? []
    const needle = modelFilter.trim().toLowerCase()
    if (needle === '') return all
    return all.filter((entry) =>
      entry.canonical_model_name.toLowerCase().includes(needle)
    )
  }, [compareQuery.data, modelFilter])

  const groupOptions = useMemo(() => {
    const names = new Set(groupsQuery.data ?? [])
    names.add(DEFAULT_COMPARE_GROUP)
    return [...names].sort((a, b) => a.localeCompare(b))
  }, [groupsQuery.data])

  const usageDirty = usageVectorKey(draftUsage) !== usageVectorKey(appliedUsage)
  const usageInvalid = USAGE_FIELDS.some((field) => {
    const value = draftUsage[field.key]
    return (
      !Number.isFinite(value) || value < 0 || value > MAX_COMPARE_USAGE_TOKENS
    )
  })
  const comparison = compareQuery.data
  const alerts = comparison?.alerts ?? []
  const excludedFactors = comparison?.excluded_factors ?? []

  return (
    <div className='space-y-4'>
      <section className='bg-card space-y-3 rounded-lg border p-4 shadow-xs sm:p-5'>
        <div className='flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
          <div className='min-w-0'>
            <h3 className='text-sm font-semibold'>{t('Comparison basis')}</h3>
            <p className='text-muted-foreground mt-0.5 text-xs'>
              {t(
                'Estimated on a fixed usage vector. This is not equivalent to the online charge.'
              )}
            </p>
          </div>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => {
              void compareQuery.refetch()
              void catalogQuery.refetch()
            }}
            disabled={compareQuery.isFetching}
          >
            <RefreshCw
              data-icon='inline-start'
              className={cn(
                'size-3.5',
                compareQuery.isFetching && 'animate-spin'
              )}
              aria-hidden='true'
            />
            {t('Refresh')}
          </Button>
        </div>

        <div className='grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3'>
          <div className='space-y-1.5'>
            <Label htmlFor='price-compare-group'>{t('Sale group')}</Label>
            <Select
              items={groupOptions.map((name) => ({
                value: name,
                label: name,
              }))}
              value={group}
              onValueChange={(value) =>
                setGroup(value ?? DEFAULT_COMPARE_GROUP)
              }
            >
              <SelectTrigger id='price-compare-group'>
                <SelectValue placeholder={t('Sale group')} />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                {groupOptions.map((name) => (
                  <SelectItem key={name} value={name}>
                    {name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className='space-y-1.5'>
            <Label htmlFor='price-compare-model-filter'>
              {t('Filter models')}
            </Label>
            <Input
              id='price-compare-model-filter'
              value={modelFilter}
              onChange={(e) => setModelFilter(e.target.value)}
              placeholder={t('e.g. gpt-4o')}
            />
          </div>
        </div>

        <div className='grid grid-cols-2 gap-3 lg:grid-cols-4'>
          {USAGE_FIELDS.map((field) => (
            <div key={field.key} className='space-y-1.5'>
              <Label htmlFor={`price-compare-usage-${field.key}`}>
                {t(field.labelKey)}
              </Label>
              <Input
                id={`price-compare-usage-${field.key}`}
                type='number'
                min={0}
                max={MAX_COMPARE_USAGE_TOKENS}
                step={1000}
                value={String(draftUsage[field.key])}
                onChange={(e) =>
                  setDraftUsage((previous) => ({
                    ...previous,
                    [field.key]: Number.parseFloat(e.target.value) || 0,
                  }))
                }
              />
            </div>
          ))}
        </div>

        <div className='flex flex-wrap items-center gap-2'>
          <Button
            type='button'
            size='sm'
            disabled={!usageDirty || usageInvalid}
            onClick={() => setAppliedUsage(draftUsage)}
          >
            {t('Apply usage vector')}
          </Button>
          <Button
            type='button'
            size='sm'
            variant='ghost'
            onClick={() => {
              setDraftUsage(DEFAULT_COMPARE_USAGE)
              setAppliedUsage(DEFAULT_COMPARE_USAGE)
            }}
          >
            {t('Reset to default')}
          </Button>
          {usageInvalid && (
            <span className='text-destructive text-xs'>
              {t('Each dimension must be between 0 and {{max}}.', {
                max: MAX_COMPARE_USAGE_TOKENS,
              })}
            </span>
          )}
        </div>

        {comparison && (
          <div className='text-muted-foreground flex flex-wrap items-center gap-2 text-xs'>
            <Badge variant='outline'>
              {t('Group ratio: {{ratio}}', { ratio: comparison.group_ratio })}
            </Badge>
            {!comparison.group_ratio_configured && (
              <Badge variant='warning'>
                {t('No ratio configured for this group; 1 is assumed')}
              </Badge>
            )}
            <span>
              {t('Generated at {{time}}', {
                time: formatTimestampToDate(comparison.generated_at),
              })}
            </span>
            <span>
              {t('{{shown}} of {{total}} models', {
                shown: entries.length,
                total: comparison.total_models,
              })}
            </span>
          </div>
        )}
      </section>

      <Alert>
        <Info aria-hidden='true' />
        <AlertTitle>
          {t('Estimate only, not equivalent to the online charge')}
        </AlertTitle>
        <AlertDescription>
          <p>
            {t(
              'Amounts project the current sale price and the catalog costs onto the same usage vector. They are not a settlement and no routing information is involved, so the margin is an estimate.'
            )}
          </p>
          {excludedFactors.length > 0 && (
            <p className='mt-1'>
              {t('Excluded from this projection')}:{' '}
              {excludedFactors
                .map((factor) =>
                  t(EXCLUDED_FACTOR_LABEL_KEYS[factor] ?? factor)
                )
                .join(' · ')}
            </p>
          )}
        </AlertDescription>
      </Alert>

      {comparison?.truncated && (
        <Alert>
          <Info aria-hidden='true' />
          <AlertTitle>{t('Result truncated')}</AlertTitle>
          <AlertDescription>
            {t(
              'The catalog holds more models than one comparison returns. Narrow the filter to inspect the rest.'
            )}
          </AlertDescription>
        </Alert>
      )}

      <CatalogAlerts alerts={alerts} />

      {compareQuery.isLoading && (
        <div className='space-y-2'>
          {['a', 'b', 'c', 'd'].map((slot) => (
            <Skeleton key={slot} className='h-10 w-full rounded-md' />
          ))}
        </div>
      )}

      {!compareQuery.isLoading && compareQuery.isError && (
        <ErrorState
          title={t('We could not compare catalog prices.')}
          description={
            compareQuery.error instanceof Error
              ? compareQuery.error.message
              : undefined
          }
          onRetry={() => {
            void compareQuery.refetch()
          }}
          className='min-h-[240px]'
        />
      )}

      {!compareQuery.isLoading && !compareQuery.isError && (
        <PriceCompareTable
          entries={entries}
          catalogEntries={catalogQuery.data?.entries ?? []}
        />
      )}
    </div>
  )
}
