// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shared by the repository and the project cleanup screens: the UI view of a
// policy, the edit form and the request body. The two scopes use the same
// wire shape.

/** A policy as the API returns it (repository or project scope). */
export interface ApiCleanupPolicy {
  id: string
  name: string
  enabled: boolean
  keep_last_n?: number | null
  keep_since_days?: number | null
  name_pattern?: string | null
  last_run_at?: string | null
  created_at: string
}

export type CleanupPolicyType = 'keep-last-n' | 'keep-by-age'

export interface CleanupPolicyView {
  id: string
  name: string
  type: CleanupPolicyType
  keep_count?: number
  keep_days?: number
  name_pattern: string
  enabled: boolean
  last_run_at?: string
  created_at: string
}

export interface CleanupPolicyForm {
  name: string
  type: CleanupPolicyType
  keep_count: number | undefined
  keep_days: number | undefined
  /** Regular expression a package name must match for the rule to apply; empty means every package. */
  name_pattern: string
  enabled: boolean
}

export function cleanupPolicyFromApi(p: ApiCleanupPolicy): CleanupPolicyView {
  return {
    id: p.id,
    name: p.name,
    type: p.keep_last_n != null ? 'keep-last-n' : 'keep-by-age',
    keep_count: p.keep_last_n ?? undefined,
    keep_days: p.keep_since_days ?? undefined,
    name_pattern: p.name_pattern ?? '',
    enabled: p.enabled,
    last_run_at: p.last_run_at ?? undefined,
    created_at: p.created_at,
  }
}

export function emptyCleanupForm(): CleanupPolicyForm {
  return { name: '', type: 'keep-last-n', keep_count: 10, keep_days: 30, name_pattern: '', enabled: true }
}

export function cleanupFormFrom(p: CleanupPolicyView): CleanupPolicyForm {
  return { name: p.name, type: p.type, keep_count: p.keep_count, keep_days: p.keep_days, name_pattern: p.name_pattern, enabled: p.enabled }
}

/**
 * True when `pattern` compiles as a regular expression. The server compiles it with
 * Go's RE2 (`regexp.Compile`), the browser with JavaScript's engine; the few RE2-only
 * spellings (leading inline flags, `(?P<name>`, `\A`, `\z`, POSIX classes) are
 * rewritten first so a pattern the server accepts is not refused here, and lookaround
 * and backreferences, which only JavaScript accepts, are refused.
 */
export function cleanupPatternValid(pattern: string): boolean {
  if (pattern === '') return true
  // RE2 has no lookaround and no backreferences; JavaScript accepts both.
  if (/\(\?<?[=!]|(^|[^\\])(\\\\)*\\[1-9]/.test(pattern)) return false
  const js = pattern
    .replace(/^\(\?[imsU]+\)/, '')
    .replace(/\(\?P</g, '(?<')
    .replace(/\\A/g, '^')
    .replace(/\\z/g, '$')
    .replace(/\[:\^?\w+:\]/g, 'a-z')
  try {
    new RegExp(js)
    return true
  } catch {
    return false
  }
}

/** True when the rule of the selected type has a usable value and the name pattern compiles (what the server evaluates). */
export function cleanupRuleValid(f: CleanupPolicyForm): boolean {
  const value = f.type === 'keep-last-n' ? (f.keep_count ?? 0) > 0 : (f.keep_days ?? 0) > 0
  return value && cleanupPatternValid(f.name_pattern)
}

export function cleanupFormValid(f: CleanupPolicyForm): boolean {
  return f.name.trim() !== '' && cleanupRuleValid(f)
}

/** Request body for create and update: only the rule of the selected type is sent, with the name pattern. */
export function cleanupPayload(f: CleanupPolicyForm): Record<string, unknown> {
  // name_pattern is always sent: an update replaces it, so an empty one clears it.
  const payload: Record<string, unknown> = { name: f.name, enabled: f.enabled, name_pattern: f.name_pattern }
  if (f.type === 'keep-last-n') payload.keep_last_n = f.keep_count
  else payload.keep_since_days = f.keep_days
  return payload
}
