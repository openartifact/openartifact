// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { User } from '../api/generated/types.gen'

export type StatusFilter = 'all' | 'active' | 'disabled'

/** Whether a user passes the status filter and the text (username, email, full name; case-insensitive). */
export function matchesUser(u: User, q: string, status: StatusFilter): boolean {
  if (status !== 'all' && (status === 'disabled') !== !!u.disabled) return false
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return [u.username, u.email, u.full_name].some((v) => v?.toLowerCase().includes(needle))
}

export function userCounts(users: readonly User[]): { total: number; active: number; disabled: number } {
  const disabled = users.filter((u) => u.disabled).length
  return { total: users.length, active: users.length - disabled, disabled }
}
