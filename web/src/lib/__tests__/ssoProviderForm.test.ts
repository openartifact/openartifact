// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import type { SSOProvider } from '../../api/sso'
import { t } from '../../i18n'
import { applyDexConnectorType, applyPreset } from '../ssoPresets'
import { buildSSOConfig, emptyForm, formFromProvider, ssoVendorLabel } from '../ssoProviderForm'

function provider(over: Partial<SSOProvider> = {}): SSOProvider {
  return {
    id: '1', slug: 'corp', kind: 'oidc', display_name: 'Corp', enabled: true,
    icon: '', resolved_icon: 'openid', callback_url: 'https://oa.example.org/api/v1/auth/sso/oidc/corp/callback',
    oidc_config: { type: '', issuer: 'https://idp.example.org', client_id: 'c' },
    auto_provision_users: false, link_by_email: false, created_at: '', updated_at: '',
    ...over,
  }
}

describe('icon round trip', () => {
  it('an automatic icon is read as "" and sent as "" so a PATCH clears a stored one', () => {
    const form = formFromProvider(provider({ icon: '' }))
    expect(form.icon).toBe('')
    const built = buildSSOConfig(form, provider({ icon: 'github' }))
    expect('config' in built && built.config.icon).toBe('')
  })

  it('a stored icon survives an edit', () => {
    const form = formFromProvider(provider({ icon: 'gitea' }))
    expect(form.icon).toBe('gitea')
    const built = buildSSOConfig(form, provider({ icon: 'gitea' }))
    expect('config' in built && built.config.icon).toBe('gitea')
  })

  it('is sent for SAML too', () => {
    const form = { ...emptyForm(), kind: 'saml' as const, icon: 'saml' }
    expect(buildSSOConfig(form, null)).toEqual({
      config: expect.objectContaining({ icon: 'saml', saml_config: { idp_metadata_url: '', idp_metadata_xml: '' } }),
    })
  })
})

describe('editing infers the preset only from stored data', () => {
  it('maps type and connector id back to a preset', () => {
    expect(formFromProvider(provider({ oidc_config: { type: 'github', client_id: 'c' } })).oidc_config.preset).toBe('github')
    expect(formFromProvider(provider({ oidc_config: { type: 'oauth2', client_id: 'c' } })).oidc_config.preset).toBe('oauth2')
    const dex = formFromProvider(provider({ oidc_config: { type: '', client_id: 'c', dex_connector_id: 'gitlab' } })).oidc_config
    expect(dex).toMatchObject({ preset: 'dex', dex_connector_type: 'gitlab', dex_connector_id: 'gitlab' })
    expect(formFromProvider(provider({ oidc_config: { type: '', client_id: 'c', dex_connector_id: 'corp' } })).oidc_config.dex_connector_type).toBe('other')
    expect(formFromProvider(provider({ oidc_config: { type: '', issuer: 'https://acme.okta.com', client_id: 'c' } })).oidc_config.preset).toBe('oidc')
  })

  it('an edited Dex provider saves the same connector it loaded', () => {
    const stored = provider({ oidc_config: { type: '', issuer: 'https://dex.example.org', client_id: 'c', dex_connector_id: 'corp-ldap', scopes: ['openid', 'federated:id'] } })
    const built = buildSSOConfig(formFromProvider(stored), stored)
    expect('config' in built && built.config.oidc_config).toMatchObject({ dex_connector_id: 'corp-ldap', scopes: ['openid', 'federated:id'] })
  })
})

describe('buildSSOConfig', () => {
  it('rejects an invalid Dex connector id and scopes without federated:id', () => {
    const form = emptyForm()
    applyPreset(form, 'dex')
    expect(buildSSOConfig(form, null)).toEqual({ error: 'dexConnectorInvalid' })
    applyDexConnectorType(form, 'ldap')
    form.oidc_config.scopes = 'openid email'
    expect(buildSSOConfig(form, null)).toEqual({ error: 'dexScopesFederated' })
  })

  it('asks for the secret when an edit points the stored one at another issuer', () => {
    const stored = provider()
    const form = formFromProvider(stored)
    form.oidc_config.issuer = 'https://other.example.org'
    expect(buildSSOConfig(form, stored)).toEqual({ error: 'secretRequired' })
    form.oidc_config.client_secret = 'new'
    expect('config' in buildSSOConfig(form, stored)).toBe(true)
  })

  it('omits the secret when an edit leaves it blank', () => {
    const stored = provider()
    const built = buildSSOConfig(formFromProvider(stored), stored)
    expect('config' in built && 'client_secret' in built.config.oidc_config!).toBe(false)
  })
})

describe('ssoVendorLabel', () => {
  it('shows the plain vendor, or "<vendor> via Dex" behind a Dex connector', () => {
    expect(ssoVendorLabel({ resolved_icon: 'okta' })).toBe('Okta')
    expect(ssoVendorLabel({ resolved_icon: 'github', oidc_config: { dex_connector_id: 'github' } })).toBe('GitHub via Dex')
    expect(ssoVendorLabel({ resolved_icon: 'ldap', oidc_config: { dex_connector_id: 'ldap' } })).toBe('LDAP via Dex')
  })

  it('says just Dex when the icon names no upstream', () => {
    expect(ssoVendorLabel({ resolved_icon: 'openid', oidc_config: { dex_connector_id: 'corp' } })).toBe('Dex')
    expect(ssoVendorLabel({ resolved_icon: 'key', oidc_config: { dex_connector_id: 'corp' } })).toBe('Dex')
    expect(ssoVendorLabel({ resolved_icon: 'openid' })).toBe('OpenID Connect')
    expect(ssoVendorLabel({ resolved_icon: 'key' })).toBe(t('adm.sso.icon.key'))
  })
})
