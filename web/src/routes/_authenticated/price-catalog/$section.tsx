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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { PriceCatalog } from '@/features/price-catalog'
import {
  PRICE_CATALOG_DEFAULT_SECTION,
  PRICE_CATALOG_SECTION_IDS,
} from '@/features/price-catalog/section-registry'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

// Every catalog API is RootAuth-only (spec §12), so the page is root-only too.
export const Route = createFileRoute('/_authenticated/price-catalog/$section')({
  beforeLoad: ({ params }) => {
    const { auth } = useAuthStore.getState()

    if (auth.user?.role !== ROLE.SUPER_ADMIN) {
      throw redirect({
        to: '/403',
      })
    }

    const validSections: readonly string[] = PRICE_CATALOG_SECTION_IDS
    if (!validSections.includes(params.section)) {
      throw redirect({
        to: '/price-catalog/$section',
        params: { section: PRICE_CATALOG_DEFAULT_SECTION },
      })
    }
  },
  component: PriceCatalog,
})
