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
import { useMutation } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Spinner } from '@/components/ui/spinner'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatTimestampToDate } from '@/lib/format'

import { previewPriceSource, syncPriceSource } from '../api'
import {
  CHANGE_LABEL_KEYS,
  ENTRY_STATUS_LABEL_KEYS,
  METADATA_KEY_UNSUPPORTED_DIMENSIONS,
  RUN_STATUS_LABEL_KEYS,
} from '../constants'
import type {
  PricePreviewResponse,
  PriceSourceView,
  PriceSyncResponse,
} from '../types'
import { UnsupportedDimensionsBadge } from './catalog-badges'

/** Preview rows shown inline; the full diff can be large. */
const PREVIEW_ITEM_LIMIT = 200

/** Badge tone per committed run status; an unknown status stays neutral. */
const RUN_STATUS_BADGE_VARIANTS: Record<
  string,
  'destructive' | 'warning' | 'secondary'
> = {
  failed: 'destructive',
  partial: 'warning',
  succeeded: 'secondary',
}

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  source: PriceSourceView
  onSynced: () => void
}

function SummaryStat(props: { label: string; value: number }) {
  return (
    <div className='bg-muted/40 rounded-md border px-3 py-2'>
      <div className='text-muted-foreground text-xs'>{props.label}</div>
      <div className='text-sm font-semibold tabular-nums'>{props.value}</div>
    </div>
  )
}

/**
 * Two-phase manual sync (spec §8.1). Preview fetches and diffs without
 * persisting anything; commit re-fetches server-side and only writes when the
 * recomputed result still matches the preview token's claim.
 */
export function PriceSourceSyncDialog(props: Props) {
  const { t } = useTranslation()
  const [preview, setPreview] = useState<PricePreviewResponse | null>(null)
  const [syncResult, setSyncResult] = useState<PriceSyncResponse | null>(null)

  const previewMutation = useMutation({
    mutationFn: async () => {
      const res = await previewPriceSource(props.source.id)
      if (!res.success || !res.data) {
        throw new Error(res.message || t('The preview could not be produced'))
      }
      return res.data
    },
    onSuccess: (data) => {
      setSyncResult(null)
      setPreview(data)
    },
    onError: (error: unknown) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t('The preview could not be produced')
      )
    },
  })

  const syncMutation = useMutation({
    mutationFn: async (previewToken: string) => {
      const res = await syncPriceSource(props.source.id, previewToken)
      if (!res.success || !res.data) {
        throw new Error(res.message || t('The sync could not be committed'))
      }
      return res.data
    },
    onSuccess: (data) => {
      setSyncResult(data)
      // The token is single use: force a fresh preview before another commit.
      setPreview(null)
      // The run is recorded either way, so the source list is refreshed even
      // when the gate refused the commit.
      props.onSynced()
      // A refused commit comes back inside a success envelope (spec §8.1), so
      // the run status decides how it is reported.
      if (data.status === 'failed') {
        toast.error(t('The sync run failed and nothing was written'))
        return
      }
      if (data.status === 'partial') {
        toast.warning(t('Sync committed, but some models were left out'))
        return
      }
      toast.success(t('Sync committed'))
    },
    onError: (error: unknown) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t('The sync could not be committed')
      )
    },
  })

  const previewReset = previewMutation.reset
  const syncReset = syncMutation.reset
  useEffect(() => {
    if (props.open) return
    setPreview(null)
    setSyncResult(null)
    previewReset()
    syncReset()
  }, [previewReset, props.open, syncReset])

  const items = preview?.items ?? []
  const shownItems = items.slice(0, PREVIEW_ITEM_LIMIT)
  // The server refuses to commit a sync for an orphaned or a disabled source
  // (`checkSourceRunnableForCommit`), so the dialog refuses it too and states
  // which of the two reasons applies. Preview stays available for diagnosis.
  const commitBlocked = props.source.orphaned || !props.source.enabled

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='max-h-[85dvh] w-full max-w-[calc(100%-2rem)] overflow-y-auto sm:max-w-3xl'>
        <DialogHeader>
          <DialogTitle>
            {t('Preview and sync: {{name}}', { name: props.source.name })}
          </DialogTitle>
          <DialogDescription>
            {t(
              'Preview fetches and normalizes the source without writing anything. Only after you confirm does the server re-fetch and commit the snapshots.'
            )}
          </DialogDescription>
        </DialogHeader>

        <div className='space-y-4'>
          {props.source.orphaned && (
            <Alert variant='destructive'>
              <TriangleAlert aria-hidden='true' />
              <AlertTitle>{t('Channel deleted (orphaned)')}</AlertTitle>
              <AlertDescription>
                {t(
                  'This source points at a channel that no longer exists. Preview stays available for diagnosis, but committing a sync and scheduling are refused.'
                )}
              </AlertDescription>
            </Alert>
          )}

          {!props.source.enabled && (
            <Alert variant='destructive'>
              <TriangleAlert aria-hidden='true' />
              <AlertTitle>{t('Source is disabled')}</AlertTitle>
              <AlertDescription>
                {t(
                  'This source is disabled. Preview stays available for diagnosis, but the server refuses to commit a sync until the source is enabled again.'
                )}
              </AlertDescription>
            </Alert>
          )}

          {preview === null && syncResult === null && (
            <p className='text-muted-foreground text-sm'>
              {t('Run a preview to see what a sync would change.')}
            </p>
          )}

          {syncResult !== null && (
            <div className='space-y-3'>
              <div className='flex flex-wrap items-center gap-2'>
                <Badge variant='outline'>
                  {t('Run #{{id}}', { id: syncResult.run_id })}
                </Badge>
                <Badge variant={RUN_STATUS_BADGE_VARIANTS[syncResult.status]}>
                  {t(
                    RUN_STATUS_LABEL_KEYS[syncResult.status] ??
                      syncResult.status
                  )}
                </Badge>
              </div>

              {syncResult.status === 'failed' && (
                <Alert variant='destructive'>
                  <TriangleAlert aria-hidden='true' />
                  <AlertTitle>
                    {t('The sync run failed and nothing was written')}
                  </AlertTitle>
                  <AlertDescription>
                    {syncResult.valid_count === 0
                      ? t(
                          'The fetch returned no valid observation, so no snapshot was committed. The previous prices are kept.'
                        )
                      : t(
                          'Model coverage fell further than the configured gate allows, so the server refused this commit. The previous prices are kept.'
                        )}
                  </AlertDescription>
                </Alert>
              )}

              {syncResult.status === 'partial' && (
                <Alert>
                  <TriangleAlert aria-hidden='true' />
                  <AlertTitle>{t('Some models were left out')}</AlertTitle>
                  <AlertDescription>
                    {t(
                      'The valid observations were committed. Unsupported, rejected, and missing models were not, and their previous snapshots are kept.'
                    )}
                  </AlertDescription>
                </Alert>
              )}
              <div className='grid grid-cols-2 gap-2 sm:grid-cols-4'>
                <SummaryStat
                  label={t('Discovered')}
                  value={syncResult.discovered_count}
                />
                <SummaryStat
                  label={t('Valid')}
                  value={syncResult.valid_count}
                />
                <SummaryStat
                  label={t('New snapshots')}
                  value={syncResult.new_snapshot_count}
                />
                <SummaryStat
                  label={t('Idempotent hits')}
                  value={syncResult.idempotent_hit_count}
                />
                <SummaryStat
                  label={t('Unsupported')}
                  value={syncResult.unsupported_count}
                />
                <SummaryStat
                  label={t('Rejected')}
                  value={syncResult.rejected_count}
                />
                <SummaryStat
                  label={t('Missing upstream')}
                  value={syncResult.missing_count}
                />
              </div>
              {syncResult.error_summary ? (
                <p className='text-destructive text-xs'>
                  {syncResult.error_summary}
                </p>
              ) : null}
            </div>
          )}

          {preview !== null && (
            <div className='space-y-3'>
              <div className='flex flex-wrap items-center gap-2 text-xs'>
                <Badge variant='outline'>
                  {t('Projected run status: {{status}}', {
                    status: preview.projected_run_status,
                  })}
                </Badge>
                <span className='text-muted-foreground'>
                  {t('Preview expires at {{time}}', {
                    time: formatTimestampToDate(preview.expires_at),
                  })}
                </span>
              </div>

              {preview.coverage_drop_exceeded && (
                <Alert variant='destructive'>
                  <TriangleAlert aria-hidden='true' />
                  <AlertTitle>{t('Model coverage dropped')}</AlertTitle>
                  <AlertDescription>
                    {t(
                      'Coverage fell further than the configured gate allows, so the server will refuse this commit. Investigate the source before syncing.'
                    )}
                  </AlertDescription>
                </Alert>
              )}

              {/*
                Not a destructive alert: unlike the coverage gate above, a price
                movement never refuses the commit, and styling it as a refusal
                would tell the admin the sync is about to fail.
              */}
              {preview.price_jump_count > 0 && (
                <Alert>
                  <TriangleAlert aria-hidden='true' />
                  <AlertTitle>{t('Upstream price changed sharply')}</AlertTitle>
                  <AlertDescription>
                    {t(
                      'This sync would record {{count}} price change(s) past the configured threshold. The commit is not blocked; review the changed models afterwards.',
                      { count: preview.price_jump_count }
                    )}
                  </AlertDescription>
                </Alert>
              )}

              <div className='grid grid-cols-2 gap-2 sm:grid-cols-4'>
                <SummaryStat
                  label={t('Discovered')}
                  value={preview.discovered_count}
                />
                <SummaryStat label={t('Valid')} value={preview.valid_count} />
                <SummaryStat label={t('New')} value={preview.new_count} />
                <SummaryStat
                  label={t('Changed')}
                  value={preview.changed_count}
                />
                <SummaryStat
                  label={t('Unchanged')}
                  value={preview.unchanged_count}
                />
                <SummaryStat
                  label={t('Unsupported')}
                  value={preview.unsupported_count}
                />
                <SummaryStat
                  label={t('Rejected')}
                  value={preview.rejected_count}
                />
                <SummaryStat
                  label={t('Missing upstream')}
                  value={preview.missing_count}
                />
              </div>

              {preview.missing.length > 0 && (
                <div className='rounded-md border p-3'>
                  <h4 className='text-sm font-medium'>
                    {t('Models missing from this fetch')}
                  </h4>
                  <p className='text-muted-foreground mt-1 text-xs'>
                    {t(
                      'Their previous snapshots are kept; a missing model is never read as a zero cost.'
                    )}
                  </p>
                  <p className='mt-2 font-mono text-xs break-words'>
                    {preview.missing.join(', ')}
                  </p>
                </div>
              )}

              <div className='overflow-x-auto rounded-md border'>
                <Table className='min-w-[720px]'>
                  <TableHeader>
                    <TableRow className='bg-muted/40 hover:bg-muted/40'>
                      <TableHead className='h-9 px-3 text-xs'>
                        {t('Source model')}
                      </TableHead>
                      <TableHead className='h-9 text-xs'>
                        {t('Canonical model')}
                      </TableHead>
                      <TableHead className='h-9 text-xs'>
                        {t('Status')}
                      </TableHead>
                      <TableHead className='h-9 text-xs'>
                        {t('Change')}
                      </TableHead>
                      <TableHead className='h-9 pr-3 text-xs'>
                        {t('Notes')}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {shownItems.map((item) => (
                      <TableRow
                        key={`${item.source_model_name}-${item.status}-${item.change ?? ''}`}
                      >
                        <TableCell className='px-3 py-2 font-mono text-xs'>
                          {item.source_model_name}
                        </TableCell>
                        <TableCell className='py-2 font-mono text-xs'>
                          {item.canonical_model_name || '-'}
                        </TableCell>
                        <TableCell className='py-2 text-xs'>
                          {t(
                            ENTRY_STATUS_LABEL_KEYS[item.status] ?? item.status
                          )}
                        </TableCell>
                        <TableCell className='py-2 text-xs'>
                          {item.change
                            ? t(CHANGE_LABEL_KEYS[item.change] ?? item.change)
                            : '-'}
                        </TableCell>
                        <TableCell className='py-2 pr-3 text-xs'>
                          <span className='flex flex-wrap gap-1'>
                            {item.warning_code ? (
                              <Badge variant='warning'>
                                {item.warning_code}
                              </Badge>
                            ) : null}
                            {item.varies_by_provider ? (
                              <Badge variant='warning'>
                                {t(
                                  'Prices differ across providers, cost is not confirmed'
                                )}
                              </Badge>
                            ) : null}
                            <UnsupportedDimensionsBadge
                              dimensions={
                                item.metadata?.[
                                  METADATA_KEY_UNSUPPORTED_DIMENSIONS
                                ]
                              }
                            />
                          </span>
                        </TableCell>
                      </TableRow>
                    ))}
                    {shownItems.length === 0 && (
                      <TableRow>
                        <TableCell
                          colSpan={5}
                          className='text-muted-foreground py-6 text-center text-sm'
                        >
                          {t('The preview returned no model rows.')}
                        </TableCell>
                      </TableRow>
                    )}
                  </TableBody>
                </Table>
              </div>
              {items.length > shownItems.length && (
                <p className='text-muted-foreground text-xs'>
                  {t('Showing the first {{shown}} of {{total}} rows.', {
                    shown: shownItems.length,
                    total: items.length,
                  })}
                </p>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            type='button'
            variant='outline'
            onClick={() => previewMutation.mutate()}
            disabled={previewMutation.isPending || syncMutation.isPending}
          >
            {previewMutation.isPending && <Spinner />}
            {preview === null ? t('Run preview') : t('Refresh preview')}
          </Button>
          <Button
            type='button'
            onClick={() => {
              if (preview) syncMutation.mutate(preview.preview_token)
            }}
            disabled={
              preview === null ||
              commitBlocked ||
              syncMutation.isPending ||
              previewMutation.isPending
            }
          >
            {syncMutation.isPending && <Spinner />}
            {t('Confirm and sync')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
