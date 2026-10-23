// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { buildOIDCPayload, emptyOIDCForm, oidcFormFrom } from '../ssoOidcForm'

describe('buildOIDCPayload', () => {
  it('sends only the OIDC settings for a standard provider and clears the others', () => {
    const form = {
      ...emptyOIDCForm(),
      issuer: 'https://login.microsoftonline.com/organizations/v2.0',
      client_id: 'cid',
      client_secret: 's3cret',
      allowed_tenants: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa, bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      scopes: 'openid  email groups',
      graph_groups: true,
      allowed_orgs: 'ignored',
      token_url: 'https://ignored.example/token',
    }
    expect(buildOIDCPayload(form, true)).toMatchObject({
      type: '',
      issuer: 'https://login.microsoftonline.com/organizations/v2.0',
      client_secret: 's3cret',
      allowed_tenants: ['aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb'],
      scopes: ['openid', 'email', 'groups'],
      graph_groups: true,
      allowed_orgs: [],
      token_url: '',
    })
  })

  it('omits the secret when it is not being changed', () => {
    const payload = buildOIDCPayload({ ...emptyOIDCForm(), issuer: 'https://idp.example', client_id: 'c' }, false)
    expect('client_secret' in payload).toBe(false)
  })

  it('github keeps its organizations and enterprise URLs, and drops the issuer and Graph settings', () => {
    const form = {
      ...emptyOIDCForm(),
      type: 'github' as const,
      issuer: 'https://stale.example',
      client_id: 'c',
      allowed_orgs: 'Acme,other',
      api_base_url: 'https://ghe.example/api/v3',
      graph_groups: true,
      hosted_domain: 'example.com',
    }
    expect(buildOIDCPayload(form, false)).toMatchObject({
      type: 'github',
      issuer: '',
      allowed_orgs: ['Acme', 'other'],
      api_base_url: 'https://ghe.example/api/v3',
      graph_groups: false,
      hosted_domain: '',
    })
  })

  it('oauth2 sends its three endpoints', () => {
    const form = {
      ...emptyOIDCForm(),
      type: 'oauth2' as const,
      client_id: 'c',
      authorization_url: 'https://idp.example/authorize',
      token_url: 'https://idp.example/token',
      userinfo_url: 'https://idp.example/userinfo',
    }
    expect(buildOIDCPayload(form, true)).toMatchObject({
      type: 'oauth2',
      authorization_url: 'https://idp.example/authorize',
      token_url: 'https://idp.example/token',
      userinfo_url: 'https://idp.example/userinfo',
    })
  })
})

describe('oidcFormFrom', () => {
  it('round-trips a stored config into the editable form', () => {
    const form = oidcFormFrom({
      type: 'github',
      client_id: 'c',
      allowed_orgs: ['acme', 'other'],
      scopes: ['read:user', 'user:email'],
      require_email_verified: true,
    })
    expect(form).toMatchObject({
      type: 'github',
      client_id: 'c',
      allowed_orgs: 'acme, other',
      scopes: 'read:user user:email',
      require_email_verified: true,
      client_secret: '',
    })
  })

  it('an absent config gives the empty form', () => {
    expect(oidcFormFrom(undefined)).toEqual(emptyOIDCForm())
  })
})
