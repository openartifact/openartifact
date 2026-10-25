// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import { pagedResponse, type PagedResult } from './transform'

/**
 * Mirrors the server's StorageBackendResponse. The generated type is stale
 * (no `config`/status fields, narrower `kind`), so it is declared locally.
 * Secret config fields are never returned; the server reports a
 * `has_<field>: true` flag in `config` instead.
 */
export interface StorageBackend {
  id?: string
  name?: string
  kind?: string
  is_default?: boolean
  config?: Record<string, unknown>
  /** `healthy` | `unreachable` | `unknown` */
  status?: string
  status_message?: string | null
  last_checked_at?: string | null
  created_at?: string
  updated_at?: string
}

interface StorageTestResult {
  ok: boolean
  error?: string
  message?: string
}

export async function listStorageBackends(): Promise<PagedResult<StorageBackend>> {
  const raw = await request<Record<string, unknown>>('/api/v1/admin/storage')
  return pagedResponse<StorageBackend>(raw)
}

export async function createStorageBackend(body: {
  name: string
  kind: string
  is_default?: boolean
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
  body: Partial<{ name: string; kind: string; is_default: boolean; config: Record<string, unknown> }>,
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
