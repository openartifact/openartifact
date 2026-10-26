// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'
import { settingsSectionTitle } from '../../lib/settingsSections'

// `/settings` is the hub; `/settings/:section` renders one section from the
// registry (lib/settingsSections.ts). Admin-only sections are gated in the
// router guard (index.ts), the rest are open to every signed-in user.
export const settingsRoutes: RouteRecordRaw[] = [
  {
    path: '/settings',
    name: 'settings-hub',
    component: () => import('../../pages/settings/SettingsHubPage.vue'),
    meta: { crumbs: [{ key: 'nav.settings', to: '/settings' }], requiresAuth: true },
  },
  {
    path: '/settings/:section',
    name: 'settings-section',
    component: () => import('../../pages/settings/SettingsSectionPage.vue'),
    meta: {
      crumbs: [
        { key: 'nav.settings', to: '/settings' },
        { dynamic: (route) => settingsSectionTitle(route.params.section) },
      ],
      requiresAuth: true,
    },
  },
]
