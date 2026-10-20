// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import { pagedResponse, type PagedResult } from './transform'
import type { GpgKey } from './generated/types.gen'

export type { GpgKey }

export async function listGpgKeys(): Promise<PagedResult<GpgKey>> {
  const raw = await request<Record<string, unknown>>('/api/v1/admin/gpg/keys')
  return pagedResponse<GpgKey>(raw)
}

export interface ImportKeyInput {
  armored: string
  passphrase?: string
}

export async function importGpgKey(body: ImportKeyInput): Promise<GpgKey> {
  return request<GpgKey>('/api/v1/admin/gpg/keys', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export interface GenerateKeyInput {
  name: string
  email: string
  comment?: string
  passphrase?: string
}

export async function generateGpgKey(body: GenerateKeyInput): Promise<GpgKey> {
  return request<GpgKey>('/api/v1/admin/gpg/keys/generate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export async function revokeGpgKey(fingerprint: string): Promise<void> {
  return request<void>(`/api/v1/admin/gpg/keys/${encodeURIComponent(fingerprint)}`, {
    method: 'DELETE',
  })
}
