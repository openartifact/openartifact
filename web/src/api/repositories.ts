// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, preflight, JSON_HEADERS } from './client'
import type { Repository, RepositoryMember } from './generated/types.gen'

export type { Repository, RepositoryMember }

export interface PackageItem {
  id: string
  name: string
  version: string
  size?: number
  sha256?: string
  format: string
  /** Architecture, platform or classifier of this file; empty when the format has none. */
  arch?: string
  filename?: string
  created_at?: string
  /** Vulnerability counts of this file's latest scan. */
  security?: { critical: number; high: number; medium: number; low: number }
  /** A critical or high vulnerability, a secret finding or a quarantined scan result. */
  flagged?: boolean
  /** Latest completed scan; absent when never scanned. */
  scanned_at?: string
}

/** Security counts of a repository from its artifacts' latest scans (raw: VEX does not suppress any). */
export interface RepositorySecurity {
  critical: number
  high: number
  medium: number
  low: number
  secrets: number
  /** Artifacts whose license scan is quarantined. */
  license_violations: number
  /** Latest completed scan; absent when never scanned. */
  scanned_at?: string
}

/** A storage quota next to its live usage; limits are absent when unlimited. */
export interface QuotaLine {
  limit_bytes?: number
  used_bytes: number
  soft_limit_bytes?: number
  warn_threshold_percent: number
  max_artifact_count?: number
  artifact_count: number
  usage_percent: number
  exceeded: boolean
}

/** Catalog figures a repository list row carries. */
export interface RepositoryStats {
  package_count: number
  size_bytes: number
  last_published_at?: string
  security: RepositorySecurity
  quota: { limit_bytes?: number; used_bytes: number }
}

export type RepositoryListItem = Repository & { stats?: RepositoryStats }

export interface RepositoryUsage {
  package_count: number
  version_count: number
  size_bytes: number
  last_published_at?: string
  quota: QuotaLine
}

export type RepositorySort = 'name' | 'updated' | 'last_published' | 'size'

export interface RepositoryListParams {
  page?: number
  limit?: number
  format?: string
  packageName?: string
  /** Case-insensitive name substring. */
  q?: string
  type?: 'hosted' | 'proxy' | 'group'
  project?: string
  /** Only repositories with a critical or high vulnerability, a secret or a quarantined scan result. */
  flagged?: boolean
  /** Only repositories at or above the quota threshold. */
  nearQuota?: boolean
  /** Only repositories that hold artifacts and have no completed scan. */
  unscanned?: boolean
  sort?: RepositorySort
}

interface ListRepositoriesResult {
  total: number
  repositories: RepositoryListItem[]
}

interface ListPackagesResult {
  /** Absent on a request that carries a cursor (it is not counted). */
  total?: number
  /** The listing continues after this page (per-file listing). */
  has_more?: boolean
  /** Set while `has_more`, on the newest-first listing: the `cursor` of the next page. */
  next_cursor?: string
  packages: PackageItem[]
}

export function listRepositories(params: RepositoryListParams = {}): Promise<ListRepositoriesResult> {
  const qs = new URLSearchParams()
  if (params.page != null) qs.set('page', String(params.page))
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.format) qs.set('format', params.format)
  if (params.packageName) qs.set('package_name', params.packageName)
  if (params.q) qs.set('q', params.q)
  if (params.type) qs.set('type', params.type)
  if (params.project) qs.set('project', params.project)
  if (params.flagged) qs.set('flagged', 'true')
  if (params.nearQuota) qs.set('near_quota', 'true')
  if (params.unscanned) qs.set('unscanned', 'true')
  if (params.sort) qs.set('sort', params.sort)
  const query = qs.toString() ? `?${qs}` : ''
  return request<ListRepositoriesResult>(`/api/v1/repositories${query}`)
}

/** Walks the paginated list endpoint (page size is clamped server-side). */
export async function listAllRepositories(): Promise<RepositoryListItem[]> {
  const all: RepositoryListItem[] = []
  for (let page = 1; page <= 50; page++) {
    const res = await listRepositories({ page, limit: 100 })
    const batch = res.repositories || []
    all.push(...batch)
    if (batch.length === 0 || all.length >= res.total) break
  }
  return all
}

export function getRepository(name: string): Promise<Repository> {
  return request<Repository>(`/api/v1/repository/${encodeURIComponent(name)}`)
}

/** Members of a group repository, ordered by resolution priority (first wins). */
export async function listRepoMembers(name: string): Promise<RepositoryMember[]> {
  const res = await request<{ members?: RepositoryMember[] }>(`/api/v1/repositories/${encodeURIComponent(name)}/members`)
  return res.members ?? []
}

export function createRepository(body: Partial<Repository> & { upstream_password?: string }): Promise<Repository> {
  return request<Repository>('/api/v1/repositories', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export type PresignMode = 'inherit' | 'always' | 'never'

/** Maps the tri-state presign_downloads field (null/absent = inherit) to a form value. */
export function presignModeOf(repo: Pick<Repository, 'presign_downloads'>): PresignMode {
  if (repo.presign_downloads === true) return 'always'
  if (repo.presign_downloads === false) return 'never'
  return 'inherit'
}

/** The presign_downloads value to PATCH for a form value (null clears the override). */
export function presignValueOf(mode: PresignMode): boolean | null {
  if (mode === 'always') return true
  if (mode === 'never') return false
  return null
}

export function updateRepository(name: string, body: Partial<Repository>): Promise<Repository> {
  return request<Repository>(`/api/v1/repository/${encodeURIComponent(name)}`, {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export function deleteRepository(name: string): Promise<void> {
  // The server requires the repository name as explicit confirmation.
  return request<void>(`/api/v1/repository/${encodeURIComponent(name)}?confirm=${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
}

export type PackageSort = 'created' | 'name'

interface ListPackagesParams {
  page?: number
  limit?: number
  format?: string
  /** Exact listed package name (every version of one package). */
  packageName?: string
  /** Case-insensitive text the listed name or version must contain. */
  q?: string
  /** `created` (newest first, the default) or `name` (name, then version). */
  sort?: PackageSort
  /** `next_cursor` of the previous page (newest-first listing; `page` is then ignored). */
  cursor?: string
}

export function listPackages(name: string, params?: ListPackagesParams): Promise<ListPackagesResult> {
  const qs = new URLSearchParams()
  if (params?.page != null) qs.set('page', String(params.page))
  if (params?.limit != null) qs.set('limit', String(params.limit))
  if (params?.format) qs.set('format', params.format)
  if (params?.packageName) qs.set('package_name', params.packageName)
  if (params?.q) qs.set('q', params.q)
  if (params?.sort) qs.set('sort', params.sort)
  if (params?.cursor) qs.set('cursor', params.cursor)
  const query = qs.toString() ? `?${qs}` : ''
  return request<ListPackagesResult>(`/api/v1/repositories/${encodeURIComponent(name)}/packages${query}`)
}

/** Path of the streaming download route of a listed package (any format). */
function packageDownloadUrl(repoName: string, packageId: string): string {
  return `/api/v1/repositories/${encodeURIComponent(repoName)}/packages/${encodeURIComponent(packageId)}/download`
}

/**
 * Downloads a listed package (any format). A preflight request reports what
 * the server would answer (quarantined, forbidden, gone) as an ApiError, then
 * the browser fetches the file itself so it streams to disk instead of being
 * buffered in memory. Rejects with ApiError on 4xx/5xx.
 */
export async function downloadPackage(repoName: string, pkg: Pick<PackageItem, 'id' | 'name' | 'version'>): Promise<void> {
  const url = packageDownloadUrl(repoName, pkg.id)
  const { filename } = await preflight(url)

  const link = document.createElement('a')
  link.href = url
  // The attachment header names the file; the attribute keeps an error that
  // appears after the preflight from replacing the app with a JSON page.
  link.download = filename ?? (pkg.version ? `${pkg.name}-${pkg.version}` : pkg.name)
  document.body.appendChild(link)
  link.click()
  link.remove()
}

// Deletes a package by its component id (the backend route is
// DELETE /repositories/{name}/packages/{id}).
export function deletePackage(repoName: string, id: string): Promise<void> {
  return request<void>(
    `/api/v1/repositories/${encodeURIComponent(repoName)}/packages/${encodeURIComponent(id)}`,
    { method: 'DELETE' },
  )
}

const COMPONENT_PAGE_SIZE = 100 // backend clamps `limit` to 100

/**
 * Every row (all versions) of one package, looked up server-side by its exact
 * listed name, so repositories of any size resolve without walking the whole
 * listing.
 */
export function listPackageVersions(repoName: string, packageName: string, format?: string): Promise<PackageItem[]> {
  return listAllPackages(repoName, { format, packageName })
}

/**
 * Every row of a repository's newest-first listing that matches the filters,
 * walked by keyset cursor (`next_cursor` / `has_more`) so a deep page costs the
 * same as the first.
 */
export async function listAllPackages(
  repoName: string,
  filters: Pick<ListPackagesParams, 'format' | 'packageName' | 'q'> = {},
): Promise<PackageItem[]> {
  const items: PackageItem[] = []
  let cursor: string | undefined
  for (;;) {
    const res = await listPackages(repoName, { ...filters, limit: COMPONENT_PAGE_SIZE, cursor })
    const rows = res.packages ?? []
    items.push(...rows)
    if (!res.has_more || !res.next_cursor || rows.length === 0) break
    cursor = res.next_cursor
  }
  return items
}

/** One package (repository, format, name) rolled up across its versions. */
export interface PackageGroup {
  name: string
  format: string
  latest_version: string
  version_count: number
  /** Size of the latest version, all its files summed. */
  size_bytes: number
  updated_at: string
  /** Vulnerability counts of the latest version. */
  security: { critical: number; high: number; medium: number; low: number }
  /** A critical or high vulnerability, a secret finding or a quarantined scan result. */
  flagged: boolean
  /** Latest completed scan of the latest version; absent when never scanned. */
  scanned_at?: string
}

interface ListPackageGroupsParams {
  page?: number
  limit?: number
  format?: string
  q?: string
  sort?: PackageSort
  flagged?: boolean
}

/** Packages of a repository, one row per package name (`group=package`). */
export function listPackageGroups(
  name: string,
  params: ListPackageGroupsParams = {},
): Promise<{ total: number; packages: PackageGroup[] }> {
  const qs = new URLSearchParams({ group: 'package' })
  if (params.page != null) qs.set('page', String(params.page))
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.format) qs.set('format', params.format)
  if (params.q) qs.set('q', params.q)
  if (params.sort) qs.set('sort', params.sort)
  if (params.flagged) qs.set('flagged', 'true')
  return request(`/api/v1/repositories/${encodeURIComponent(name)}/packages?${qs}`)
}

/** What a repository holds next to its quota (maintainers and admins). */
export function getRepositoryUsage(name: string): Promise<RepositoryUsage> {
  return request<RepositoryUsage>(`/api/v1/repositories/${encodeURIComponent(name)}/usage`)
}

/** Replaces the ordered members of a group repository. */
export async function updateRepoMembers(name: string, memberRepos: string[]): Promise<RepositoryMember[]> {
  const res = await request<{ members?: RepositoryMember[] }>(`/api/v1/repositories/${encodeURIComponent(name)}/members`, {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify({ member_repos: memberRepos }),
  })
  return res.members ?? []
}

/** Counts of the latest scans of a repository; no finding details. */
export interface RepoSecuritySummary {
  scanned_at: string | null
  vuln: { critical: number; high: number; medium: number; low: number }
  license: { violations: number }
  secret: { count: number }
  ai: { quarantine_recommended: number; review_required: number }
  quarantined: boolean
}

export function getRepoSecuritySummary(name: string): Promise<RepoSecuritySummary> {
  return request<RepoSecuritySummary>(`/api/v1/repositories/${encodeURIComponent(name)}/security/summary`)
}

/** An audit event of a repository with the actor and the subject resolved to names. */
export interface RepoActivityEntry {
  id: string
  timestamp: string
  action: string
  actor: string
  actor_name: string
  message?: string
  subject: { kind: string; name: string | null; version?: string; repository?: string; project?: string } | null
}

interface RepoActivityPage {
  items: RepoActivityEntry[]
  hasMore: boolean
  nextCursor?: string
}

export async function listRepoActivity(
  name: string,
  params: { cursor?: string; limit?: number; packageName?: string; version?: string } = {},
): Promise<RepoActivityPage> {
  const qs = new URLSearchParams()
  if (params.cursor) qs.set('cursor', params.cursor)
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.packageName) qs.set('package_name', params.packageName)
  if (params.version) qs.set('version', params.version)
  const query = qs.toString() ? `?${qs}` : ''
  const res = await request<{ data?: RepoActivityEntry[]; has_more?: boolean; next_cursor?: string }>(
    `/api/v1/repositories/${encodeURIComponent(name)}/activity${query}`,
  )
  return { items: res.data ?? [], hasMore: res.has_more === true, nextCursor: res.next_cursor }
}
