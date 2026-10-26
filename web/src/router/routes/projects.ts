// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteRecordRaw } from 'vue-router'
import { projectsTrail, projectTrail } from '../trails'

export const projectRoutes: RouteRecordRaw[] = [
  {
    path: '/projects',
    name: 'projects',
    component: () => import('../../pages/ProjectListPage.vue'),
    meta: { crumbs: projectsTrail, requiresAuth: true },
  },
  {
    path: '/projects/:project',
    name: 'project-detail',
    component: () => import('../../pages/ProjectHomePage.vue'),
    meta: { crumbs: projectTrail, requiresAuth: true },
  },
]
