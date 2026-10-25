// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import { pagedResponse } from './transform'
import type { Repository, User } from './generated/types.gen'

/** Repositories whose name contains `q`, for the command palette. */
export async function searchRepositories(q: string, limit: number, signal?: AbortSignal): Promise<Repository[]> {
  const qs = new URLSearchParams({ q, limit: String(limit) })
  const res = await request<{ repositories?: Repository[] }>(`/api/v1/repositories?${qs}`, { signal })
  return res?.repositories ?? []
}

/** Users matching `q` (administrators only), for the command palette. */
export async function searchUsers(q: string, limit: number, signal?: AbortSignal): Promise<User[]> {
  const qs = new URLSearchParams({ q, per_page: String(limit) })
  const raw = await request<Record<string, unknown>>(`/api/v1/users?${qs}`, { signal })
  return pagedResponse<User>(raw ?? {}).items
}
