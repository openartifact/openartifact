// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure logic of the Access tokens page: filter values, expiry state, and the
// rules of the create drawer (which types are offered, role and lifetime caps).

import type { TokenListParams, TokenType, UnifiedToken } from '../api/tokens'
import type { ProjectMemberRole } from '../api/projects'
import { atLeast } from './projectAccess'
import { t, tp } from '../i18n'

export const TOKEN_TYPES: readonly TokenType[] = ['personal', 'project', 'federation']
/** `all` is the unfiltered list (the legacy `/admin/tokens` redirect uses it). */
export const TOKEN_TYPE_FILTERS = ['all', ...TOKEN_TYPES] as const
export type TokenTypeFilter = (typeof TOKEN_TYPE_FILTERS)[number]

/** Quick filter: tokens that expire within this many days (the dashboard links here). */
export const EXPIRING_FILTERS = ['', '7', '30'] as const
export type ExpiringFilter = (typeof EXPIRING_FILTERS)[number]

/** A token that expires within this many days is flagged as expiring. */
export const SOON_DAYS = 14

const DAY_MS = 86_400_000

/** RFC 3339 instant `days` from `now`: the `expires_before` of a quick filter. */
export function expiresBeforeFor(days: ExpiringFilter, now: number = Date.now()): string | undefined {
  return days === '' ? undefined : new Date(now + Number(days) * DAY_MS).toISOString()
}

/** The list request of the page's filters. */
export function toListParams(
  f: { type: TokenTypeFilter; q: string; expiring: ExpiringFilter; project: string },
  now: number = Date.now(),
): TokenListParams {
  return {
    type: f.type === 'all' ? undefined : f.type,
    q: f.q.trim() || undefined,
    expiresBefore: expiresBeforeFor(f.expiring, now),
    project: f.project || undefined,
  }
}

export type ExpiryState =
  | { kind: 'never' }
  | { kind: 'expired'; days: number }
  | { kind: 'soon'; days: number }
  | { kind: 'ok'; days: number }

/** Whole days until `expires_at` (negative once passed); `expired` from the server wins over the clock. */
export function expiryOf(token: Pick<UnifiedToken, 'expires_at' | 'expired'>, now: number = Date.now()): ExpiryState {
  if (!token.expires_at) return { kind: 'never' }
  const ms = new Date(token.expires_at).getTime() - now
  const days = Math.ceil(ms / DAY_MS)
  if (token.expired || ms <= 0) return { kind: 'expired', days: Math.abs(Math.floor(ms / DAY_MS)) }
  return days <= SOON_DAYS ? { kind: 'soon', days } : { kind: 'ok', days }
}

/** The expiry as words, so state never rests on colour alone: "Expires in 3 days", "Expired yesterday", "No expiry". */
export function expiryText(state: ExpiryState): string {
  const days = String(state.kind === 'never' ? 0 : state.days)
  switch (state.kind) {
    case 'never':
      return t('tok.expiry.never')
    case 'expired':
      return state.days === 0 ? t('tok.expiry.expiredToday') : state.days === 1 ? t('tok.expiry.expiredYesterday') : tp('tok.expiry.expiredAgo', { days })
    default:
      return state.days <= 1 ? t('tok.expiry.tomorrow') : tp('tok.expiry.inDays', { days })
  }
}

/** Parses a `?type=` value for a form preselection: only an offered type is accepted. */
export function presetType(raw: string | null | undefined, offered: readonly TokenType[]): TokenType {
  return offered.find((t) => t === raw) ?? offered[0] ?? 'personal'
}

/** Types the create drawer offers: personal always, project when the caller has a project, federation for admins. */
export function offeredTypes(opts: { isAdmin: boolean; hasProjects: boolean }): TokenType[] {
  const out: TokenType[] = ['personal']
  if (opts.hasProjects) out.push('project')
  if (opts.isAdmin) out.push('federation')
  return out
}

/** Project token roles, lowest first. */
export const PROJECT_TOKEN_ROLES: readonly ('viewer' | 'developer')[] = ['viewer', 'developer']

/** A token never outranks its creator: viewers mint viewer tokens, developers and above both roles. */
export function grantableTokenRoles(myRole: ProjectMemberRole | undefined): readonly ('viewer' | 'developer')[] {
  return atLeast(myRole, 'developer') ? PROJECT_TOKEN_ROLES : ['viewer']
}

/** Lifetimes offered in days; 0 is no expiry. */
export const EXPIRY_PRESETS: readonly number[] = [30, 90, 365, 0]
export const DEFAULT_EXPIRY_DAYS = 90

/**
 * Lifetimes the drawer offers for a type. Federation tokens never expire.
 * A project can cap its tokens (`token_max_ttl_days`, 0 = no cap): the presets
 * above the cap go away, the cap itself is offered when it falls between presets,
 * and "no expiry" is no longer an option.
 */
export function expiryChoices(type: TokenType, maxDays: number): number[] {
  if (type === 'federation') return []
  if (type !== 'project' || maxDays <= 0) return [...EXPIRY_PRESETS]
  const finite = EXPIRY_PRESETS.filter((d) => d > 0)
  const within = finite.filter((d) => d <= maxDays)
  return within.includes(maxDays) ? within : [...within, maxDays]
}

/** The default lifetime among `choices`: 90 days when offered, else the longest offered. */
export function defaultExpiry(choices: readonly number[]): number {
  if (choices.includes(DEFAULT_EXPIRY_DAYS)) return DEFAULT_EXPIRY_DAYS
  const finite = choices.filter((d) => d > 0)
  return finite.length ? Math.max(...finite) : 0
}

/** `expires_at` of a lifetime in days; undefined for "no expiry". */
export function expiresAtFor(days: number, now: number = Date.now()): string | undefined {
  return days > 0 ? new Date(now + days * DAY_MS).toISOString() : undefined
}

export type NameProblem = 'required' | 'tooLong' | 'invalid'

const FEDERATION_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]*$/

/** Client-side name check mirroring the server: 1-100 characters, federation names 1-63 of letters, digits, `.`, `_`, `-`. */
export function nameProblem(type: TokenType, raw: string): NameProblem | null {
  const name = raw.trim()
  if (!name) return 'required'
  if (type === 'federation') {
    if (name.length > 63) return 'tooLong'
    return FEDERATION_NAME.test(name) ? null : 'invalid'
  }
  return name.length > 100 ? 'tooLong' : null
}

/** What the table shows in the scope column. */
export type ScopeCell =
  | { kind: 'personal'; scopes: number }
  | { kind: 'project'; project: string; role?: string }
  | { kind: 'federation'; repository: string; project?: string }

export function scopeOf(t: UnifiedToken): ScopeCell {
  switch (t.type) {
    case 'personal':
      return { kind: 'personal', scopes: t.scopes?.length ?? 0 }
    case 'project':
      return { kind: 'project', project: t.project ?? '', role: t.role }
    case 'federation':
      return { kind: 'federation', repository: t.repository ?? '', project: t.project }
  }
}
