// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import { pagedResponse, type PagedResult } from './transform'
import type {
  PatchApiV1AdminTieringTiersByNameData,
  PostApiV1AdminTieringDryRunData,
  PostApiV1AdminTieringRunResponse,
  PostApiV1AdminTieringTiersData,
  StoragePolicy,
  StoragePolicyInput,
  StorageTier,
  TierDryRun,
  TierMoverStatus,
} from './generated/types.gen'

export type { StoragePolicy, StoragePolicyInput, StorageTier, TierDryRun, TierMoverStatus }

export type TierCreateInput = PostApiV1AdminTieringTiersData['body']
export type TierUpdateInput = PatchApiV1AdminTieringTiersByNameData['body']
export type DryRunInput = PostApiV1AdminTieringDryRunData['body']

const BASE = '/api/v1/admin/tiering'
const JSON_HEADERS = { 'Content-Type': 'application/json' }
const enc = encodeURIComponent

export async function listTiers(): Promise<PagedResult<StorageTier>> {
  return pagedResponse<StorageTier>(await request<Record<string, unknown>>(`${BASE}/tiers`))
}

export function createTier(body: TierCreateInput): Promise<StorageTier> {
  return request<StorageTier>(`${BASE}/tiers`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export function updateTier(name: string, body: TierUpdateInput): Promise<StorageTier> {
  return request<StorageTier>(`${BASE}/tiers/${enc(name)}`, {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export async function deleteTier(name: string): Promise<void> {
  await request<void>(`${BASE}/tiers/${enc(name)}`, { method: 'DELETE' })
}

export async function listPolicies(): Promise<PagedResult<StoragePolicy>> {
  return pagedResponse<StoragePolicy>(await request<Record<string, unknown>>(`${BASE}/policies`))
}

export function createPolicy(body: StoragePolicyInput): Promise<StoragePolicy> {
  return request<StoragePolicy>(`${BASE}/policies`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

/** Full replace. */
export function updatePolicy(id: string, body: StoragePolicyInput): Promise<StoragePolicy> {
  return request<StoragePolicy>(`${BASE}/policies/${enc(id)}`, {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export async function deletePolicy(id: string): Promise<void> {
  await request<void>(`${BASE}/policies/${enc(id)}`, { method: 'DELETE' })
}

/** Read-only simulation: nothing is moved. */
export function dryRun(body: DryRunInput = {}): Promise<TierDryRun> {
  return request<TierDryRun>(`${BASE}/dry-run`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

/** Asks the mover for a pass now; `started: false` when one is already running. */
export function runMover(): Promise<PostApiV1AdminTieringRunResponse> {
  return request<PostApiV1AdminTieringRunResponse>(`${BASE}/run`, { method: 'POST' })
}

export function getMoverStatus(): Promise<TierMoverStatus> {
  return request<TierMoverStatus>(`${BASE}/status`)
}
