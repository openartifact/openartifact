// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, ApiError, JSON_HEADERS } from './client'

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

/** Response shape of the admin SSO API: the client secret is never returned, so `oidc_config.client_secret` is always absent. */
export interface SSOProvider {
  id: string
  slug: string
  kind: 'oidc' | 'saml'
  display_name: string
  enabled: boolean
  oidc_config?: OIDCConfigInput & { has_secret?: boolean }
  saml_config?: SAMLConfigInput
  auto_provision_users: boolean
  link_by_email: boolean
  created_at: string
  updated_at: string
}

/** Partial update sent by {@link updateSSOProvider}; the server merges it into the stored provider. */
export type SSOProviderPatch = Partial<Omit<SSOConfigInput, 'slug' | 'kind'>> & { enabled?: boolean }

interface SSOTestResult {
  ok: boolean
  message?: string
  error?: string
}

const BASE = '/api/v1/admin/sso-providers'
const enc = encodeURIComponent

export async function listAdminSSOProviders(): Promise<SSOProvider[]> {
  const res = await request<{ providers?: SSOProvider[] }>(BASE)
  return res?.providers ?? []
}

export async function saveSSOProvider(body: SSOConfigInput): Promise<void> {
  return request<void>(BASE, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export function updateSSOProvider(slug: string, body: SSOProviderPatch): Promise<SSOProvider> {
  return request<SSOProvider>(`${BASE}/${enc(slug)}`, {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export function deleteSSOProvider(slug: string): Promise<void> {
  return request<void>(`${BASE}/${enc(slug)}`, { method: 'DELETE' })
}

/** Resolves with `ok: false` both for transport errors and for 200 responses that report failure. */
export async function testSSOProvider(slug: string): Promise<SSOTestResult> {
  try {
    const res = await request<SSOTestResult>(`${BASE}/${enc(slug)}/test`, { method: 'POST' })
    return { ...res, ok: res?.ok === true }
  } catch (err) {
    if (err instanceof ApiError) return { ok: false, message: err.message }
    throw err
  }
}
