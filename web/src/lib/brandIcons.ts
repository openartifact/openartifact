// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Component } from 'vue'
import { KeyIcon, ServerStackIcon, ShieldCheckIcon } from '@heroicons/vue/24/outline'
import {
  siAtlassian,
  siAuth0,
  siAuthentik,
  siBitbucket,
  siGitea,
  siGithub,
  siGitlab,
  siGoogle,
  siKeycloak,
  siOkta,
  siOllama,
  siOpenid,
  siOpenstack,
  siRedhatopenshift,
} from 'simple-icons'
import { t } from '../i18n'

interface BrandIcon {
  path: string
  viewBox: string
}

const viewBox = '0 0 24 24'

const ollamaIcon: BrandIcon = { path: siOllama.path, viewBox }

export function aiBrandIcon(provider: string): BrandIcon | undefined {
  return provider.toLowerCase() === 'ollama' ? ollamaIcon : undefined
}

/**
 * The icon keys the server stores for an SSO provider (besides "" = automatic)
 * and returns, resolved, on the login page. The order is the picker's order.
 */
export const SSO_ICON_KEYS = [
  'google', 'github', 'gitlab', 'okta', 'auth0', 'keycloak', 'authentik', 'gitea',
  'bitbucket', 'atlassian', 'openshift', 'openstack', 'openid', 'ldap', 'saml', 'key',
] as const

export type SsoIconKey = (typeof SSO_ICON_KEYS)[number]

/** A vendor mark from simple-icons (filled path) or a generic heroicon for a protocol. */
export type SsoGlyph =
  | { kind: 'brand'; path: string; viewBox: string }
  | { kind: 'generic'; component: Component }

const brand = (icon: { path: string }): SsoGlyph => ({ kind: 'brand', path: icon.path, viewBox })

const SSO_GLYPHS: Record<SsoIconKey, SsoGlyph> = {
  google: brand(siGoogle),
  github: brand(siGithub),
  gitlab: brand(siGitlab),
  okta: brand(siOkta),
  auth0: brand(siAuth0),
  keycloak: brand(siKeycloak),
  authentik: brand(siAuthentik),
  gitea: brand(siGitea),
  bitbucket: brand(siBitbucket),
  atlassian: brand(siAtlassian),
  openshift: brand(siRedhatopenshift),
  openstack: brand(siOpenstack),
  openid: brand(siOpenid),
  ldap: { kind: 'generic', component: ServerStackIcon },
  saml: { kind: 'generic', component: ShieldCheckIcon },
  key: { kind: 'generic', component: KeyIcon },
}

// Product names and protocol acronyms are not translated.
const SSO_LABELS: Record<Exclude<SsoIconKey, 'key'>, string> = {
  google: 'Google',
  github: 'GitHub',
  gitlab: 'GitLab',
  okta: 'Okta',
  auth0: 'Auth0',
  keycloak: 'Keycloak',
  authentik: 'authentik',
  gitea: 'Gitea',
  bitbucket: 'Bitbucket',
  atlassian: 'Atlassian',
  openshift: 'OpenShift',
  openstack: 'OpenStack',
  openid: 'OpenID Connect',
  ldap: 'LDAP',
  saml: 'SAML',
}

export function isSsoIconKey(key: string): key is SsoIconKey {
  return (SSO_ICON_KEYS as readonly string[]).includes(key)
}

/** The glyph of an icon key; a key this build does not know (a newer server) shows the generic key. */
export function ssoIcon(key: string): SsoGlyph {
  return SSO_GLYPHS[isSsoIconKey(key) ? key : 'key']
}

/** The vendor or protocol name of an icon key, for the picker and the provider card. */
export function ssoIconLabel(key: string): string {
  if (!isSsoIconKey(key) || key === 'key') return t('adm.sso.icon.key')

  return SSO_LABELS[key]
}
