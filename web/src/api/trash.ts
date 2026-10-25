// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { TrashEmpty, TrashItem } from './generated/types.gen'

export type { TrashEmpty, TrashItem }
export type TrashKind = TrashItem['kind']

interface TrashPage {
  items: TrashItem[]
  total: number
  page: number
  limit: number
}

interface TrashQuery {
  page?: number
  limit?: number
  kind?: string
  /** Global listing only. */
  repository?: string
}

function queryString(q: TrashQuery): string {
  const qs = new URLSearchParams()
  if (q.page != null) qs.set('page', String(q.page))
  if (q.limit != null) qs.set('limit', String(q.limit))
  if (q.kind) qs.set('kind', q.kind)
  if (q.repository) qs.set('repository', q.repository)
  const s = qs.toString()
  return s ? `?${s}` : ''
}

const enc = encodeURIComponent

/** Lists trashed items; pass `repo` for the per-repository view. */
export function listTrash(q: TrashQuery = {}, repo?: string): Promise<TrashPage> {
  const base = repo ? `/api/v1/repositories/${enc(repo)}/trash` : '/api/v1/trash'
  return request<TrashPage>(`${base}${queryString(q)}`)
}

export function restoreTrash(item: Pick<TrashItem, 'kind' | 'id'>, repo?: string): Promise<TrashItem> {
  const base = repo ? `/api/v1/repositories/${enc(repo)}/trash` : '/api/v1/trash'
  return request<TrashItem>(`${base}/${enc(item.kind)}/${enc(item.id)}/restore`, { method: 'POST' })
}

/** Permanently destroys a trashed item (admin only). */
export function purgeTrash(item: Pick<TrashItem, 'kind' | 'id'>): Promise<void> {
  return request<void>(`/api/v1/trash/${enc(item.kind)}/${enc(item.id)}`, { method: 'DELETE' })
}

/**
 * Permanently destroys every trashed item of a repository (repository manage
 * permission). The server purges a capped number per call; `remaining` says how many are left.
 */
export function emptyRepoTrash(repo: string): Promise<TrashEmpty> {
  return request<TrashEmpty>(`/api/v1/repositories/${enc(repo)}/trash`, { method: 'DELETE' })
}
