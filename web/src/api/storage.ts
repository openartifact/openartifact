// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import { pagedResponse, type PagedResult } from './transform'
import type { StorageBackend, StoragePrimary } from './generated/types.gen'

export type { StorageBackend, StoragePrimary }

export interface StorageList extends PagedResult<StorageBackend> {
  primary?: StoragePrimary
}

interface StorageTestResult {
  ok: boolean
  error?: string
  message?: string
}

export async function listStorageBackends(): Promise<StorageList> {
  const raw = await request<Record<string, unknown>>('/api/v1/admin/storage')
  const primary = raw.primary as StoragePrimary | undefined
  return { ...pagedResponse<StorageBackend>(raw), ...(primary ? { primary } : {}) }
}

export async function createStorageBackend(body: {
  name: string
  kind: string
  config: Record<string, unknown>
}): Promise<StorageBackend> {
  return request<StorageBackend>('/api/v1/admin/storage', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export async function updateStorageBackend(
  name: string,
  body: Partial<{ name: string; kind: string; config: Record<string, unknown> }>,
): Promise<StorageBackend> {
  return request<StorageBackend>(`/api/v1/admin/storage/${encodeURIComponent(name)}`, {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export async function deleteStorageBackend(name: string): Promise<void> {
  await request<void>(`/api/v1/admin/storage/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
}

/** Tests the *stored* backend; a failed check resolves with `ok: false`. */
export async function testStorageBackend(name: string): Promise<StorageTestResult> {
  return request<StorageTestResult>(`/api/v1/admin/storage/${encodeURIComponent(name)}/test`, {
    method: 'POST',
  })
}

/** Tests the configured primary store; a failed check resolves with `ok: false`. */
export async function testStoragePrimary(): Promise<StorageTestResult> {
  return request<StorageTestResult>('/api/v1/admin/storage/primary/test', { method: 'POST' })
}
