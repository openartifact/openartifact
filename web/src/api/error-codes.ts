// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Error codes returned by the backend API.
 * Auto-generated from Go constants — keep in sync with internal/api/errors/codes.go
 */
export const ErrorCodes = {
  // General errors
  INTERNAL_ERROR: 'internal_error',
  NOT_FOUND: 'not_found',
  BAD_REQUEST: 'bad_request',
  UNAUTHORIZED: 'unauthorized',
  FORBIDDEN: 'forbidden',
  VALIDATION_FAILED: 'validation_failed',
  INVALID_REQUEST: 'invalid_request',
  NOT_IMPLEMENTED: 'not_implemented',

  // Auth errors
  PASSWORD_CHANGE_REQUIRED: 'password_change_required',
  AUTH_EVALUATION_FAILED: 'auth_evaluation_failed',
  CSRF_PROTECTION: 'csrf_protection',
  RATE_LIMIT_EXCEEDED: 'rate_limit_exceeded',
  IP_NOT_ALLOWED: 'ip_not_allowed',
  ACCOUNT_DISABLED: 'account_disabled',

  // Upload errors
  INVALID_INPUT: 'invalid_input',
  UPLOAD_ERROR: 'upload_error',
  DATABASE_ERROR: 'database_error',
  STORAGE_ERROR: 'storage_error',

  // SSO errors
  PROVIDER_NOT_FOUND: 'provider_not_found',
  IDP_METADATA_UNAVAILABLE: 'idp_metadata_unavailable',
  SAML_SIGNATURE_INVALID: 'saml_signature_invalid',
  SAML_AUDIENCE_MISMATCH: 'saml_audience_mismatch',
  SAML_ASSERTION_EXPIRED: 'saml_assertion_expired',
  SAML_INVALID: 'saml_invalid',
  MISSING_SUBJECT: 'missing_subject',
  USER_NOT_PROVISIONED: 'user_not_provisioned',
  EMAIL_ALREADY_IN_USE: 'email_already_in_use',
  SSO_LOGIN_DENIED: 'sso_login_denied',
  EMAIL_LINK_AMBIGUOUS: 'email_link_ambiguous',
  INVALID_RELAY_STATE: 'invalid_relay_state',
  OIDC_DISCOVERY_FAILED: 'oidc_discovery_failed',
  LOAD_FAILED: 'load_failed',
  UNAUTHENTICATED: 'unauthenticated',
} as const
