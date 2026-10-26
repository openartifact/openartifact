// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RepositoryWithStats, FindingsSummary, SecurityHealth, TokenRow } from '../api/dashboard'
import type { StorageBackend } from '../api/storage'
import { formatDate } from './datetime'
import { percentOf } from './quota'

export type AttentionSeverity = 'critical' | 'high' | 'medium' | 'low'

export type AttentionKind =
  | 'critical-findings'
  | 'quarantined'
  | 'storage-down'
  | 'scanner-down'
  | 'scans-failed'
  | 'quota'
  | 'token-expired'
  | 'token-expiring'
  | 'flagged-repos'
  | 'unscanned'
  | 'dtrack-failed'

/** One row of "Needs attention": a catalog key (`dashboard.attention.<kind>`), its values and where to act. */
export interface AttentionItem {
  id: string
  kind: AttentionKind
  severity: AttentionSeverity
  params: Record<string, string>
  to: string
}

/** Each source is absent (null/undefined) when the caller may not read it or it failed. */
export interface AttentionInput {
  now: number
  health?: SecurityHealth | null
  summary?: FindingsSummary | null
  storage?: StorageBackend[] | null
  nearQuota?: RepositoryWithStats[] | null
  flagged?: { total: number } | null
  unscanned?: { total: number } | null
  tokens?: TokenRow[] | null
}

const DAY_MS = 86_400_000
/** Tokens expiring within this many days are listed. */
export const TOKEN_HORIZON_DAYS = 30

/** The `expires_before` value of the token query (the Settings link uses the matching `?expiring=` preset). */
export function tokenHorizon(now: number): string {
  return new Date(now + TOKEN_HORIZON_DAYS * DAY_MS).toISOString()
}

const RANK: Record<AttentionSeverity, number> = { critical: 0, high: 1, medium: 2, low: 3 }

export function daysUntil(iso: string, now: number): number {
  return Math.ceil((Date.parse(iso) - now) / DAY_MS)
}

/** Everything that needs a decision, most severe first (source order within one severity). */
export function deriveAttention(input: AttentionInput): AttentionItem[] {
  const items: AttentionItem[] = []
  const add = (item: AttentionItem): void => void items.push(item)

  for (const b of input.storage ?? []) {
    if (b.status === 'unreachable') {
      add({ id: `storage:${b.name}`, kind: 'storage-down', severity: 'critical', params: { name: b.name ?? '' }, to: '/settings/backends' })
    }
  }

  const byType = input.summary?.by_type
  if (byType) {
    const critical = Object.values(byType).reduce((n, c) => n + (c?.critical ?? 0), 0)
    if (critical > 0) {
      add({ id: 'findings:critical', kind: 'critical-findings', severity: 'critical', params: { count: String(critical) }, to: '/security?severity=critical' })
    }
    const quarantined = Object.values(byType).reduce((n, c) => n + (c?.quarantined ?? 0), 0)
    if (quarantined > 0) {
      add({ id: 'findings:quarantined', kind: 'quarantined', severity: 'medium', params: { count: String(quarantined) }, to: '/security?status=quarantined' })
    }
  }

  for (const s of input.health?.scanners ?? []) {
    if (!s.ready) {
      add({ id: `scanner:${s.name}`, kind: 'scanner-down', severity: 'high', params: { name: s.name, message: s.message ?? '' }, to: '/settings/scanners' })
    }
  }
  const failed = input.health?.scans_failed_24h ?? 0
  if (failed > 0) add({ id: 'scans:failed', kind: 'scans-failed', severity: 'high', params: { count: String(failed) }, to: '/security' })
  const dt = input.health?.dependency_track
  if (dt?.enabled && dt.last_push_ok === false) {
    add({ id: 'dtrack', kind: 'dtrack-failed', severity: 'medium', params: {}, to: '/settings/dependency-track' })
  }

  if (input.flagged && input.flagged.total > 0) {
    add({ id: 'repos:flagged', kind: 'flagged-repos', severity: 'high', params: { count: String(input.flagged.total) }, to: '/repos?flagged=1' })
  }

  for (const r of input.nearQuota ?? []) {
    const q = r.stats?.quota
    const percent = q?.limit_bytes ? Math.round(percentOf(q.used_bytes, q.limit_bytes)) : null
    const severity: AttentionSeverity = percent !== null && percent >= 95 ? 'high' : 'medium'
    add({ id: `quota:${r.name}`, kind: 'quota', severity, params: { name: r.name, percent: percent === null ? '' : String(percent) }, to: `/repos/${encodeURIComponent(r.name)}?tab=settings` })
  }

  for (const tok of input.tokens ?? []) {
    const link = `/settings/tokens?expiring=${TOKEN_HORIZON_DAYS}`
    if (tok.expired || (tok.expires_at && Date.parse(tok.expires_at) <= input.now)) {
      add({ id: `token:${tok.id}`, kind: 'token-expired', severity: 'high', params: { name: tok.name }, to: link })
    } else if (tok.expires_at) {
      const days = Math.max(daysUntil(tok.expires_at, input.now), 1)
      add({ id: `token:${tok.id}`, kind: 'token-expiring', severity: days <= 3 ? 'high' : 'medium', params: { name: tok.name, days: String(days), date: formatDate(tok.expires_at) }, to: link })
    }
  }

  if (input.unscanned && input.unscanned.total > 0) {
    add({ id: 'repos:unscanned', kind: 'unscanned', severity: 'medium', params: { count: String(input.unscanned.total) }, to: '/repos?unscanned=1' })
  }

  return items
    .map((item, order) => ({ item, order }))
    .sort((a, b) => RANK[a.item.severity] - RANK[b.item.severity] || a.order - b.order)
    .map((x) => x.item)
}
