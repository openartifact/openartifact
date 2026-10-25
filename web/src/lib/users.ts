// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { User } from '../api/generated/types.gen'

export type StatusFilter = 'all' | 'active' | 'disabled'

/** Users matching the text (username, email, full name), the status and ordered by `sort`. */
export function filterUsers<U extends User>(
  users: readonly U[],
  q: string,
  status: StatusFilter,
  sort: string,
  dir: 'asc' | 'desc',
): U[] {
  const needle = q.trim().toLowerCase()
  const out = users.filter((u) => {
    if (status !== 'all' && (status === 'disabled') !== !!u.disabled) return false
    if (!needle) return true
    return [u.username, u.email, u.full_name].some((v) => v?.toLowerCase().includes(needle))
  })
  const key = sort === 'created_at' ? 'created_at' : 'username'
  const sign = dir === 'desc' ? -1 : 1
  return out.sort((a, b) => sign * String(a[key] ?? '').localeCompare(String(b[key] ?? '')))
}

export function userCounts(users: readonly User[]): { total: number; active: number; disabled: number } {
  const disabled = users.filter((u) => u.disabled).length
  return { total: users.length, active: users.length - disabled, disabled }
}
