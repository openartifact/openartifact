// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import { pagedResponse, type PagedResult } from './transform'
import type { OidcCondition, OidcTrustPolicy, ServiceAccount } from './generated/types.gen'

export type { OidcCondition }

/** A service account as the list and detail endpoints return it (the id is always present). */
export type ServiceAccountItem = ServiceAccount & { id: string; name: string }

/** A trust policy of the CI OIDC token exchange. */
export type OidcPolicy = Required<OidcTrustPolicy>

/** The writable fields of a trust policy; a patch may send any subset. */
export interface OidcPolicyInput {
  name: string
  issuer: string
  audience: string
  conditions: OidcCondition[]
  service_account_id: string
  token_ttl_seconds: number
  enabled: boolean
}

const enc = encodeURIComponent
const SA_BASE = '/api/v1/service-accounts'
const POLICY_BASE = '/api/v1/oidc-trust-policies'

/** Server-side maximum page size for GET /service-accounts. */
const PAGE_MAX = 200

function send<T>(path: string, method: string, body: unknown): Promise<T> {
  return request<T>(path, { method, headers: JSON_HEADERS, body: JSON.stringify(body) })
}

async function listPage(page: number): Promise<PagedResult<ServiceAccountItem>> {
  const raw = await request<Record<string, unknown>>(`${SA_BASE}?page=${page}&per_page=${PAGE_MAX}`)
  return pagedResponse<ServiceAccountItem>(raw ?? {})
}

/** Every service account by name: walks the pages, since the server clamps `per_page` to 200. */
export async function listAllServiceAccounts(): Promise<PagedResult<ServiceAccountItem>> {
  const items: ServiceAccountItem[] = []
  let total = 0
  for (let page = 1; ; page++) {
    const res = await listPage(page)
    items.push(...res.items)
    total = res.total
    if (res.items.length < PAGE_MAX || items.length >= total) break
  }
  return { total: Math.max(total, items.length), items }
}

export const getServiceAccount = (id: string) => request<ServiceAccountItem>(`${SA_BASE}/${enc(id)}`)

/** `name` is immutable (3-64 letters, digits, `-`, `_`); 409 when a user or account already has it. */
export const createServiceAccount = (body: { name: string; description?: string }) =>
  send<ServiceAccountItem>(SA_BASE, 'POST', body)

export const updateServiceAccount = (id: string, body: { description?: string; disabled?: boolean }) =>
  send<ServiceAccountItem>(`${SA_BASE}/${enc(id)}`, 'PATCH', body)

/** Removes the account with its tokens, memberships, role assignments and trust policies. */
export const deleteServiceAccount = (id: string) => request<void>(`${SA_BASE}/${enc(id)}`, { method: 'DELETE' })

/** Every trust policy, by name (the endpoint is not paged). */
export async function listOidcPolicies(): Promise<OidcPolicy[]> {
  const res = await request<{ data?: OidcPolicy[] }>(POLICY_BASE)
  return res?.data ?? []
}

export const getOidcPolicy = (id: string) => request<OidcPolicy>(`${POLICY_BASE}/${enc(id)}`)

export const createOidcPolicy = (body: OidcPolicyInput) => send<OidcPolicy>(POLICY_BASE, 'POST', body)

export const updateOidcPolicy = (id: string, body: Partial<OidcPolicyInput>) =>
  send<OidcPolicy>(`${POLICY_BASE}/${enc(id)}`, 'PATCH', body)

export const deleteOidcPolicy = (id: string) => request<void>(`${POLICY_BASE}/${enc(id)}`, { method: 'DELETE' })
