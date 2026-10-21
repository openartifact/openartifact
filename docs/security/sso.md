<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Single sign-on (OIDC and SAML)

Sign in through an external identity provider; provisioned users get a normal session, and IdP groups can be mapped to local groups and permission targets.

Providers are database rows managed through the admin API (admin only), not config-file entries. Sessions, CSRF and tokens behave as for local login: see [authentication.md](authentication.md).

## Prerequisites

| Setting | Why |
|---|---|
| `BASE_URL` | builds the callback/ACS URLs. Defaults to `http://localhost:8080` with a stderr warning; SSO does not work behind a proxy until it is set to the public URL |
| `OPENARTIFACT_SECRET_KEY` | encrypts the stored OIDC `client_secret`; without it the secret is stored unencrypted (startup warning) |
| `AUTH_HMAC_KEY` (`auth.hmacKey`) | signs SAML RelayState and the request-ID cookie. Unset: a random per-boot key is used (stderr warning) and SAML logins break across restarts and replicas |
| `OPENARTIFACT_SAML_SP_CERT`, `OPENARTIFACT_SAML_SP_KEY` | PEM service-provider certificate and key (RSA, PKCS1 or PKCS8). Unset: an ephemeral self-signed pair is generated per process (warning with an `openssl req -x509 -newkey rsa:2048 …` command) |

## Provider API

| Endpoint | Purpose |
|---|---|
| `POST/GET /api/v1/admin/sso-providers` | create / list |
| `GET/PATCH/DELETE /api/v1/admin/sso-providers/{slug}` | read / update / delete |
| `POST /api/v1/admin/sso-providers/{slug}/test` | OIDC discovery / SAML IdP metadata check; returns `{ok,kind}` or `{ok:false,error,…}` |
| `GET /api/v1/auth/sso/providers` | public: `slug`, `kind`, `display_name` of enabled providers (for the login page) |

Create body:

| Field | Notes |
|---|---|
| `slug` | `^[a-z0-9_-]{1,64}$`, used in URLs |
| `kind` | `oidc` or `saml` |
| `display_name` | shown on the login page |
| `oidc_config` / `saml_config` | see below; the one matching `kind` |
| `group_mapping` | IdP group → local group name |
| `permission_mapping` | local group name → permission target name |
| `auto_provision_users` | create unknown users on first login |
| `link_by_email` | link to an existing local user with the same verified email |

Providers are created enabled; `PATCH` can toggle `enabled`. Mappings accept at most 256 entries, keys and values up to 512 characters. A `claim_mapping` field is stored and returned but not used by login.

### `oidc_config`

| Field | Default |
|---|---|
| `issuer` (validated against SSRF rules) | required |
| `client_id`, `client_secret` | required. The secret is never returned (`has_secret` is); an empty value in a `PATCH` keeps the stored one |
| `scopes` | `openid profile email groups` (`openid` is always added) |
| `username_claim` / `email_claim` / `name_claim` / `groups_claim` | `preferred_username` / `email` / `name` / `groups` |

Register the redirect URI `BASE_URL/api/v1/auth/sso/oidc/{slug}/callback` with the IdP. The flow uses the authorization code with PKCE (S256), state and nonce cookies (`oa_oidc_*`), and accepts only same-site relative `redirect` targets.

| Route | |
|---|---|
| `GET /api/v1/auth/sso/oidc/{slug}/login?redirect=/path` | start login |
| `GET /api/v1/auth/sso/oidc/{slug}/callback` | IdP return |
| `POST /api/v1/auth/sso/oidc/{slug}/logout` | returns `end_session_url` when the IdP supports RP-initiated logout |

### `saml_config`

| Field | Default |
|---|---|
| `idp_metadata_url` or `idp_metadata_xml` (one required; URL is SSRF-checked, XML is validated) | |
| `username_attribute` | `uid` |
| `email_attribute` | `mail` |
| `name_attribute` | `displayName` |
| `groups_attribute` | none: no groups are read unless set |

SP entity ID / metadata: `BASE_URL/api/v1/auth/sso/saml/{slug}/metadata`; ACS URL: `BASE_URL/api/v1/auth/sso/saml/{slug}/acs`. IdP-initiated login is disabled.

| Route | |
|---|---|
| `GET /api/v1/auth/sso/saml/{slug}/login` | start login (SP-initiated) |
| `POST /api/v1/auth/sso/saml/{slug}/acs` | assertion consumer (CSRF-exempt) |
| `GET /api/v1/auth/sso/saml/{slug}/metadata` | SP metadata XML |

## Provisioning rules

1. The user is looked up by (external ID, provider slug).
2. Unknown user and `link_by_email` on and the email is IdP-verified: an existing unlinked local user with that email is linked. If that account is already bound to another identity: 409 `identity_conflict`.
3. Email already in use but unverified, or `link_by_email` off: 409 `email_already_in_use`.
4. Unknown user and `auto_provision_users` off: 403 `user_not_provisioned`. Disabled account: 403 `account_disabled`.
5. Otherwise a user is created (username from the username claim, else the email local part, else the subject) with no password and SSO-managed groups.

Email verification: for OIDC `email_verified` must be `true`, and a custom `email_claim` counts as never verified; for SAML the email is treated as verified unless an `email_verified` attribute says otherwise.

### Groups and permissions

- `group_mapping` creates missing local groups and **replaces** the user's SSO-managed memberships on every login; memberships added manually are kept.
- `permission_mapping` assigns the named permission target to the local group. It is **add-only**: removing a mapping never removes an assignment. Unknown target names are logged and skipped.
- Sync failures do not block the login.

How targets are evaluated: [rbac.md](rbac.md).
