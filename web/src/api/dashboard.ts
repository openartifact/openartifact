// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { Repository } from './generated/types.gen'

// Home dashboard sources. Every function is one endpoint; the page decides what a
// 403 or an error means for its block. Shapes follow the web rationalization
// backend contract (snake_case).

/** Catalog figures of one repository in `GET /repositories`. */
export interface RepositoryStats {
  package_count: number
  size_bytes: number
  last_published_at?: string
  security: {
    critical: number
    high: number
    medium: number
    low: number
    secrets: number
    license_violations: number
    scanned_at?: string
  }
  quota: { limit_bytes?: number; used_bytes: number }
}

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

export interface ScannerHealth {
  name: string
  ready: boolean
  message?: string
  docs_url?: string
}

/** `GET /api/v1/security/health` (admin). */
export interface SecurityHealth {
  scanners: ScannerHealth[]
  last_scan_at?: string
  scans_pending?: number
  scans_failed_24h?: number
  dependency_track?: { enabled: boolean; last_push_ok?: boolean; last_push_at?: string }
  ai?: { enabled: boolean }
  unscanned_repositories?: number
}

export const getSecurityHealth = () => request<SecurityHealth>('/api/v1/security/health')

export interface FindingCounts {
  total: number
  critical: number
  high: number
  medium: number
  low: number
  quarantined: number
}

/** `GET /api/v1/security/findings/summary`. */
export interface FindingsSummary {
  by_type: Record<'vuln' | 'license' | 'secret' | 'ai', FindingCounts>
  scanned_repositories: number
  total_repositories: number
  last_scan_at?: string
}

export const getFindingsSummary = () => request<FindingsSummary>('/api/v1/security/findings/summary')

/** One row of `GET /api/v1/tokens`. */
export interface TokenRow {
  id: string
  type: 'personal' | 'project' | 'federation'
  name: string
  owner: string
  project?: string
  repository?: string
  created_at: string
  expires_at?: string
  last_used_at?: string
  expired: boolean
}

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

export interface RecentPackage {
  repository: string
  format: string
  name: string
  version: string
  created_at: string
  created_by_name?: string
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
