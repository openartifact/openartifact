// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'

// One findings page, `?type=vuln|license|secret|ai&severity=&status=&repo=`.
// Until the security slice replaces the placeholder, `?type` picks one of the
// four existing scan pages.
export const securityRoutes: RouteRecordRaw[] = [
  {
    path: '/security',
    name: 'security',
    component: () => import('../../pages/security/SecurityPage.vue'),
    meta: { crumbs: [{ key: 'nav.security', to: '/security' }], requiresAuth: true, requiresAdmin: true },
  },
]
