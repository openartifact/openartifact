// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

export type BackupType = 'full' | 'metadata-only'

export interface BackupJob {
  id: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  type: BackupType
  size_bytes?: number
  created_at: string
  completed_at?: string | null
  error_message?: string | null
}

const BASE = '/api/v1/admin/backup'

export async function listBackups(): Promise<BackupJob[]> {
  const res = await request<{ backups?: BackupJob[] }>(BASE)
  return res?.backups ?? []
}

export async function createBackup(type: BackupType): Promise<void> {
  await request(BASE, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify({ type }) })
}

/** Direct download link of a completed backup archive. */
export function backupDownloadUrl(id: string): string {
  return `${BASE}/${encodeURIComponent(id)}/download`
}

/**
 * Uploads a backup archive (multipart field "file"). The server answers 202
 * once the archive is received; the restore itself runs in the background.
 */
export async function restoreBackup(file: File): Promise<void> {
  const body = new FormData()
  body.append('file', file)
  await request('/api/v1/admin/restore', { method: 'POST', body })
}
