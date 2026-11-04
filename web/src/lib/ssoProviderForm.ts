// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { SSOProvider } from '../api/sso'
import { emptyOIDCForm, oidcFormFrom } from './ssoOidcForm'

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
    oidc_config: oidcFormFrom(provider.oidc_config),
    saml_config: {
      idp_metadata_url: provider.saml_config?.idp_metadata_url ?? '',
      idp_metadata_xml: provider.saml_config?.idp_metadata_xml ?? '',
    },
    auto_provision_users: provider.auto_provision_users,
    link_by_email: provider.link_by_email,
  }
}
