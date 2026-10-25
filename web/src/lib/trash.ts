// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t, tp } from '../i18n/en'
import type { Repository } from '../api/generated/types.gen'

const DAY_MS = 86_400_000

/** True when the typed text equals the repository name exactly (no trimming). */
export function confirmNameMatches(typed: string, name: string): boolean {
  return name !== '' && typed === name
}

/** Whole days (rounded up) until `purgeAt`; 0 when due or in the past; null when unscheduled/invalid. */
export function daysUntilPurge(purgeAt: string | null | undefined, now: number = Date.now()): number | null {
  if (!purgeAt) return null
  const at = new Date(purgeAt).getTime()
  if (Number.isNaN(at)) return null
  return Math.max(0, Math.ceil((at - now) / DAY_MS))
}

/** Human text for the purge column: "purges in 3 days", "purges today", or a dash. */
export function purgeInText(purgeAt: string | null | undefined, now: number = Date.now()): string {
  const days = daysUntilPurge(purgeAt, now)
  if (days === null) return '—'
  if (days === 0) return t('trash.purgesToday')
  if (days === 1) return t('trash.purgesInOneDay')
  return tp('trash.purgesInDays', { n: String(days) })
}

/** Parses the retention input: '' -> null (server default), integer >= 0 -> number, otherwise undefined (invalid). */
export function parseRetentionInput(raw: string | number | null | undefined): number | null | undefined {
  if (raw === null || raw === undefined) return null
  const s = String(raw).trim()
  if (s === '') return null
  if (!/^\d+$/.test(s)) return undefined
  return Number(s)
}

/** Toast text for an "empty trash" result: how many were purged and, when capped, how many are left. */
export function emptyTrashResultText(purged: number, remaining: number): string {
  const base = tp('trash.emptiedCount', { n: String(purged) })
  return remaining > 0 ? `${base} ${tp('trash.emptiedRemaining', { n: String(remaining) })}` : base
}

/** Per-repository capabilities of the current user (`Repository.permissions`). */
export type RepoPermissions = NonNullable<Repository['permissions']>

interface TrashCapabilities {
  restore: boolean
  empty: boolean
  purge: boolean
}

/**
 * What the trash UI offers. Repo scope: restore needs repository `delete`, emptying needs
 * `manage` (admins have both); purging a single item is always administrative. The global
 * view is admin-only, so everything it shows is actionable.
 */
export function trashCapabilities(
  scope: 'repo' | 'global',
  isAdmin: boolean,
  permissions: Partial<RepoPermissions> | null | undefined,
): TrashCapabilities {
  if (scope === 'global') return { restore: isAdmin, empty: false, purge: isAdmin }
  return {
    restore: isAdmin || permissions?.delete === true,
    empty: isAdmin || permissions?.manage === true,
    purge: isAdmin,
  }
}
