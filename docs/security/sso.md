<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Single sign-on (OIDC, SAML and GitHub OAuth)

Sign in through an external identity provider; provisioned users get a normal session, and IdP groups can be mapped to local groups and permission targets.

Providers are database rows managed through the admin API (admin only), not config-file entries. Sessions, CSRF and tokens behave as for local login: see [authentication.md](authentication.md).

Contents: [Prerequisites](#prerequisites) · [Provider API](#provider-api) · [`oidc_config`](#oidc_config) · [`saml_config`](#saml_config) · [Logout](#logout) · [Vendor compatibility](#vendor-compatibility) · [Vendor setup](#vendor-setup) · [LDAP via Dex](#ldap-and-active-directory-via-dex) · [Provisioning rules](#provisioning-rules) · [Groups and permissions](#groups-and-permissions)

## Prerequisites

| Setting | Why |
|---|---|
| `BASE_URL` | builds the callback/ACS URLs. Defaults to `http://localhost:8080` with a stderr warning; SSO does not work behind a proxy until it is set to the public URL |
| `OPENARTIFACT_SECRET_KEY` | encrypts the stored `client_secret` and the generated SAML SP private key. Unset: they are stored **unencrypted** in the database (startup warning) |
| `AUTH_HMAC_KEY` (`auth.hmacKey`) | signs SAML RelayState, the request-ID cookie and OIDC login state. Optional: when unset a random key is generated **once** and persisted in `system_settings`, so it survives restarts and is shared by all replicas. Set it to manage the key yourself (it then wins over the stored one) |
| `OPENARTIFACT_SAML_SP_CERT`, `OPENARTIFACT_SAML_SP_KEY` | PEM service-provider certificate and key (RSA, PKCS1 or PKCS8). Optional: when unset a self-signed SP keypair is generated **once** and persisted encrypted in `system_settings`, shared by all replicas. Regenerating it (for example after wiping the database) means re-registering the SP certificate at the IdP |

No manual step is needed for multi-replica deployments.

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
| `kind` | `oidc` or `saml` (GitHub and generic OAuth2 use `kind: oidc` with `oidc_config.type`) |
| `display_name` | shown on the login page |
| `oidc_config` / `saml_config` | see below; the one matching `kind` |
| `group_mapping` | IdP group → local group name |
| `permission_mapping` | local group name → permission target name |
| `auto_provision_users` | create unknown users on first login |
| `link_by_email` | link to an existing local user with the same IdP-verified email. Local users cannot change their own email (only an admin can), so a user cannot pre-claim another person's address to be linked to their SSO identity |

Providers are created enabled; `PATCH` can toggle `enabled`. Mappings accept at most 256 entries, keys and values up to 512 characters. A `claim_mapping` field is stored and returned but not used by login.

### `oidc_config`

| Field | Default / meaning |
|---|---|
| `type` | `""` (standard OIDC with discovery), `github` or `oauth2` (see [GitHub](#github-oauth2-githubcom-and-enterprise-server)) |
| `issuer` (must be `https://`; `http://` only for a loopback host; validated against SSRF rules) | required for standard OIDC. The ID token `iss` must match it exactly (a trailing slash matters) |
| `client_id`, `client_secret` | required. The secret is never returned (`has_secret` is); an empty value in a `PATCH` keeps the stored one |
| `scopes` | `openid email profile` (`openid` is always added). `groups` or any IdP-specific scope is **opt-in**: add it explicitly. For `github` the default is `read:user user:email read:org` |
| `username_claim` / `email_claim` / `name_claim` / `groups_claim` | `preferred_username` / `email` / `name` / `groups` |
| `allowed_tenants` | Entra multi-tenant only: list of tenant IDs (GUIDs) allowed to sign in. **Required** when the issuer is `https://login.microsoftonline.com/{common\|organizations\|consumers}/v2.0`. The `tid` claim must be in the list and `iss` must equal `https://login.microsoftonline.com/<tid>/v2.0`. For these providers the external ID becomes `<tid>:<sub>`; single-tenant issuers are unchanged |
| `hosted_domain` | Google Workspace domain (`hd`): sent as an authorization hint **and** enforced against the `hd` claim of the ID token |
| `require_email_verified` | reject logins whose `email_verified` is not `true`. Google always enforces it |
| `graph_groups`, `graph_endpoint` | Entra groups overage: when the token carries `_claim_names`/`_claim_sources` instead of `groups`, fetch the groups from Microsoft Graph with the access token. Needs a Graph scope in `scopes` (for example `GroupMember.Read.All` or `User.Read`, see Entra section) and admin consent. On failure the groups count as **absent** (memberships kept) |
| `authorization_url`, `token_url`, `userinfo_url` | `github`/`oauth2` endpoints (defaults for github.com; required for generic `oauth2`) |
| `auth_base_url`, `api_base_url` | GitHub Enterprise Server web and API base URLs |
| `allowed_orgs` | **Required** for `github`: the user must belong to at least one listed organization |

Register the redirect URI `BASE_URL/api/v1/auth/sso/oidc/{slug}/callback` with the IdP. The flow uses the authorization code with PKCE (S256), state and nonce cookies (`oa_oidc_*`), and accepts only same-site relative `redirect` targets.

| Route | |
|---|---|
| `GET /api/v1/auth/sso/oidc/{slug}/login?redirect=/path` | start login |
| `GET /api/v1/auth/sso/oidc/{slug}/callback` | IdP return |
| `POST /api/v1/auth/sso/oidc/{slug}/logout` | same-origin only; see [Logout](#logout) |

### `saml_config`

| Field | Default / meaning |
|---|---|
| `idp_metadata_url` or `idp_metadata_xml` (one required; URL must be `https://` (`http://` only for a loopback host), is SSRF-checked, capped at 1 MiB, and on a private or loopback network needs `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`; XML is validated) | |
| `idp_metadata_refresh_seconds` | `3600`, allowed `60`..`86400`. The metadata `cacheDuration`/`validUntil` is honoured too. If the URL is down a stale copy is served for up to 24 h. Static `idp_metadata_xml` **never refreshes** |
| `username_attribute` | `uid` |
| `email_attribute` | `mail` |
| `name_attribute` | `displayName` |
| `groups_attribute` | none: no groups are read unless set |
| `allow_idp_initiated` | `false`. See [IdP-initiated login](#idp-initiated-saml-login) |

SP entity ID / metadata: `BASE_URL/api/v1/auth/sso/saml/{slug}/metadata`; ACS URL (HTTP-POST binding): `BASE_URL/api/v1/auth/sso/saml/{slug}/acs`.

| Route | |
|---|---|
| `GET /api/v1/auth/sso/saml/{slug}/login` | start login (SP-initiated) |
| `POST /api/v1/auth/sso/saml/{slug}/acs` | assertion consumer (CSRF-exempt) |
| `GET /api/v1/auth/sso/saml/{slug}/metadata` | SP metadata XML |

Error codes: `saml_signature_invalid` (400), `saml_audience_mismatch` (400), `saml_assertion_expired` (401), `saml_invalid` (400, generic).

**Certificate rollover.** Every signing certificate listed in the IdP metadata is accepted. With `idp_metadata_url` the new certificate is picked up at the next refresh; with static `idp_metadata_xml` include **both** the old and the new signing certificate in the XML before the IdP switches (or prefer the URL).

#### IdP-initiated SAML login

Off by default; set `allow_idp_initiated: true` to accept unsolicited assertions (no `InResponseTo`). Assertion IDs are kept in a replay cache, and `RelayState` is honoured only as a same-origin relative path, otherwise it is ignored and the user lands on `/`. Caveat: IdP-initiated SSO has no request binding, so it cannot protect against login CSRF (an attacker can make a victim's browser post an assertion issued for the attacker, signing the victim into the attacker's account). Enable it only when users must start from the IdP portal.

SAML is implemented with [crewjam/saml](https://github.com/crewjam/saml) through the fork [openartifact/saml](https://github.com/openartifact/saml) (a `replace` in `go.mod`). The fork adds one fix on top of upstream: with goxmldsig 1.6, signed responses or assertions with more than 1000 XML elements (for example a large groups attribute) were rejected. Prefer OIDC when the IdP supports both.

### Logout

`POST /api/v1/auth/logout` and `POST /api/v1/auth/sso/oidc/{slug}/logout` require a same-origin request. For an OIDC SSO session whose IdP advertises `end_session_endpoint` the response is `200 {"end_session_url": "..."}` and the web UI navigates there. The URL carries `id_token_hint` (the token stored for that session), `client_id` and `post_logout_redirect_uri` = `BASE_URL/login`, which **must be registered** at the IdP as a post-logout redirect URI. Otherwise the response is `204` and only the local session ends. A hint or redirect supplied in the request body is ignored. Google has no `end_session_endpoint`; SAML sign-out is local only (no SLO).

Sessions are rotated on every SSO login: the pre-login session is revoked and a new session and CSRF token are issued.

## Vendor compatibility

Status from vendor documentation; "unverified" means not confirmed in vendor docs or by a live test.

| Vendor | Login | Groups | Logout (RP-initiated) | Cert rollover | IdP-initiated |
|---|---|---|---|---|---|
| Entra ID OIDC | yes | GUIDs; overage via `graph_groups` | yes (`end_session_endpoint`) | JWKS (automatic) | n/a (OIDC) |
| Entra ID SAML | yes | GUIDs; overage (150) not resolvable, groups treated as absent | local only | metadata URL | opt-in |
| Okta OIDC / SAML | yes | needs groups claim / attribute statement | yes (OIDC) | JWKS / metadata URL | SAML opt-in |
| Google Workspace OIDC | yes (`hosted_domain`) | none | no (`end_session_endpoint` absent) | JWKS | n/a |
| Keycloak OIDC / SAML | yes | Group Membership mapper | yes (OIDC) | JWKS / metadata URL | SAML opt-in |
| Auth0 OIDC | yes | namespaced custom claim | yes (if `end_session_endpoint` advertised; unverified) | JWKS | n/a |
| Ping (PingOne / PingFederate) | yes | claim / attribute contract (unverified details) | PingOne yes | JWKS / metadata URL | SAML opt-in |
| AD FS OIDC / SAML | yes | claim rules | unverified | JWKS / metadata URL | SAML opt-in |
| JumpCloud OIDC / SAML | yes | group attribute | unverified | unverified | SAML opt-in (portal launch) |
| OneLogin OIDC / SAML | yes | unverified | unverified | unverified | SAML opt-in |
| Authentik OIDC / SAML | yes | `groups` claim (needs `groups` scope) | yes | JWKS / metadata URL | SAML opt-in |
| Zitadel OIDC | yes | roles claim is an object, not a list: needs an Action (unverified) | yes | JWKS | n/a |
| GitHub (OAuth2) | yes (`allowed_orgs`) | orgs and teams | local only | n/a | n/a |

## Vendor setup

Every OIDC provider uses redirect URI `BASE_URL/api/v1/auth/sso/oidc/{slug}/callback`; every SAML provider uses ACS `BASE_URL/api/v1/auth/sso/saml/{slug}/acs` and entity ID `BASE_URL/api/v1/auth/sso/saml/{slug}/metadata`. Also register `BASE_URL/login` as post-logout redirect where the IdP supports it. Configs below are the `POST /api/v1/admin/sso-providers` body (secrets and group names are placeholders).

### Microsoft Entra ID

Sources: [OIDC protocol](https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc), [ID token claims, groups overage](https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference). Verified from these pages: the `common`/`organizations`/`consumers` authorities, the 150 (SAML) / 200 (JWT) overage limits, the `tid` claim, `hasgroups`/`_claim_names` behaviour.

Single-tenant OIDC:

```json
{ "slug": "entra", "kind": "oidc", "display_name": "Microsoft",
  "oidc_config": { "issuer": "https://login.microsoftonline.com/<tenant-id>/v2.0",
    "client_id": "<app-id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile"] },
  "auto_provision_users": true }
```

- App registration, Web platform, redirect URI as above. The issuer is the v2.0 endpoint of **your** tenant GUID.
- `email` is not always present for work accounts (it depends on the mailbox); `preferred_username` is the UPN. Consider `"email_claim": "email"` plus the optional claim `email`. Entra does not emit `email_verified`; email linking therefore does not apply unless your tenant is trusted and `require_email_verified` is left off (then `link_by_email` does not link, see [Provisioning rules](#provisioning-rules)).
- Groups: Token configuration, Add groups claim (Security groups or Groups assigned to the application). The claim holds group **object IDs (GUIDs)**, not names, so `group_mapping` keys must be GUIDs. Cloud-only group names need "Cloud-only group display names" under the application-assigned option.
- Overage: above 200 groups (JWT) the token has `_claim_names`/`_claim_sources` instead of `groups`. Set `"graph_groups": true`, add a Graph scope (for example `https://graph.microsoft.com/User.Read` plus a group-read permission) to `scopes` and grant admin consent; the default `graph_endpoint` is Microsoft Graph. Without it (or if the call fails) groups are absent and memberships are kept. Simplest fix: assign groups to the application and emit only assigned groups.
- Logout: `end_session_endpoint` is advertised; register `BASE_URL/login` as a front-channel/post-logout URL.

Multi-tenant OIDC (issuer `common`, `organizations` or `consumers`):

```json
{ "slug": "entra-mt", "kind": "oidc", "display_name": "Microsoft",
  "oidc_config": { "issuer": "https://login.microsoftonline.com/organizations/v2.0",
    "client_id": "<app-id>", "client_secret": "<secret>",
    "allowed_tenants": ["<tenant-guid-1>", "<tenant-guid-2>"] } }
```

`allowed_tenants` is mandatory; without it the provider is rejected. The app registration must be "Accounts in any organizational directory" (the `common` authority adds personal accounts). The external ID is `<tid>:<sub>` because `sub` is only unique per application and not across tenants. Changing a provider from single- to multi-tenant therefore creates new identities.

SAML:

```json
{ "slug": "entra-saml", "kind": "saml", "display_name": "Microsoft",
  "saml_config": { "idp_metadata_url": "https://login.microsoftonline.com/<tenant-id>/federationmetadata/2007-06/federationmetadata.xml?appid=<app-id>",
    "username_attribute": "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
    "email_attribute": "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
    "name_attribute": "http://schemas.microsoft.com/identity/claims/displayname",
    "groups_attribute": "http://schemas.microsoft.com/ws/2008/06/identity/claims/groups" } }
```

- Enterprise application, SAML SSO: Identifier (Entity ID) and Reply URL as above. Attribute claim URIs are Entra's defaults (the metadata URL pattern and URIs are from memory of the portal defaults; check them in your tenant: unverified).
- SAML groups also hold GUIDs and overage starts at 150 groups; there is no Graph fallback for SAML, so an overage assertion has no groups attribute and memberships are kept. Assign groups to the application to stay below the limit.
- Entra does not send `email_verified`; add a constant claim `email_verified` = `true` only if your tenant controls the mail attribute and you want `link_by_email`.
- Signing certificate rollover: use the metadata URL (an app-specific `?appid=` URL).

### Okta

Sources (not fetched in this session, from product knowledge: unverified): Okta Developer docs "Create an OIDC web app", "Customize tokens returned from Okta with a Groups claim".

OIDC:

```json
{ "slug": "okta", "kind": "oidc", "display_name": "Okta",
  "oidc_config": { "issuer": "https://<org>.okta.com/oauth2/default",
    "client_id": "<id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile", "groups"] },
  "group_mapping": { "Engineering": "developers" } }
```

- Issuer: the **authorization server** URL (`https://<org>.okta.com` for the org server, `.../oauth2/default` for the custom `default` server); it must equal the `iss` in tokens. The org server does not allow custom claims; use a custom server for groups.
- Groups: on the authorization server add a `groups` scope and a claim "groups" in ID token, "Matches regex" filter (for example `.*`), so the claim is present. Without that claim OpenArtifact sees no groups (memberships kept).
- Sign-in redirect URI as above; sign-out redirect URI `BASE_URL/login`. `end_session_endpoint` is advertised.

SAML: create a SAML 2.0 app with Single sign-on URL = ACS, Audience URI = entity ID, Name ID format `EmailAddress`; attribute statements `uid`/`mail`/`displayName` (the defaults) and a group attribute statement (name `groups`, filter e.g. `Matches regex .*`) with `"groups_attribute": "groups"`. Use the "Identity Provider metadata" URL. Add an attribute `email_verified` = `"true"` for `link_by_email`. IdP-initiated works from the Okta dashboard with `allow_idp_initiated: true`.

### Google Workspace

Source: [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect) (verified: discovery document, `hd` parameter).

```json
{ "slug": "google", "kind": "oidc", "display_name": "Google",
  "oidc_config": { "issuer": "https://accounts.google.com",
    "client_id": "<id>.apps.googleusercontent.com", "client_secret": "<secret>",
    "hosted_domain": "example.com" },
  "auto_provision_users": true }
```

- Google Cloud console: OAuth client of type Web application, redirect URI as above. Set the consent screen to **Internal** to restrict to your Workspace.
- `hosted_domain` is sent as the `hd` hint and **checked in the ID token**, because the parameter alone is only a UI hint. `email_verified` is always enforced.
- Google has **no groups claim**: `group_mapping` has no effect, memberships stay as set manually (claim absent, kept). Use Dex or Cloud Identity groups through another IdP if groups are required.
- No `end_session_endpoint`: logout is local only.

### Keycloak

Sources: Keycloak Server Administration Guide (not fetched in this session: unverified).

OIDC (`issuer` is the realm URL: `https://kc.example.com/realms/<realm>`; older Quarkus-less distributions use `/auth/realms/<realm>`):

```json
{ "slug": "keycloak", "kind": "oidc", "display_name": "Keycloak",
  "oidc_config": { "issuer": "https://kc.example.com/realms/<realm>",
    "client_id": "openartifact", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile"] },
  "group_mapping": { "developers": "developers" } }
```

- Client: Client authentication on, Standard flow, valid redirect URI as above, valid post-logout redirect URI `BASE_URL/login`.
- Groups: add a **Group Membership** mapper to the client's dedicated scope (token claim name `groups`, add to ID token) and turn **Full group path off**; with it on, values look like `/parent/child` and `group_mapping` keys must match those.
- Keycloak sends `email_verified` per user; mark emails verified for `link_by_email`.
- SAML: client of type SAML, Client ID = entity ID, valid redirect URIs / ACS as above, "Sign assertions" on, Name ID format `email` or `username`; add mappers "User Property" (`email`, `username`) and "Group list" (attribute `groups`, Full group path off) and set `groups_attribute: "groups"`. Metadata: `https://kc.example.com/realms/<realm>/protocol/saml/descriptor`. IdP-initiated: set "IDP-Initiated SSO URL name" in the client and `allow_idp_initiated: true`.

### Auth0

Sources: Auth0 docs (not fetched in this session: unverified).

```json
{ "slug": "auth0", "kind": "oidc", "display_name": "Auth0",
  "oidc_config": { "issuer": "https://<tenant>.<region>.auth0.com/",
    "client_id": "<id>", "client_secret": "<secret>",
    "groups_claim": "https://example.com/groups" },
  "group_mapping": { "admin": "admins" } }
```

- The issuer **must include the trailing slash**: Auth0's tokens carry `iss` = `https://<tenant>.auth0.com/` and the match is exact.
- Application type Regular Web Application; Allowed Callback URLs as above, Allowed Logout URLs `BASE_URL/login`.
- Groups: Auth0 has no built-in groups claim. Add a Post-Login **Action** that sets `api.idToken.setCustomClaim("https://example.com/groups", event.authorization.roles)`; custom claims must be namespaced with a URL and set `groups_claim` to it.
- Auth0 supports RP-initiated logout (`/oidc/logout`); whether it is advertised in discovery for your tenant is unverified, otherwise logout is local.

### Ping Identity (PingOne, PingFederate)

Sources: [PingOne discovery](https://developer.pingidentity.com/pingone-api/auth/openid-connect-oauth-2/authorization/discovery-openid-configuration.html) (verified: issuer `https://auth.pingone.com/<environment-id>/as`, `end_session_endpoint` `.../as/signoff`), [PingOne SAML attribute mapping example](https://workos.com/docs/integrations/pingone-saml) (third party; verified `email`, `firstName`, `lastName`, `id` mapping example). PingOne group claims/attributes: see [Including external groups](https://docs.pingidentity.com/pingone/applications/p1_include_external_groups_in_applications.html) (not fetched: unverified).

```json
{ "slug": "ping", "kind": "oidc", "display_name": "PingOne",
  "oidc_config": { "issuer": "https://auth.pingone.com/<environment-id>/as",
    "client_id": "<id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile"] } }
```

- OIDC web app, redirect URI as above, sign-off URL `BASE_URL/login`. Regions use other domains (`auth.pingone.eu`, `.asia`, `.ca`; unverified); copy the issuer from the app's discovery document.
- Groups: add the group membership attribute to the application's attribute mapping and the `groups` scope (names vs ids: unverified). PingFederate: issuer is `https://<host>:9031` by default (unverified) and claims come from the OIDC policy contract.
- SAML: PingOne SAML app with ACS and entity ID as above; map `email`, `username`/`uid`, and a group attribute, then set the attribute names in `saml_config`. Use the IdP metadata URL from the application.

### AD FS

Sources: Microsoft AD FS docs (not fetched in this session: unverified).

```json
{ "slug": "adfs", "kind": "oidc", "display_name": "Corporate",
  "oidc_config": { "issuer": "https://adfs.example.com/adfs",
    "client_id": "<id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile"],
    "username_claim": "upn", "email_claim": "email", "groups_claim": "group" } }
```

- Application Group, "Server application" (confidential client); redirect URI as above. Discovery: `https://adfs.example.com/adfs/.well-known/openid-configuration`.
- Add the claims through the application's transform rules: "Send LDAP Attributes as Claims" (E-Mail-Addresses → `email`, User-Principal-Name → `upn`) and "Send Group Membership as a Claim" (group claim `group`). Default AD FS group claims contain sAMAccountName or the group SID depending on the rule. AD FS does not emit `email_verified` unless a rule adds it.
- SAML: Relying Party Trust from `BASE_URL/api/v1/auth/sso/saml/{slug}/metadata`; claim rules "Send LDAP Attributes as Claims" and "Transform an Incoming Claim" for Name ID (E-Mail → Name ID, format email). Metadata URL: `https://adfs.example.com/FederationMetadata/2007-06/FederationMetadata.xml`. Claim URIs are the long `http://schemas.xmlsoap.org/ws/2005/05/identity/claims/...` URIs unless the rule sets another name. IdP-initiated: `https://adfs.example.com/adfs/ls/idpinitiatedsignon` (unverified).

### JumpCloud

Sources: [JumpCloud OIDC SSO](https://jumpcloud.com/support/sso-with-oidc), [SAML SSO overview](https://jumpcloud.com/support/get-started-applications-saml-sso) (verified: issuer endpoints, redirect URI field, "include group attribute", IdP-initiated from the user portal, metadata export/URL).

```json
{ "slug": "jumpcloud", "kind": "oidc", "display_name": "JumpCloud",
  "oidc_config": { "issuer": "https://oauth.id.jumpcloud.com/",
    "client_id": "<id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile"],
    "groups_claim": "groups" } }
```

- Issuer: US `https://oauth.id.jumpcloud.com/`, EU `https://oauth.id.eu.jumpcloud.com/`, IN `https://oauth.id.in.jumpcloud.com/` (note the trailing slash as published).
- SSO application, Custom OIDC App, Authorization Code grant, redirect URI as above, client authentication type Client Secret POST or Basic. Under attribute mapping select the Email and Profile scopes and tick **include group attribute** with the claim name (use `groups` or set `groups_claim`). Only groups bound to the application are sent.
- SAML: Custom SAML App; IdP URL / SP entity ID and ACS as above; export metadata or copy the metadata URL; map `email`, `username`, and group attributes (JumpCloud requires adding "include group attribute" with a name). Attribute details beyond that are unverified.
- Logout and certificate rollover: unverified.

### OneLogin

Sources: [OneLogin OIDC authorization code flow](https://developers.onelogin.com/openid-connect/api/authorization-code) (verified: `https://<subdomain>.onelogin.com/oidc/2/auth`). Everything else unverified.

```json
{ "slug": "onelogin", "kind": "oidc", "display_name": "OneLogin",
  "oidc_config": { "issuer": "https://<subdomain>.onelogin.com/oidc/2",
    "client_id": "<id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile"] } }
```

- OpenId Connect (OIDC) app connector, redirect URI as above, token endpoint authentication method "POST" or "Basic" matching the app. Groups: the `groups` scope returns group names (unverified; check the app's Parameters tab).
- SAML: "SAML Custom Connector (Advanced)" with Audience (EntityID) and ACS as above, SAML nameID format email, parameters `email`, `name`, and `groups` (value "User Roles"/"MemberOf", unverified).

### Authentik

Sources: [authentik OAuth2/OIDC provider](https://goauthentik.io/docs/providers/oauth2/) (verified: per-application issuer and endpoint paths `/application/o/<slug>/`, `end-session`, `read:org` GitHub mention, `email_verified` change in 2025.10).

```json
{ "slug": "authentik", "kind": "oidc", "display_name": "authentik",
  "oidc_config": { "issuer": "https://auth.example.com/application/o/<app-slug>/",
    "client_id": "<id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile", "groups"] } }
```

- The issuer depends on the provider's "Issuer mode" (per-provider URL by default; trailing slash included). Copy it from the discovery document `.../application/o/<app-slug>/.well-known/openid-configuration`.
- Redirect URI as above (strict). Authentik has a `groups` scope mapping; confirm it is attached to the provider.
- Before authentik 2025.10 the `email` scope always returned `email_verified: true`; later it reflects the user.
- SAML: SAML provider with ACS URL and Audience = entity ID, Issuer, Service Provider Binding Post, signing certificate set, NameID property mapping Email. Metadata is downloadable from the provider (unverified path). Group attribute: add a property mapping (unverified).

### Zitadel

Source: [Zitadel claims](https://zitadel.com/docs/apis/openidoauth/claims) (verified: `urn:zitadel:iam:org:project:roles` is an **object** keyed by role name, `{"role": {"orgId": "domain"}}`).

```json
{ "slug": "zitadel", "kind": "oidc", "display_name": "Zitadel",
  "oidc_config": { "issuer": "https://<instance>.zitadel.cloud",
    "client_id": "<id>", "client_secret": "<secret>",
    "scopes": ["openid", "email", "profile", "urn:zitadel:iam:org:project:roles"],
    "groups_claim": "urn:zitadel:iam:org:project:roles" } }
```

- Application type Web, Code flow with PKCE-capable client (Basic or Post auth), redirect URI as above, post-logout URI `BASE_URL/login`. Issuer is the instance domain (no path).
- **Caveat:** the roles claim is a JSON object (role name as key), not an array of strings. Whether OpenArtifact flattens an object-valued groups claim is not part of the documented contract: unverified. If it does not, add a Zitadel Action that emits a plain `groups` string array.
- Zitadel emits `email_verified`.

### GitHub (OAuth2, github.com and Enterprise Server)

Source: [GitHub OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps) (verified: `read:org`).

```json
{ "slug": "github", "kind": "oidc", "display_name": "GitHub",
  "oidc_config": { "type": "github", "client_id": "<id>", "client_secret": "<secret>",
    "allowed_orgs": ["my-org"] },
  "group_mapping": { "my-org/platform": "platform", "my-org": "developers" },
  "auto_provision_users": true }
```

- GitHub is not OIDC. Register an OAuth App (callback URL as above). Default scopes `read:user user:email read:org`. The user must belong to one of `allowed_orgs` (required). Groups become `org` and `org/team-slug`. Only the **primary and verified** email is used. The external ID is the numeric GitHub user id. Organizations with OAuth app access restrictions must approve the app, or the membership is not visible.
- GitHub Enterprise Server: set `auth_base_url` (for example `https://ghe.example.com`) and `api_base_url` (`https://ghe.example.com/api/v3`).
- Generic OAuth2 (`type: "oauth2"`): set `authorization_url`, `token_url`, `userinfo_url`; the subject is `sub` from the userinfo response, else `id`. No ID token, so no RP-initiated logout.
- Logout: local only.

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

Add `"scopes": ["openid", "email", "profile", "groups"]` to `oidc_config` (the default scopes no longer include `groups`); the default claims (`preferred_username`, `email`, `name`, `groups`) match what Dex issues; Dex marks LDAP emails as verified, so `link_by_email` works. The issuer is SSRF-checked: if Dex is only reachable on a private network (for example an in-cluster Service), set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`.

## Provisioning rules

1. The user is looked up by (external ID, provider slug).
2. Unknown user and `link_by_email` on and the email is IdP-verified: an existing unlinked local user with that email is linked.
3. Conflicts return a generic `403 sso_login_denied` ("SSO sign-in could not be completed; contact your administrator"): the email is already used by another account (unverified email, or `link_by_email` off), the matching account is already bound to another identity, or the email matches more than one account. The reason is only in the server log.
4. Unknown user and `auto_provision_users` off: 403 `user_not_provisioned`. Disabled account: 403 `account_disabled`.
5. Otherwise a user is created (username from the username claim, else the email local part, else the subject; reduced to `[a-zA-Z0-9_-]`, 3 to 64 characters, so it cannot contain `/` or control characters; a hash of the subject is used when nothing usable remains. An email without `@`, with whitespace or over 254 characters is ignored) with no password and SSO-managed groups.

Email verification: for OIDC `email_verified` must be `true`, and a custom `email_claim` counts as never verified; for SAML the email counts as verified only when the assertion carries an `email_verified` attribute equal to `true`; without it the email is never used to link or overwrite an account. Configure the IdP to release `email_verified=true` for a SAML provider that should use `link_by_email`, and only when the IdP controls the mail attribute.

### Groups and permissions

- `group_mapping` creates missing local groups and **replaces** the user's SSO-managed memberships on login, but **only when the IdP actually sent a groups claim/attribute**. An absent claim (or an unresolved Entra overage) keeps the existing memberships and logs a warning. A present but empty claim clears them. For SAML this applies only when `groups_attribute` is configured and present in the assertion. Memberships added manually are always kept.
- `permission_mapping` assigns the named permission target to the local group. It is **add-only**: removing a mapping never removes an assignment. Unknown target names are logged and skipped.
- Sync failures do not block the login.

How targets are evaluated: [rbac.md](rbac.md).
