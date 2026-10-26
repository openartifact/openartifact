// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { ActivityEntry, PackageDetail as GeneratedPackageDetail, RepoSecuritySummary } from './generated/types.gen'
import { listLicenseScans, listSecretScans, listVulnScans, setLicenseQuarantine, setSecretQuarantine, setVulnQuarantine } from './security'

// Reads behind the artifact page that the package listing cannot give: the
// metadata of one version, its security counts and its activity.

const repoBase = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}`

/** One package version with the metadata and dependencies its format stores. */
export type PackageDetail = GeneratedPackageDetail
/** One file of a package version (an architecture, classifier or distribution). */
export type PackageFile = PackageDetail['files'][number]

/** `id` is the row id of the package listing. */
export function getPackageDetail(repo: string, id: string): Promise<PackageDetail> {
  return request<PackageDetail>(`${repoBase(repo)}/packages/${encodeURIComponent(id)}`)
}

/**
 * Counts only: what a reader may see of a package version's scans. It carries
 * no finding ids or titles, so read access to the repository is enough.
 */
export type SecuritySummary = RepoSecuritySummary

export function getSecuritySummary(repo: string, packageName: string, version: string): Promise<SecuritySummary> {
  const qs = new URLSearchParams({ package_name: packageName, version })
  return request<SecuritySummary>(`${repoBase(repo)}/security/summary?${qs}`)
}

export type { ActivityEntry }

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
  opts: { cursor?: string; action?: string; limit?: number } = {},
): Promise<ActivityPage> {
  const qs = new URLSearchParams({ package_name: packageName, version })
  if (opts.cursor) qs.set('cursor', opts.cursor)
  if (opts.action) qs.set('action', opts.action)
  if (opts.limit) qs.set('limit', String(opts.limit))
  const res = await request<RawActivityPage>(`${repoBase(repo)}/activity?${qs}`)
  const hasMore = res.has_more === true && !!res.next_cursor
  return { entries: res.data ?? [], hasMore, nextCursor: hasMore ? (res.next_cursor ?? null) : null }
}

/**
 * Quarantines or releases every scan result of the given files (vulnerability,
 * license and secret scans each keep their own flag). Returns how many results
 * changed: 0 means there is nothing scanned to quarantine, or nothing to release.
 */
export async function setVersionQuarantine(repo: string, fileIds: readonly string[], quarantined: boolean): Promise<number> {
  const ids = new Set(fileIds)
  const [vuln, license, secret] = await Promise.all([listVulnScans(repo), listLicenseScans(repo), listSecretScans(repo)])
  const pending = (scans: Array<{ id: string; artifact_id: string; quarantined: boolean }>) =>
    scans.filter((s) => ids.has(s.artifact_id) && s.quarantined !== quarantined)
  const changes = [
    ...pending(vuln).map((s) => setVulnQuarantine(repo, s.id, quarantined)),
    ...pending(license).map((s) => setLicenseQuarantine(repo, s.id, quarantined)),
    ...pending(secret).map((s) => setSecretQuarantine(repo, s.id, quarantined)),
  ]
  await Promise.all(changes)
  return changes.length
}
