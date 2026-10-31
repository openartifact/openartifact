// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ServiceAccountItem } from '../api/serviceAccounts'

/** Account names: 3-64 letters, digits, `-` and `_` (the server's rule, checked again there). */
export const SERVICE_ACCOUNT_NAME = /^[a-zA-Z0-9_-]{3,64}$/

export type StatusFilter = 'all' | 'active' | 'disabled'

/** Search box and status filter of the list: name and description, case-insensitive. */
export function matchesAccount(account: ServiceAccountItem, query: string, status: StatusFilter): boolean {
  if (status === 'active' && account.disabled) return false
  if (status === 'disabled' && !account.disabled) return false
  const q = query.trim().toLowerCase()
  return !q || [account.name, account.description].some((v) => v?.toLowerCase().includes(q))
}

/** Where an account's own page lives. */
export const accountPath = (id: string): string => `/settings/service-accounts/${encodeURIComponent(id)}`
