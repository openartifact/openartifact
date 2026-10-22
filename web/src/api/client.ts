// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

export class ApiError extends Error {
  status: number
  code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

function csrfToken(): string | undefined {
  for (const cookie of document.cookie.split(';')) {
    const [name, value] = cookie.trim().split('=', 2)
    if (name === 'oa_csrf') return value
  }
}

export function csrfHeader(): Record<string, string> {
  const token = csrfToken()
  return token ? { 'X-CSRF-Token': token } : {}
}

const NO_REDIRECT_401 = new Set([
  '/api/v1/auth/login',
  '/api/v1/auth/change-password',
  '/api/v1/auth/me',
  // Signing out an already expired session is not an error worth a redirect.
  '/api/v1/auth/logout',
])

/** `ApiError.code` of the error thrown for an expired session; the redirect to /login is already under way. */
export const SESSION_EXPIRED = 'session_expired'

/** True for the error `request()` throws after it redirected an expired session to /login. */
export function isSessionExpired(err: unknown): boolean {
  return err instanceof ApiError && err.code === SESSION_EXPIRED
}

let expiring: Promise<void> | null = null

/**
 * Drops the session and sends the user to /login with the current location as
 * `next`. Concurrent expiries (several requests answered 401 at once) share
 * one redirect.
 */
export function expireSession(): Promise<void> {
  expiring ??= (async () => {
    const { useSessionStore } = await import('../stores/session')
    useSessionStore().clearSession()

    const currentPath = window.location.pathname + window.location.search
    if (currentPath !== '/login') {
      const { default: router } = await import('../router')
      await router.push({ name: 'login', query: { next: currentPath } })
    }
  })().finally(() => {
    expiring = null
  })
  return expiring
}

/**
 * Sends a same-origin API call with the standard headers (CSRF, XHR marker,
 * cookies) and handles an expired session: a 401 outside the auth probes
 * clears the session, redirects to login and throws.
 */
async function send(path: string, init: RequestInit): Promise<Response> {
  const headers = new Headers(init.headers)
  headers.set('X-Requested-With', 'XMLHttpRequest')
  for (const [name, value] of Object.entries(csrfHeader())) headers.set(name, value)

  const res = await fetch(path, {
    ...init,
    headers,
    credentials: 'include',
  })

  // These endpoints return 401 for reasons other than an expired session
  // (bad credentials, wrong current password, "not signed in" probe), so
  // their 401s propagate as ApiError without clearing the session.
  const isAuthProbe = NO_REDIRECT_401.has(path)

  if (res.status === 401 && !isAuthProbe) {
    await expireSession()
    throw new ApiError('Session expired', 401, SESSION_EXPIRED)
  }

  return res
}

async function apiError(res: Response): Promise<ApiError> {
  let message = `HTTP ${res.status}`
  let code: string | undefined
  try {
    const body = (await res.json()) as { error?: string; code?: string }
    if (body.error) message = body.error
    if (body.code) code = body.code
  } catch {
    // non-JSON error body: keep the HTTP status message
  }
  return new ApiError(message, res.status, code)
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await send(path, init)

  if (!res.ok) throw await apiError(res)

  if (res.status === 204 || res.headers.get('content-length') === '0') {
    return undefined as T
  }

  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

/** Extracts the attachment filename from a Content-Disposition header value. */
export function attachmentFilename(header: string | null): string | undefined {
  if (!header) return undefined

  const extended = /filename\*\s*=\s*(?:UTF-8|utf-8)''([^;]+)/.exec(header)
  if (extended?.[1]) {
    try {
      return decodeURIComponent(extended[1].trim())
    } catch {
      // malformed escape: fall through to the plain filename
    }
  }

  const plain = /filename\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^;]+))/.exec(header)
  const value = plain?.[1] !== undefined ? plain[1].replace(/\\(.)/g, '$1') : plain?.[2]?.trim()
  return value || undefined
}

/**
 * Checks that a GET would succeed without downloading the body: resolves with
 * the attachment filename once the response headers are in and the request is
 * aborted, or throws the server's ApiError (quarantined, forbidden, missing).
 * Lets the caller hand the actual transfer to the browser (streamed to disk,
 * no Blob in memory) and still report failures in-app.
 */
export async function preflight(path: string): Promise<{ filename?: string }> {
  const controller = new AbortController()
  try {
    const res = await send(path, { signal: controller.signal })
    if (!res.ok) throw await apiError(res)
    return { filename: attachmentFilename(res.headers.get('content-disposition')) }
  } finally {
    controller.abort()
  }
}
