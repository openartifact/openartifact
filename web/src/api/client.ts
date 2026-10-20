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

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
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

  if (res.status === 204 || res.headers.get('content-length') === '0') {
    return undefined as T
  }

  if (!res.ok) {
    let message = `HTTP ${res.status}`
    let code: string | undefined
    try {
      const body = (await res.json()) as { error?: string; code?: string }
      if (body.error) message = body.error
      if (body.code) code = body.code
    } catch {
      // non-JSON error body: keep the HTTP status message
    }
    throw new ApiError(message, res.status, code)
  }

  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}
