// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'
import { repoTrail } from '../trails'

const packagesTrail = [{ key: 'nav.search' as const, to: '/packages' }]

// Package names may contain slashes (npm scope, docker image path, go module), so
// `:pkg(.+)` matches raw slashes. Build links with `artifactPath` (lib/artifactPath.ts).
// Old `/repos/:n/packages/:format/:pkg?version=` URLs are mapped by installLegacyArtifactRedirect (redirects.ts).
export const packageRoutes: RouteRecordRaw[] = [
  {
    path: '/packages',
    name: 'packages',
    component: () => import('../../pages/SearchPage.vue'),
    meta: { crumbs: packagesTrail, requiresAuth: true },
  },
  {
    path: '/repos/:repo/packages/:pkg(.+)/versions/:v(.+)',
    name: 'artifact-version',
    component: () => import('../../pages/PackageDetailPage.vue'),
    meta: { crumbs: [...repoTrail, { param: 'pkg' }], requiresAuth: true },
  },
  {
    path: '/repos/:repo/packages/:pkg(.+)',
    name: 'artifact',
    component: () => import('../../pages/PackageDetailPage.vue'),
    meta: { crumbs: [...repoTrail, { param: 'pkg' }], requiresAuth: true },
  },
]
