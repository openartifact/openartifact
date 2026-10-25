// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type { CleanupPreview } from './generated'
import type { ApiCleanupPolicy } from '../lib/cleanupPolicy'

export type { CleanupPreview }

/** Fields of a repository cleanup policy accepted on create and update (update accepts any subset). */
export type CleanupPolicyInput = Record<string, unknown>

const baseUrl = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}/cleanup-policies`
const policyUrl = (repo: string, id: string): string => `${baseUrl(repo)}/${encodeURIComponent(id)}`

export async function listCleanupPolicies(repo: string): Promise<ApiCleanupPolicy[]> {
  const res = await request<{ policies: ApiCleanupPolicy[] }>(baseUrl(repo))
  return res?.policies ?? []
}

export async function createCleanupPolicy(repo: string, body: CleanupPolicyInput): Promise<void> {
  await request(baseUrl(repo), { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export async function updateCleanupPolicy(repo: string, id: string, body: CleanupPolicyInput): Promise<void> {
  await request(policyUrl(repo, id), { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export async function deleteCleanupPolicy(repo: string, id: string): Promise<void> {
  await request(policyUrl(repo, id), { method: 'DELETE' })
}

/** Starts the policy now; the server answers once it is queued. */
export async function runCleanupPolicy(repo: string, id: string): Promise<void> {
  await request(`${policyUrl(repo, id)}/run`, { method: 'POST' })
}

/** Reports what a run would delete without deleting anything. */
export function dryRunCleanupPolicy(repo: string, id: string): Promise<CleanupPreview> {
  return request<CleanupPreview>(`${policyUrl(repo, id)}/dry-run`, { method: 'POST' })
}
