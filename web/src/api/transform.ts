// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Response envelope utilities.
 * The backend uses different envelope keys for different endpoints.
 * These helpers normalize them into a consistent shape.
 */

/** Extract items from any envelope shape the backend returns */
function extractItems<T>(raw: Record<string, unknown>): T[] {
  return (raw.items ?? raw.data ?? raw.keys ?? raw.repositories ?? raw.packages ?? raw.webhooks ?? raw.policies ?? raw.backups ?? raw.reports ?? raw.scans ?? raw.projects ?? raw.members ?? raw.providers ?? []) as T[]
}

/** Extract total from any envelope shape */
function extractTotal(raw: Record<string, unknown>, items: unknown[]): number {
  return (raw.total as number) ?? items.length
}

/** Normalize a paginated response into a consistent shape */
export interface PagedResult<T> {
  total: number
  items: T[]
}

export function pagedResponse<T>(raw: Record<string, unknown>): PagedResult<T> {
  const items = extractItems<T>(raw)
  const total = extractTotal(raw, items)
  return { total, items }
}

/** Create an empty paged result for error fallbacks */
export function emptyPaged<T>(): PagedResult<T> {
  return { total: 0, items: [] }
}
