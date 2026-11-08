// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { SSOProvider } from '../../api/sso'
import SSOProviderForm from '../../pages/settings/sections/SSOProviderForm.vue'
import { emptyForm, formFromProvider } from '../../lib/ssoProviderForm'
import { SSO_ICON_KEYS, ssoIcon } from '../../lib/brandIcons'

function mountForm(editing: SSOProvider | null = null, form = editing ? formFromProvider(editing) : emptyForm()) {
  const wrapper = mount(SSOProviderForm, {
    props: { editing, modelValue: form, 'onUpdate:modelValue': () => {} },
    global: { plugins: [createPinia()] },
  })
  return { form, wrapper }
}

function stored(over: Partial<SSOProvider> = {}): SSOProvider {
  return {
    id: '1', slug: 'corp', kind: 'oidc', display_name: 'Corp SSO', enabled: true,
    icon: '', resolved_icon: 'github', callback_url: 'https://artifacts.example.org/api/v1/auth/sso/oidc/corp/callback',
    oidc_config: { type: 'github', client_id: 'c', allowed_orgs: ['acme'] },
    auto_provision_users: false, link_by_email: false, created_at: '', updated_at: '',
    ...over,
  }
}

const FIELDS = ['allowed_tenants', 'hosted_domain', 'graph_endpoint', 'allowed_orgs', 'auth_base_url', 'api_base_url', 'authorization_url', 'token_url', 'userinfo_url', 'dex_connector_id']

function visibleFields(wrapper: ReturnType<typeof mountForm>['wrapper']): string[] {
  return FIELDS.filter((key) => wrapper.find(`#oidc-${key}`).exists())
}

describe('SSOProviderForm Dex preset', () => {
  it('hides the connector field until Dex is selected, then shows it and hides it again', async () => {
    const { form, wrapper } = mountForm()
    expect(wrapper.find('#oidc-dex_connector_id').exists()).toBe(false)
    await wrapper.get('#oidc-type').setValue('dex')
    expect(form.oidc_config.preset).toBe('dex')
    expect(form.oidc_config.type).toBe('')
    const input = wrapper.get('#oidc-dex_connector_id')
    await input.setValue('github-main')
    expect(form.oidc_config.dex_connector_id).toBe('github-main')
    await wrapper.get('#oidc-type').setValue('github')
    expect(form.oidc_config.dex_connector_id).toBe('')
    expect(wrapper.find('#oidc-dex_connector_id').exists()).toBe(false)
  })

  it('drops the Entra and Google settings when switching a plain OIDC provider to Dex', async () => {
    const { form, wrapper } = mountForm()
    await wrapper.get('#oidc-allowed_tenants').setValue('tenant-a')
    await wrapper.get('#oidc-hosted_domain').setValue('example.org')
    form.oidc_config.graph_groups = true
    await wrapper.get('#oidc-type').setValue('dex')
    expect(wrapper.find('#oidc-allowed_tenants').exists()).toBe(false)
    expect(wrapper.find('#oidc-hosted_domain').exists()).toBe(false)
    expect(form.oidc_config.allowed_tenants).toBe('')
    expect(form.oidc_config.hosted_domain).toBe('')
    expect(form.oidc_config.graph_groups).toBe(false)
  })

  it('choosing a connector type sets the connector ID and the icon, and the snippet follows', async () => {
    const { form, wrapper } = mountForm()
    await wrapper.get('#oidc-type').setValue('dex')
    await wrapper.get('#oidc-dex_connector_type').setValue('github')
    expect(form.oidc_config.dex_connector_id).toBe('github')
    expect(form.icon).toBe('github')
    await wrapper.get('#issuer').setValue('https://dex.example.org')
    await wrapper.get('#client-id').setValue('openartifact')
    await wrapper.get('#display-name').setValue('Corp GitHub')
    const snippet = wrapper.get('[data-testid="sso-dex-snippet"]').text()
    expect(snippet).toContain('id: openartifact')
    expect(snippet).toContain('type: github')
    expect(snippet).toContain('id: github')
    expect(snippet).toContain('https://dex.example.org/callback')
    expect(snippet).toContain(`${window.location.origin}/api/v1/auth/sso/oidc/<slug>/callback`)
    expect(wrapper.get('a[href="https://dexidp.io/docs/connectors/github/"]').attributes('rel')).toContain('noopener')
  })

  it('shows no Dex snippet for another preset', async () => {
    const { wrapper } = mountForm()
    expect(wrapper.find('[data-testid="sso-dex-snippet"]').exists()).toBe(false)
    await wrapper.get('#oidc-type').setValue('okta')
    expect(wrapper.find('[data-testid="sso-dex-snippet"]').exists()).toBe(false)
  })
})

describe('SSOProviderForm presets', () => {
  it('shows only the settings relevant to the selected preset', async () => {
    const { wrapper } = mountForm()
    expect(visibleFields(wrapper)).toEqual(['allowed_tenants', 'hosted_domain', 'graph_endpoint'])
    const expected: Record<string, string[]> = {
      google: ['hosted_domain'],
      entra: ['allowed_tenants', 'graph_endpoint'],
      okta: [],
      keycloak: [],
      auth0: [],
      authentik: [],
      github: ['allowed_orgs', 'auth_base_url', 'api_base_url'],
      oauth2: ['authorization_url', 'token_url', 'userinfo_url'],
      dex: ['dex_connector_id'],
    }
    for (const [preset, fields] of Object.entries(expected)) {
      await wrapper.get('#oidc-type').setValue(preset)
      expect(visibleFields(wrapper)).toEqual(fields)
    }
  })

  it('the Entra groups toggle belongs to the generic and Entra presets', async () => {
    const { wrapper } = mountForm()
    expect(wrapper.find('#sso-graphgroups-label').exists()).toBe(true)
    await wrapper.get('#oidc-type').setValue('entra')
    expect(wrapper.find('#sso-graphgroups-label').exists()).toBe(true)
    await wrapper.get('#oidc-type').setValue('keycloak')
    expect(wrapper.find('#sso-graphgroups-label').exists()).toBe(false)
  })

  it('has no issuer field for GitHub and OAuth 2.0', async () => {
    const { wrapper } = mountForm()
    expect(wrapper.find('#issuer').exists()).toBe(true)
    for (const preset of ['github', 'oauth2']) {
      await wrapper.get('#oidc-type').setValue(preset)
      expect(wrapper.find('#issuer').exists()).toBe(false)
    }
  })

  it('shows the issuer pattern as a placeholder and leaves the value empty, but prefills Google', async () => {
    const { form, wrapper } = mountForm()
    await wrapper.get('#oidc-type').setValue('okta')
    expect(wrapper.get<HTMLInputElement>('#issuer').attributes('placeholder')).toBe('https://<org>.okta.com')
    expect(form.oidc_config.issuer).toBe('')
    await wrapper.get('#oidc-type').setValue('keycloak')
    expect(wrapper.get<HTMLInputElement>('#issuer').attributes('placeholder')).toBe('https://<host>/realms/<realm>')
    await wrapper.get('#oidc-type').setValue('google')
    expect(form.oidc_config.issuer).toBe('https://accounts.google.com')
    expect(wrapper.get<HTMLInputElement>('#issuer').element.value).toBe('https://accounts.google.com')
  })

  it('prefills the scopes of the preset', async () => {
    const { form, wrapper } = mountForm()
    await wrapper.get('#oidc-type').setValue('github')
    expect(wrapper.get<HTMLInputElement>('#oidc-scopes').element.value).toBe('read:user user:email read:org')
    expect(form.oidc_config.scopes).toBe('read:user user:email read:org')
  })

  it('an existing provider opens on the preset its stored data implies', () => {
    const github = mountForm(stored())
    expect(github.wrapper.get<HTMLSelectElement>('#oidc-type').element.value).toBe('github')
    const okta = mountForm(stored({ oidc_config: { type: '', issuer: 'https://acme.okta.com', client_id: 'c' } }))
    expect(okta.wrapper.get<HTMLSelectElement>('#oidc-type').element.value).toBe('oidc')
    expect(visibleFields(okta.wrapper)).toEqual(['allowed_tenants', 'hosted_domain', 'graph_endpoint'])
    const dex = mountForm(stored({ oidc_config: { type: '', issuer: 'https://dex.example.org', client_id: 'c', dex_connector_id: 'corp' } }))
    expect(dex.wrapper.get<HTMLSelectElement>('#oidc-type').element.value).toBe('dex')
    expect(dex.wrapper.get<HTMLSelectElement>('#oidc-dex_connector_type').element.value).toBe('other')
  })
})

describe('SSOProviderForm icon picker', () => {
  it('offers Automatic and the 16 icons, each with a glyph, as one radio group', () => {
    const { wrapper } = mountForm()
    const radios = wrapper.findAll<HTMLInputElement>('input[name="sso-icon"]')
    expect(radios.map((r) => r.element.value)).toEqual(['', ...SSO_ICON_KEYS])
    expect(radios.filter((r) => r.element.checked).map((r) => r.element.value)).toEqual([''])
    expect(wrapper.find('fieldset[role="radiogroup"]').exists()).toBe(true)
    const pick = wrapper.get('input[value="github"]').element.closest('label')!
    expect(pick.querySelector('path')?.getAttribute('d')).toBe((ssoIcon('github') as { path: string }).path)
  })

  it('emits the chosen key and can go back to automatic', async () => {
    const { form, wrapper } = mountForm()
    await wrapper.get('input[value="gitea"]').setValue()
    expect(form.icon).toBe('gitea')
    expect(wrapper.get<HTMLInputElement>('input[value="gitea"]').element.checked).toBe(true)
    await wrapper.get('input[value=""]').setValue()
    expect(form.icon).toBe('')
  })

  it('names what Automatic resolves to for a saved provider, plain Automatic for a new one', () => {
    expect(mountForm().wrapper.get('input[value=""]').element.closest('label')!.textContent!.trim()).toBe('Automatic')
    const saved = mountForm(stored({ resolved_icon: 'github' }))
    expect(saved.wrapper.get('input[value=""]').element.closest('label')!.textContent!.trim()).toBe('Automatic (GitHub)')
    expect(saved.wrapper.get('input[value=""]').element.closest('label')!.querySelector('path')).not.toBeNull()
  })

  it('preselects the stored icon of an existing provider', () => {
    const { wrapper } = mountForm(stored({ icon: 'okta' }))
    expect(wrapper.get<HTMLInputElement>('input[value="okta"]').element.checked).toBe(true)
  })
})

describe('SSOProviderForm callback URL', () => {
  it('computes the URL of the typed slug from the page origin for a new provider', async () => {
    const { wrapper } = mountForm()
    await wrapper.get('#slug').setValue('corp')
    expect(wrapper.get('[data-testid="sso-callback-url"]').text()).toBe(`${window.location.origin}/api/v1/auth/sso/oidc/corp/callback`)
    await wrapper.findAll('[role="radio"]')[1].trigger('click')
    expect(wrapper.get('[data-testid="sso-callback-url"]').text()).toBe(`${window.location.origin}/api/v1/auth/sso/saml/corp/acs`)
  })

  it('shows the exact URL the server returned for a saved provider', () => {
    const { wrapper } = mountForm(stored())
    expect(wrapper.get('[data-testid="sso-callback-url"]').text()).toBe('https://artifacts.example.org/api/v1/auth/sso/oidc/corp/callback')
  })
})
