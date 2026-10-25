// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Route policy of the service worker. Pure and DOM-free: the worker (sw.ts)
// and the unit tests share it, so what the tests pin is what the worker does.

/**
 * Paths the worker never touches: the REST API (which includes the login and
 * SSO endpoints under /api/v1/auth), repository and registry traffic, MCP, and
 * the operational endpoints, and the web app manifest (rendered per request by
 * the server from the branding settings, so it must never be precached).
 * Requests to them go straight to the network without respondWith(), so no response can ever be stored or replayed.
 * Matched on whole path segments: `/api` and `/api/x` match, `/apiary` does not.
 */
const BYPASS_PREFIXES = [
  '/api',
  '/repos',
  '/v2',
  '/mcp',
  '/metrics',
  '/health',
  '/ready',
  '/auth',
  '/sso',
  '/manifest.webmanifest',
] as const

export function isBypassPath(pathname: string): boolean {
  return BYPASS_PREFIXES.some((p) => pathname === p || pathname.startsWith(`${p}/`))
}

/** What a fetch event for a request is answered with. */
type RequestKind =
  /** A precached build asset or shell file: served from the precache. */
  | 'precache'
  /** A page navigation to a SPA route: network, with the offline page as fallback. */
  | 'navigate'
  /** Everything else: left to the browser. */
  | 'bypass'

export interface RequestInfo {
  method: string
  url: string
  /** `Request.mode`; 'navigate' for a top-level page load. */
  mode: string
  /** `Accept` request header. */
  accept?: string | null
}

/**
 * Decides how the worker handles a request. Only same-origin GETs are ever
 * handled, and the bypass prefixes win over everything else, including the
 * precache list.
 */
export function classifyRequest(
  req: RequestInfo,
  origin: string,
  precached: ReadonlySet<string>,
): RequestKind {
  if (req.method !== 'GET') return 'bypass'

  let url: URL
  try {
    url = new URL(req.url)
  } catch {
    return 'bypass'
  }
  if (url.origin !== origin || isBypassPath(url.pathname)) return 'bypass'

  if (precached.has(url.pathname) && url.search === '') return 'precache'

  if (req.mode === 'navigate' && (req.accept ?? '').includes('text/html')) return 'navigate'

  return 'bypass'
}
