// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { OIDCConfigInput, OIDCType } from '../api/admin'

// List-valued settings are edited as one comma/space-separated text field.
export type OIDCTextKey =
  | 'scopes' | 'allowed_tenants' | 'allowed_orgs' | 'hosted_domain' | 'graph_endpoint'
  | 'authorization_url' | 'token_url' | 'userinfo_url' | 'auth_base_url' | 'api_base_url'

export interface OIDCForm extends Record<OIDCTextKey, string> {
  type: OIDCType
  issuer: string
  client_id: string
  client_secret: string
  require_email_verified: boolean
  graph_groups: boolean
}

export function emptyOIDCForm(): OIDCForm {
  return {
    type: '', issuer: '', client_id: '', client_secret: '',
    scopes: '', allowed_tenants: '', allowed_orgs: '', hosted_domain: '', graph_endpoint: '',
    authorization_url: '', token_url: '', userinfo_url: '', auth_base_url: '', api_base_url: '',
    require_email_verified: false, graph_groups: false,
  }
}

function splitList(value: string): string[] {
  return value.split(/[\s,]+/).filter(Boolean)
}

/**
 * The oidc_config body for the form. Every setting is sent, those that do not
 * apply to the selected type as empty, so switching type clears the previous
 * type's fields on the server.
 */
export function buildOIDCPayload(f: OIDCForm, includeSecret: boolean): OIDCConfigInput {
  const oidc = f.type === ''
  const payload: OIDCConfigInput = {
    type: f.type,
    issuer: oidc ? f.issuer : '',
    client_id: f.client_id,
    require_email_verified: f.require_email_verified,
    graph_groups: oidc && f.graph_groups,
    hosted_domain: oidc ? f.hosted_domain : '',
    graph_endpoint: oidc ? f.graph_endpoint : '',
    allowed_tenants: oidc ? splitList(f.allowed_tenants) : [],
    allowed_orgs: f.type === 'github' ? splitList(f.allowed_orgs) : [],
    auth_base_url: f.type === 'github' ? f.auth_base_url : '',
    api_base_url: f.type === 'github' ? f.api_base_url : '',
    authorization_url: f.type === 'oauth2' ? f.authorization_url : '',
    token_url: f.type === 'oauth2' ? f.token_url : '',
    userinfo_url: f.type === 'oauth2' ? f.userinfo_url : '',
    scopes: splitList(f.scopes),
  }
  if (includeSecret) payload.client_secret = f.client_secret
  return payload
}

/** Fills the form from a stored (secret-less) provider config. */
export function oidcFormFrom(cfg: OIDCConfigInput | undefined): OIDCForm {
  const form = emptyOIDCForm()
  if (!cfg) return form
  form.type = cfg.type ?? ''
  form.issuer = cfg.issuer ?? ''
  form.client_id = cfg.client_id ?? ''
  form.require_email_verified = cfg.require_email_verified ?? false
  form.graph_groups = cfg.graph_groups ?? false
  form.hosted_domain = cfg.hosted_domain ?? ''
  form.graph_endpoint = cfg.graph_endpoint ?? ''
  form.authorization_url = cfg.authorization_url ?? ''
  form.token_url = cfg.token_url ?? ''
  form.userinfo_url = cfg.userinfo_url ?? ''
  form.auth_base_url = cfg.auth_base_url ?? ''
  form.api_base_url = cfg.api_base_url ?? ''
  form.scopes = (cfg.scopes ?? []).join(' ')
  form.allowed_tenants = (cfg.allowed_tenants ?? []).join(', ')
  form.allowed_orgs = (cfg.allowed_orgs ?? []).join(', ')
  return form
}
