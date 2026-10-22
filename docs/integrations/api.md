<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# REST API

The management API lives under `/api/v1`; the web UI, the [CLI](cli.md) and the [MCP server](mcp.md) all use it. Package-manager protocols (npm, Maven, Docker, ...) are separate native routes, see [formats](../formats/README.md).

## Spec and explorer

| URL | Content |
|---|---|
| `/api/v1/openapi.json` | OpenAPI 3 spec (embedded from [`internal/api/openapi.json`](../../internal/api/openapi.json)) |
| `/api/v1/openapi.yaml` | same spec rendered as YAML |
| `/swagger/` | Swagger UI (`/swagger` redirects here); it loads its JS/CSS from `unpkg.com`, so the browser needs internet access |

These routes need no authentication. The spec is the reference for request and response schemas; a test (`TestOpenAPISpecMatchesRoutes`) fails when a registered route is missing from it or the spec documents a route nobody serves. `GET /health`, `GET /ready` and `GET /metrics` are also unauthenticated and are covered in [monitoring](../operations/monitoring.md).

## Authentication

Details, token kinds and SSO are in [authentication](../security/authentication.md); permissions in [RBAC](../security/rbac.md). In short:

- **API token:** `Authorization: Bearer <TOKEN>`. No CSRF header needed. Tokens are `access` (management API, per the owner's permissions) or `deploy` (repository credentials; never granted `manage`, so no `/api/v1` management calls).
- **Session cookie:** `POST /api/v1/auth/login` (`{"username","password"}`) sets `oa_session` (HttpOnly, SameSite=Lax) and a readable `oa_csrf` cookie. State-changing requests (`POST`, `PUT`, `PATCH`, `DELETE`) on a session must echo it in `X-CSRF-Token`, otherwise `403 {"error":"csrf_protection",...}`. Login is rate limited (`429 rate_limited` with `Retry-After`).
- **Self-service limits:** an API token cannot create or revoke tokens, change a password, or edit its own user profile; those need a login session. An account flagged `must_change_password` gets `403 password_change_required` everywhere except `change-password` and `logout`.
- **Gate:** every `/api/v1/*` route requires the global `manage` permission, except `GET /repositories` and `GET /search` (any authenticated user; results are filtered per repository), `/projects/**` (decided by the caller's [project role](../security/projects.md); unlisted sub-paths still need `manage`), `PATCH`/`DELETE /repository/{name}`, `/repositories/{name}/cleanup-policies...` and `/repositories/{name}/quota` (`manage`, or maintainer/owner of the repository's project), `GET /repository/{name}` (`read` on that repository), `/repositories/{name}/{packages,debs,comps}` and `/repositories/{name}/trash...` (`read`/`write`/`delete`, or `delete`, on that repository; `DELETE .../trash` needs `manage` on it). Anonymous callers can only read public repositories (or what `allowAnonymousRead` exposes).

### Example: token and repository

Tokens are minted from a login session, then used for everything else:

```sh
HOST=http://localhost:8080

# 1. log in (cookie jar), read the CSRF cookie
curl -sS -c jar.txt -X POST "$HOST/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<PASSWORD>"}'
CSRF=$(awk '$6=="oa_csrf"{print $7}' jar.txt)

# 2. create an access token; the secret is only returned here ("token")
curl -sS -b jar.txt -X POST "$HOST/api/v1/auth/tokens" \
  -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"name":"ci","kind":"access"}'
# -> {"id":"...","name":"ci","kind":"access","scopes":[],"created_at":"...","token":"<TOKEN>"}

# 3. use it
curl -sS -X POST "$HOST/api/v1/repositories" \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"npm-hosted","format":"npm","description":"internal packages","public":false}'

curl -sS -H "Authorization: Bearer <TOKEN>" "$HOST/api/v1/repositories?limit=10"
```

Token body fields: `name` (required, max 100 characters), `kind` (`access` or `deploy`), optional `scopes` (permission target IDs; a scope can only narrow the owner's access) and `expires_at` (RFC 3339, in the future, at most 2 years out). Creating repositories requires an administrator. Valid `format` values: `deb`, `rpm`, `maven`, `npm`, `docker`, `helm`, `pypi`, `nuget`, `go`, `cargo`, `rubygems`, `raw`, `conda`, `conan`, `ivy`, `sbt`. The proxy/group options (`repo_type`, ...) are in the spec; see [hosted, proxy and group repositories](../repositories/hosted-proxy-group.md).

## Conventions

- **JSON, snake_case.** Request and response bodies use snake_case keys. Timestamps are RFC 3339 strings (`created_at`, `expires_at`, ...); optional ones are `null`.
- **Unknown request fields are ignored**, not rejected: a misspelled key silently does nothing, so check the spec.
- **Secrets are never returned** (webhook secrets, upstream passwords, SSO client secrets, AI keys, storage credentials); write-only fields come back masked or as `has_*` flags.

### Errors

Handler errors share one shape; `details` is only present when a handler adds it:

```json
{"error": "name must be 1-100 alphanumeric/hyphen/underscore characters", "code": "validation_failed"}
```

| `code` | Status | Meaning |
|---|---|---|
| `bad_request` | 400 | malformed JSON or parameters |
| `validation_failed` | 400 | well-formed request with invalid values |
| `unauthorized` | 401 | authentication required or invalid |
| `invalid_credentials` | 401 | wrong username/password |
| `forbidden` | 403 | authenticated but not permitted |
| `admin_required` | 403 | administrator permission needed |
| `not_found` | 404 | missing resource, or a repository the caller may not see |
| `conflict` | 409 | duplicate or state conflict |
| `payload_too_large` | 413 | body over the endpoint limit |
| `rate_limited`, `too_many_scans` | 429 | throttled |
| `not_implemented` | 501 | endpoint exists but the feature is not available (currently the `/admin/gpg/keys` import, generate and delete endpoints) |
| `internal_error` | 500 | server error; the cause is logged, never returned |
| `LICENSE_BLOCKED` | 403 | a package upload was rejected by a [license policy](../security/scanning.md#blocking-licenses) with action `block`; the body adds `licenses` and `policies` |
| `LICENSE_POLICY_UNAVAILABLE` | 500 | the license policies could not be read, so the upload was refused |
| `upstream_error` | 502 | an upstream (LLM, IdP, proxy) failed |

SSO endpoints add their own codes (`oidc_*`, `saml_*`, `state_mismatch`, ...). Errors raised before a handler runs use simpler bodies: `401 {"error":"unauthorized"}`, `403 {"error":"forbidden","action":"...","path":"..."}`, `403 {"error":"csrf_protection","message":"..."}`, `400 {"error":"invalid_path"}`. Clients should key on the HTTP status first and on `code` when present.

### Pagination

List endpoints do not share one scheme. Always check the spec for the endpoint:

| Style | Endpoints | Request | Response keys |
|---|---|---|---|
| `page` + `limit` | repositories, repository packages, bundles | `page` from 1, `limit` default 25, max 100 | `total`, `page`, `limit`, plus the list (`repositories`, `packages`, ...) |
| `page` + `per_page` | users, admin tokens, admin audit | `per_page` default 25 (users 50), max 100 (users 200) | `data`, `page`, `per_page`, `total` |
| `limit` + `offset` | AI analyses | `limit` default 20, max 100; `offset` from 0 | `analyses`, `total`, `limit`, `offset` |

Out-of-range values are clamped rather than rejected. The trash lists (`/trash`, `/repositories/{name}/trash`) use the `page` + `limit` style with the list under `items`.

## Repository settings and deletion

`POST /api/v1/repositories` and `PATCH /api/v1/repository/{name}` accept two per-repository settings for the [trash](../operations/trash.md) and are returned in repository responses:

| Field | Type | Meaning |
|---|---|---|
| `trash_retention_days` | integer >= 0, or `null` | Days deleted items of this repository stay restorable. `null` uses the server default (`OPENARTIFACT_TRASH_RETENTION`, 7); `0` deletes permanently and immediately. Negative values are `400` |
| `immutable_releases` | bool | When `true`, deleting or overwriting a released version answers `409` unless an admin passes `?force=true` ([per-format definition](../operations/trash.md#immutable-releases)) |

`DELETE /api/v1/repository/{name}` requires the repository name as confirmation, either as a query parameter or a JSON body; without it the answer is `400 confirmation_required`:

```sh
curl -X DELETE "$HOST/api/v1/repository/old-repo?confirm=old-repo" -H "Authorization: Bearer <TOKEN>"
curl -X DELETE "$HOST/api/v1/repository/old-repo" -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"confirm":"old-repo"}'
```

The repository then moves to the trash for its retention period (unless that is `0`); its name stays reserved until it is purged or restored.

## Trash API

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /api/v1/trash` | admin | List trashed items of all repositories, trashed repositories included. Query: `page`, `limit`, `kind`, `repository` |
| `GET /api/v1/repositories/{name}/trash` | `delete` on the repository | List that repository's trashed items. Query: `page`, `limit`, `kind` |
| `POST /api/v1/trash/{kind}/{id}/restore` | admin | Restore an item or a repository; `200` with the item |
| `POST /api/v1/repositories/{name}/trash/{kind}/{id}/restore` | `delete` on the repository | Restore an item of that repository (not for kind `repository`) |
| `DELETE /api/v1/trash/{kind}/{id}` | admin | Purge now (permanent); `204` |
| `DELETE /api/v1/repositories/{name}/trash` | `manage` on the repository (or admin) | Empty that repository's trash (items only; at most 1000 per call); `200` `{"purged":N,"remaining":M}` |

An item is `{"id","kind","repository_id","repository","format","name","version","size_bytes","deleted_at","deleted_by","purge_at"}`; a list is `{"items":[...],"total":N,"page":P,"limit":L}`. Restore conflicts are `409` (`trash_conflict`, `repository_in_trash`). Kinds, examples and semantics: [Trash](../operations/trash.md#api).

## Route groups

Prefix `/api/v1`. Most groups are admin-only (the gate above); see the spec for methods, parameters and schemas.

| Prefix | Purpose |
|---|---|
| `/auth` | login, logout, `me`, `change-password`, own API tokens (`/auth/tokens`), SSO login and callbacks (OIDC, SAML) |
| `/repositories`, `/repository/{name}` | list and create; get, update and delete use the **singular** `/repository/{name}` path. The optional `presign_downloads` field (`true`, `false`, `null` to inherit) controls [presigned downloads](../operations/storage.md#presigned-urls) per repository |
| `/repositories/{name}/packages`, `/debs`, `/comps` | list (`GET /packages` accepts `format` and `package_name`, an exact listed-name match returning every version of one package), download and delete packages; upload (`debs` for Debian, `packages` for RPM) |
| `/repositories/{name}/uploads`, `/repositories/{name}/uploads/{id}/complete` | `POST` (write): presigned direct-to-bucket upload for hosted raw repositories on S3/GCS storage. The first call returns a presigned `PUT` URL (`Cache-Control: no-store`; `501` on unsupported backends, `429` with `Retry-After` past `presignMaxPendingUploads`). The second, callable by the creator or an admin and idempotent (`201` first, `200` on retry), reads the object back, verifies size and sha256 and records the artifact. `complete` errors: `403` not creator/admin; `404` unknown; `409` with code `upload_missing` (nothing uploaded yet), `upload_in_progress` (retry after `Retry-After`) or `upload_changed` (staging object re-uploaded during verification); `410` expired; `422` size or sha256 mismatch. See [Presigned uploads](../operations/storage.md#presigned-uploads) |
| `/repositories/{name}/members` | member repositories of a group repository |
| `/trash`, `/repositories/{name}/trash` | [trash](../operations/trash.md): list, restore and purge deleted items and repositories |
| `/repositories/{name}/{cleanup-policies,quota,webhooks}` | [cleanup and quotas](../repositories/cleanup-and-quotas.md), per-repository [webhooks](../operations/webhooks.md) |
| `/projects/{project}/{usage,quota,cleanup-policies}` | project storage usage, the project [quota](../repositories/cleanup-and-quotas.md#project-quotas) (`PUT` is global-admin only) and [default cleanup policies](../repositories/cleanup-and-quotas.md#project-default-cleanup-policies); access follows the caller's [project role](../security/projects.md) |
| `/repositories/{name}/registry-gc` | `POST` (admin): run [Docker registry garbage collection](../formats/docker.md#garbage-collection), optionally as a dry run |
| `/repositories/{name}/{sbom,vuln,license,secrets}` | SBOMs and [scanning](../security/scanning.md) (scans, quarantine, license policies) |
| `/repositories/{name}/artifacts/...` | [properties](../repositories/properties.md) and property search |
| `/repositories/{name}/packages/{id}/{delta,deltas}` | binary deltas |
| `/repositories/{name}/ai-analysis` | [AI analysis](ai-analysis.md) |
| `/search` | search packages and repositories |
| `/users`, `/groups`, `/permission-targets` | identity and [RBAC](../security/rbac.md) objects |
| `/projects` (with `/repositories`, `/members`, `/groups` and `/tokens`) | [projects](../security/projects.md): open to every authenticated user and decided per project role, not by global `manage` (`404` for non-members, `403` for members lacking the role). Updates use `PATCH` for the project and for a member's role |
| `/webhooks` | global webhooks, test, deliveries |
| `/bundles`, `/pipelines` | [release bundles](../repositories/release-bundles.md) and promotion pipelines |
| `/admin/{tokens,storage,backup,restore,audit,gpg,sso-providers,ai,license,system}` | token administration, [storage](../operations/storage.md), [backup](../operations/backup.md), audit log, GPG keys (`/admin/gpg/keys` is a stub: empty list, 501 on write; see [signing](../security/signing.md)), SSO providers, AI settings, global license policies, detailed system info |
| `/system/info` | version and build information |

`/admin/pprof/` is also registered when `server.pprofEnabled` is set (admin only; for diagnostics, see [performance](../operations/performance.md)).

## Regenerating the web client types

The SPA's types are generated from the spec. After changing routes or schemas, keep `internal/api/openapi.json` in sync with the handlers, then:

```sh
cd web && bun run generate-api    # writes web/src/api/generated
```

Calls from the SPA go through `web/src/api/client.ts`, which adds the CSRF header and handles 401.
