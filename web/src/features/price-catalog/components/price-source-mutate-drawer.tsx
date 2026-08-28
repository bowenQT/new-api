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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { CalendarClock, Settings2 } from 'lucide-react'
import { useEffect } from 'react'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { IconBadge } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { createPriceSource, updatePriceSource } from '../api'
import {
  ADAPTER_OPTIONS,
  MIN_SCHEDULE_INTERVAL_SECONDS,
  ROLE_LABEL_KEYS,
  SCOPE_LABEL_KEYS,
  findAdapterOption,
} from '../constants'
import {
  PRICE_SOURCE_FORM_DEFAULTS,
  formValuesToPriceSourcePayload,
  getPriceSourceFormSchema,
  priceSourceToFormValues,
  type PriceSourceFormValues,
} from '../lib/source-form'
import type { PriceSourceView } from '../types'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Absent when creating a source. */
  source?: PriceSourceView
  onSaved: () => void
}

export function PriceSourceMutateDrawer(props: Props) {
  const { t } = useTranslation()
  const isEdit = props.source !== undefined

  const form = useForm<PriceSourceFormValues>({
    resolver: zodResolver(
      getPriceSourceFormSchema(t)
    ) as unknown as Resolver<PriceSourceFormValues>,
    defaultValues: PRICE_SOURCE_FORM_DEFAULTS,
  })

  useEffect(() => {
    if (!props.open) return
    form.reset(
      props.source
        ? priceSourceToFormValues(props.source)
        : PRICE_SOURCE_FORM_DEFAULTS
    )
  }, [form, props.open, props.source])

  const adapterKey = form.watch('adapter_key')
  const scheduleEnabled = form.watch('schedule_enabled')
  const intervalSeconds = form.watch('schedule_interval_seconds')
  const adapter = findAdapterOption(adapterKey)

  const saveMutation = useMutation({
    mutationFn: async (values: PriceSourceFormValues) => {
      const payload = formValuesToPriceSourcePayload(values)
      const res = props.source
        ? await updatePriceSource(props.source.id, payload)
        : await createPriceSource(payload)
      if (!res.success) {
        throw new Error(res.message || t('The price source could not be saved'))
      }
      return res.data
    },
    onSuccess: () => {
      toast.success(
        isEdit ? t('Price source updated') : t('Price source created')
      )
      props.onSaved()
      props.onOpenChange(false)
    },
    onError: (error: unknown) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t('The price source could not be saved')
      )
    },
  })

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent className={sideDrawerContentClassName('sm:max-w-[560px]')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>
            {isEdit ? t('Edit price source') : t('Add price source')}
          </SheetTitle>
          <SheetDescription>
            {t(
              'A price source only records observed prices. It never writes sale pricing.'
            )}
          </SheetDescription>
        </SheetHeader>
        <Form {...form}>
          <form
            id='price-source-form'
            onSubmit={form.handleSubmit((values) =>
              saveMutation.mutate(values)
            )}
            className={sideDrawerFormClassName()}
          >
            <SideDrawerSection>
              <h3 className='flex items-center gap-2 text-sm font-medium'>
                <IconBadge tone='info' size='xs'>
                  <Settings2 />
                </IconBadge>
                {t('Source')}
              </h3>

              <FormField
                control={form.control}
                name='name'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Source name')}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        placeholder={t('e.g. Vercel gateway cost')}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='adapter_key'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Adapter')}</FormLabel>
                    <Select
                      items={ADAPTER_OPTIONS.map((option) => ({
                        value: option.key,
                        label: option.label,
                      }))}
                      value={field.value}
                      onValueChange={field.onChange}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue placeholder={t('Select an adapter')} />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        {ADAPTER_OPTIONS.map((option) => (
                          <SelectItem key={option.key} value={option.key}>
                            {option.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {adapter
                        ? t('Fixed endpoint: {{endpoint}}', {
                            endpoint: adapter.endpoint,
                          })
                        : null}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {adapter && (
                <div className='bg-muted/40 grid gap-1 rounded-md border p-3 text-xs'>
                  <p className='text-muted-foreground'>
                    {t(
                      'Role and scope are fixed by the adapter and cannot be chosen freely.'
                    )}
                  </p>
                  <p>
                    <span className='text-muted-foreground'>{t('Role')}: </span>
                    {t(ROLE_LABEL_KEYS[adapter.role] ?? adapter.role)}
                    <span className='text-muted-foreground'>
                      {' · '}
                      {t('Scope')}:{' '}
                    </span>
                    {t(SCOPE_LABEL_KEYS[adapter.scope] ?? adapter.scope)}
                  </p>
                  {adapter.role === 'curated_reference' && (
                    <p className='text-warning'>
                      {t(
                        'This is a third-party compilation, not an official vendor price.'
                      )}
                    </p>
                  )}
                </div>
              )}

              {adapter?.requiresChannel && (
                <FormField
                  control={form.control}
                  name='channel_id'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Channel ID')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          inputMode='numeric'
                          placeholder={t('e.g. 12')}
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'A supplier cost source must point at an enabled channel. Deleting that channel makes the source orphaned.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}

              <FormField
                control={form.control}
                name='enabled'
                render={({ field }) => (
                  <FormItem className='flex flex-row items-center justify-between gap-4'>
                    <div className='space-y-0.5'>
                      <FormLabel>{t('Enabled')}</FormLabel>
                      <FormDescription>
                        {t('Disabled sources are never synced.')}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
            </SideDrawerSection>

            <SideDrawerSection>
              <h3 className='flex items-center gap-2 text-sm font-medium'>
                <IconBadge tone='info' size='xs'>
                  <CalendarClock />
                </IconBadge>
                {t('Scheduled sync')}
              </h3>

              <FormField
                control={form.control}
                name='schedule_enabled'
                render={({ field }) => (
                  <FormItem className='flex flex-row items-center justify-between gap-4'>
                    <div className='space-y-0.5'>
                      <FormLabel>{t('Run on a schedule')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Scheduled runs only write price snapshots. They can never change sale pricing.'
                        )}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='schedule_interval_seconds'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Interval (seconds)')}</FormLabel>
                    <FormControl>
                      <Input
                        name={field.name}
                        ref={field.ref}
                        onBlur={field.onBlur}
                        value={String(field.value)}
                        type='number'
                        min={0}
                        step={3600}
                        disabled={!scheduleEnabled}
                        onChange={(e) =>
                          field.onChange(
                            Number.parseInt(e.target.value, 10) || 0
                          )
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Minimum {{hours}} hours, enforced by the server. Currently {{current}} hours.',
                        {
                          hours: MIN_SCHEDULE_INTERVAL_SECONDS / 3600,
                          current: (intervalSeconds / 3600).toFixed(1),
                        }
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SideDrawerSection>

            <SideDrawerSection>
              <FormField
                control={form.control}
                name='settings'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Settings (JSON)')}</FormLabel>
                    <FormControl>
                      <Textarea
                        {...field}
                        rows={6}
                        className='font-mono text-xs'
                        placeholder='{"model_mappings":{},"coverage_drop_threshold":0.2,"stale_threshold_seconds":604800}'
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Optional. Only model_mappings, coverage_drop_threshold and stale_threshold_seconds are accepted; credentials and endpoints are rejected.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SideDrawerSection>
          </form>
        </Form>
        <SheetFooter className={sideDrawerFooterClassName()}>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='price-source-form'
            disabled={saveMutation.isPending}
          >
            {saveMutation.isPending ? t('Saving...') : t('Save')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
