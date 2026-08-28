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
import type { TFunction } from 'i18next'
import { z } from 'zod'

import {
  ADAPTER_OPTIONS,
  DEFAULT_SCHEDULE_INTERVAL_SECONDS,
  MIN_SCHEDULE_INTERVAL_SECONDS,
  findAdapterOption,
} from '../constants'
import type { PriceSourceRequest, PriceSourceView } from '../types'

export interface PriceSourceFormValues {
  name: string
  adapter_key: string
  channel_id: string
  enabled: boolean
  schedule_enabled: boolean
  schedule_interval_seconds: number
  settings: string
}

export const PRICE_SOURCE_FORM_DEFAULTS: PriceSourceFormValues = {
  name: '',
  adapter_key: ADAPTER_OPTIONS[0].key,
  channel_id: '',
  enabled: true,
  schedule_enabled: false,
  schedule_interval_seconds: DEFAULT_SCHEDULE_INTERVAL_SECONDS,
  settings: '',
}

/**
 * Client-side mirror of `upstreamprice.ValidatePriceSourceForWrite`. The
 * server stays authoritative; this only stops the admin from submitting a
 * combination that is already known to be refused (spec §7.1, §8.4).
 */
export function getPriceSourceFormSchema(t: TFunction) {
  return z
    .object({
      name: z
        .string()
        .trim()
        .min(1, t('Source name is required'))
        .max(128, t('Source name must be at most 128 characters')),
      adapter_key: z.string().min(1, t('Adapter is required')),
      channel_id: z.string(),
      enabled: z.boolean(),
      schedule_enabled: z.boolean(),
      schedule_interval_seconds: z.number(),
      settings: z.string(),
    })
    .superRefine((values, ctx) => {
      const adapter = findAdapterOption(values.adapter_key)
      if (!adapter) {
        ctx.addIssue({
          code: 'custom',
          path: ['adapter_key'],
          message: t('Unknown adapter'),
        })
        return
      }

      if (adapter.requiresChannel) {
        const channelId = Number.parseInt(values.channel_id, 10)
        if (!Number.isInteger(channelId) || channelId <= 0) {
          ctx.addIssue({
            code: 'custom',
            path: ['channel_id'],
            message: t('A supplier cost source must reference a channel'),
          })
        }
      }

      // 0 means "no interval configured", which is only acceptable while
      // scheduling stays off.
      const interval = values.schedule_interval_seconds
      const intervalUnset = !values.schedule_enabled && interval === 0
      if (!intervalUnset && interval < MIN_SCHEDULE_INTERVAL_SECONDS) {
        ctx.addIssue({
          code: 'custom',
          path: ['schedule_interval_seconds'],
          message: t('The sync interval must be at least {{hours}} hours', {
            hours: MIN_SCHEDULE_INTERVAL_SECONDS / 3600,
          }),
        })
      }

      const settings = values.settings.trim()
      if (settings === '') return
      try {
        const parsed: unknown = JSON.parse(settings)
        if (
          parsed === null ||
          typeof parsed !== 'object' ||
          Array.isArray(parsed)
        ) {
          ctx.addIssue({
            code: 'custom',
            path: ['settings'],
            message: t('Settings must be a JSON object'),
          })
        }
      } catch {
        ctx.addIssue({
          code: 'custom',
          path: ['settings'],
          message: t('Settings must be valid JSON'),
        })
      }
    })
}

export function priceSourceToFormValues(
  source: PriceSourceView
): PriceSourceFormValues {
  return {
    name: source.name,
    adapter_key: source.adapter_key,
    channel_id: source.channel_id == null ? '' : String(source.channel_id),
    enabled: source.enabled,
    schedule_enabled: source.schedule_enabled,
    schedule_interval_seconds:
      source.schedule_interval_seconds || DEFAULT_SCHEDULE_INTERVAL_SECONDS,
    settings: source.settings ?? '',
  }
}

/**
 * Role and scope are not free-form: each registered adapter admits exactly one
 * pair, so they are derived from the adapter rather than offered as inputs
 * that the server would reject.
 */
export function formValuesToPriceSourcePayload(
  values: PriceSourceFormValues
): PriceSourceRequest {
  const adapter = findAdapterOption(values.adapter_key)
  const channelId = Number.parseInt(values.channel_id, 10)
  const usesChannel = adapter?.requiresChannel === true

  return {
    name: values.name.trim(),
    adapter_key: values.adapter_key,
    role: adapter?.role ?? '',
    scope: adapter?.scope ?? '',
    channel_id: usesChannel && Number.isInteger(channelId) ? channelId : null,
    enabled: values.enabled,
    schedule_enabled: values.schedule_enabled,
    schedule_interval_seconds: values.schedule_interval_seconds,
    settings: values.settings.trim(),
  }
}
