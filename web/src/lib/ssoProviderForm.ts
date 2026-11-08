// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { SSOConfigInput, SSOProvider } from '../api/sso'
import { tp } from '../i18n'
import { ssoIconLabel } from './brandIcons'
import { buildOIDCPayload, emptyOIDCForm, oidcFormFrom, validateOIDCForm, type OIDCFormError } from './ssoOidcForm'

export interface SSOTestOutcome {
  ok: boolean
  message?: string
  at: string
}

export function emptyForm() {
  return {
    display_name: '',
    slug: '',
    kind: 'oidc' as 'oidc' | 'saml',
    /** The stored icon key; `''` is automatic. */
    icon: '',
    oidc_config: emptyOIDCForm(),
    saml_config: { idp_metadata_url: '', idp_metadata_xml: '' },
    auto_provision_users: false,
    link_by_email: false,
  }
}

export type SSOFormState = ReturnType<typeof emptyForm>

export function formFromProvider(provider: SSOProvider): SSOFormState {
  return {
    display_name: provider.display_name,
    slug: provider.slug,
    kind: provider.kind,
    icon: provider.icon ?? '',
    oidc_config: oidcFormFrom(provider.oidc_config),
    saml_config: {
      idp_metadata_url: provider.saml_config?.idp_metadata_url ?? '',
      idp_metadata_xml: provider.saml_config?.idp_metadata_xml ?? '',
    },
    auto_provision_users: provider.auto_provision_users,
    link_by_email: provider.link_by_email,
  }
}

export type SSOConfigError = OIDCFormError | 'secretRequired'

/**
 * The top-level config fields shared by POST and PATCH, or the reason the form
 * cannot be saved. The icon is always sent, so a PATCH can set it back to
 * automatic with `''`. On edit the stored client secret cannot be read back:
 * leaving it blank keeps it (the server merges the patch) unless the type,
 * issuer or client ID changed.
 */
export function buildSSOConfig(
  f: SSOFormState,
  editing: SSOProvider | null,
): { config: Partial<SSOConfigInput> } | { error: SSOConfigError } {
  const config: Partial<SSOConfigInput> = {
    icon: f.icon,
    auto_provision_users: f.auto_provision_users,
    link_by_email: f.link_by_email,
  }
  if (f.kind === 'saml') {
    config.saml_config = { ...f.saml_config }
    return { config }
  }
  const invalid = validateOIDCForm(f.oidc_config)
  if (invalid) return { error: invalid }
  const existing = editing?.oidc_config
  if (f.oidc_config.client_secret || !editing) {
    config.oidc_config = buildOIDCPayload(f.oidc_config, true)
    return { config }
  }
  // The server keeps the stored secret when it is omitted, but pointing it at another issuer/client is refused.
  const changed =
    f.oidc_config.type !== (existing?.type ?? '') ||
    f.oidc_config.issuer !== (existing?.issuer ?? '') ||
    f.oidc_config.client_id !== (existing?.client_id ?? '')
  if (changed) return { error: 'secretRequired' }
  config.oidc_config = buildOIDCPayload(f.oidc_config, false)
  return { config }
}

/** The vendor line of a provider card: "GitHub via Dex" behind a Dex connector, the plain vendor otherwise. */
export function ssoVendorLabel(provider: Pick<SSOProvider, 'resolved_icon' | 'oidc_config'>): string {
  const vendor = ssoIconLabel(provider.resolved_icon)
  if (!provider.oidc_config?.dex_connector_id) return vendor
  // OpenID Connect and the generic key say nothing about the upstream.
  if (provider.resolved_icon === 'openid' || provider.resolved_icon === 'key') return 'Dex'

  return tp('adm.sso.viaDex', { vendor })
}
