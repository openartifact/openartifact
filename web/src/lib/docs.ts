// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Where the web UI links into the published documentation. This is the one
 * place that knows which docs page explains which screen; the base URL (and so
 * the docs version) comes from the server's `docs_url` (`/api/v1/system/info`),
 * never from anywhere else.
 *
 * A path is a page under `docs/` without `.md` (`getting-started/installation`),
 * or a directory with a trailing slash (`formats/` is `formats/README.md`);
 * `null` is an explicit "no page": the route shows no help link.
 */
export type DocsPath = string | null

export const ROUTE_DOCS: Readonly<Record<string, DocsPath>> = {
  // Overview and auth screens: no page explains them (the dashboard is the
  // home of the app, login/password-change/not-found render without a header).
  dashboard: null,
  login: null,
  'change-password': null,
  'not-found': null,
  // The search page has no dedicated docs page (properties search is API-only).
  search: null,

  // Repositories
  repos: 'repositories/hosted-proxy-group',
  'repo-new': 'repositories/hosted-proxy-group',
  'repo-edit': 'repositories/hosted-proxy-group',
  'repo-settings': 'repositories/hosted-proxy-group',
  'repo-detail': 'formats/client-setup',
  'repo-browse': 'formats/',
  'repo-graph': 'security/sbom',
  'repo-trash': 'operations/trash',
  'package-detail': 'formats/',

  // Projects and bundles
  projects: 'security/projects',
  'project-detail': 'security/projects',
  bundles: 'repositories/release-bundles',
  'bundle-detail': 'repositories/release-bundles',

  // Administration
  'admin-users': 'security/rbac',
  'admin-groups': 'security/rbac',
  'admin-permissions': 'security/rbac',
  'admin-sso': 'security/sso',
  'admin-system': 'operations/monitoring',
  'admin-gpg': 'security/signing',
  'admin-tokens': 'security/authentication',
  'admin-trash': 'operations/trash',
  'admin-audit': 'operations/monitoring',
  'admin-storage': 'operations/storage',
  'admin-storage-tiers': 'operations/storage-tiering',
  'admin-projects': 'security/projects',
  'admin-webhooks': 'operations/webhooks',
  'admin-backup': 'operations/backup',
  'admin-ai': 'integrations/ai-analysis',
  'admin-dependency-track': 'security/dependency-track',

  // Security
  'security-vulns': 'security/scanning',
  'security-licenses': 'security/licenses',
  'security-secrets': 'security/scanning',
  'security-ai-analysis': 'integrations/ai-analysis',

  // Settings
  'settings-profile': 'security/authentication',
  'settings-password': 'security/authentication',
  'settings-tokens': 'security/authentication',
}

/**
 * Absolute docs URL for a route, or null when the docs links are disabled
 * (`docsBase` empty) or the route has no page. `docsBase` is the server's
 * `docs_url` and ends in a slash.
 */
export function docsUrlFor(docsBase: string | null | undefined, routeName: string | symbol | null | undefined): string | null {
  if (!docsBase || typeof routeName !== 'string') return null
  const path = Object.hasOwn(ROUTE_DOCS, routeName) ? ROUTE_DOCS[routeName] : null
  if (path === null) return null
  return docsBase.endsWith('/') ? docsBase + path : `${docsBase}/${path}`
}
