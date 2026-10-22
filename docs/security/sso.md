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
| `link_by_email` | link to an existing local user with the same IdP-verified email. Local users cannot change their own email (only an admin can), so a user cannot pre-claim another person's address to be linked to their SSO identity |

Providers are created enabled; `PATCH` can toggle `enabled`. Mappings accept at most 256 entries, keys and values up to 512 characters. A `claim_mapping` field is stored and returned but not used by login.

### `oidc_config`

| Field | Default |
|---|---|
| `issuer` (must be `https://`; `http://` only for a loopback host; validated against SSRF rules) | required |
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
| `idp_metadata_url` or `idp_metadata_xml` (one required; URL must be `https://` (`http://` only for a loopback host), is SSRF-checked, capped at 1 MiB, and on a private or loopback network needs `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`; XML is validated) | |
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

SAML is implemented with [crewjam/saml](https://github.com/crewjam/saml) through the fork [openartifact/saml](https://github.com/openartifact/saml) (a `replace` in `go.mod`). The fork adds one fix on top of upstream: with goxmldsig 1.6, signed responses or assertions with more than 1000 XML elements (for example a large groups attribute) were rejected. Prefer OIDC when the IdP supports both.

## LDAP and Active Directory (via Dex)

OpenArtifact has no built-in LDAP. Put [Dex](https://dexidp.io) in front of the directory and add Dex as an OIDC provider. Dex's [LDAP connector](https://dexidp.io/docs/connectors/ldap/) does the bind and the group search; OpenArtifact sees a normal OIDC login with a `groups` claim. Do not use Dex's SAML connector: Dex documents it as unmaintained.

Dex config (excerpt):

```yaml
issuer: https://dex.example.com
connectors:
  - type: ldap
    id: ldap
    name: Corporate directory
    config:
      host: ldap.example.com:636
      bindDN: cn=dex,ou=services,dc=example,dc=com
      bindPW: $LDAP_BIND_PASSWORD
      userSearch:
        baseDN: ou=people,dc=example,dc=com
        filter: "(objectClass=person)"
        username: uid            # sAMAccountName for Active Directory
        idAttr: uid
        emailAttr: mail
        nameAttr: cn
        preferredUsernameAttr: uid
      groupSearch:
        baseDN: ou=groups,dc=example,dc=com
        filter: "(objectClass=groupOfNames)"
        userMatchers:
          - userAttr: DN
            groupAttr: member
        nameAttr: cn
staticClients:
  - id: openartifact
    name: OpenArtifact
    secretEnv: OPENARTIFACT_DEX_CLIENT_SECRET
    redirectURIs:
      - https://artifacts.example.com/api/v1/auth/sso/oidc/ldap/callback
```

OpenArtifact provider (`POST /api/v1/admin/sso-providers`):

```json
{
  "slug": "ldap",
  "kind": "oidc",
  "display_name": "Corporate directory",
  "oidc_config": {
    "issuer": "https://dex.example.com",
    "client_id": "openartifact",
    "client_secret": "<same as OPENARTIFACT_DEX_CLIENT_SECRET>"
  },
  "group_mapping": { "artifact-admins": "admins", "developers": "developers" },
  "auto_provision_users": true
}
```

The default scopes already include `groups`, and the default claims (`preferred_username`, `email`, `name`, `groups`) match what Dex issues; Dex marks LDAP emails as verified, so `link_by_email` works. The issuer is SSRF-checked: if Dex is only reachable on a private network (for example an in-cluster Service), set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`.

## Provisioning rules

1. The user is looked up by (external ID, provider slug).
2. Unknown user and `link_by_email` on and the email is IdP-verified: an existing unlinked local user with that email is linked. If that account is already bound to another identity: 409 `identity_conflict`.
3. Email already in use but unverified, or `link_by_email` off: 409 `email_already_in_use`.
4. Unknown user and `auto_provision_users` off: 403 `user_not_provisioned`. Disabled account: 403 `account_disabled`.
5. Otherwise a user is created (username from the username claim, else the email local part, else the subject; reduced to `[a-zA-Z0-9_-]`, 3 to 64 characters, so it cannot contain `/` or control characters; a hash of the subject is used when nothing usable remains. An email without `@`, with whitespace or over 254 characters is ignored) with no password and SSO-managed groups.

Email verification: for OIDC `email_verified` must be `true`, and a custom `email_claim` counts as never verified; for SAML the email counts as verified only when the assertion carries an `email_verified` attribute equal to `true`; without it the email is never used to link or overwrite an account. Configure the IdP to release `email_verified=true` for a SAML provider that should use `link_by_email`, and only when the IdP controls the mail attribute.

### Groups and permissions

- `group_mapping` creates missing local groups and **replaces** the user's SSO-managed memberships on every login; memberships added manually are kept.
- `permission_mapping` assigns the named permission target to the local group. It is **add-only**: removing a mapping never removes an assignment. Unknown target names are logged and skipped.
- Sync failures do not block the login.

How targets are evaluated: [rbac.md](rbac.md).
