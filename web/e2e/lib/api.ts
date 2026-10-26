// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, type APIRequestContext } from '@playwright/test'
import { requireEnv } from '../env'

// Inside a test, newContext() inherits the role's storageState from the config; a
// session cookie next to a bearer token would put the request under CSRF rules.
const NO_COOKIES = { cookies: [], origins: [] }

/** An API client authenticated with the bootstrap admin token (seeded by scripts/testenv.sh). */
export async function adminApi(): Promise<APIRequestContext> {
  return request.newContext({
    baseURL: requireEnv('OA_URL'),
    storageState: NO_COOKIES,
    extraHTTPHeaders: { Authorization: `Bearer ${requireEnv('OA_ADMIN_TOKEN')}` },
  })
}

/** An API client with no credentials at all: what an anonymous visitor reaches. */
export async function anonApi(): Promise<APIRequestContext> {
  return request.newContext({ baseURL: requireEnv('OA_URL'), storageState: NO_COOKIES })
}

/**
 * Mints a personal access token the way a person would: a session login, then
 * POST /api/v1/auth/tokens with the CSRF header (API tokens cannot mint tokens).
 */
export async function mintToken(username: string, password: string, name: string): Promise<string> {
  const ctx = await request.newContext({ baseURL: requireEnv('OA_URL'), storageState: NO_COOKIES })
  try {
    const login = await ctx.post('/api/v1/auth/login', { data: { username, password } })
    if (!login.ok()) throw new Error(`login as ${username} failed: HTTP ${login.status()}`)
    const csrf = (await ctx.storageState()).cookies.find((c) => c.name === 'oa_csrf')?.value
    if (!csrf) throw new Error('login set no oa_csrf cookie')
    const res = await ctx.post('/api/v1/auth/tokens', { data: { name, kind: 'access' }, headers: { 'X-CSRF-Token': csrf } })
    if (!res.ok()) throw new Error(`token creation failed: HTTP ${res.status()} ${await res.text()}`)
    const body = (await res.json()) as { token?: string }
    if (!body.token) throw new Error('token response has no token')
    return body.token
  } finally {
    await ctx.dispose()
  }
}

/** A name that is unique per run, so reruns against one server never collide. */
export function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}
