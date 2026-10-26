// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'
import { bundlesTrail } from '../trails'

export const bundleRoutes: RouteRecordRaw[] = [
  {
    path: '/bundles',
    name: 'bundles',
    component: () => import('../../pages/BundleListPage.vue'),
    meta: { crumbs: bundlesTrail, requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/bundles/:name/:version',
    name: 'bundle-detail',
    component: () => import('../../pages/BundleDetailPage.vue'),
    meta: { crumbs: [...bundlesTrail, { dynamic: (route) => `${String(route.params.name)} ${String(route.params.version)}` }], requiresAuth: true, requiresAdmin: true },
  },
]
