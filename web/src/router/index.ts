// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useSessionStore } from '../stores/session'

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'dashboard',
    component: () => import('../pages/DashboardPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('../pages/LoginPage.vue'),
    meta: { layout: 'blank' },
  },
  {
    path: '/change-password',
    name: 'change-password',
    component: () => import('../pages/ChangePasswordPage.vue'),
    meta: { requiresAuth: true, layout: 'blank' },
  },
  {
    path: '/repos',
    name: 'repos',
    component: () => import('../pages/RepoListPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/repos/new',
    name: 'repo-new',
    component: () => import('../pages/RepoWizardPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/repos/:name/edit',
    name: 'repo-edit',
    component: () => import('../pages/RepoWizardPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/repos/:name/browse',
    name: 'repo-browse',
    component: () => import('../pages/ArtifactBrowserPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/repos/:name/graph',
    name: 'repo-graph',
    component: () => import('../pages/DependencyGraphPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/repos/:name/trash',
    name: 'repo-trash',
    component: () => import('../pages/TrashPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/repos/:name',
    name: 'repo-detail',
    component: () => import('../pages/RepoDetailPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/repos/:name/packages/:format/:pkg',
    name: 'package-detail',
    component: () => import('../pages/PackageDetailPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/search',
    name: 'search',
    component: () => import('../pages/SearchPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/stats',
    redirect: '/',
  },

  // Admin
  {
    path: '/admin/users',
    name: 'admin-users',
    component: () => import('../pages/admin/UsersPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/groups',
    name: 'admin-groups',
    component: () => import('../pages/admin/GroupsPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/permissions',
    name: 'admin-permissions',
    component: () => import('../pages/admin/PermissionsPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/sso',
    name: 'admin-sso',
    component: () => import('../pages/admin/SSOPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/system',
    name: 'admin-system',
    component: () => import('../pages/admin/SystemPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/gpg',
    name: 'admin-gpg',
    component: () => import('../pages/admin/GpgKeysPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/tokens',
    name: 'admin-tokens',
    component: () => import('../pages/admin/TokensPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/trash',
    name: 'admin-trash',
    component: () => import('../pages/TrashPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/audit',
    name: 'admin-audit',
    component: () => import('../pages/admin/AuditLogPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/storage',
    name: 'admin-storage',
    component: () => import('../pages/admin/StorageBackendsPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/orgs',
    name: 'admin-orgs',
    component: () => import('../pages/admin/OrgsPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/webhooks',
    name: 'admin-webhooks',
    component: () => import('../pages/admin/WebhooksPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/backup',
    name: 'admin-backup',
    component: () => import('../pages/admin/BackupPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/ai',
    name: 'admin-ai',
    component: () => import('../pages/admin/AISettingsPage.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },

  // Security
  {
    path: '/security/vulns',
    name: 'security-vulns',
    component: () => import('../pages/security/VulnScanPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/security/licenses',
    name: 'security-licenses',
    component: () => import('../pages/security/LicenseScanPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/security/secrets',
    name: 'security-secrets',
    component: () => import('../pages/security/SecretScanPage.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/security/ai-analysis',
    name: 'security-ai-analysis',
    component: () => import('../pages/security/AIAnalysisPage.vue'),
    meta: { requiresAuth: true },
  },

  // Settings
  {
    path: '/settings',
    component: () => import('../pages/settings/SettingsLayout.vue'),
    meta: { requiresAuth: true },
    redirect: { name: 'settings-profile' },
    children: [
      {
        path: 'profile',
        name: 'settings-profile',
        component: () => import('../pages/settings/ProfilePage.vue'),
      },
      {
        path: 'password',
        name: 'settings-password',
        component: () => import('../pages/settings/PasswordPage.vue'),
      },
      {
        path: 'tokens',
        name: 'settings-tokens',
        component: () => import('../pages/settings/TokensPage.vue'),
      },
      {
        path: 'notifications',
        name: 'settings-notifications',
        component: () => import('../pages/settings/NotificationsPage.vue'),
      },
    ],
  },

  // Legacy redirects
  {
    path: '/profile',
    redirect: '/settings/profile',
  },

  // Catch-all: must stay last
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('../pages/NotFoundPage.vue'),
    meta: { requiresAuth: true },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

let sessionInitialized = false

router.beforeEach(async (to) => {
  const session = useSessionStore()

  if (!sessionInitialized) {
    sessionInitialized = true
    if (!session.isAuthenticated) {
      await session.fetchMe()
    }
  }

  if (to.meta.requiresAuth && !session.isAuthenticated) {
    return { name: 'login', query: { next: to.fullPath } }
  }

  // Force users with the must_change_password flag through the dedicated screen
  // before any other authenticated route is allowed.
  if (
    session.isAuthenticated &&
    session.mustChangePassword &&
    to.name !== 'change-password' &&
    to.name !== 'login'
  ) {
    return { name: 'change-password' }
  }

  // Already past the gate: don't let the change-password page show for users
  // who don't need it.
  if (to.name === 'change-password' && session.isAuthenticated && !session.mustChangePassword) {
    return { name: 'dashboard' }
  }

  if (to.meta.requiresAdmin && !session.isAdmin) {
    return { name: 'dashboard' }
  }
})

export default router
