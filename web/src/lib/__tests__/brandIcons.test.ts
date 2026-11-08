// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { siGithub } from 'simple-icons'
import { t } from '../../i18n'
import { SSO_ICON_KEYS, isSsoIconKey, ssoIcon, ssoIconLabel } from '../brandIcons'

describe('SSO icons', () => {
  it('has the 16 keys the server stores', () => {
    expect([...SSO_ICON_KEYS]).toEqual([
      'google', 'github', 'gitlab', 'okta', 'auth0', 'keycloak', 'authentik', 'gitea',
      'bitbucket', 'atlassian', 'openshift', 'openstack', 'openid', 'ldap', 'saml', 'key',
    ])
  })

  it('gives every key a drawable glyph and a label', () => {
    for (const key of SSO_ICON_KEYS) {
      const glyph = ssoIcon(key)
      if (glyph.kind === 'brand') {
        expect(glyph.path.length).toBeGreaterThan(20)
        expect(glyph.viewBox).toBe('0 0 24 24')
      } else {
        expect(glyph.component).toBeDefined()
      }
      expect(ssoIconLabel(key)).not.toBe('')
    }
  })

  it('draws vendors from simple-icons and the protocols with generic icons', () => {
    expect(ssoIcon('github')).toEqual({ kind: 'brand', path: siGithub.path, viewBox: '0 0 24 24' })
    for (const key of ['ldap', 'saml', 'key']) expect(ssoIcon(key).kind).toBe('generic')
    expect(SSO_ICON_KEYS.filter((k) => ssoIcon(k).kind === 'generic')).toEqual(['ldap', 'saml', 'key'])
  })

  it('labels vendors with their own names and translates only the generic key', () => {
    expect(ssoIconLabel('github')).toBe('GitHub')
    expect(ssoIconLabel('authentik')).toBe('authentik')
    expect(ssoIconLabel('openid')).toBe('OpenID Connect')
    expect(ssoIconLabel('openshift')).toBe('OpenShift')
    expect(ssoIconLabel('key')).toBe(t('adm.sso.icon.key'))
  })

  it('falls back to the generic key for a key this build does not know', () => {
    expect(isSsoIconKey('microsoft')).toBe(false)
    expect(ssoIcon('microsoft')).toEqual(ssoIcon('key'))
    expect(ssoIconLabel('microsoft')).toBe(t('adm.sso.icon.key'))
  })
})
