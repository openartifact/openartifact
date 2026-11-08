// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import type { SSOProvider } from '../../../../api/sso'
import { ssoIcon } from '../../../../lib/brandIcons'
import SSOProviderCard from '../SSOProviderCard.vue'

function card(over: Partial<SSOProvider>) {
  const provider: SSOProvider = {
    id: '1', slug: 'corp', kind: 'oidc', display_name: 'Corp', enabled: true, icon: '', resolved_icon: 'okta', callback_url: '',
    auto_provision_users: false, link_by_email: false, created_at: '', updated_at: new Date().toISOString(), ...over,
  }
  return mount(SSOProviderCard, { props: { provider, testing: false } })
}

describe('SSOProviderCard', () => {
  it('shows the resolved icon and the vendor name', () => {
    const w = card({ resolved_icon: 'okta' })
    expect(w.get('path').attributes('d')).toBe((ssoIcon('okta') as { path: string }).path)
    expect(w.get('[data-testid="sso-vendor"]').text()).toBe('Okta')
  })

  it('says "<vendor> via Dex" behind a Dex connector', () => {
    const w = card({ resolved_icon: 'ldap', oidc_config: { type: '', dex_connector_id: 'ldap' } })
    expect(w.get('[data-testid="sso-vendor"]').text()).toBe('LDAP via Dex')
  })

  it('says just Dex when the icon is the generic OpenID one', () => {
    const w = card({ resolved_icon: 'openid', oidc_config: { type: '', dex_connector_id: 'corp-sso' } })
    expect(w.get('[data-testid="sso-vendor"]').text()).toBe('Dex')
  })
})
