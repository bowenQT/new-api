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
import { createSectionRegistry } from '@/features/system-settings/utils/section-registry'

/**
 * Price catalog sections. The former single "upstream price sync" entry is
 * split into the two explicit surfaces required by spec §11: managing
 * observation sources, and comparing catalog prices against sale prices.
 */
const PRICE_CATALOG_SECTIONS = [
  {
    id: 'sources',
    titleKey: 'Price Sources',
    build: () => null, // Content is rendered directly in the page component
  },
  {
    id: 'compare',
    titleKey: 'Price Comparison',
    build: () => null, // Content is rendered directly in the page component
  },
] as const

export type PriceCatalogSectionId =
  (typeof PRICE_CATALOG_SECTIONS)[number]['id']

const priceCatalogRegistry = createSectionRegistry<
  PriceCatalogSectionId,
  Record<string, never>,
  []
>({
  sections: PRICE_CATALOG_SECTIONS,
  defaultSection: 'sources',
  basePath: '/price-catalog',
  urlStyle: 'path',
})

export const PRICE_CATALOG_SECTION_IDS = priceCatalogRegistry.sectionIds
export const PRICE_CATALOG_DEFAULT_SECTION = priceCatalogRegistry.defaultSection
