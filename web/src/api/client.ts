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
])

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
    const { useSessionStore } = await import('../stores/session')
    const session = useSessionStore()
    session.clearSession()

    const currentPath = window.location.pathname + window.location.search
    if (currentPath !== '/login') {
      const { default: router } = await import('../router')
      await router.push({ name: 'login', query: { next: currentPath } })
    }

    throw new ApiError('Session expired', 401, 'unauthorized')
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
 * Fetches a binary response (file download). Errors surface as ApiError just
 * like request(); filename comes from the Content-Disposition header.
 */
export async function requestBlob(path: string, init: RequestInit = {}): Promise<{ blob: Blob; filename?: string }> {
  const res = await send(path, init)
  if (!res.ok) throw await apiError(res)

  return { blob: await res.blob(), filename: attachmentFilename(res.headers.get('content-disposition')) }
}
