// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { OIDCConfigInput, OIDCType } from '../api/sso'
import { t, type TranslationKey } from '../i18n'
import type { SsoIconKey } from './brandIcons'
import type { OIDCTextKey } from './ssoOidcForm'
import type { SSOFormState } from './ssoProviderForm'

// Vendor presets are UI only: they prefill and narrow the OIDC form, and nothing
// about them is stored. An existing provider shows the preset its stored data
// implies (see inferPreset), never one guessed from its issuer.

export const SSO_PRESET_IDS = [
  'oidc', 'google', 'entra', 'okta', 'keycloak', 'auth0', 'authentik', 'github', 'oauth2', 'dex',
] as const

export type SSOPresetId = (typeof SSO_PRESET_IDS)[number]

export const DEX_CONNECTOR_TYPES = ['ldap', 'github', 'gitlab', 'google', 'microsoft', 'oidc', 'local', 'other'] as const

export type DexConnectorType = (typeof DEX_CONNECTOR_TYPES)[number]

export interface SSOPreset {
  id: SSOPresetId
  /** The stored `oidc_config.type` of the preset. */
  type: OIDCType
  /** Vendor names are not translated; the generic presets use a catalog label. */
  brand?: string
  labelKey?: TranslationKey
  /** Scopes prefilled while the admin has not typed their own. */
  scopes: string
  /** A fixed, real issuer that is prefilled (Google only); every other issuer is typed by the admin. */
  issuer?: string
  /** What the issuer looks like, shown as the input placeholder; never saved. */
  issuerPattern?: string
  hint: TranslationKey
  /** Settings shown after the client secret (the scopes field is common to all). */
  fields: readonly OIDCTextKey[]
  /** Whether the Entra groups-overage toggle applies. */
  graphGroups: boolean
}

const OIDC_SCOPES = 'openid email profile'

export const SSO_PRESETS: Record<SSOPresetId, SSOPreset> = {
  oidc: {
    id: 'oidc', type: '', labelKey: 'adm.sso.type.oidc', scopes: '',
    issuerPattern: 'https://idp.example.com', hint: 'adm.sso.preset.hint.oidc',
    fields: ['allowed_tenants', 'hosted_domain', 'graph_endpoint'], graphGroups: true,
  },
  google: {
    id: 'google', type: '', brand: 'Google', scopes: OIDC_SCOPES,
    issuer: 'https://accounts.google.com', issuerPattern: 'https://accounts.google.com',
    hint: 'adm.sso.preset.hint.google', fields: ['hosted_domain'], graphGroups: false,
  },
  entra: {
    id: 'entra', type: '', brand: 'Microsoft Entra', scopes: OIDC_SCOPES,
    issuerPattern: 'https://login.microsoftonline.com/<tenant-id>/v2.0', hint: 'adm.sso.preset.hint.entra',
    fields: ['allowed_tenants', 'graph_endpoint'], graphGroups: true,
  },
  okta: {
    id: 'okta', type: '', brand: 'Okta', scopes: 'openid email profile groups',
    issuerPattern: 'https://<org>.okta.com', hint: 'adm.sso.preset.hint.okta', fields: [], graphGroups: false,
  },
  keycloak: {
    id: 'keycloak', type: '', brand: 'Keycloak', scopes: OIDC_SCOPES,
    issuerPattern: 'https://<host>/realms/<realm>', hint: 'adm.sso.preset.hint.keycloak', fields: [], graphGroups: false,
  },
  auth0: {
    id: 'auth0', type: '', brand: 'Auth0', scopes: OIDC_SCOPES,
    issuerPattern: 'https://<tenant>.auth0.com/', hint: 'adm.sso.preset.hint.auth0', fields: [], graphGroups: false,
  },
  authentik: {
    id: 'authentik', type: '', brand: 'authentik', scopes: OIDC_SCOPES,
    issuerPattern: 'https://<host>/application/o/<slug>/', hint: 'adm.sso.preset.hint.authentik', fields: [], graphGroups: false,
  },
  github: {
    id: 'github', type: 'github', labelKey: 'adm.sso.type.github', scopes: 'read:user user:email read:org',
    hint: 'adm.sso.preset.hint.github', fields: ['allowed_orgs', 'auth_base_url', 'api_base_url'], graphGroups: false,
  },
  oauth2: {
    id: 'oauth2', type: 'oauth2', labelKey: 'adm.sso.type.oauth2', scopes: '',
    hint: 'adm.sso.preset.hint.oauth2', fields: ['authorization_url', 'token_url', 'userinfo_url'], graphGroups: false,
  },
  dex: {
    id: 'dex', type: '', labelKey: 'adm.sso.type.dex', scopes: 'openid email profile groups federated:id',
    issuerPattern: 'https://dex.example.com', hint: 'adm.sso.preset.hint.dex', fields: ['dex_connector_id'], graphGroups: false,
  },
}

export function presetLabel(preset: SSOPreset): string {
  return preset.labelKey ? t(preset.labelKey) : (preset.brand ?? preset.id)
}

/** Every text setting a preset may show; those a preset does not show are cleared when it is selected. */
const PRESET_TEXT_KEYS: readonly OIDCTextKey[] = [
  'allowed_tenants', 'hosted_domain', 'graph_endpoint', 'allowed_orgs', 'auth_base_url', 'api_base_url',
  'authorization_url', 'token_url', 'userinfo_url', 'dex_connector_id',
]

/** The preset a stored config shows: only stored data counts, a vendor is never guessed from the issuer. */
export function inferPreset(cfg: OIDCConfigInput | undefined): SSOPresetId {
  if (cfg?.type === 'github') return 'github'
  if (cfg?.type === 'oauth2') return 'oauth2'
  if (cfg?.dex_connector_id) return 'dex'

  return 'oidc'
}

interface DexConnectorDefaults {
  /** The connector ID a connector of this type is usually given; empty leaves the choice to the admin. */
  id: string
  /** The login icon that fits the upstream; empty keeps it automatic. */
  icon: SsoIconKey | ''
  /** Page of the connector in the Dex docs. */
  docs: string
}

const DEX_DOCS = 'https://dexidp.io/docs/connectors/'

export const DEX_CONNECTORS: Record<DexConnectorType, DexConnectorDefaults> = {
  ldap: { id: 'ldap', icon: 'ldap', docs: `${DEX_DOCS}ldap/` },
  github: { id: 'github', icon: 'github', docs: `${DEX_DOCS}github/` },
  gitlab: { id: 'gitlab', icon: 'gitlab', docs: `${DEX_DOCS}gitlab/` },
  google: { id: 'google', icon: 'google', docs: `${DEX_DOCS}google/` },
  microsoft: { id: 'microsoft', icon: '', docs: `${DEX_DOCS}microsoft/` },
  oidc: { id: 'oidc', icon: '', docs: `${DEX_DOCS}oidc/` },
  local: { id: 'local', icon: '', docs: `${DEX_DOCS}local/` },
  other: { id: '', icon: '', docs: DEX_DOCS },
}

/** The connector type of a stored connector ID: the one whose default ID it is, else `other`. */
export function guessDexConnectorType(id: string): DexConnectorType {
  return DEX_CONNECTOR_TYPES.find((type) => type !== 'other' && DEX_CONNECTORS[type].id === id) ?? 'other'
}

// Connector types are product and protocol names, except the two generic ones.
export const DEX_TYPE_NAMES: Record<Exclude<DexConnectorType, 'local' | 'other'>, string> = {
  ldap: 'LDAP', github: 'GitHub', gitlab: 'GitLab', google: 'Google', microsoft: 'Microsoft', oidc: 'OpenID Connect',
}

export function dexConnectorLabel(type: DexConnectorType): string {
  if (type === 'local') return t('adm.sso.dexType.local')
  if (type === 'other') return t('adm.sso.dexType.other')

  return DEX_TYPE_NAMES[type]
}

/**
 * Selects a preset: sets the stored type, clears the settings it does not show
 * and prefills its scopes and issuer, but only over values the admin has not
 * typed (empty, or still the previous preset's prefill).
 */
export function applyPreset(form: SSOFormState, id: SSOPresetId): void {
  const oidc = form.oidc_config
  const prev = SSO_PRESETS[oidc.preset]
  const next = SSO_PRESETS[id]

  if (oidc.preset === 'dex' && id !== 'dex' && form.icon === DEX_CONNECTORS[oidc.dex_connector_type].icon) form.icon = ''
  oidc.preset = id
  oidc.type = next.type
  if (prev.id !== 'dex' || id !== 'dex') oidc.dex_connector_type = 'other'
  for (const key of PRESET_TEXT_KEYS) if (!next.fields.includes(key)) oidc[key] = ''
  if (!next.graphGroups) oidc.graph_groups = false
  if (oidc.scopes === '' || oidc.scopes === prev.scopes) oidc.scopes = next.scopes
  if (oidc.issuer === '' || oidc.issuer === (prev.issuer ?? '')) oidc.issuer = next.issuer ?? ''
}

/**
 * Selects the Dex connector type: the connector ID becomes the type's usual ID
 * and the icon the one that fits the upstream, only while the admin has not
 * typed their own ID or picked another icon.
 */
export function applyDexConnectorType(form: SSOFormState, type: DexConnectorType): void {
  const oidc = form.oidc_config
  const from = DEX_CONNECTORS[oidc.dex_connector_type]
  const to = DEX_CONNECTORS[type]

  if (oidc.dex_connector_id === '' || oidc.dex_connector_id === from.id) oidc.dex_connector_id = to.id
  if (form.icon === '' || form.icon === from.icon) form.icon = to.icon
  oidc.dex_connector_type = type
}

/** The redirect URI (OIDC) or ACS URL (SAML) of a provider, as the server builds it from BASE_URL. */
export function ssoCallbackUrl(origin: string, kind: 'oidc' | 'saml', slug: string): string {
  const base = `${origin.replace(/\/+$/, '')}/api/v1/auth/sso/${kind}/${slug || '<slug>'}`

  return kind === 'oidc' ? `${base}/callback` : `${base}/acs`
}
