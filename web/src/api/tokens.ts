// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type { TokenCreateResponse, UnifiedToken } from './generated/types.gen'

export type CreatedToken = TokenCreateResponse

export type { UnifiedToken }

export type TokenType = UnifiedToken['type']

export interface UnifiedTokenPage {
  tokens: UnifiedToken[]
  total: number
  page: number
  limit: number
}

export interface TokenListParams {
  /** Omitted: every type. */
  type?: TokenType
  /** RFC 3339: only tokens that expire before it (already expired ones included). */
  expiresBefore?: string
  q?: string
  /** Project name: its project tokens and the federation tokens of its repositories. */
  project?: string
  page?: number
  limit?: number
}

const enc = encodeURIComponent

/** Personal, project and federation tokens in one list; admins see all, others what they own or maintain. */
export async function listTokens(params: TokenListParams = {}): Promise<UnifiedTokenPage> {
  const qs = new URLSearchParams()
  if (params.type) qs.set('type', params.type)
  if (params.expiresBefore) qs.set('expires_before', params.expiresBefore)
  if (params.q) qs.set('q', params.q)
  if (params.project) qs.set('project', params.project)
  if (params.page) qs.set('page', String(params.page))
  if (params.limit) qs.set('limit', String(params.limit))
  const query = qs.toString()
  const res = await request<Partial<UnifiedTokenPage>>(`/api/v1/tokens${query ? `?${query}` : ''}`)
  return {
    tokens: res.tokens ?? [],
    total: res.total ?? res.tokens?.length ?? 0,
    page: res.page ?? params.page ?? 1,
    limit: res.limit ?? params.limit ?? 25,
  }
}

/** Personal token of the caller: `kind: deploy` cannot reach the management API. */
export function createMyToken(body: {
  name: string
  kind: 'access' | 'deploy'
  scopes?: string[]
  expires_at?: string
}): Promise<CreatedToken> {
  return request<CreatedToken>('/api/v1/auth/tokens', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

/** Who is revoking: decides between the self-service and the admin endpoint for a personal token. */
export interface Revoker {
  username: string
  isAdmin: boolean
}

/** The endpoint that revokes `token`, or null when this caller has none for it. */
export function revokeEndpoint(
  token: Pick<UnifiedToken, 'id' | 'type' | 'owner' | 'project' | 'repository'>,
  who: Revoker,
): string | null {
  const id = enc(token.id)
  switch (token.type) {
    case 'personal':
      if (token.owner === who.username) return `/api/v1/auth/tokens/${id}`
      return who.isAdmin ? `/api/v1/admin/tokens/${id}` : null
    case 'project':
      if (token.project) return `/api/v1/projects/${enc(token.project)}/tokens/${id}`
      return who.isAdmin ? `/api/v1/admin/tokens/${id}` : null
    case 'federation':
      return token.repository ? `/api/v1/repositories/${enc(token.repository)}/federation/tokens/${id}` : null
  }
}

/** Revokes a token through the endpoint of its type; throws when the caller has none. */
export async function revokeToken(
  token: Pick<UnifiedToken, 'id' | 'type' | 'owner' | 'project' | 'repository'>,
  who: Revoker,
): Promise<void> {
  const url = revokeEndpoint(token, who)
  if (!url) throw new Error(`token ${token.id} cannot be revoked by ${who.username}`)
  await request<void>(url, { method: 'DELETE' })
}
