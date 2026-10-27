// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type {
  LocationQueryRaw,
  NavigationGuard,
  Router,
  RouteLocationNormalized,
  RouteLocationRaw,
  RouteRecordRaw,
  RouteRecordRedirectOption,
} from 'vue-router'
import { artifactPath } from '../lib/artifactPath'

// Every legacy URL keeps working. Redirects preserve the query string and hash
// unless a query key is remapped; function redirects are used so params with
// special characters survive (repo names are decoded by the router, re-encoded here).

const enc = encodeURIComponent

function go(to: Pick<RouteLocationNormalized, 'query' | 'hash'>, path: string, query: LocationQueryRaw = {}): RouteLocationRaw {
  return { path, query: { ...to.query, ...query }, hash: to.hash }
}

function repoTab(tab: string): RouteRecordRedirectOption {
  return (to) => go(to, `/repos/${enc(String(to.params.n))}`, { tab })
}

function to(path: string, query?: LocationQueryRaw): RouteRecordRedirectOption {
  return (from) => go(from, path, query)
}

/** `/admin/<section>` -> `/settings/<section>`. */
const ADMIN_SECTIONS: Record<string, string> = {
  users: 'users',
  groups: 'groups',
  permissions: 'permissions',
  sso: 'sso',
  webhooks: 'webhooks',
  backup: 'backup',
  ai: 'ai',
  branding: 'branding',
  audit: 'audit',
  'dependency-track': 'dependency-track',
  trash: 'trash',
  system: 'health',
  gpg: 'signing',
  storage: 'backends',
  'storage/tiers': 'tiers',
}

const SECURITY_TYPES: Record<string, string> = {
  vulns: 'vuln',
  licenses: 'license',
  secrets: 'secret',
  'ai-analysis': 'ai',
}

/**
 * Format ids that prefixed the old artifact URL (`/repos/:n/packages/:format/:pkg`).
 * Duplicated from `lib/formats` on purpose: importing it would put every brand
 * icon in the initial chunk. A test keeps the two in sync.
 */
export const LEGACY_FORMAT_IDS: ReadonlySet<string> = new Set([
  'docker', 'npm', 'pypi', 'maven', 'helm', 'cargo', 'rubygems', 'deb',
  'rpm', 'nuget', 'go', 'raw', 'conda', 'conan', 'ivy', 'sbt', 'terraform', 'pacman',
])

const LEGACY_ARTIFACT = /^\/repos\/[^/]+\/packages\/([^/]+)\/([^/]+)\/?$/

/**
 * Guard for the artifact route. Old links were `/repos/:n/packages/:format/:pkg`
 * with the package name as ONE encoded segment (`%2F` for slashes) and the version in
 * `?version=`. New links are `/repos/:repo/packages/:pkg` (+ `/versions/:v`).
 * A URL is legacy when its raw path has exactly two segments after `packages` and
 * the first is a format id; a stray `?version=` is moved into the path either way.
 */
const legacyArtifactRedirect: NavigationGuard = (to) => {
  if (to.name !== 'artifact') return true
  const repo = String(to.params.repo)
  let pkg = String(to.params.pkg)
  let legacy = false
  const m = LEGACY_ARTIFACT.exec(to.path)
  if (m && LEGACY_FORMAT_IDS.has(decodeURIComponent(m[1]))) {
    pkg = decodeURIComponent(m[2])
    legacy = true
  }
  const version = typeof to.query.version === 'string' && to.query.version !== '' ? to.query.version : undefined
  if (!legacy && version === undefined) return true
  const rest = { ...to.query }
  delete rest.version
  return { path: artifactPath(repo, pkg, version), query: rest, hash: to.hash, replace: true }
}

export const legacyRedirects: RouteRecordRaw[] = [
  { path: '/stats', redirect: to('/') },
  { path: '/search', redirect: to('/packages') },
  { path: '/profile', redirect: to('/settings/profile') },

  { path: '/repos/new', redirect: to('/repos', { new: '1' }) },
  { path: '/repos/:n/edit', redirect: repoTab('settings') },
  { path: '/repos/:n/settings', redirect: repoTab('settings') },
  { path: '/repos/:n/browse', redirect: repoTab('packages') },
  { path: '/repos/:n/graph', redirect: repoTab('packages') },
  { path: '/repos/:n/trash', redirect: repoTab('trash') },

  ...Object.entries(SECURITY_TYPES).map(
    ([old, type]): RouteRecordRaw => ({ path: `/security/${old}`, redirect: to('/security', { type }) }),
  ),

  { path: '/admin', redirect: to('/settings') },
  ...Object.entries(ADMIN_SECTIONS).map(
    ([old, section]): RouteRecordRaw => ({ path: `/admin/${old}`, redirect: to(`/settings/${section}`) }),
  ),
  // Admins used to see every token here: no type filter.
  { path: '/admin/tokens', redirect: to('/settings/tokens', { type: 'all' }) },
  { path: '/admin/projects', redirect: to('/projects', { scope: 'all' }) },
]

/**
 * Registers the artifact URL guard. It is a global guard, not the route's
 * `beforeEnter`, because that one does not run when only the params change
 * (a legacy link followed from another artifact page).
 */
export function installLegacyArtifactRedirect(router: Router): void {
  router.beforeEach(legacyArtifactRedirect)
}
