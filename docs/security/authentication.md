<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Authentication

Use this page to sign in, create API tokens for scripts and package clients, and set the network controls in front of the server. What an identity may do is covered in [rbac.md](rbac.md); OIDC and SAML are in [sso.md](sso.md).

## Credentials

| Credential | Sent as | Used for | CSRF header |
|---|---|---|---|
| Session cookie `oa_session` | cookie, set by login | web UI, interactive API use, minting tokens | required on POST/PUT/PATCH/DELETE |
| API token `oa_pat_…` / `oa_dep_…` | `Authorization: Bearer <TOKEN>`, or Basic with the token as the **password** (Basic is not accepted on [MCP](#mcp-endpoint)) | package clients, CI, CLI, MCP | not needed |
| Legacy API key | `X-Api-Key`, Bearer, or Basic password | deprecated, see [below](#legacy-api-key) | not needed |
| none | | anonymous read, see [below](#anonymous-access) | |

Precedence when several are present: session cookie, then token, then legacy key, then anonymous. Account passwords are accepted only by the UI login; package clients must use a token.

## First start and admin password

With an empty user table the server creates the user `admin` with a random password and prints it **once** to stderr in an `OPENARTIFACT BOOTSTRAP` block. The account must change its password at first login: until then every request except `POST /api/v1/auth/change-password` and `POST /api/v1/auth/logout` returns 403 `password_change_required`. Bootstrap is safe with several replicas starting together. It also re-grants the `admin-all` permission target if it is missing.

Reset a password later (reads `DB_*` env vars; at least 12 characters; revokes the user's sessions; the password is never taken from argv):

```sh
OPENARTIFACT_RESET_PASSWORD='<new-password>' go run ./cmd/resetpw
# or: printf '%s\n' '<new-password>' | go run ./cmd/resetpw -username admin
```

## Passwords

Stored as argon2id (m=65536 KiB, t=1, p=4). 12 to 1024 characters (the maximum counts bytes). A failed login still costs one password verification, so unknown usernames are not distinguishable by timing. Users auto-provisioned by SSO have no password.

## Login and sessions

```sh
curl -sS -c jar -X POST HOST/api/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"<PASSWORD>"}'
```

| Endpoint | Notes |
|---|---|
| `POST /api/v1/auth/login` | JSON body `username`, `password` (`Content-Type: application/json` is required; form bodies get 415). Browser requests from another origin (`Origin` / `Sec-Fetch-Site: cross-site`) get 403, which closes login CSRF |
| `POST /api/v1/auth/logout` | ends the session; cross-origin browser requests get 403 |
| `GET /api/v1/auth/me` | current user, `is_admin`, `groups`; works with any credential |
| `POST /api/v1/auth/change-password` | `{current_password,new_password}`; session only (tokens get 403); revokes the user's other sessions; wrong guesses count against the login failure budget |

- Login sets two cookies: `oa_session` (HttpOnly, SameSite=Lax) and `oa_csrf` (readable by JS).
- Both are `Secure` over TLS, when `X-Forwarded-Proto: https` arrives from a trusted proxy, or when `BASE_URL` is `https://`. HSTS is sent only when `server.tlsEnabled` is set or `BASE_URL` is `https://`; the `X-Forwarded-Proto` header does not enable it.
- The server stores only the SHA-256 of the 32-byte random session id.
- Expiry is sliding: `auth.sessionDurationHours` / `SESSION_DURATION_HOURS` (default 24), extended at most once a minute. A session also ends 30 days after login regardless of activity. That limit is fixed.
- Disabling a user (`PATCH /api/v1/users/{id}` with `disabled: true`) keeps the account, revokes the user's sessions and rejects the user on every credential type.
- `DELETE /api/v1/users/{id}` permanently removes the account with its tokens, sessions, memberships and SSO link. Use disable to keep it.

#### Login rate limits

Login is limited in four ways. Each limit answers `429` with `Retry-After`, except the last, which only slows unknown addresses.

- **Per IP:** 10 login requests per minute.
- **Per username and IP:** after 10 failures in 15 minutes, that pair is refused until the oldest failure ages out. Other addresses are unaffected.
- **Distributed guessing:** a username with 100 failures in 15 minutes across all addresses is treated as under attack. Addresses that logged in as that user within the last 30 days carry on as usual. Every other address shares 3 attempts per minute for that username.
- **IPv6:** clients are counted per /64 network.

Nobody can lock an account out for its real users by guessing.

#### First admin without a printed password

On an empty instance you can choose the first admin's password and an admin API token yourself, so no secret is printed. Both settings are ignored once any user exists.

- `bootstrap.adminPasswordFile`: file holding the admin password.
- `bootstrap.adminTokenFile`: file for the admin API token.
- A generated, printed password expires after `bootstrap.generatedPasswordTtl` (default 24 h). A password change or `cmd/resetpw` clears the expiry. Login with an expired password fails with the generic `invalid_credentials`.

See [first login](../getting-started/installation.md#first-login).

Session cookies (`oa_session`, `oa_csrf`) carry `Max-Age` equal to the absolute session lifetime; the sliding idle expiry is enforced server-side, so an active user is no longer logged out by the browser after the idle window.

### CSRF

Session-cookie requests that change state need a CSRF token. Token, Basic-token and legacy-key requests do not.

**Rule.** Every request other than GET, HEAD and OPTIONS that is authenticated by the session cookie must send `X-CSRF-Token`, on every path (`/api/v1/*`, `/repos/**`, `/v2/**`). Otherwise it gets `403 csrf_protection`.

- The token is an HMAC of the `oa_session` value. The `oa_csrf` cookie of another session, or one planted from a sibling subdomain, never validates.
- Login and SSO login issue the matching `oa_csrf` cookie. The web UI echoes it for you.

**Exemptions.** Bearer, Basic-token and legacy-key requests, and the SAML `/acs` POST, are exempt. Package clients (`curl`, `mvn`, `npm`, `docker`, `pip`) authenticate with a token and are unaffected.

**Exception.** On `/api/v1/*` only, a Basic-token request that looks cross-site is not exempt and needs a CSRF token. This is the case when it carries `Sec-Fetch-Site: cross-site` or `same-site`, or an `Origin` that is neither the origin the client reached nor the `BASE_URL` origin. Browsers cache Basic credentials and attach them to cross-site requests. The origin the client reached follows the [trusted-proxy rules](../getting-started/installation.md#the-public-url-and-base_url): `X-Forwarded-Host` and `X-Forwarded-Proto` count only from a peer in `OPENARTIFACT_TRUSTED_PROXIES`.

## API tokens

| Prefix | `kind` | Rights |
|---|---|---|
| `oa_pat_` | `access` | everything the owner can do, including the management API |
| `oa_dep_` | `deploy` | the owner's repository data actions only: the `manage` action is denied, so `/api/v1/*` management routes are unreachable (can read/write/delete package data). Two groups of `/api/v1` routes are decided per repository instead of by `manage` and therefore work with a deploy token: artifact [properties](../repositories/properties.md) and [release bundles](../repositories/release-bundles.md#permissions), which is what the [CI commands](../integrations/ci.md) of `oa` use |

A token is 32 random bytes in hex after the prefix; only its SHA-256 is stored, and the plain value is shown once in the creation response. Revoked, expired and disabled-owner tokens are rejected; `last_used` is updated at most once a minute.

Create body for `POST /api/v1/auth/tokens` (HTTP 201, response includes `token`):

| Field | Rule |
|---|---|
| `name` | required, at most 100 characters |
| `kind` | `access` or `deploy` |
| `scopes` | optional list (max 64) of permission-target IDs; the token gets the intersection of these targets and the owner's rights. Admins may name any target, others only targets they hold |
| `expires_at` | optional RFC 3339 time, in the future and at most 2 years ahead; omitted means never |

| Endpoint | Who |
|---|---|
| `POST/GET /api/v1/auth/tokens`, `DELETE /api/v1/auth/tokens/{id}` | the caller's own tokens |
| `POST/GET /api/v1/users/{id}/tokens`, `DELETE /api/v1/users/{id}/tokens/{tokenId}` | the owner, or an admin |
| `GET /api/v1/admin/tokens?page=&per_page=`, `DELETE /api/v1/admin/tokens/{tokenId}` | admins, all tokens |

**Minting and revoking require an interactive session** (cookie plus `X-CSRF-Token`). A token, including an admin's, gets 403 (`API tokens cannot create tokens`), so a leaked token cannot spawn replacements. Scripts that need a token must log in first:

```sh
curl -sS -c jar -X POST HOST/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<PASSWORD>"}' -o /dev/null
CSRF=$(awk '$6=="oa_csrf"{print $7}' jar)
curl -sS -b jar -X POST HOST/api/v1/auth/tokens -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' -d '{"name":"ci","kind":"deploy"}'
```

`oa auth login` does the same exchange and saves an `access` token ([CLI](../integrations/cli.md)). The web UI creates and revokes tokens in **Settings > Access tokens** (`/settings/tokens`): personal tokens, [project tokens](projects.md#project-tokens) and federation tokens in one list, filtered by type.

Use a token:

```sh
curl -H 'Authorization: Bearer <TOKEN>' HOST/api/v1/auth/me
curl -u any-user:<TOKEN> HOST/repos/REPO/path/to/file     # Basic: token as password
```

Per-format client configuration is under [formats](../formats/README.md).

### MCP endpoint

The [MCP server](../integrations/mcp.md) accepts only `Authorization: Bearer`: an API token, an OIDC access token (a JWT from an enabled OIDC provider whose `aud` is the MCP resource URI, mapped to an existing local user), or the deprecated legacy key. The session cookie and Basic credentials are ignored on that path, so there is no CSRF surface and no CSRF token. A missing or invalid credential gets 401 with `WWW-Authenticate: Bearer realm="openartifact", resource_metadata="…"`, and project tokens get 403. Permissions are checked per tool with the same evaluator as the REST API, so a read-only token can use the read tools.

## Legacy API key

`API_KEY` / `server.apiKey` sets a single shared key; `LEGACY_API_KEY_ENABLED` / `auth.legacyApiKeyEnabled` (default `false`; set it to `true` to keep using `API_KEY`) switches the mechanism. It is active only when the key is non-empty and enabled, and is read from `X-Api-Key`, else the Bearer value, else the Basic password (constant-time compare).

It maps to the principal `system/legacy-api-key`, which is **full admin for every action**, not write-only, and bypasses CSRF. Every use logs a `legacy_api_key_used` deprecation warning. Leave `API_KEY` unset and use API tokens instead. An empty key does not disable authentication: RBAC is always enforced.

## Anonymous access

Anonymous principals can only `read`.

- A repository with `public: true` is readable anonymously regardless of any global setting.
- With `ALLOW_ANONYMOUS_READ=true` (`auth.allowAnonymousRead`) anonymous reads are matched against the seeded permission target `anonymous-read` (repository `.*`, path `.*`, `read`). Edit that target to narrow anonymous access.
- `GET /api/v1/repositories` and `/api/v1/search` always need an authenticated principal; authenticated users see only repositories they can read.
- Writes always need a credential.

A denied anonymous request gets 401 with `WWW-Authenticate` (`Bearer realm="openartifact"` outside `/v2/`, `Basic realm="openartifact"` outside `/api/`); a denied authenticated request gets 403 `{"error":"forbidden","action","path"}`.

## Network controls

| Setting | Env | Behaviour |
|---|---|---|
| `server.ipAllowlist` | `OPENARTIFACT_IP_ALLOWLIST` | other client IPs get 403 `IP not allowed`; empty allows all |
| `server.rateLimitRps` | `OPENARTIFACT_RATE_LIMIT_RPS` | per-client-IP requests per second; 0 turns it off |
| `server.trustedProxies` | `OPENARTIFACT_TRUSTED_PROXIES` | proxies whose forwarding headers are believed |

**IP allowlist.** `OPENARTIFACT_IP_ALLOWLIST` is a comma-separated list of CIDRs or bare IPs. A bare IP counts as a /32. An entry that parses as neither stops startup, so a typo never silently disables the allowlist. Empty means no restriction.

**Rate limit.** Over the limit, a client gets `429 rate limit exceeded` with `Retry-After: 1`. Each replica counts separately.

**Trusted proxies.** `OPENARTIFACT_TRUSTED_PROXIES` is a comma-separated list of CIDRs only. A bare IP such as `10.0.0.1` is not accepted; write `10.0.0.1/32`. Invalid entries are skipped silently, so check the list in effect.

- Only a peer inside these CIDRs may set `X-Forwarded-For` or `X-Real-IP`.
- The client IP is the rightmost `X-Forwarded-For` entry that is not itself a trusted proxy, reading every header line from last to first. An unparseable entry falls back to the peer.

The resolved client IP feeds the allowlist, rate limit, login throttle, audit log and sessions. Behind a reverse proxy, set `trustedProxies`; otherwise every client appears as the proxy. A warning is logged once when forwarding headers arrive from an untrusted peer.

Other related settings: `BASE_URL` (used for SSO callbacks), `AUTH_HMAC_KEY`, `OPENARTIFACT_SECRET_KEY` (encrypts stored secrets), `METRICS_AUTH_TOKEN`. See [../../.env.example](../../.env.example), [../../openartifact.example.yaml](../../openartifact.example.yaml) and [configuration](../getting-started/configuration.md).
