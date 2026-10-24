// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, ApiError } from './client'
import { pagedResponse, type PagedResult } from './transform'
import type { User, Group, PermissionTarget, PublicSystemInfo, SystemInfo } from './generated/types.gen'

export type UserItem = User
export type GroupItem = Group
export type PermissionItem = PermissionTarget
export type { PublicSystemInfo, SystemInfo }

/** Actions understood by the permission-target handler (validActions in Go). */
export const PERMISSION_ACTIONS = ['read', 'write', 'delete', 'manage', 'annotate'] as const
export type PermissionAction = (typeof PERMISSION_ACTIONS)[number]

const JSON_HEADERS = { 'Content-Type': 'application/json' }

async function paged<T>(qs: URLSearchParams, path: string): Promise<PagedResult<T>> {
  const query = qs.toString() ? `?${qs}` : ''
  const raw = await request<Record<string, unknown>>(`${path}${query}`)
  return pagedResponse<T>(raw ?? {})
}

export interface ListParams {
  page?: number
  limit?: number
  q?: string
}

function buildQs(p?: ListParams): URLSearchParams {
  const qs = new URLSearchParams()
  if (p?.page != null) qs.set('page', String(p.page))
  if (p?.limit != null) qs.set('per_page', String(p.limit))
  if (p?.q) qs.set('q', p.q)
  return qs
}

// Users

/** Server-side maximum page size for GET /users. */
const USERS_PAGE_MAX = 200

export const listUsers = (p?: ListParams) => paged<UserItem>(buildQs(p), '/api/v1/users')

/**
 * Fetch every user by walking the paginated endpoint. The server clamps
 * `per_page` to 200, so a single large request would silently truncate.
 */
export async function listAllUsers(): Promise<PagedResult<UserItem>> {
  const items: UserItem[] = []
  let total = 0
  for (let page = 1; ; page++) {
    const res = await listUsers({ page, limit: USERS_PAGE_MAX })
    items.push(...res.items)
    total = res.total
    if (res.items.length < USERS_PAGE_MAX || items.length >= total) break
  }
  return { total: Math.max(total, items.length), items }
}

export const createUser = (body: {
  username: string
  email?: string
  full_name?: string
  password: string
  must_change_password?: boolean
}) =>
  request<UserItem>('/api/v1/users', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
export const updateUser = (
  id: string | number,
  body: { email?: string; full_name?: string; disabled?: boolean; must_change_password?: boolean; password?: string },
) =>
  request<UserItem>(`/api/v1/users/${encodeURIComponent(String(id))}`, {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
/** Note: the backend soft-deletes by disabling the account. */
export const deleteUser = (id: string | number) =>
  request<void>(`/api/v1/users/${encodeURIComponent(String(id))}`, { method: 'DELETE' })
export const setUserDisabled = (id: string | number, disabled: boolean) => updateUser(id, { disabled })
export const forceUserPasswordReset = (id: string | number) => updateUser(id, { must_change_password: true })

// Groups
export const listGroups = (p?: ListParams) => paged<GroupItem>(buildQs(p), '/api/v1/groups')
export const createGroup = (body: { name: string; description?: string }) =>
  request<GroupItem>('/api/v1/groups', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
export const renameGroup = (id: string | number, name: string) =>
  request<GroupItem>(`/api/v1/groups/${encodeURIComponent(String(id))}`, {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify({ name }),
  })
export const deleteGroup = (id: string | number) =>
  request<void>(`/api/v1/groups/${encodeURIComponent(String(id))}`, { method: 'DELETE' })
export const listGroupMembers = (id: string | number) =>
  paged<UserItem>(new URLSearchParams(), `/api/v1/groups/${encodeURIComponent(String(id))}/members`)
/** Adds a single member without touching the rest of the membership. */
export const addGroupMember = (groupId: string | number, userId: string | number) =>
  request<void>(
    `/api/v1/groups/${encodeURIComponent(String(groupId))}/members/${encodeURIComponent(String(userId))}`,
    { method: 'POST' },
  )
export const removeGroupMember = (groupId: string | number, userId: string | number) =>
  request<void>(
    `/api/v1/groups/${encodeURIComponent(String(groupId))}/members/${encodeURIComponent(String(userId))}`,
    { method: 'DELETE' },
  )

// Permission targets
export const listPermissions = (p?: ListParams) => paged<PermissionItem>(buildQs(p), '/api/v1/permission-targets')
export const createPermissionTarget = (body: {
  name: string
  repository_filter: string
  path_pattern: string
  actions: PermissionAction[]
}) =>
  request<PermissionItem>('/api/v1/permission-targets', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
export const assignPermissionTarget = (
  id: string | number,
  principal: { principal_type: 'user' | 'group'; principal_id: string },
) =>
  request<void>(`/api/v1/permission-targets/${encodeURIComponent(String(id))}/assignments`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(principal),
  })
export const revokePermission = (id: string | number) =>
  request<void>(`/api/v1/permission-targets/${encodeURIComponent(String(id))}`, { method: 'DELETE' })

// System
export async function getSystemInfo(): Promise<PublicSystemInfo | null> {
  try {
    return await request<PublicSystemInfo>('/api/v1/system/info')
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 501)) {
      return null
    }
    throw err
  }
}

/** Full details from the admin-only endpoint; the public one returns only version/status. */
export interface AdminSystemInfo extends SystemInfo {
  commit?: string
  build_time?: string
  go_version?: string
  os?: string
  arch?: string
  uptime?: string
  db_status?: string
  storage_kind?: string
  storage_status?: string
}

export const getAdminSystemInfo = () => request<AdminSystemInfo>('/api/v1/admin/system/info')

// SSO config. OIDC/SAML settings sit at the top level of the body
// (`oidc_config` / `saml_config`), matching models.SSOProvider.
/** `''` is a standards-compliant OIDC provider (issuer discovery). */
export type OIDCType = '' | 'github' | 'oauth2'

/** Mirrors models.OIDCConfig; `client_secret` is write-only (omit it to keep the stored one). */
export interface OIDCConfigInput {
  type?: OIDCType
  issuer?: string
  client_id?: string
  client_secret?: string
  scopes?: string[]
  allowed_tenants?: string[]
  hosted_domain?: string
  require_email_verified?: boolean
  graph_groups?: boolean
  graph_endpoint?: string
  authorization_url?: string
  token_url?: string
  userinfo_url?: string
  auth_base_url?: string
  api_base_url?: string
  allowed_orgs?: string[]
}

export interface SAMLConfigInput {
  idp_metadata_url?: string
  idp_metadata_xml?: string
}

export interface SSOConfigInput {
  slug: string
  kind: 'oidc' | 'saml'
  display_name: string
  oidc_config?: OIDCConfigInput
  saml_config?: SAMLConfigInput
  auto_provision_users?: boolean
  link_by_email?: boolean
}

export async function saveSSOProvider(body: SSOConfigInput): Promise<void> {
  return request<void>('/api/v1/admin/sso-providers', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export interface SSOTestResult {
  ok: boolean
  message?: string
  error?: string
}

/** Resolves with `ok: false` both for transport errors and for 200 responses that report failure. */
export async function testSSOProvider(slug: string): Promise<SSOTestResult> {
  try {
    const res = await request<SSOTestResult>(
      `/api/v1/admin/sso-providers/${encodeURIComponent(slug)}/test`,
      { method: 'POST' },
    )
    return { ...res, ok: res?.ok === true }
  } catch (err) {
    if (err instanceof ApiError) return { ok: false, message: err.message }
    throw err
  }
}
