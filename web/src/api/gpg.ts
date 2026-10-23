// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ApiError, request } from './client'
import type { GpgKey } from './generated/types.gen'

export type { GpgKey }

export type GpgAlgorithm = 'ed25519' | 'rsa4096'

export interface GpgKeyList {
  keys: GpgKey[]
  can_store_keys: boolean
}

const BASE = '/api/v1/admin/gpg/keys'
const JSON_HEADERS = { 'Content-Type': 'application/json' }

export async function listGpgKeys(): Promise<GpgKeyList> {
  const raw = await request<Partial<GpgKeyList> | undefined>(BASE)
  return { keys: raw?.keys ?? [], can_store_keys: raw?.can_store_keys ?? false }
}

export interface ImportKeyInput {
  name?: string
  private_key: string
  passphrase?: string
  activate?: boolean
}

export function importGpgKey(body: ImportKeyInput): Promise<GpgKey> {
  return request<GpgKey>(BASE, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export interface GenerateKeyInput {
  name?: string
  algorithm?: GpgAlgorithm
  user_name?: string
  email?: string
  comment?: string
  activate?: boolean
}

export function generateGpgKey(body: GenerateKeyInput): Promise<GpgKey> {
  return request<GpgKey>(`${BASE}/generate`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export function deleteGpgKey(id: string, force = false): Promise<void> {
  const query = force ? '?force=true' : ''
  return request<void>(`${BASE}/${encodeURIComponent(id)}${query}`, { method: 'DELETE' })
}

/** Makes the key sign globally, or only `repository` when given. */
export function activateGpgKey(id: string, repository?: string): Promise<GpgKey> {
  return request<GpgKey>(`${BASE}/${encodeURIComponent(id)}/activate`, {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify(repository ? { repository } : {}),
  })
}

/** Stops the key signing `repository`, or globally when omitted. */
export function deactivateGpgKey(id: string, repository?: string): Promise<void> {
  const query = repository ? `?repository=${encodeURIComponent(repository)}` : ''
  return request<void>(`${BASE}/${encodeURIComponent(id)}/activate${query}`, { method: 'DELETE' })
}

/** Fetches the armored public key and saves it as `<name>.pub.asc`. */
export async function exportGpgKey(key: Pick<GpgKey, 'id' | 'name'>): Promise<void> {
  const res = await fetch(`${BASE}/${encodeURIComponent(key.id ?? '')}/export`, {
    credentials: 'include',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  })
  if (!res.ok) {
    let message = `HTTP ${res.status}`
    let code: string | undefined
    try {
      const body = (await res.json()) as { error?: string; code?: string }
      if (body.error) message = body.error
      code = body.code
    } catch {
      // non-JSON error body: keep the HTTP status message
    }
    throw new ApiError(message, res.status, code)
  }

  const url = URL.createObjectURL(await res.blob())
  try {
    const link = document.createElement('a')
    link.href = url
    link.download = `${key.name || key.id}.pub.asc`
    document.body.appendChild(link)
    link.click()
    link.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}
