// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'

// One findings inbox, `?type=vuln|license|secret|ai&severity=&status=&repo=&q=`.
// Open to every signed-in user: the server scopes it to the caller's projects
// and answers 403 when there are none (the page shows the forbidden state).
export const securityRoutes: RouteRecordRaw[] = [
  {
    path: '/security',
    name: 'security',
    component: () => import('../../pages/security/SecurityPage.vue'),
    meta: { crumbs: [{ key: 'nav.security', to: '/security' }], requiresAuth: true },
  },
]
