// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, ApiError } from './client'
import type { User } from '../stores/session'
import type { EndSessionResponse } from './generated/types.gen'

export interface SSOProvider {
  slug: string
  kind: 'oidc' | 'saml'
  display_name: string
  enabled: boolean
}

export function login(credentials: { username: string; password: string }): Promise<User> {
  return request<User>('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(credentials),
  })
}

/**
 * Returns `raw` when it is an absolute http(s) URL, else undefined. The
 * end-session URL comes from the server but ends up in a navigation, so a
 * `javascript:` or `data:` value must never get through.
 */
export function safeEndSessionURL(raw: string | undefined): string | undefined {
  if (!raw) return undefined
  try {
    const url = new URL(raw)
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : undefined
  } catch {
    return undefined
  }
}

/**
 * Ends the server session. Resolves to the IdP's RP-initiated logout URL when
 * the session came from an OIDC provider that supports it (the browser must
 * navigate there to end the IdP session too), else undefined.
 */
export async function logout(): Promise<string | undefined> {
  const res = await request<EndSessionResponse | undefined>('/api/v1/auth/logout', { method: 'POST' })
  return safeEndSessionURL(res?.end_session_url)
}

export function me(): Promise<User> {
  return request<User>('/api/v1/auth/me')
}

export function changePassword(body: { current_password: string; new_password: string }): Promise<void> {
  return request<void>('/api/v1/auth/change-password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export async function listSSOProviders(): Promise<SSOProvider[]> {
  try {
    const res = await request<{ providers: SSOProvider[] }>('/api/v1/auth/sso/providers')
    return res?.providers ?? []
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      return []
    }
    throw err
  }
}
