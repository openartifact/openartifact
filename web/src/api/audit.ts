// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import { pagedResponse, type PagedResult } from './transform'
import type { AuditEntry } from './generated/types.gen'

export type { AuditEntry }

/** Filters understood by `GET /api/v1/admin/audit` (the server clamps per_page to 100). */
export interface AuditFilters {
  page?: number
  limit?: number
  actor?: string
  action?: string
}

export async function listAudit(filters?: AuditFilters): Promise<PagedResult<AuditEntry>> {
  const qs = new URLSearchParams()
  if (filters?.page != null) qs.set('page', String(filters.page))
  if (filters?.limit != null) qs.set('per_page', String(filters.limit))
  if (filters?.actor) qs.set('actor', filters.actor)
  if (filters?.action) qs.set('action', filters.action)
  const query = qs.toString() ? `?${qs}` : ''
  const raw = await request<Record<string, unknown>>(`/api/v1/admin/audit${query}`)
  return pagedResponse<AuditEntry>(raw)
}
