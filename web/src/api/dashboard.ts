// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { RecentPackage, Repository, RepositoryStats, SecurityHealth, SecuritySummary, UnifiedToken } from './generated/types.gen'

export type { RecentPackage, RepositoryStats, SecurityHealth }

// Home dashboard sources. Every function is one endpoint; the page decides what a
// 403 or an error means for its block. Shapes follow the web rationalization
// backend contract (snake_case).

export type RepositoryWithStats = Repository & { stats?: RepositoryStats }

export interface RepositoryFilters {
  near_quota?: boolean
  flagged?: boolean
  unscanned?: boolean
  sort?: 'name' | 'updated' | 'last_published' | 'size'
  limit?: number
}

export interface RepositoryPage {
  total: number
  repositories: RepositoryWithStats[]
}

export async function listRepositoryPage(filters: RepositoryFilters = {}): Promise<RepositoryPage> {
  const qs = new URLSearchParams()
  if (filters.near_quota) qs.set('near_quota', 'true')
  if (filters.flagged) qs.set('flagged', 'true')
  if (filters.unscanned) qs.set('unscanned', 'true')
  if (filters.sort) qs.set('sort', filters.sort)
  qs.set('limit', String(filters.limit ?? 100))
  const res = await request<Partial<RepositoryPage>>(`/api/v1/repositories?${qs}`)
  const repositories = res?.repositories ?? []
  return { total: res?.total ?? repositories.length, repositories }
}

/** `GET /api/v1/security/health` (admin). */
export const getSecurityHealth = () => request<SecurityHealth>('/api/v1/security/health')

/** `GET /api/v1/security/findings/summary`. */
export type FindingsSummary = SecuritySummary

export const getFindingsSummary = () => request<FindingsSummary>('/api/v1/security/findings/summary')

/** One row of `GET /api/v1/tokens`. */
export type TokenRow = UnifiedToken

export interface TokenPage {
  total: number
  tokens: TokenRow[]
}

/** Tokens the caller can see that expire before `before` (already expired ones included). */
export async function listTokens(params: { expiresBefore?: string; limit?: number } = {}): Promise<TokenPage> {
  const qs = new URLSearchParams()
  if (params.expiresBefore) qs.set('expires_before', params.expiresBefore)
  qs.set('limit', String(params.limit ?? 20))
  const res = await request<Partial<TokenPage>>(`/api/v1/tokens?${qs}`)
  const tokens = res?.tokens ?? []
  return { total: res?.total ?? tokens.length, tokens }
}

export async function listRecentPackages(params: { mine: boolean; limit?: number }): Promise<RecentPackage[]> {
  const qs = new URLSearchParams({ mine: String(params.mine), limit: String(params.limit ?? 8) })
  const res = await request<{ packages?: RecentPackage[] }>(`/api/v1/packages/recent?${qs}`)
  return res?.packages ?? []
}

/** Number of users (admin): only the total is needed. */
export async function countUsers(): Promise<number> {
  const res = await request<{ total?: number; users?: unknown[] }>('/api/v1/users?per_page=1')
  return res?.total ?? res?.users?.length ?? 0
}

/** Number of tokens the caller can see. */
export async function countTokens(): Promise<number> {
  return (await listTokens({ limit: 1 })).total
}
