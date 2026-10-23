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
