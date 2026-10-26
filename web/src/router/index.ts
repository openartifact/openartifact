// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createRouter, createWebHistory, type RouteLocationNormalizedLoaded, type RouteRecordRaw } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { useBrandingStore } from '../stores/branding'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'
import { safeNextPath } from '../lib/safeRedirect'
import { resolveCrumbs } from '../lib/crumbs'
import { findSettingsSection } from '../lib/settingsSections'
import { homeRoutes } from './routes/home'
import { repoRoutes } from './routes/repos'
import { packageRoutes } from './routes/packages'
import { projectRoutes } from './routes/projects'
import { bundleRoutes } from './routes/bundles'
import { securityRoutes } from './routes/security'
import { settingsRoutes } from './routes/settings'
import { installLegacyArtifactRedirect, legacyRedirects } from './redirects'

// Route records live in routes/<area>.ts, legacy URLs in redirects.ts; this file
// only assembles them and holds the navigation guards.
const routes: RouteRecordRaw[] = [
  ...homeRoutes,
  ...repoRoutes,
  ...packageRoutes,
  ...projectRoutes,
  ...bundleRoutes,
  ...securityRoutes,
  ...settingsRoutes,
  ...legacyRedirects,

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

// Registered first: a legacy URL is rewritten before the auth gates see it.
installLegacyArtifactRedirect(router)

/** Where to land after a gate (sign-in, password change): a safe in-app path that is not /login itself. */
function landingPath(next: unknown): string {
  const path = safeNextPath(next)
  return router.resolve(path).name === 'login' ? '/' : path
}

/** Sends the user home and says why, instead of dropping them there silently. */
function denied(reasonKey: 'shell.denied.admin' | 'shell.denied.repoCreate') {
  useToastStore().push({ level: 'info', title: t(reasonKey), description: t('shell.denied.hint') })
  return { name: 'dashboard' }
}

router.beforeEach(async (to, from) => {
  const session = useSessionStore()

  // Resolve the session before the first navigation settles, and retry the
  // probe on every navigation after a transient failure. fetchMe() shares one
  // request between concurrent navigations and never throws.
  if (session.probeFailed || (!session.ready && !session.isAuthenticated)) {
    await session.fetchMe()
  }

  if (to.meta.requiresAuth && !session.isAuthenticated) {
    return { name: 'login', query: { next: to.fullPath } }
  }

  // Force users with the must_change_password flag through the dedicated screen
  // before any other authenticated route is allowed, remembering where they were
  // headed.
  if (
    session.isAuthenticated &&
    session.mustChangePassword &&
    to.name !== 'change-password' &&
    to.name !== 'login'
  ) {
    const next = landingPath(to.fullPath)
    return next === '/' ? { name: 'change-password' } : { name: 'change-password', query: { next } }
  }

  // The login form pushes straight to change-password: carry its ?next= along.
  if (
    to.name === 'change-password' &&
    session.mustChangePassword &&
    to.query.next === undefined &&
    from.name === 'login'
  ) {
    const next = landingPath(from.query.next)
    if (next !== '/') return { name: 'change-password', query: { next } }
  }

  // A signed-in user has no business on the login form: send them on to ?next=.
  // (Users locked by must_change_password stay, they are on their way to the
  // password screen.)
  if (to.name === 'login' && session.isAuthenticated && !session.mustChangePassword) {
    return landingPath(to.query.next)
  }

  // Already past the gate: don't let the change-password page show for users
  // who don't need it.
  if (to.name === 'change-password' && session.isAuthenticated && !session.mustChangePassword) {
    return { name: 'dashboard' }
  }

  if (to.meta.requiresAdmin && !session.isAdmin) {
    return denied('shell.denied.admin')
  }

  if (to.name === 'settings-section') {
    const section = findSettingsSection(to.params.section)
    // Admin-only settings sections gate like requiresAdmin routes.
    if (section?.adminOnly && !session.isAdmin) return denied('shell.denied.admin')
    // The access-token list opens on the caller's own tokens unless a type is chosen.
    if (section?.id === 'tokens' && to.query.type === undefined) {
      return { path: to.path, query: { ...to.query, type: 'personal' }, hash: to.hash, replace: true }
    }
  }

  // Creating a repository needs admin or maintainer in some project; the server
  // still checks the chosen project.
  if (to.meta.requiresRepoCreate && !session.canCreateRepo) {
    return denied('shell.denied.repoCreate')
  }
})

// The window title is `<page> · <product name>`: the page is the last breadcrumb
// of the route, the product name comes from the branding store.
router.afterEach((to) => {
  const trail = resolveCrumbs(to as RouteLocationNormalizedLoaded)
  useBrandingStore().setPageTitle(trail[trail.length - 1]?.label ?? '')
})

export default router
