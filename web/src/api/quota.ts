// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

/** A repository quota with its current usage; a limit of 0 means unlimited. */
export interface RepoQuota {
  id?: string
  repo_id?: string
  max_size_bytes: number
  max_artifact_count?: number
  current_size_bytes: number
  current_artifact_count?: number
  is_exceeded?: boolean
  created_at?: string
  updated_at?: string
}

const quotaUrl = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}/quota`

/** The backend always answers 200 for a repository, configured or not. */
export function getRepoQuota(repo: string): Promise<RepoQuota> {
  return request<RepoQuota>(quotaUrl(repo))
}

export function setRepoQuota(repo: string, maxSizeBytes: number): Promise<RepoQuota> {
  return request<RepoQuota>(quotaUrl(repo), {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify({ max_size_bytes: maxSizeBytes }),
  })
}

export async function removeRepoQuota(repo: string): Promise<void> {
  await request<void>(quotaUrl(repo), { method: 'DELETE' })
}
