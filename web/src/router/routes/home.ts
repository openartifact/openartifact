// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'

export const homeRoutes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'dashboard',
    component: () => import('../../pages/DashboardPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('../../pages/LoginPage.vue'),
    meta: { layout: 'blank' },
  },
  {
    path: '/change-password',
    name: 'change-password',
    component: () => import('../../pages/ChangePasswordPage.vue'),
    meta: { requiresAuth: true, layout: 'blank' },
  },
]
