// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { PackageGroup, PackageSecurity, Repository, RepositoryStats } from '../api/generated/types.gen'
import type { DemoRepository } from './fixtures/repos'
import type { DemoFile, DemoState } from './state'

/** Files a repository serves: its own, or for a group those of its members in order. */
export function repoFiles(state: DemoState, repo: DemoRepository): DemoFile[] {
  if (repo.repo_type !== 'group') return state.files.filter((f) => f.repository === repo.name)
  return (repo.member_repos ?? []).flatMap((m) => state.files.filter((f) => f.repository === m))
}

const newest = (a: string | undefined, b: string): string => (a && a > b ? a : b)

/** One severity count per version: every file of a version carries the same scan. */
function versionSecurity(files: DemoFile[]): PackageSecurity {
  const sum: PackageSecurity = { critical: 0, high: 0, medium: 0, low: 0 }
  const seen = new Set<string>()
  for (const f of files) {
    const key = `${f.repository}/${f.name}/${f.version}`
    if (seen.has(key)) continue
    seen.add(key)
    sum.critical += f.security.critical
    sum.high += f.security.high
    sum.medium += f.security.medium
    sum.low += f.security.low
  }
  return sum
}

export function repoStats(state: DemoState, repo: DemoRepository): RepositoryStats {
  const files = repoFiles(state, repo)
  const findings = state.findings.filter((f) => f.repository === repo.name || repo.member_repos?.includes(f.repository))
  const size = files.reduce((n, f) => n + f.size, 0)
  return {
    package_count: new Set(files.map((f) => f.name)).size,
    size_bytes: size,
    last_published_at: files.reduce<string | undefined>((at, f) => newest(at, f.created_at), undefined),
    security: {
      ...versionSecurity(files),
      secrets: findings.filter((f) => f.type === 'secret').length,
      license_violations: findings.filter((f) => f.type === 'license').length,
      scanned_at: files.reduce<string | undefined>((at, f) => newest(at, f.scanned_at ?? ''), undefined),
    },
    quota: { limit_bytes: repo.quota_limit_bytes, used_bytes: size },
  }
}

/** The repository as the API returns it: with stats and the caller's (admin) permissions. */
export function repoView(state: DemoState, repo: DemoRepository): Repository {
  const { quota_limit_bytes: _quota, ...rest } = repo
  return { ...rest, stats: repoStats(state, repo), permissions: { write: true, delete: true, manage: true, admin: true } }
}

export function findRepo(state: DemoState, name: string): DemoRepository | undefined {
  return state.repositories.find((r) => r.name === name)
}

/** One row per package name, newest version first. */
export function packageGroups(files: DemoFile[]): PackageGroup[] {
  const byName = new Map<string, DemoFile[]>()
  for (const f of files) byName.set(f.name, [...(byName.get(f.name) ?? []), f])
  return [...byName.entries()].map(([name, rows]) => {
    const latest = rows.reduce((a, b) => (b.created_at > a.created_at ? b : a))
    return {
      name,
      format: latest.format,
      latest_version: latest.version,
      version_count: new Set(rows.map((r) => r.version)).size,
      size_bytes: rows.reduce((n, r) => n + r.size, 0),
      updated_at: latest.created_at,
      security: versionSecurity(rows),
      flagged: rows.some((r) => r.flagged),
      scanned_at: latest.scanned_at,
    }
  })
}

/** `page`/`limit` (1-based, default 20) of `items`. */
export function paginate<T>(items: T[], query: URLSearchParams, limitKey = 'limit'): T[] {
  const limit = Math.max(1, Number(query.get(limitKey)) || 20)
  const page = Math.max(1, Number(query.get('page')) || 1)
  return items.slice((page - 1) * limit, page * limit)
}

/** Case-insensitive substring match of `q` (absent or empty matches everything). */
export function matches(text: string, q: string | null): boolean {
  return !q || text.toLowerCase().includes(q.toLowerCase())
}
