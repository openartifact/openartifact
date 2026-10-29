// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { tDynamic } from '../i18n'

/**
 * The failure codes the server puts in `/login?sso_error=<code>` when an SSO
 * login, callback or ACS fails (internal/api/sso_common.go `ssoErrorCodes`; a
 * Go test keeps the two lists equal), each with the catalog key of its message.
 * Several codes share a message when the user can do the same about them.
 */
const SSO_ERROR_KEYS: Record<string, string> = {
  sso_login_denied: 'login.ssoError.denied',
  user_not_provisioned: 'login.ssoError.notProvisioned',
  account_disabled: 'login.ssoError.disabled',
  provider_not_found: 'login.ssoError.providerUnavailable',
  oidc_discovery_failed: 'login.ssoError.idp',
  idp_metadata_unavailable: 'login.ssoError.idp',
  id_token_missing: 'login.ssoError.idp',
  exchange_failed: 'login.ssoError.idp',
  id_token_expired: 'login.ssoError.response',
  nonce_mismatch: 'login.ssoError.response',
  saml_signature_invalid: 'login.ssoError.response',
  saml_audience_mismatch: 'login.ssoError.response',
  saml_assertion_expired: 'login.ssoError.response',
  saml_invalid: 'login.ssoError.response',
  missing_subject: 'login.ssoError.response',
  invalid_relay_state: 'login.ssoError.response',
  state_mismatch: 'login.ssoError.session',
  missing_session_cookies: 'login.ssoError.session',
  access_denied: 'login.ssoError.accessDenied',
  oidc_error: 'login.ssoError.generic',
  internal_error: 'login.ssoError.generic',
}

/**
 * The localized message of a `sso_error` query value, or undefined when there
 * is none. A code the table does not know gets the generic message: the value
 * comes from the URL and is never shown as is.
 */
export function ssoErrorMessage(code: unknown): string | undefined {
  if (typeof code !== 'string' || code === '') return undefined
  return tDynamic(SSO_ERROR_KEYS[code] ?? 'login.ssoError.generic')
}
