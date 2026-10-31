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
type DocsPath = string | null

export const ROUTE_DOCS: Readonly<Record<string, DocsPath>> = {
  // Overview and auth screens: no page explains them (the dashboard is the
  // home of the app, login/password-change/not-found render without a header).
  dashboard: null,
  login: null,
  'change-password': null,
  'not-found': null,
  // The package search has no dedicated docs page (properties search is API-only).
  packages: null,

  // Repositories and artifacts
  repos: 'repositories/hosted-proxy-group',
  'repo-detail': 'formats/client-setup',
  artifact: 'formats/',
  'artifact-version': 'formats/',

  // Projects and bundles
  projects: 'security/projects',
  'project-detail': 'security/projects',
  bundles: 'repositories/release-bundles',
  'bundle-detail': 'repositories/release-bundles',

  // Security findings
  security: 'security/scanning',

  // Settings: the hub, and one entry per section id (`settings-<id>`, see `docsKey`).
  'settings-hub': null,
  'settings-profile': 'security/authentication',
  'settings-password': 'security/authentication',
  'settings-tokens': 'security/authentication',
  'settings-users': 'security/rbac',
  'settings-service-accounts': 'integrations/ci',
  'service-account-detail': 'integrations/ci',
  'settings-groups': 'security/rbac',
  'settings-permissions': 'security/rbac',
  'settings-sso': 'security/sso',
  'settings-project-policy': 'security/projects',
  'settings-backends': 'operations/storage',
  'settings-tiers': 'operations/storage-tiering',
  'settings-quotas': 'repositories/cleanup-and-quotas',
  'settings-cleanup': 'repositories/cleanup-and-quotas',
  'settings-trash': 'operations/trash',
  'settings-backup': 'operations/backup',
  'settings-scanners': 'security/scanning',
  'settings-policies': 'security/licenses',
  'settings-signing': 'security/signing',
  'settings-dependency-track': 'security/dependency-track',
  'settings-webhooks': 'operations/webhooks',
  'settings-federation': 'operations/federation',
  'settings-ai': 'integrations/ai-analysis',
  'settings-branding': 'operations/branding',
  'settings-audit': 'operations/monitoring',
  'settings-health': 'operations/monitoring',
}

/**
 * Docs key of a route: its name, except the generic `/settings/:section` route,
 * which is keyed by section (`settings-users`).
 */
export function docsKey(route: { name?: string | symbol | null; params: Record<string, unknown> }): string | symbol | null | undefined {
  return route.name === 'settings-section' && typeof route.params.section === 'string'
    ? `settings-${route.params.section}`
    : route.name
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
