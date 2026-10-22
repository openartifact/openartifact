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
  enabled: boolean
  last_run_at?: string
  created_at: string
}

export interface CleanupPolicyForm {
  name: string
  type: CleanupPolicyType
  keep_count: number | undefined
  keep_days: number | undefined
  enabled: boolean
}

export function cleanupPolicyFromApi(p: ApiCleanupPolicy): CleanupPolicyView {
  return {
    id: p.id,
    name: p.name,
    type: p.keep_last_n != null ? 'keep-last-n' : 'keep-by-age',
    keep_count: p.keep_last_n ?? undefined,
    keep_days: p.keep_since_days ?? undefined,
    enabled: p.enabled,
    last_run_at: p.last_run_at ?? undefined,
    created_at: p.created_at,
  }
}

export function emptyCleanupForm(): CleanupPolicyForm {
  return { name: '', type: 'keep-last-n', keep_count: 10, keep_days: 30, enabled: true }
}

export function cleanupFormFrom(p: CleanupPolicyView): CleanupPolicyForm {
  return { name: p.name, type: p.type, keep_count: p.keep_count, keep_days: p.keep_days, enabled: p.enabled }
}

export function cleanupFormValid(f: CleanupPolicyForm): boolean {
  if (!f.name.trim()) return false
  return f.type === 'keep-last-n' ? (f.keep_count ?? 0) > 0 : (f.keep_days ?? 0) > 0
}

/** Request body for create and update: only the rule of the selected type is sent. */
export function cleanupPayload(f: CleanupPolicyForm): Record<string, unknown> {
  const payload: Record<string, unknown> = { name: f.name, enabled: f.enabled }
  if (f.type === 'keep-last-n') payload.keep_last_n = f.keep_count
  else payload.keep_since_days = f.keep_days
  return payload
}
