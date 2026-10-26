// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { QuotaOverviewItem } from '../api/quota'

export type QuotaState = 'over' | 'near' | 'ok'

/** Usage percentage from which a quota counts as near its limit when the entry sets no threshold of its own. */
export const DEFAULT_WARN_PERCENT = 80

/** Over the limit, near it (from the entry's warn threshold, else 80 %), or within it. */
export function quotaState(q: Pick<QuotaOverviewItem, 'exceeded' | 'usage_percent' | 'warn_threshold_percent'>): QuotaState {
  if (q.exceeded || q.usage_percent >= 100) return 'over'
  const warn = q.warn_threshold_percent && q.warn_threshold_percent > 0 ? q.warn_threshold_percent : DEFAULT_WARN_PERCENT
  return q.usage_percent >= warn ? 'near' : 'ok'
}

export function quotaCounts(rows: readonly QuotaOverviewItem[]): Record<QuotaState, number> {
  const counts: Record<QuotaState, number> = { over: 0, near: 0, ok: 0 }
  for (const q of rows) counts[quotaState(q)]++
  return counts
}

/** Row key: repositories and projects may share a name. */
export function quotaKey(q: Pick<QuotaOverviewItem, 'scope' | 'name'>): string {
  return `${q.scope}:${q.name}`
}

export function compareQuotas(a: QuotaOverviewItem, b: QuotaOverviewItem, sort: string): number {
  switch (sort) {
    case 'used':
      return a.used_bytes - b.used_bytes
    case 'usage':
      return a.usage_percent - b.usage_percent || a.used_bytes - b.used_bytes
    default:
      return a.name.localeCompare(b.name)
  }
}
