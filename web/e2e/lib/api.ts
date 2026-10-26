// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { requireEnv } from '../env'

/** The parts of a response the suites look at, read once. */
export interface ApiResponse {
  status: number
  ok: boolean
  headers: Headers
  body: Buffer
  text(): string
  json<T = unknown>(): T
}

export interface RequestOptions {
  /** Sent as JSON, unless it is a string (sent as is). */
  data?: unknown
  headers?: Record<string, string>
  params?: Record<string, string>
}

/** A small HTTP client for the server under test: no cookies, only the headers it is given. */
export class Api {
  constructor(
    private readonly base: string,
    private readonly headers: Record<string, string> = {},
  ) {}

  async request(method: string, path: string, opts: RequestOptions = {}): Promise<ApiResponse> {
    const url = new URL(path, this.base)
    for (const [k, v] of Object.entries(opts.params ?? {})) url.searchParams.set(k, v)
    const headers: Record<string, string> = { ...this.headers, ...opts.headers }
    let body: string | undefined
    if (opts.data !== undefined) {
      if (typeof opts.data === 'string') {
        body = opts.data
      } else {
        body = JSON.stringify(opts.data)
        headers['Content-Type'] ??= 'application/json'
      }
    }
    const res = await fetch(url, { method, headers, body, redirect: 'manual' })
    const bytes = Buffer.from(await res.arrayBuffer())
    return {
      status: res.status,
      ok: res.ok,
      headers: res.headers,
      body: bytes,
      text: () => bytes.toString('utf8'),
      json: <T>() => JSON.parse(bytes.toString('utf8')) as T,
    }
  }

  get = (path: string, opts?: RequestOptions) => this.request('GET', path, opts)
  post = (path: string, opts?: RequestOptions) => this.request('POST', path, opts)
  put = (path: string, opts?: RequestOptions) => this.request('PUT', path, opts)
}

/** An API client authenticated with the bootstrap admin token (seeded by scripts/testenv.sh). */
export function adminApi(): Api {
  return new Api(requireEnv('OA_URL'), { Authorization: `Bearer ${requireEnv('OA_ADMIN_TOKEN')}` })
}

/** An API client with no credentials at all: what an anonymous visitor reaches. */
export function anonApi(): Api {
  return new Api(requireEnv('OA_URL'))
}

/** A bearer-token client, as a package manager is. */
export function tokenApi(token: string): Api {
  return new Api(requireEnv('OA_URL'), { Authorization: `Bearer ${token}` })
}

/** The cookies a login response sets, as name/value pairs (httpOnly ones included). */
export function sessionCookies(res: ApiResponse): Array<{ name: string; value: string; httpOnly: boolean }> {
  return res.headers.getSetCookie().map((line) => {
    const [pair = '', ...attrs] = line.split(';').map((s) => s.trim())
    const eq = pair.indexOf('=')
    return {
      name: pair.slice(0, eq),
      value: pair.slice(eq + 1),
      httpOnly: attrs.some((a) => a.toLowerCase() === 'httponly'),
    }
  })
}

/** Signs in through the API; the response carries the session and CSRF cookies. */
export async function login(username: string, password: string): Promise<ApiResponse> {
  const res = await anonApi().post('/api/v1/auth/login', { data: { username, password } })
  if (!res.ok) throw new Error(`login as ${username} failed: HTTP ${res.status}`)
  return res
}

/**
 * Mints a personal access token the way a person would: a session login, then
 * POST /api/v1/auth/tokens with the CSRF header (API tokens cannot mint tokens).
 */
export async function mintToken(username: string, password: string, name: string): Promise<string> {
  const session = await login(username, password)
  const cookies = sessionCookies(session)
  const csrf = cookies.find((c) => c.name === 'oa_csrf')?.value
  if (!csrf) throw new Error('login set no oa_csrf cookie')
  const jar = cookies.map((c) => `${c.name}=${c.value}`).join('; ')
  const res = await anonApi().post('/api/v1/auth/tokens', { data: { name, kind: 'access' }, headers: { 'X-CSRF-Token': csrf, Cookie: jar } })
  if (!res.ok) throw new Error(`token creation failed: HTTP ${res.status} ${res.text()}`)
  const body = res.json<{ token?: string }>()
  if (!body.token) throw new Error('token response has no token')
  return body.token
}

/** A name that is unique per run, so reruns against one server never collide. */
export function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}
