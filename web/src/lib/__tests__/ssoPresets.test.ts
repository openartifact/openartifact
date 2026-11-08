// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { t } from '../../i18n'
import { buildOIDCPayload } from '../ssoOidcForm'
import {
  DEX_CONNECTORS, DEX_CONNECTOR_TYPES, SSO_PRESETS, SSO_PRESET_IDS, applyDexConnectorType, applyPreset,
  dexConnectorLabel, guessDexConnectorType, inferPreset, presetLabel, ssoCallbackUrl,
} from '../ssoPresets'
import { emptyForm } from '../ssoProviderForm'

const PLACEHOLDER = /[<>{}]/

describe('presets', () => {
  it('only a fixed real issuer is ever prefilled, never a placeholder pattern', () => {
    for (const id of SSO_PRESET_IDS) {
      const form = emptyForm()
      applyPreset(form, id)
      expect(form.oidc_config.issuer).toBe(id === 'google' ? 'https://accounts.google.com' : '')
      expect(PLACEHOLDER.test(form.oidc_config.issuer)).toBe(false)
      expect(buildOIDCPayload(form.oidc_config, true).issuer).not.toMatch(PLACEHOLDER)
    }
    expect(SSO_PRESETS.okta.issuerPattern).toBe('https://<org>.okta.com')
    expect(SSO_PRESETS.keycloak.issuerPattern).toBe('https://<host>/realms/<realm>')
    expect(SSO_PRESETS.auth0.issuerPattern).toBe('https://<tenant>.auth0.com/')
    expect(SSO_PRESETS.authentik.issuerPattern).toBe('https://<host>/application/o/<slug>/')
    expect(SSO_PRESETS.entra.issuerPattern).toBe('https://login.microsoftonline.com/<tenant-id>/v2.0')
  })

  it('prefills the scopes and the stored type of the preset', () => {
    const form = emptyForm()
    applyPreset(form, 'github')
    expect(form.oidc_config).toMatchObject({ type: 'github', preset: 'github', scopes: 'read:user user:email read:org' })
    applyPreset(form, 'oauth2')
    expect(form.oidc_config).toMatchObject({ type: 'oauth2', scopes: '' })
    applyPreset(form, 'okta')
    expect(form.oidc_config).toMatchObject({ type: '', scopes: 'openid email profile groups' })
    applyPreset(form, 'dex')
    expect(form.oidc_config).toMatchObject({ type: '', preset: 'dex', scopes: 'openid email profile groups federated:id' })
  })

  it('keeps what the admin typed when the preset changes, and replaces what the previous preset prefilled', () => {
    const form = emptyForm()
    applyPreset(form, 'google')
    form.oidc_config.scopes = 'openid email custom'
    form.oidc_config.issuer = 'https://accounts.example.org'
    applyPreset(form, 'okta')
    expect(form.oidc_config.scopes).toBe('openid email custom')
    expect(form.oidc_config.issuer).toBe('https://accounts.example.org')

    const prefilled = emptyForm()
    applyPreset(prefilled, 'google')
    applyPreset(prefilled, 'keycloak')
    expect(prefilled.oidc_config.issuer).toBe('')
    expect(prefilled.oidc_config.scopes).toBe('openid email profile')
  })

  it('clears the settings the new preset does not show so they are not saved', () => {
    const form = emptyForm()
    applyPreset(form, 'entra')
    form.oidc_config.allowed_tenants = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'
    form.oidc_config.graph_groups = true
    form.oidc_config.hosted_domain = 'example.org'
    applyPreset(form, 'okta')
    expect(form.oidc_config).toMatchObject({ allowed_tenants: '', hosted_domain: '', graph_groups: false })
    const payload = buildOIDCPayload({ ...form.oidc_config, client_id: 'c', issuer: 'https://acme.okta.com' }, false)
    expect(payload).toMatchObject({ allowed_tenants: [], hosted_domain: '', graph_groups: false })
  })

  it('shows only the settings relevant to a preset', () => {
    expect(SSO_PRESETS.entra.fields).toEqual(['allowed_tenants', 'graph_endpoint'])
    expect(SSO_PRESETS.entra.graphGroups).toBe(true)
    expect(SSO_PRESETS.google.fields).toEqual(['hosted_domain'])
    expect(SSO_PRESETS.github.fields).toEqual(['allowed_orgs', 'auth_base_url', 'api_base_url'])
    expect(SSO_PRESETS.oauth2.fields).toEqual(['authorization_url', 'token_url', 'userinfo_url'])
    expect(SSO_PRESETS.dex.fields).toEqual(['dex_connector_id'])
    expect(SSO_PRESETS.dex.graphGroups).toBe(false)
    expect(SSO_PRESETS.oidc.fields).toEqual(['allowed_tenants', 'hosted_domain', 'graph_endpoint'])
  })

  it('labels vendors by name and the generic presets from the catalog', () => {
    expect(presetLabel(SSO_PRESETS.entra)).toBe('Microsoft Entra')
    expect(presetLabel(SSO_PRESETS.authentik)).toBe('authentik')
    expect(presetLabel(SSO_PRESETS.dex)).toBe(t('adm.sso.type.dex'))
    expect(presetLabel(SSO_PRESETS.oidc)).toBe(t('adm.sso.type.oidc'))
  })
})

describe('inferPreset', () => {
  it('uses only stored data: type, then the Dex connector, else generic OIDC', () => {
    expect(inferPreset({ type: 'github' })).toBe('github')
    expect(inferPreset({ type: 'oauth2' })).toBe('oauth2')
    expect(inferPreset({ type: '', dex_connector_id: 'ldap' })).toBe('dex')
    expect(inferPreset({ type: '', issuer: 'https://acme.okta.com' })).toBe('oidc')
    expect(inferPreset({ type: '', issuer: 'https://accounts.google.com' })).toBe('oidc')
    expect(inferPreset(undefined)).toBe('oidc')
  })

  it('guesses the Dex connector type from the ID only when it is a known type', () => {
    for (const type of DEX_CONNECTOR_TYPES.filter((t) => t !== 'other')) expect(guessDexConnectorType(type)).toBe(type)
    expect(guessDexConnectorType('corp-ldap')).toBe('other')
    expect(guessDexConnectorType('')).toBe('other')
  })
})

describe('Dex connector type', () => {
  function dexForm() {
    const form = emptyForm()
    applyPreset(form, 'dex')
    return form
  }

  it('defaults the connector ID to the type ID and the icon to the matching one', () => {
    const expected: Record<string, [string, string]> = {
      ldap: ['ldap', 'ldap'], github: ['github', 'github'], gitlab: ['gitlab', 'gitlab'], google: ['google', 'google'],
      microsoft: ['microsoft', ''], oidc: ['oidc', ''], local: ['local', ''], other: ['', ''],
    }
    for (const type of DEX_CONNECTOR_TYPES) {
      const form = dexForm()
      applyDexConnectorType(form, type)
      expect([form.oidc_config.dex_connector_id, form.icon]).toEqual(expected[type])
      expect(form.oidc_config.dex_connector_type).toBe(type)
    }
  })

  it('follows the type while the admin has not typed their own ID, then stops', () => {
    const form = dexForm()
    applyDexConnectorType(form, 'ldap')
    applyDexConnectorType(form, 'github')
    expect(form.oidc_config.dex_connector_id).toBe('github')
    expect(form.icon).toBe('github')
    form.oidc_config.dex_connector_id = 'corp-sso'
    applyDexConnectorType(form, 'gitlab')
    expect(form.oidc_config.dex_connector_id).toBe('corp-sso')
    expect(form.icon).toBe('gitlab')
  })

  it('keeps an icon the admin picked', () => {
    const form = dexForm()
    form.icon = 'okta'
    applyDexConnectorType(form, 'ldap')
    expect(form.icon).toBe('okta')
    expect(form.oidc_config.dex_connector_id).toBe('ldap')
  })

  it('drops the connector and its icon when leaving Dex, keeps another icon', () => {
    const form = dexForm()
    applyDexConnectorType(form, 'github')
    applyPreset(form, 'okta')
    expect(form.oidc_config).toMatchObject({ dex_connector_id: '', preset: 'okta' })
    expect(form.icon).toBe('')

    const custom = dexForm()
    applyDexConnectorType(custom, 'github')
    custom.icon = 'gitea'
    applyPreset(custom, 'okta')
    expect(custom.icon).toBe('gitea')
  })

  it('persists only the connector id and the icon, nothing preset-related', () => {
    const form = dexForm()
    applyDexConnectorType(form, 'ldap')
    form.oidc_config.client_id = 'oa'
    form.oidc_config.issuer = 'https://dex.example.org'
    const payload = buildOIDCPayload(form.oidc_config, false)
    expect(payload.dex_connector_id).toBe('ldap')
    expect(Object.keys(payload)).not.toContain('preset')
    expect(Object.keys(payload)).not.toContain('dex_connector_type')
  })

  it('has a docs page for every connector type and a label', () => {
    for (const type of DEX_CONNECTOR_TYPES) {
      expect(DEX_CONNECTORS[type].docs.startsWith('https://dexidp.io/docs/connectors/')).toBe(true)
      expect(dexConnectorLabel(type)).not.toBe('')
    }
    expect(dexConnectorLabel('local')).toBe(t('adm.sso.dexType.local'))
  })
})

describe('ssoCallbackUrl', () => {
  it('builds the OIDC redirect URI and the SAML ACS URL from the origin and the slug', () => {
    expect(ssoCallbackUrl('https://artifacts.example.org', 'oidc', 'corp')).toBe(
      'https://artifacts.example.org/api/v1/auth/sso/oidc/corp/callback',
    )
    expect(ssoCallbackUrl('https://artifacts.example.org/', 'saml', 'corp')).toBe(
      'https://artifacts.example.org/api/v1/auth/sso/saml/corp/acs',
    )
  })

  it('shows a slug placeholder until one is typed', () => {
    expect(ssoCallbackUrl('http://localhost:8080', 'oidc', '')).toBe('http://localhost:8080/api/v1/auth/sso/oidc/<slug>/callback')
  })
})
