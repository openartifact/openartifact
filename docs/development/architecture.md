<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Architecture

OpenArtifact is one Go binary (`cmd/server`) that serves the REST API, the native package-manager protocols and an embedded web UI, backed by PostgreSQL and a pluggable blob store.

## I want to change... look in

| I want to change | Look in |
|---|---|
| A REST endpoint | `internal/api`, then `internal/api/openapi.json` |
| A package protocol (the URLs a client calls) | `internal/format/<format>` |
| Which formats can be proxy, group or federated | `internal/repotype`; no other file lists formats |
| How a format is wired in (hooks, storage descriptor) | `internal/formats/<format>.go`, see [adding a format](adding-a-format.md) |
| Proxy caching or group merging | `internal/proxy`, `internal/group`, `internal/repodispatch` |
| Permissions, tokens, login, SSO | `internal/auth`, `internal/middleware` |
| A database query or table | `internal/database/queries/*.sql` and `migrations/`, then `make generate` |
| Where blobs live | `internal/storage`, `internal/presign`, `internal/replication`, `internal/tiering` |
| A background job | `internal/worker`, see [workers](#workers) |
| Delete, trash and cleanup behaviour | `internal/trash`, `internal/worker/cleanup*.go`, `internal/quota` |
| Vulnerability, license, SBOM and exploit data | `internal/vuln`, `internal/osv`, `internal/exploit`, `internal/licensepolicy`, `internal/sbom` |
| Disaster recovery and backups | `internal/recovery`, `internal/backup` |
| Replication between instances | `internal/federation` |
| Configuration keys | `internal/config`, then `make docs-gen` |
| Startup, shutdown and route wiring | `cmd/server` |
| The web UI | `web/src`, see [development guide](README.md#web-ui-development) |
| Client setup snippets | `internal/clientsetup` |

## Layout

Top-level directories:

| Path | Contents |
|---|---|
| `cmd/server` | The `openartifact` binary: startup and shutdown (`app.go`, `app_*.go`), HTTP chain (`app_http.go`), route wiring (`app_routes.go`), roles, health, rebuild |
| `cmd/oa` | The `oa` binary: a thin `main` around `internal/oacli` |
| `internal/oacli` | CLI client: command tree, HTTP client, importers. The server binary embeds it too (`openartifact oa ...`) |
| `cmd/resetpw` | Admin password reset tool |
| `cmd/docgen` | Generates `docs/reference/` and checks `.env.example` and the example YAML |
| `cmd/genformats` | Generates `web/src/lib/formats.generated.ts` and `clientsetup.generated.ts` |
| `internal/` | Server packages, listed below |
| `pkg/*` | Format parsers and helpers: `ansible`, `apk`, `cargo`, `compactindex`, `composer`, `conan`, `conda`, `deb`, `delta`, `gemversion`, `gpg`, `helm`, `logger`, `maven`, `nix`, `npm`, `nuget`, `pacman`, `pyversion`, `rpm`, `rpmsign`, `rubymarshal` |
| `migrations/` | Embedded SQL migrations, applied at startup |
| `web/` | Vue 3 + TypeScript + Tailwind SPA (bun), embedded via `web/embed.go` |
| `tests/` | Go handler tests (many need `-tags=integration`); `tests/integration/` uses testcontainers; `tests/apifuzz/` configures API fuzzing |
| `scripts/` | CI and dev scripts: smoke tests per format (`smoke/`), test server, dead-code and duplication gates, coverage tools |
| `bench/` | `oabench` load generator and `run.sh` driver, see [performance](../operations/performance.md) |
| `tools/` | Separate Go module pinning `sqlc`, `govulncheck`, `deadcode`, `dupl` and `actionlint` |
| `docker/` | Dockerfile and compose files |
| `examples/` | Example configuration and manifests |

Packages under `internal/`, by area:

| Area | Packages |
|---|---|
| HTTP and API | `api` (REST handlers under `/api/v1/*`, upload handlers, `openapi.json`), `middleware` (security headers, recovery, request ID, tracing, metrics, logging, CORS, IP allowlist, rate limit, RBAC, CSRF, audit), `httpx` (JSON writers), `httpcache` (ETag and validators), `urlparams` (chi URL parameters as a map), `publicurl` (the externally visible origin), `branding` (white-labelling) |
| Formats | `format/<format>` (native protocol handlers; `base` is the shared plumbing, `filerepo` the one handler behind ivy and sbt, `plugin` the plugin types, `lifecycle` and `storage` the format hooks, `formattest` test helpers), `formats` (one plugin registration per format), `repotype` (the format registry), `registry` (the name-keyed hook tables), `clientsetup` (client URLs and snippets), `pypisimple`, `ocipath`, `ocimanifest`, `pkgdeps`, `licensemeta`, `xzutil`, `nixkeys`, `apkkeys`, `gpgkeys` (signing keys) |
| Proxy and group | `proxy` (fetch and cache service), `group` (ordered read-only merge of members), `repodispatch` (routes proxy and group requests in front of the per-format routes), `curation` (which upstream packages a proxy lets through), `netutil` (outbound HTTP safe against SSRF) |
| Auth | `auth` (sessions, tokens, permissions, OIDC and SAML, provisioning, bootstrap), `passwd` (password policy and hashing), `crypto` (AES-256-GCM for stored secrets, `OPENARTIFACT_SECRET_KEY`) |
| Data | `database` (`PostgresDB` composition root over one store per domain, sqlc code in `dbsqlc/`), `storage` (filesystem, S3, GCS, Azure and OCI blob stores, `SafeDeleteBlob` refcounting), `presign`, `quota`, `trash` (soft delete policy), `tiering` (storage tiers and lifecycle), `replication` (copies blobs to a secondary store), `recovery` (row journal and rebuild from storage), `backup`, `promote` (copies release bundle items between repositories) |
| Security data | `vuln`, `osv` (OSV import), `exploit` (CISA KEV and exploit feeds), `secrets` (secret scanning), `quarantine` (blocks flagged downloads), `license`, `licensepolicy`, `sbom`, `attest` (signs SBOM and VEX), `dtrack` (Dependency-Track export), `scanexport` |
| Background work | `worker` (workers, see below), `workq` (queue a web process uses to ask a worker process), `bgtask` (fire-and-forget goroutines), `pglock` (Postgres advisory locks), `cron` (5-field expression parser, UTC), `webhook` |
| Integrations | `federation` (pull replication between instances), `declarative` (`oa apply`, `oa diff`, `oa export`), `search`, `mcp` (MCP server), `ai` (LLM analysis), `importmeta` (`X-OA-Import-Created-At` for migrations) |
| Observability and config | `config`, `metrics`, `telemetry` (OpenTelemetry), `audit`, `buildtags` (which optional features were compiled in) |
| Small shared helpers | `artifactpath`, `itempath`, `pathseg` (path segment rules), `iocopy`, `limitio` (bounded readers) |

## Request flow

```text
client
  │
  ▼  global chain, in this order (cmd/server/app_http.go)
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

Every repo-scoped format handler registers **relative** routes on the single `/repos/{name}` subrouter. `plugin.MountShared` (`internal/format/plugin/mount.go`) builds that subrouter from the registrations in `internal/formats`, and `cmd/server/app_routes.go` calls it. Each handler exposes its routes as `RegisterRepoRoutes`. Never register an absolute `/repos/{name}/...` route on `authedAPI`: the subrouter mount shadows it (404) or the route hijacks the subrouter.

- Routes that need the same pattern are dispatched explicitly. `PUT /*` is shared: a format claims it through `plugin.PutClaim`. Go claims the upload paths that `formatgomod.IsUploadPath` matches, and npm publish takes every other path.
- Formats whose wildcard paths would shadow others are mounted on isolated prefixes: `/repos/ivy/{name}`, `/repos/maven/{name}`, `/repos/raw/{name}`, `/repos/sbt/{name}`. All `/repos/...` mounts sit behind `repodispatch`.
- Which formats can be proxy or group repositories is defined only in `internal/repotype`; see the `Proxy` and `Group` fields of each `Spec`.
- Docker proxies and groups are not served under `/repos/{name}`. `repodispatch.OCIMiddleware` sits in front of the `/v2` routes and hands them to `proxy.ServeOCI` and `group.ServeOCI`. Bearer-token handling is in `internal/proxy/oci_auth.go`.
- The Terraform and OpenTofu registry needs two routes outside RBAC on the bare router: the service discovery document (`/.well-known/terraform.json`) and the ticket download route. The clients send no credentials to download URLs ([terraform](../formats/terraform.md#download-tickets)). Everything else, including the publish API (`/_api/...`), is on the `/repos/{name}` subrouter.
- For a proxy repository the ticket route stays in the hosted handler. A ticket whose repository is a proxy goes to `proxy.Service.ServeTerraformDownload` (`terraform.ProxyServer`), which fetches the file from its upstream host on first use. `terraform.Minter` mints the tickets with the same HMAC key.
- The Ansible Galaxy API v3 is served by `internal/format/ansible` on the `/repos/{name}` subrouter under `/api/` (hosted); proxy and group repositories are `internal/proxy/ansible.go` and `internal/group/ansible.go`.
- The Composer repository (`packages.json`, `p2/`, `list.json`, `search.json`, upload and dist download) is served by `internal/format/composer` on the `/repos/{name}` subrouter (hosted); proxy and group repositories are `internal/proxy/composer.go` and `internal/group/composer.go`.
- Group repositories read hosted members through in-memory routers of the hosted handlers (`plugin.ReadRouters`), one per format that registers a hosted router. Every format supports proxy and group repositories; Docker/OCI groups read their members through the `/v2` path instead.
- `cmd/server/routes_test.go` guards the registration order against chi `Mount()` panics.

## Proxy and group pipeline

A format does not re-implement caching, revalidation, SSRF checks or member merging; it supplies hooks to one pipeline per side.

**Proxy** (`internal/proxy/pipeline.go`). Stages: fetch the upstream metadata, cache it with serve-stale, render it (rewrite download URLs to the repository), resolve a download only from the cached metadata, SSRF-check the target, store it content-addressed. A format describes its documents with a `metaDoc` (cache key, upstream URL, fetch options and verification, size limit, what to do with an unparsable copy) and plugs into three entry points:

| Entry point | Used for | Format hook |
|---|---|---|
| `serveVerbatim` | metadata served byte for byte (cargo index, go `@v`, rubygems, conan) | `metaDoc` |
| `serveRendered` | metadata rewritten per request (npm packument, pypi simple pages, helm index, composer p2, ansible, nuget) | `render`, `etag` |
| `serveDownload` | files named by cached metadata (npm tarball, pypi file, helm chart, cargo crate, composer dist, ansible tarball, gem, conda package, terraform archive) | `locate`: parse the cached metadata, return the URL and the digest the bytes must have |

Credentials go only to the upstream's own origin (the pipeline forces `noAuth` for any other), a file is cached only when its digest matches what the metadata promised, and a missing blob of a cached row is refetched. Docker has its own OCI path (`internal/proxy/oci*.go`); apk, deb, ivy, maven, nix, pacman, rpm and sbt mirror the upstream path by path and use the generic `Serve` route (`fetch`/`fetchURL`), which is the same fetch, cache and store stages without a metadata parser.

**Group** (`internal/group/pipeline.go`). Files, and every path a format does not merge, are first-hit: the first readable member that has them answers. Hosted members go first when hosted wins applies (`applyHostedWins` in `hostedwins.go`).

A merged document is described by a `mergedDoc`: what to ask every member, size limit, merge hook, content type and cache lifetime. `serveMergedDoc` serves it in four steps:

1. Collect the members' 200 answers. A 404 is skipped; a failing member is recorded.
2. Merge by the format's key. The first member that has an entry wins.
3. Answer with a content ETag.
4. Mark a partial merge (a failed member left out) uncacheable.

Adding a format: write its `pkg/<format>` grammar and parsers, then its proxy hooks (`internal/proxy/<format>.go`) and merge hook (`internal/group/<format>.go`); do not copy the resolve/revalidate/read/gate sequence.

## Storage and database

- **Blob store** (`storage.BlobStore`): `Put/Get/Delete/Exists/GetMetadata/List/GetSize/ExistsBySHA256/PresignURL`. Blobs are content-addressed by sha256 and deduplicated across repositories, so deletes go through `storage.SafeDeleteBlob` (refcounting). Backends are chosen by `storage.type`; see [../operations/storage.md](../operations/storage.md). The server wraps the store in `NewMetricsBlobStore` for Prometheus metrics.
- **Presigned URLs** ([storage](../operations/storage.md#presigned-urls)): the S3 and Azure stores implement the optional `storage.Presigner` interface. Every store implements `storage.StagingStore` and `storage.MultipartStagingStore`: S3 and Azure natively, filesystem and OCI through the HMAC relay in `internal/storage/relay.go`. `MetricsBlobStore` forwards them.
  - The `presign.Store` decorator in `internal/presign` wraps the blob store. Per request it decides whether to answer a blob download with a `307` to a presigned GET URL (global switch, per-repository `presign_downloads`, `presignMinSize`, backend support).
  - Handlers built on `base.Handler` call the `TryPresign` hook after the authorisation check. Handlers without it (docker, deb, rpm) call `presign.Try` directly. Metadata responses never use it.
  - Presigned raw uploads live in `internal/api/presigned_uploads.go` (create and complete) and share the write path with the raw `PUT` through `raw.Commit`. `worker.PresignedUploadWorker` removes stale staging objects.
- **Database** (`database.PostgresDB`): one store type per domain, each in its own file and each a `struct{ *core }` over the shared connection state (`core`: pool, sqlc queries, logger, auth cache). Examples are `RepositoryStore` (`repositories.go`), `UserStore`, `TokenStore`, `QuotaStore`, `BlobRefStore`, `CatalogStore` (cross-format package listing) and one `*Store` per package format (`pkg_<format>.go`).
  - `PostgresDB` (`stores.go`) embeds them all, so the methods stay reachable as `db.Method(...)` and a `*PostgresDB` satisfies any consumer interface.
  - There is no whole-database interface. Every handler, worker and middleware declares the small interface of the methods it calls next to the code that uses it (for example `api.BundlesStore`, `quota.EnforceStore`, `worker.CleanupStore`), and tests give those interfaces small local fakes.
  - Hand-written SQL goes in `queries/*.sql`; sqlc generates `dbsqlc/`, which is never edited by hand.
  - A package format adds its own `pkg_<format>.go` store and nothing else here: it shares `package_files`, so `blobRefSpecs` (`internal/database/postgres.go`) needs no entry. Only a table of its own (as docker has) goes into `blobRefSpecs`, `internal/recovery/tables.go` and the catalog queries. See [adding a format](adding-a-format.md).
- **Migrations** are `migrations/*.sql`, embedded (`migrations/embed.go`) and applied at startup unless `MIGRATE_ON_START=false`; `openartifact -migrate` applies them and exits. They are forward-only: the migrator aborts on checksum drift of an applied file.

## Workers

The `app` in `cmd/server` starts the workers. `app.go` is the startup and shutdown sequence, and the `app_*.go` files hold its steps. On shutdown the server stops in this order:

1. HTTP.
2. The fire-and-forget background tasks: async backups, restores and scans, and startup metadata regeneration. They run on `internal/bgtask`, get a context cancelled at shutdown and are waited for at most 15 s.
3. The workers.
4. The audit flush.
5. The database.

The workers, all in `internal/worker` unless noted:

| Worker | Role |
|---|---|
| Metadata workers: deb, rpm, pacman, apk, maven, npm | Regenerate per-format repository metadata after uploads and deletes |
| Group metadata | Builds the indexes of apk, deb, pacman and rpm group repositories from their members |
| Proxy cache | Evicts expired proxy cache entries, and their blobs once unreferenced, honouring each repository's max-stale window |
| Cleanup | Applies cleanup policies and reclaims blobs no package row references any more |
| Blob deletion sweeper | Removes the bytes of deleted blobs after a delay, see [storage](../operations/storage.md#deferred-blob-deletion) |
| Trash purge | Permanently removes soft-deleted items whose retention window has passed |
| Docker registry GC | Deletes untagged Docker manifests past a grace period, and the blobs only they referenced ([details](../formats/docker.md#garbage-collection)) |
| Docker upload sweeper | Deletes abandoned Docker blob upload sessions and the data of uploads that no session owns |
| Presigned upload | Expires presigned uploads that were never completed |
| Tier mover | Moves blobs between storage tiers on a schedule, see [storage tiering](../operations/storage-tiering.md) |
| Auth purge | Deletes expired sessions and long-expired or revoked API tokens |
| Audit retention | Deletes audit entries older than `audit.retentionDays` |
| OSV mirror | Keeps the local OSV database current and matches stored packages against it |
| Exploit feeds | Imports the CISA KEV catalog and EPSS scores |
| AI analysis | Optional; runs when AI is enabled |
| Webhook dispatcher | Delivers events (`artifact.uploaded`, `artifact.deleted`, ...) |
| Recovery journal (`internal/recovery`) | Writes the disaster-recovery row journal to the blob store |
| Replication (`internal/replication`) | Copies blobs to the secondary store and verifies both sides |
| Federation engine (`internal/federation`) | Pulls artifacts from peer instances |

Workers that take Postgres advisory locks hold them on a pinned connection (`internal/worker/lock.go`), so several server instances can run them safely. With `server.role` set, web and worker processes split: `internal/workq` is the queue a web process uses to ask a worker process for work.

## Web UI

`web/` is built with bun/Vite into `web/dist`, embedded with `go:embed` in `web/embed.go`, and served by the `authedAPI` NotFound handler (client-side routing). Unmatched `/api/` paths return a JSON 404, non-GET/HEAD requests return 405. In binaries built with `-tags dev`, setting `OPENARTIFACT_DEV` makes the server proxy the UI to the Vite dev server (`http://localhost:5173`); see [README](README.md).
