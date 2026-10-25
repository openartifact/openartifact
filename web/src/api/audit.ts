// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { downloadAttachment, request } from './client'
import type { AuditEntry, GetApiV1AdminAuditData, GetApiV1AdminAuditResponses } from './generated/types.gen'

export type { AuditEntry }

/**
 * Filters understood by `GET /api/v1/admin/audit`. `limit` is sent as
 * `per_page`, which the server clamps to 100. `cursor` is the `nextCursor` of
 * the previous page; leave it out for the newest entries.
 */
interface AuditFilters {
  limit?: number
  cursor?: string
  actor?: string
  action?: string
}

interface AuditPage {
  items: AuditEntry[]
  hasMore: boolean
  /** Pass as `cursor` to load the following page; null on the last page. */
  nextCursor: string | null
}

type AuditQuery = NonNullable<GetApiV1AdminAuditData['query']>
type AuditResponse = GetApiV1AdminAuditResponses[200]

/**
 * Lists audit entries newest first with keyset paging: the server neither
 * counts rows nor skips an offset, so every page costs the same on a large
 * table. There is deliberately no total.
 */
export async function listAudit(filters?: AuditFilters): Promise<AuditPage> {
  // An empty cursor selects keyset paging from the newest entry.
  const query: AuditQuery = { cursor: filters?.cursor ?? '' }
  if (filters?.limit != null) query.per_page = filters.limit
  if (filters?.actor) query.actor = filters.actor
  if (filters?.action) query.action = filters.action

  const qs = new URLSearchParams(Object.entries(query).map(([k, v]) => [k, String(v)]))
  const raw = await request<AuditResponse>(`/api/v1/admin/audit?${qs}`)

  return {
    items: raw.data ?? [],
    hasMore: raw.has_more ?? false,
    nextCursor: raw.next_cursor ?? null,
  }
}

/** Wire formats of the audit and project activity exports. */
export type ExportFormat = 'jsonl' | 'csv'

/**
 * Downloads the audit log (admin) as JSONL or CSV, narrowed by the same
 * actor/action filters as the listing. The server streams the whole log, not
 * just the loaded page.
 */
export function downloadAuditExport(format: ExportFormat, filters?: Pick<AuditFilters, 'actor' | 'action'>): Promise<void> {
  const qs = new URLSearchParams({ format })
  if (filters?.actor) qs.set('actor', filters.actor)
  if (filters?.action) qs.set('action', filters.action)
  return downloadAttachment(`/api/v1/admin/audit/export?${qs}`, `openartifact-audit.${format}`)
}
