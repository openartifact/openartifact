// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n'

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

/**
 * True for the 501 a server answers for a feature compiled out of its build (an AI route of a
 * `-tags noai` binary). It is a permanent state, not a failure to retry or report.
 */
export function isFeatureUnavailable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 501 && err.code === 'feature_unavailable'
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

    // Only the path decides whether this already is the login page: a late 401
    // on /login?next=/repos/foo must keep that ?next=, not wrap it in another.
    if (window.location.pathname !== '/login') {
      const { default: router } = await import('../router')
      await router.push({ name: 'login', query: { next: window.location.pathname + window.location.search } })
    }
  })().finally(() => {
    expiring = null
  })
  return expiring
}

/** The client span of one API call (experimental frontend telemetry, web/src/telemetry). */
export interface ClientSpan {
  /** W3C traceparent of the span: the server span of the call becomes its child. */
  readonly traceparent: string
  /** Ends the span with the response status. */
  end(status: number): void
  /** Ends the span as failed: the request did not get a response. */
  fail(err: unknown): void
}

let startClientSpan: ((method: string) => ClientSpan) | undefined

/** Installs the client span factory once frontend telemetry is on (undefined removes it); without one calls carry no traceparent. */
export function setClientSpanFactory(start: ((method: string) => ClientSpan) | undefined): void {
  startClientSpan = start
}

/**
 * Sends a same-origin API call with the standard headers (CSRF, XHR marker,
 * cookies, and traceparent while telemetry is on) and handles an expired
 * session: a 401 outside the auth probes clears the session, redirects to
 * login and throws.
 */
/** The headers every API call carries: the XHR marker, the CSRF token, and `extra` (which wins). */
function standardHeaders(extra?: HeadersInit): Headers {
  const headers = new Headers(extra)
  headers.set('X-Requested-With', 'XMLHttpRequest')
  for (const [name, value] of Object.entries(csrfHeader())) headers.set(name, value)
  return headers
}

/** Handles a 401 outside the auth probes: clears the session, redirects to login and throws. */
async function rejectExpired(path: string, status: number): Promise<void> {
  // These endpoints return 401 for reasons other than an expired session
  // (bad credentials, wrong current password, "not signed in" probe), so
  // their 401s propagate as ApiError without clearing the session.
  if (status !== 401 || NO_REDIRECT_401.has(path)) return
  await expireSession()
  throw new ApiError(t('session.expired'), 401, SESSION_EXPIRED)
}

async function send(path: string, init: RequestInit): Promise<Response> {
  const headers = standardHeaders(init.headers)

  const span = startClientSpan?.(init.method ?? 'GET')
  if (span) headers.set('traceparent', span.traceparent)

  let res: Response
  try {
    res = await fetch(path, {
      ...init,
      headers,
      credentials: 'include',
    })
  } catch (err) {
    span?.fail(err)
    throw err
  }
  span?.end(res.status)

  await rejectExpired(path, res.status)

  return res
}

async function apiError(res: Response): Promise<ApiError> {
  let body: unknown
  try {
    body = await res.json()
  } catch {
    // non-JSON error body: keep the HTTP status message
  }
  return apiErrorFromBody(res.status, body)
}

/** The error of a failed response: the JSON `{error, code}` body's text and code, else "HTTP <status>". */
export function apiErrorFromBody(status: number, body: unknown): ApiError {
  const { error, code } = (body ?? {}) as { error?: string; code?: string }
  return new ApiError(error || `HTTP ${status}`, status, code || undefined)
}

/** Headers of a request whose body is `JSON.stringify(...)`. */
export const JSON_HEADERS = { 'Content-Type': 'application/json' }

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

export interface UploadOptions {
  /** Defaults to POST. */
  method?: string
  /** A FormData (multipart) or a File/Blob sent as the raw body. */
  body: XMLHttpRequestBodyInit
  headers?: Record<string, string>
  /** Aborting rejects the upload with an ApiError('aborted', 0). */
  signal?: AbortSignal
  /** Called with the bytes sent so far while the browser can compute the total. */
  onProgress?: (loaded: number, total: number) => void
}

/**
 * Sends a body with upload progress (fetch cannot report it, so this one call
 * path uses XMLHttpRequest) with the same headers, credentials, error mapping
 * and expired-session handling as request(). Resolves on a 2xx answer.
 */
export function upload(path: string, opts: UploadOptions): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const method = opts.method ?? 'POST'
    const xhr = new XMLHttpRequest()
    xhr.open(method, path)
    xhr.withCredentials = true
    const headers = standardHeaders(opts.headers)
    const span = startClientSpan?.(method)
    if (span) headers.set('traceparent', span.traceparent)
    headers.forEach((value, name) => xhr.setRequestHeader(name, value))

    const onAbort = () => xhr.abort()
    const finish = (settle: () => void) => {
      opts.signal?.removeEventListener('abort', onAbort)
      settle()
    }
    xhr.upload.addEventListener('progress', (ev) => {
      if (ev.lengthComputable) opts.onProgress?.(ev.loaded, ev.total)
    })
    xhr.addEventListener('load', () => {
      span?.end(xhr.status)
      if (xhr.status >= 200 && xhr.status < 300) {
        finish(resolve)
        return
      }
      let body: unknown
      try {
        body = JSON.parse(xhr.responseText)
      } catch {
        // non-JSON body: keep the HTTP status message
      }
      const failure = apiErrorFromBody(xhr.status, body)
      rejectExpired(path, xhr.status).then(
        () => finish(() => reject(failure)),
        (err) => finish(() => reject(err)),
      )
    })
    xhr.addEventListener('error', () => {
      span?.fail(new Error('network error'))
      finish(() => reject(new ApiError('network error', 0)))
    })
    xhr.addEventListener('abort', () => {
      span?.fail(new Error('aborted'))
      finish(() => reject(new ApiError('aborted', 0)))
    })

    if (opts.signal?.aborted) {
      finish(() => reject(new ApiError('aborted', 0)))
      return
    }
    opts.signal?.addEventListener('abort', onAbort, { once: true })
    xhr.send(opts.body)
  })
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

/**
 * Fetches a same-origin GET through the shared request path (CSRF, 401
 * handling, ApiError on failure) and saves the body as `filename`. For small
 * payloads: the body is held in memory; use {@link downloadAttachment} to
 * stream large files.
 */
export async function downloadBlob(path: string, filename: string): Promise<void> {
  const res = await send(path, {})
  if (!res.ok) throw await apiError(res)

  const url = URL.createObjectURL(await res.blob())
  try {
    const link = document.createElement('a')
    link.href = url
    link.download = filename
    document.body.appendChild(link)
    link.click()
    link.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}

/**
 * Downloads a same-origin GET: preflights it so a refusal (403, 400) surfaces
 * as an ApiError, then hands the transfer to the browser, which streams it to
 * disk named by the attachment header (`fallbackName` when it sends none).
 */
export async function downloadAttachment(url: string, fallbackName: string): Promise<void> {
  const { filename } = await preflight(url)

  const link = document.createElement('a')
  link.href = url
  link.download = filename ?? fallbackName
  document.body.appendChild(link)
  link.click()
  link.remove()
}
