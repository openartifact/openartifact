<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Architecture

OpenArtifact is one Go binary (`cmd/server`) that serves the REST API, the native package-manager protocols and an embedded web UI, backed by PostgreSQL and a pluggable blob store.

## Layout

| Path | Contents |
|---|---|
| `cmd/server` | HTTP server entrypoint: route wiring, startup/shutdown, bootstrap admin |
| `cmd/oa` | CLI client |
| `cmd/resetpw` | Admin password reset tool |
| `internal/api` | REST handlers (`/api/v1/*`), upload handlers (`upload_{deb,rpm,npm,maven}.go`), `openapi.json` |
| `internal/format/<fmt>` | Native protocol handlers per package format (`/repos/{name}/...`, `/v2/...`); `base` holds the shared handler plumbing, `formattest` test helpers |
| `internal/middleware` | Security headers, recovery, request ID, tracing, metrics, logging, CORS, IP allowlist, rate limit, RBAC, CSRF, audit |
| `internal/auth` | Sessions, tokens, permission evaluation, OIDC/SAML, SSO provisioning, bootstrap |
| `internal/database` | `Database` interface (`interfaces.go`), Postgres implementation (`postgres*.go`), sqlc code in `dbsqlc/`, queries in `queries/*.sql` |
| `internal/storage` | Blob stores: filesystem, S3, GCS, Azure, OCI; `SafeDeleteBlob` refcounting |
| `internal/worker` | Background workers: per-format metadata regeneration, cleanup, Docker registry GC, proxy cache, auth purge, AI analysis, blob deletion sweeper (deferred byte removal, see [storage](../operations/storage.md#deferred-blob-deletion)) |
| `internal/repodispatch` | Chi middleware that serves proxy and group repositories in front of the per-format routes |
| `internal/proxy`, `internal/group` | Proxy-repository fetch/cache service; group (ordered, read-only merge of members) service |
| `internal/pypisimple` | PyPI simple API (PEP 503/691) parsing, rendering, content negotiation and path/name rules shared by the pypi proxy and group |
| `internal/repotype` | Which formats may be proxy or group repositories (shared by repo creation and dispatch) |
| `internal/ocipath` | Parses `/v2/<name>/...` paths; shared by RBAC and the docker handler so both agree on the repository |
| `internal/importmeta` | Admin-only `X-OA-Import-Created-At` header that preserves origin timestamps during migrations |
| `internal/clientsetup` | Native client URLs and configure/publish snippets per format (Go port of `web/src/lib/installCommands.ts`; keep both in sync) |
| `internal/quarantine` | Blocks downloads of artifacts flagged by vulnerability/secret scans |
| `internal/quota` | Reserves/releases repository storage quota on upload, delete and cleanup-policy deletes (the database's `ReconcileStorageUsage` recounts the counters from the package rows) |
| `internal/netutil` | SSRF-safe outbound HTTP helpers (webhooks, proxy upstreams) |
| `internal/cron` | 5-field cron expression parser (UTC fire times) |
| `internal/urlparams` | `Vars(r)`: chi URL parameters as a map |
| `internal/{backup,webhook,search,vuln,sbom,license,secrets,ai,mcp,audit,metrics,config}` | Feature subsystems |
| `internal/crypto` | AES-256-GCM encryption of stored secrets (`OPENARTIFACT_SECRET_KEY`) |
| `pkg/*` | Format parsers and helpers: `deb`, `rpm`, `npm`, `nuget`, `maven`, `helm`, `rubymarshal`, `gpg`, `delta`, `errors`, `logger` |
| `migrations/` | Embedded SQL migrations, applied at startup |
| `web/` | Vue 3 + TypeScript + Tailwind SPA (bun), embedded via `web/embed.go` |
| `tests/` | Go handler tests (many need `-tags=integration`); `tests/integration/` uses testcontainers |
| `bench/` | `oabench` load generator and `run.sh` driver; see [performance](../operations/performance.md) |
| `tools/` | Separate Go module pinning `sqlc`, `govulncheck`, `actionlint` |
| `docker/` | Dockerfile and compose files |

## Request flow

```text
client
  │
  ▼  global chain, in this order (cmd/server/main.go)
  SecurityHeaders → Recovery → RequestID → Tracing → Metrics → Logging → CORS
    → [IPAllowlist] → [RateLimit] → AuthAudit
  │
  ├─ bare router (no auth): /health /ready /metrics*, login/logout, OIDC/SAML
  │    callbacks, public SSO list, system info, OpenAPI
  │
  └─ authedAPI, mounted at "/":  RBAC → importmeta → CSRF → Audit
       ├─ /api/v1/*             → internal/api handlers, search, MCP mount
       ├─ /v2/*                 → docker handler (path parsed by ocipath)
       ├─ /repos/{name}/*       → repodispatch ─┬─ hosted: format handler
       │                                        └─ proxy/group: internal/proxy, internal/group
       └─ anything else         → embedded SPA (NotFound handler)
                                       │
            format handler / API ──────┴──► quota, quarantine → storage (BlobStore) + database (Database)
```

`[...]` middlewares are only installed when configured. `/metrics` answers 401 unless `METRICS_AUTH_TOKEN` is set or `METRICS_ALLOW_ANONYMOUS=true`.

RBAC is the authorization gate: `/api/v1/*` maps to the `manage` action, `/repos/{name}/...` to `read`/`write` by HTTP method, `/v2/...` via `ocipath`. Page loads of the SPA bypass RBAC (GET/HEAD only). Details: [../security/rbac.md](../security/rbac.md).

## Routing invariant

Every repo-scoped format handler registers **relative** routes on the single `/repos/{name}` subrouter in `cmd/server/main.go` (`RegisterRepoRoutes(r)`). Never register an absolute `/repos/{name}/...` route on `authedAPI`: the subrouter mount shadows it (404) or the route hijacks the subrouter.

- Routes that need the same pattern are dispatched explicitly. `PUT /*` is shared by npm publish and GOPROXY upload: the catch-all calls `gomodHTTP.HandlePut` when `formatgomod.IsUploadPath` matches the tail, `npmUpload.HandlePublish` otherwise.
- Formats whose wildcard paths would shadow others are mounted on isolated prefixes: `/repos/raw/{name}`, `/repos/maven/{name}`, `/repos/sbt/{name}`, `/repos/ivy/{name}`. All `/repos/...` mounts except sbt and Ivy sit behind `repodispatch`. Proxy repositories are supported for maven, deb, rpm, docker, npm, pypi, cargo, go, helm, nuget and raw; groups for maven, docker, npm, pypi, cargo, go, helm, nuget and raw (`internal/repotype`). sbt and Ivy are hosted-only. Docker proxies and groups are not served under `/repos/{name}`: `repodispatch.OCIMiddleware` sits in front of the `/v2` routes and hands them to `proxy.ServeOCI` / `group.ServeOCI` (bearer-token handling in `internal/proxy/oci_auth.go`).
- Group repositories read hosted members through in-memory routers of the same Maven, npm, PyPI, cargo, Go, Helm, NuGet and raw handlers.
- `cmd/server/routes_test.go` guards the registration order against chi `Mount()` panics.

## Storage and database

- **Blob store** (`storage.BlobStore`): `Put/Get/Delete/Exists/GetMetadata/List/GetSize/ExistsBySHA256/PresignURL`. Blobs are content-addressed by sha256 and deduplicated across repositories, so deletes go through `storage.SafeDeleteBlob` (refcounting). Backends are chosen by `storage.type`; see [../operations/storage.md](../operations/storage.md). The server wraps the store in `NewMetricsBlobStore` for Prometheus metrics.
- **Presigned URLs** ([../operations/storage.md#presigned-urls](../operations/storage.md#presigned-urls)): the S3 and Azure stores implement the optional `storage.Presigner` interface, and every store implements `storage.StagingStore` and `storage.MultipartStagingStore` (S3, Azure natively; filesystem and OCI through the HMAC relay in `internal/storage/relay.go`) (`MetricsBlobStore` forwards them). The `presign.Store` decorator in `internal/presign` wraps the blob store and decides, per request, whether to answer a blob download with a `307` to a presigned GET URL (global switch, per-repository `presign_downloads`, `presignMinSize`, backend support). Handlers built on `base.Handler` call the `TryPresign` hook after the authorisation check; handlers without it (docker, deb, rpm) call `presign.Try` directly. Metadata responses never use it. Presigned raw uploads live in `internal/api/presigned_uploads.go` (create and complete), share the write path with the raw `PUT` through `raw.Commit`, and stale staging objects are removed by `worker.PresignedUploadWorker`.
- **Database** (`database.Database`): the interface in `internal/database/interfaces.go` is what handlers and workers depend on; `postgres*.go` implements it, hand-written SQL goes in `queries/*.sql` and sqlc generates `dbsqlc/` (never edited by hand). `ListRepoComponents` unions every per-format package table, so a new format needs a branch there.
- **Migrations** are `migrations/*.sql`, embedded (`migrations/embed.go`) and applied at startup unless `MIGRATE_ON_START=false`; `openartifact -migrate` applies them and exits. They are forward-only: the migrator aborts on checksum drift of an applied file.

## Workers

Started in `cmd/server/main.go` and stopped in order on shutdown (HTTP first, then workers, then the audit flush, DB last):

| Worker | Role |
|---|---|
| RPM / Deb / Maven / npm metadata | Regenerate per-format repository metadata after uploads and deletes |
| Proxy cache | Evicts expired proxy cache entries (and their blobs once unreferenced), honoring each repository's max-stale window |
| Cleanup | Applies cleanup policies and reclaims blobs no package row references any more |
| Docker registry GC | Deletes untagged Docker manifests past a grace period and the blobs only they referenced ([details](../formats/docker.md#garbage-collection)) |
| Auth purge | Deletes expired sessions and long-expired or revoked API tokens |
| AI analysis | Optional; runs when AI is enabled |
| Webhook dispatcher | Delivers events (`artifact.uploaded`, `artifact.deleted`, ...) |

Workers that take Postgres advisory locks hold them on a pinned connection (`internal/worker/lock.go`), so they are safe with multiple server instances.

## Web UI

`web/` is built with bun/Vite into `web/dist`, embedded with `go:embed` in `web/embed.go`, and served by the `authedAPI` NotFound handler (client-side routing). Unmatched `/api/` paths return a JSON 404, non-GET/HEAD requests return 405. In binaries built with `-tags dev`, setting `OPENARTIFACT_DEV` makes the server proxy the UI to the Vite dev server (`http://localhost:5173`); see [README](README.md).
