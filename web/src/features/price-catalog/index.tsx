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
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { PriceComparePanel } from './components/price-compare-panel'
import { PriceSourcesPanel } from './components/price-sources-panel'
import {
  PRICE_CATALOG_DEFAULT_SECTION,
  PRICE_CATALOG_SECTION_IDS,
  PRICE_CATALOG_SECTION_TITLE_KEYS,
  type PriceCatalogSectionId,
} from './section-registry'

const route = getRouteApi('/_authenticated/price-catalog/$section')

export function PriceCatalog() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const params = route.useParams()
  const activeSection = (params.section ??
    PRICE_CATALOG_DEFAULT_SECTION) as PriceCatalogSectionId

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        <span className='inline-flex min-w-0 items-center gap-2'>
          <span className='truncate'>
            {t(PRICE_CATALOG_SECTION_TITLE_KEYS[activeSection])}
          </span>
          <Badge variant='outline' className='shrink-0'>
            Root
          </Badge>
        </span>
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='flex min-h-0 flex-col gap-4'>
          <Tabs
            value={activeSection}
            onValueChange={(section) => {
              void navigate({
                to: '/price-catalog/$section',
                params: { section: section as PriceCatalogSectionId },
              })
            }}
          >
            <TabsList className='max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
              {PRICE_CATALOG_SECTION_IDS.map((section) => (
                <TabsTrigger key={section} value={section}>
                  {t(PRICE_CATALOG_SECTION_TITLE_KEYS[section])}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          {activeSection === 'sources' ? (
            <PriceSourcesPanel />
          ) : (
            <PriceComparePanel />
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
