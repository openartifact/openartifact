// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, ApiError } from './client'

export interface SearchPackageHit {
  name: string
  version: string
  format: string
  repository: string
  description?: string
}

export interface SearchRepoHit {
  name: string
  format: string
  description?: string
}

export interface SearchResult {
  packages: SearchPackageHit[]
  repositories: SearchRepoHit[]
}

export interface SearchParams {
  q: string
  format?: string
  repo?: string
  limit?: number
  /** `package` returns one hit per package (its latest version) instead of one per version. */
  group?: 'package'
  /** Only packages with an open critical/high vulnerability, a secret finding or a quarantined version. */
  flagged?: boolean
  page?: number
  /** Aborts the request; the returned promise then rejects with an AbortError. */
  signal?: AbortSignal
}

/** `ApiError.code` of a search matching more rows than the server will rank. */
export const QUERY_TOO_BROAD = 'query_too_broad'

function emptyResult(): SearchResult {
  return { packages: [], repositories: [] }
}

export async function search(params: SearchParams): Promise<SearchResult> {
  const qs = new URLSearchParams()
  qs.set('q', params.q)
  if (params.format) qs.set('format', params.format)
  if (params.repo) qs.set('repo', params.repo)
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.group) qs.set('group', params.group)
  if (params.flagged) qs.set('flagged', 'true')
  if (params.page != null && params.page > 1) qs.set('page', String(params.page))
  try {
    const res = await request<Partial<SearchResult>>(`/api/v1/search?${qs}`, { signal: params.signal })
    return {
      packages: res?.packages ?? [],
      repositories: res?.repositories ?? [],
    }
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 501)) {
      return emptyResult()
    }
    throw err
  }
}

/** One package across its versions (`group=package`). */
export interface PackageGroup {
  repository: string
  format: string
  name: string
  latest_version: string
  version_count: number
}

export interface GroupedSearchResult {
  packages: PackageGroup[]
  total: number
}

/** Package search grouped by repository and name: one row per package, not per version. */
export async function searchPackageGroups(
  params: Pick<SearchParams, 'q' | 'format' | 'repo' | 'flagged' | 'limit' | 'page' | 'signal'>,
): Promise<GroupedSearchResult> {
  const qs = new URLSearchParams({ q: params.q, group: 'package' })
  if (params.format) qs.set('format', params.format)
  if (params.repo) qs.set('repo', params.repo)
  if (params.flagged) qs.set('flagged', 'true')
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.page != null && params.page > 1) qs.set('page', String(params.page))
  const res = await request<{ packages?: PackageGroup[]; total?: number }>(`/api/v1/search?${qs}`, {
    signal: params.signal,
  })
  const packages = res?.packages ?? []
  return { packages, total: res?.total ?? packages.length }
}

/** A stored file whose checksum matched. */
export interface ChecksumHit {
  repository: string
  format: string
  name: string
  version: string
  filename: string
  checksum: string
  /** `sha256` | `sha1` | `md5` */
  algorithm: string
}

export interface ChecksumSearchResult {
  packages: ChecksumHit[]
  total: number
}

/** Looks a file checksum (md5, sha1 or sha256 hex) up across the repositories the caller can read. */
export async function searchChecksum(
  params: { checksum: string; format?: string; repo?: string; flagged?: boolean; signal?: AbortSignal },
): Promise<ChecksumSearchResult> {
  const qs = new URLSearchParams({ checksum: params.checksum })
  if (params.format) qs.set('format', params.format)
  if (params.repo) qs.set('repo', params.repo)
  if (params.flagged) qs.set('flagged', 'true')
  const res = await request<{ packages?: ChecksumHit[]; total?: number }>(`/api/v1/search?${qs}`, {
    signal: params.signal,
  })
  const packages = res?.packages ?? []
  return { packages, total: res?.total ?? packages.length }
}
