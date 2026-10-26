// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure logic of the projects list: which scope opens by default, how the URL
// filters map to the list the API returns, sorting and paging.

import type { ProjectListItem } from '../api/projects'
import { compareText } from './intl'

export const PROJECT_SCOPES = ['mine', 'all'] as const
export type ProjectScope = (typeof PROJECT_SCOPES)[number]

/** Admins open on every project so the list is never mysteriously empty; everyone else on their own. */
export function defaultScope(isAdmin: boolean): ProjectScope {
  return isAdmin ? 'all' : 'mine'
}

/** Only global admins may list every project; for anyone else any scope means their own. */
export function effectiveScope(isAdmin: boolean, scope: ProjectScope): ProjectScope {
  return isAdmin ? scope : 'mine'
}

export const PROJECT_SORTS = ['name', 'role', 'repositories', 'members', 'used', 'created'] as const
export type ProjectSort = (typeof PROJECT_SORTS)[number]

export interface ProjectQuery {
  q: string
  sort: ProjectSort
  dir: 'asc' | 'desc'
  page: number
  limit: number
}

const ROLE_RANK: Record<string, number> = { viewer: 1, developer: 2, maintainer: 3, owner: 4 }

function sortKey(p: ProjectListItem, sort: ProjectSort): string | number {
  switch (sort) {
    case 'role':
      return ROLE_RANK[p.my_role ?? ''] ?? 0
    case 'repositories':
      return p.repository_count ?? 0
    case 'members':
      return p.member_count ?? 0
    case 'used':
      return p.usage?.size_bytes ?? 0
    case 'created':
      return Date.parse(p.created_at ?? '') || 0
    default:
      return (p.name ?? '').toLowerCase()
  }
}

/** Search text (name, display name, description), sort and page of a project list. `total` counts all matches. */
export function queryProjects(
  projects: readonly ProjectListItem[],
  query: ProjectQuery,
): { rows: ProjectListItem[]; total: number } {
  const needle = query.q.trim().toLowerCase()
  const matches = needle
    ? projects.filter((p) => `${p.name ?? ''} ${p.display_name ?? ''} ${p.description ?? ''}`.toLowerCase().includes(needle))
    : [...projects]
  const sign = query.dir === 'desc' ? -1 : 1
  matches.sort((a, b) => {
    const ka = sortKey(a, query.sort)
    const kb = sortKey(b, query.sort)
    const order = ka < kb ? -1 : ka > kb ? 1 : 0
    // The name breaks ties so equal keys keep a stable, readable order.
    return order !== 0 ? order * sign : compareText(a.name, b.name)
  })
  const start = (Math.max(1, query.page) - 1) * query.limit
  return { rows: matches.slice(start, start + query.limit), total: matches.length }
}

export type QuotaLevel = 'ok' | 'warn' | 'full'

export interface QuotaUsage {
  percent: number
  level: QuotaLevel
}

/** Share of a size limit in use, `null` when the project has no limit. Warns from 80%. */
export function quotaUsage(usedBytes: number | undefined, limitBytes: number | undefined): QuotaUsage | null {
  if (!limitBytes || limitBytes <= 0) return null
  const percent = Math.round(((usedBytes ?? 0) / limitBytes) * 100)
  return { percent, level: percent >= 100 ? 'full' : percent >= 80 ? 'warn' : 'ok' }
}

/** Why the caller cannot create a project, or `null` when they can. Mirrors the API's `can_create`. */
export function createBlockedReason(canCreate: boolean, isAdmin: boolean): 'self-create-off' | null {
  return canCreate || isAdmin ? null : 'self-create-off'
}
