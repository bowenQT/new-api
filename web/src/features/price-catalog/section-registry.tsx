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
/**
 * Price catalog sections. The former single "upstream price sync" entry is
 * split into the two explicit surfaces required by spec §11: managing
 * observation sources, and comparing catalog prices against sale prices.
 *
 * Each section is a route segment of `/price-catalog/$section` whose content is
 * rendered directly by the page component, so the sections are only their ids,
 * their default and their titles.
 */
export const PRICE_CATALOG_SECTION_IDS = ['sources', 'compare'] as const

export type PriceCatalogSectionId = (typeof PRICE_CATALOG_SECTION_IDS)[number]

export const PRICE_CATALOG_DEFAULT_SECTION: PriceCatalogSectionId = 'sources'

/**
 * i18n source keys of the section titles. The page header, the page tabs and
 * the admin sidebar all name the same two surfaces, so they read them here.
 */
export const PRICE_CATALOG_SECTION_TITLE_KEYS: Record<
  PriceCatalogSectionId,
  string
> = {
  sources: 'Price Sources',
  compare: 'Price Comparison',
}
