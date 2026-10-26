// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'

// Reads behind the artifact page that the package listing cannot give: the
// metadata of one version, its security counts and its activity.

const repoBase = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}`

/** One file of a package version (an architecture, classifier or distribution). */
export interface PackageFile {
  id: string
  filename: string
  arch: string
  size: number
  sha256: string
}

/** One declared dependency; `kind` is the role as the format names it (runtime, dev, peer, ...). */
export interface PackageDependency {
  name: string
  version?: string
  kind?: string
}

/** One package version with the metadata its format stores. */
export interface PackageDetail {
  id: string
  name: string
  version: string
  format: string
  arch: string
  filename: string
  size: number
  sha256: string
  md5?: string
  sha1?: string
  created_at: string
  updated_at: string
  summary?: string
  description?: string
  license?: string
  homepage?: string
  /** Format-specific fields, ordered by key. */
  metadata: Array<{ key: string; value: string }>
  dependencies: PackageDependency[]
  /** Every file of the version, the requested one included. */
  files: PackageFile[]
  version_count: number
  latest_version: string
}

/** `id` is the row id of the package listing. */
export function getPackageDetail(repo: string, id: string): Promise<PackageDetail> {
  return request<PackageDetail>(`${repoBase(repo)}/packages/${encodeURIComponent(id)}`)
}

/**
 * Counts only: what a reader may see of a package version's scans. It carries
 * no finding ids or titles, so read access to the repository is enough.
 */
export interface SecuritySummary {
  /** Latest completed scan of the selection; null when nothing was scanned. */
  scanned_at: string | null
  vuln: { critical: number; high: number; medium: number; low: number }
  license: { violations: number }
  secret: { count: number }
  ai: { quarantine_recommended: number; review_required: number }
  /** A result of the selection is quarantined. */
  quarantined: boolean
}

export function getSecuritySummary(repo: string, packageName: string, version: string): Promise<SecuritySummary> {
  const qs = new URLSearchParams({ package_name: packageName, version })
  return request<SecuritySummary>(`${repoBase(repo)}/security/summary?${qs}`)
}

/** One audit event of a repository, with actor and subject names resolved. */
export interface ActivityEntry {
  id: string
  timestamp: string
  action: string
  actor: string
  actor_name: string
  message: string
  subject: { kind: string; name: string | null; version?: string; repository?: string; project?: string } | null
}

export interface ActivityPage {
  entries: ActivityEntry[]
  hasMore: boolean
  nextCursor: string | null
}

interface RawActivityPage {
  data?: ActivityEntry[]
  has_more?: boolean
  next_cursor?: string
}

/** Events recording one package version, newest first; keyset paging via `cursor`. */
export async function listArtifactActivity(
  repo: string,
  packageName: string,
  version: string,
  cursor?: string,
): Promise<ActivityPage> {
  const qs = new URLSearchParams({ package_name: packageName, version })
  if (cursor) qs.set('cursor', cursor)
  const res = await request<RawActivityPage>(`${repoBase(repo)}/activity?${qs}`)
  const hasMore = res.has_more === true && !!res.next_cursor
  return { entries: res.data ?? [], hasMore, nextCursor: hasMore ? (res.next_cursor ?? null) : null }
}
