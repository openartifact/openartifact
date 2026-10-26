// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'
import { reposTrail, repoTrail } from '../trails'

// `?new=1` on /repos opens the create drawer (built by the repositories slice);
// until then the list page ignores it. `?tab=` selects the repository tab.
export const repoRoutes: RouteRecordRaw[] = [
  {
    path: '/repos',
    name: 'repos',
    component: () => import('../../pages/RepoListPage.vue'),
    meta: { crumbs: reposTrail, requiresAuth: true },
  },
  {
    path: '/repos/:repo',
    name: 'repo-detail',
    component: () => import('../../pages/RepoDetailPage.vue'),
    meta: { crumbs: repoTrail, requiresAuth: true },
  },
]
