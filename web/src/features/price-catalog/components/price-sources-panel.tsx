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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
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
  listPriceSourceAlerts,
  listPriceSources,
  updatePriceSource,
} from '../api'
import {
  ADAPTER_LABELS,
  ALERT_SOURCE_CONFIG_CHANGED,
  MIN_SCHEDULE_INTERVAL_SECONDS,
  priceCatalogQueryKeys,
} from '../constants'
import { priceAlertDetail } from '../lib/alert-detail'
import {
  formValuesToPriceSourcePayload,
  priceSourceToFormValues,
} from '../lib/source-form'
import type { PriceAlert, PriceSourceView } from '../types'
import { CatalogAlerts } from './catalog-alerts'
import { RoleBadge, ScopeBadge, CatalogFlagBadges } from './catalog-badges'
import { PriceSourceMutateDrawer } from './price-source-mutate-drawer'
import { PriceSourceSyncDialog } from './price-source-sync-dialog'

/**
 * Price source catalog (spec §11.1). Sources are observation feeds: this page
 * fetches, previews, commits and schedules them, and never offers a bulk
 * "apply as sale price" action.
 */
export function PriceSourcesPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editorOpen, setEditorOpen] = useState(false)
  const [editingSource, setEditingSource] = useState<PriceSourceView>()
  const [syncSource, setSyncSource] = useState<PriceSourceView>()

  const sourcesQuery = useQuery({
    queryKey: priceCatalogQueryKeys.sources,
    queryFn: async () => {
      const res = await listPriceSources()
      if (!res.success) {
        throw new Error(res.message || t('We could not load price sources.'))
      }
      return res.data ?? []
    },
    retry: false,
  })

  // Coverage, freshness and the last successful run come from the source list
  // itself (spec §8.3), and the health alerts from the source-alerts endpoint.
  // Neither needs the current-price projection, so opening this page no longer
  // projects a row per model.
  const alertsQuery = useQuery({
    queryKey: priceCatalogQueryKeys.sourceAlerts,
    queryFn: async () => {
      const res = await listPriceSourceAlerts()
      if (!res.success || !res.data) {
        throw new Error(
          res.message || t('We could not load the price catalog.')
        )
      }
      return res.data
    },
    retry: false,
  })

  // Creating, editing, enabling, disabling or syncing a source changes what a
  // comparison would report, so the cached comparisons are dropped with the
  // rest of the catalog rather than left showing costs from before the change.
  const invalidateCatalog = () => {
    void queryClient.invalidateQueries({
      queryKey: priceCatalogQueryKeys.sources,
    })
    void queryClient.invalidateQueries({
      queryKey: priceCatalogQueryKeys.sourceAlerts,
    })
    void queryClient.invalidateQueries({
      queryKey: priceCatalogQueryKeys.compareAll,
    })
  }

  const toggleMutation = useMutation({
    mutationFn: async (input: {
      source: PriceSourceView
      field: 'enabled' | 'schedule_enabled'
      value: boolean
    }) => {
      const values = priceSourceToFormValues(input.source)
      const payload = formValuesToPriceSourcePayload({
        ...values,
        [input.field]: input.value,
      })
      const res = await updatePriceSource(input.source.id, payload)
      if (!res.success) {
        throw new Error(res.message || t('The price source could not be saved'))
      }
      return res.data
    },
    onSuccess: () => {
      toast.success(t('Price source updated'))
      invalidateCatalog()
    },
    onError: (error: unknown) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t('The price source could not be saved')
      )
    },
  })

  const catalogAlerts = useMemo(
    () => alertsQuery.data?.alerts ?? [],
    [alertsQuery.data]
  )
  const alertsBySource = useMemo(() => {
    const index = new Map<number, PriceAlert[]>()
    for (const alert of catalogAlerts) {
      if (!alert.source_id) continue
      const existing = index.get(alert.source_id)
      if (existing) existing.push(alert)
      else index.set(alert.source_id, [alert])
    }
    return index
  }, [catalogAlerts])

  const sources = sourcesQuery.data ?? []
  // The source list is the page's own data; a failing alerts query degrades to
  // an unannotated table rather than hiding the sources behind a skeleton.
  const loading = sourcesQuery.isLoading
  const refreshing =
    (sourcesQuery.isFetching || alertsQuery.isFetching) && !loading

  return (
    <div className='space-y-4'>
      {alertsQuery.isError && (
        <p className='text-warning text-xs'>
          {t(
            'Catalog health alerts could not be loaded, so this list may be missing warnings.'
          )}
        </p>
      )}
      <CatalogAlerts alerts={catalogAlerts} />

      <section className='bg-card overflow-hidden rounded-lg border shadow-xs'>
        <div className='flex flex-col gap-3 border-b px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5'>
          <div className='min-w-0'>
            <h3 className='text-sm font-semibold'>{t('Price sources')}</h3>
            <p className='text-muted-foreground mt-0.5 text-xs'>
              {t(
                'Observed upstream prices. Nothing here changes what users are charged.'
              )}
            </p>
          </div>
          <div className='flex shrink-0 items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => {
                void sourcesQuery.refetch()
                void alertsQuery.refetch()
              }}
              disabled={sourcesQuery.isFetching || alertsQuery.isFetching}
            >
              <RefreshCw
                data-icon='inline-start'
                className={cn('size-3.5', refreshing && 'animate-spin')}
                aria-hidden='true'
              />
              {t('Refresh')}
            </Button>
            <Button
              type='button'
              size='sm'
              onClick={() => {
                setEditingSource(undefined)
                setEditorOpen(true)
              }}
            >
              <Plus
                data-icon='inline-start'
                className='size-3.5'
                aria-hidden='true'
              />
              {t('Add price source')}
            </Button>
          </div>
        </div>

        <div aria-busy={sourcesQuery.isFetching || alertsQuery.isFetching}>
          {loading && (
            <div className='space-y-2 p-4 sm:p-5'>
              {['a', 'b', 'c'].map((slot) => (
                <Skeleton key={slot} className='h-10 w-full rounded-md' />
              ))}
            </div>
          )}

          {!loading && sourcesQuery.isError && (
            <ErrorState
              title={t('We could not load price sources.')}
              description={
                sourcesQuery.error instanceof Error
                  ? sourcesQuery.error.message
                  : undefined
              }
              onRetry={() => {
                void sourcesQuery.refetch()
              }}
              className='min-h-[240px]'
            />
          )}

          {!loading && !sourcesQuery.isError && sources.length === 0 && (
            <p className='text-muted-foreground px-4 py-10 text-center text-sm sm:px-5'>
              {t('No price source has been registered yet.')}
            </p>
          )}

          {!loading && !sourcesQuery.isError && sources.length > 0 && (
            <div className='overflow-x-auto'>
              <Table className='min-w-[1180px]'>
                <TableHeader>
                  <TableRow className='bg-muted/40 hover:bg-muted/40'>
                    <TableHead className='h-9 w-[240px] px-4 text-xs'>
                      {t('Source name')}
                    </TableHead>
                    <TableHead className='h-9 w-[190px] text-xs'>
                      {t('Role')}
                    </TableHead>
                    <TableHead className='h-9 w-[110px] text-xs'>
                      {t('Scope')}
                    </TableHead>
                    <TableHead className='h-9 w-[130px] text-xs'>
                      {t('Channel')}
                    </TableHead>
                    <TableHead className='h-9 w-[110px] text-xs'>
                      {t('Coverage')}
                    </TableHead>
                    <TableHead className='h-9 w-[200px] text-xs'>
                      {t('Last successful run')}
                    </TableHead>
                    <TableHead className='h-9 w-[200px] text-xs'>
                      {t('Last failure')}
                    </TableHead>
                    <TableHead className='h-9 w-[150px] text-xs'>
                      {t('Enabled')}
                    </TableHead>
                    <TableHead className='h-9 w-[200px] text-xs'>
                      {t('Scheduled sync')}
                    </TableHead>
                    <TableHead className='h-9 w-[180px] pr-4 text-xs'>
                      {t('Actions')}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sources.map((source) => {
                    const sourceAlerts = alertsBySource.get(source.id) ?? []
                    return (
                      <TableRow key={source.id} className='hover:bg-muted/30'>
                        <TableCell className='px-4 py-3 align-top'>
                          <div className='space-y-1'>
                            <div className='font-medium'>{source.name}</div>
                            <div className='text-muted-foreground font-mono text-[11px]'>
                              {ADAPTER_LABELS[source.adapter_key] ??
                                source.adapter_key}
                              {' · '}
                              {source.adapter_key}
                            </div>
                            <CatalogFlagBadges
                              flags={{
                                stale: source.stale,
                                orphaned: source.orphaned,
                                missing: source.missing_count > 0,
                                sourceConfigChanged: sourceAlerts.some(
                                  (alert) =>
                                    alert.code === ALERT_SOURCE_CONFIG_CHANGED
                                ),
                              }}
                              className='flex flex-wrap gap-1'
                            />
                            {sourceAlerts.map((alert) => (
                              <div
                                key={`${alert.code}-${alert.detail}`}
                                className='text-destructive text-[11px]'
                              >
                                {priceAlertDetail(t, alert)}
                              </div>
                            ))}
                          </div>
                        </TableCell>
                        <TableCell className='py-3 align-top'>
                          <RoleBadge role={source.role} />
                        </TableCell>
                        <TableCell className='py-3 align-top'>
                          <ScopeBadge scope={source.scope} />
                        </TableCell>
                        <TableCell className='py-3 align-top text-xs'>
                          {source.channel_id == null
                            ? t('Not linked')
                            : `#${source.channel_id}`}
                        </TableCell>
                        <TableCell className='py-3 align-top text-xs tabular-nums'>
                          <div>
                            {t('{{count}} models', {
                              count: source.coverage_count,
                            })}
                          </div>
                          {source.missing_count > 0 && (
                            <div className='text-warning'>
                              {t('{{count}} missing', {
                                count: source.missing_count,
                              })}
                            </div>
                          )}
                        </TableCell>
                        <TableCell className='text-muted-foreground py-3 align-top text-xs'>
                          {source.last_success_finished_at
                            ? formatTimestampToDate(
                                source.last_success_finished_at
                              )
                            : t('Never')}
                          {source.last_success_run_id ? (
                            <div className='font-mono text-[11px]'>
                              {t('Run #{{id}}', {
                                id: source.last_success_run_id,
                              })}
                            </div>
                          ) : null}
                        </TableCell>
                        <TableCell className='py-3 align-top text-xs'>
                          {source.last_error_at ? (
                            <div className='space-y-0.5'>
                              <div className='text-muted-foreground'>
                                {formatTimestampToDate(source.last_error_at)}
                              </div>
                              <div
                                className='text-destructive max-w-[180px] truncate'
                                title={source.last_error_summary}
                              >
                                {source.last_error_summary || '-'}
                              </div>
                            </div>
                          ) : (
                            <span className='text-muted-foreground'>-</span>
                          )}
                        </TableCell>
                        <TableCell className='py-3 align-top'>
                          <Switch
                            checked={source.enabled}
                            disabled={toggleMutation.isPending}
                            onCheckedChange={(checked) =>
                              toggleMutation.mutate({
                                source,
                                field: 'enabled',
                                value: !!checked,
                              })
                            }
                            aria-label={t('Enabled')}
                          />
                        </TableCell>
                        <TableCell className='py-3 align-top'>
                          <div className='space-y-1'>
                            <Switch
                              checked={source.schedule_enabled}
                              disabled={
                                source.orphaned ||
                                toggleMutation.isPending ||
                                (!source.schedule_enabled &&
                                  source.schedule_interval_seconds <
                                    MIN_SCHEDULE_INTERVAL_SECONDS)
                              }
                              onCheckedChange={(checked) =>
                                toggleMutation.mutate({
                                  source,
                                  field: 'schedule_enabled',
                                  value: !!checked,
                                })
                              }
                              aria-label={t('Scheduled sync')}
                            />
                            <div className='text-muted-foreground text-[11px]'>
                              {source.schedule_interval_seconds > 0
                                ? t('Every {{hours}} h', {
                                    hours: (
                                      source.schedule_interval_seconds / 3600
                                    ).toFixed(1),
                                  })
                                : t('Not set')}
                            </div>
                            {source.orphaned && (
                              <div className='text-muted-foreground text-[11px]'>
                                {t(
                                  'Scheduling is unavailable while the source is orphaned.'
                                )}
                              </div>
                            )}
                            {!source.orphaned &&
                              !source.schedule_enabled &&
                              source.schedule_interval_seconds <
                                MIN_SCHEDULE_INTERVAL_SECONDS && (
                                <div className='text-muted-foreground text-[11px]'>
                                  {t(
                                    'Set an interval of at least {{hours}} hours in Edit before scheduling.',
                                    {
                                      hours:
                                        MIN_SCHEDULE_INTERVAL_SECONDS / 3600,
                                    }
                                  )}
                                </div>
                              )}
                          </div>
                        </TableCell>
                        <TableCell className='py-3 pr-4 align-top'>
                          <div className='flex flex-wrap gap-2'>
                            <Button
                              type='button'
                              size='sm'
                              variant='outline'
                              onClick={() => setSyncSource(source)}
                            >
                              {t('Preview / sync')}
                            </Button>
                            <Button
                              type='button'
                              size='sm'
                              variant='ghost'
                              onClick={() => {
                                setEditingSource(source)
                                setEditorOpen(true)
                              }}
                            >
                              {t('Edit')}
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </div>

        <div className='text-muted-foreground border-t px-4 py-3 text-xs sm:px-5'>
          <Badge variant='outline' className='mr-2'>
            {t('Read-only catalog')}
          </Badge>
          {t(
            'Promoting a catalog price to a sale price is a separate, explicit action and is not available on this page.'
          )}
        </div>
      </section>

      <PriceSourceMutateDrawer
        open={editorOpen}
        onOpenChange={setEditorOpen}
        source={editingSource}
        onSaved={invalidateCatalog}
      />

      {syncSource !== undefined && (
        <PriceSourceSyncDialog
          open={syncSource !== undefined}
          onOpenChange={(open) => {
            if (!open) setSyncSource(undefined)
          }}
          source={syncSource}
          onSynced={invalidateCatalog}
        />
      )}
    </div>
  )
}
