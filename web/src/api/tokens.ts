// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, ApiError, JSON_HEADERS } from './client'
import { pagedResponse, emptyPaged, type PagedResult } from './transform'
import type { AdminToken, Token, TokenCreateResponse } from './generated/types.gen'

export type ApiToken = Token
export type { AdminToken }
export type CreatedToken = TokenCreateResponse

function empty(): PagedResult<ApiToken> {
  return emptyPaged<ApiToken>()
}

// --- Self-service ---

export async function listMyTokens(): Promise<PagedResult<ApiToken>> {
  try {
    const raw = await request<Record<string, unknown>>('/api/v1/auth/tokens')
    return pagedResponse<ApiToken>(raw)
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 501)) return empty()
    throw err
  }
}

export async function createMyToken(body: { name: string; kind: 'access' | 'deploy'; expires_at?: string }): Promise<CreatedToken> {
  return request<CreatedToken>('/api/v1/auth/tokens', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export async function revokeMyToken(id: string | number): Promise<void> {
  return request<void>(`/api/v1/auth/tokens/${encodeURIComponent(String(id))}`, { method: 'DELETE' })
}

// --- Admin ---

export async function listTokens(params?: { page?: number; limit?: number; principal?: string }): Promise<PagedResult<AdminToken>> {
  const qs = new URLSearchParams()
  if (params?.page != null) qs.set('page', String(params.page))
  if (params?.limit != null) qs.set('per_page', String(params.limit))
  if (params?.principal) qs.set('principal', params.principal)
  const query = qs.toString() ? `?${qs}` : ''
  try {
    const raw = await request<Record<string, unknown>>(`/api/v1/admin/tokens${query}`)
    return pagedResponse<AdminToken>(raw)
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 501)) return emptyPaged<AdminToken>()
    throw err
  }
}

export async function revokeToken(id: string | number): Promise<void> {
  return request<void>(`/api/v1/admin/tokens/${encodeURIComponent(String(id))}`, { method: 'DELETE' })
}
