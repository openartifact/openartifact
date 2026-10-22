<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# AGENTS.md

Guidance for coding agents working on OpenArtifact: a self-hosted, multi-format artifact manager (deb, rpm, npm, maven, helm, rubygems, docker/OCI, pypi, cargo, go, nuget, conda, conan, ivy, sbt, raw). License: AGPL-3.0-or-later (REUSE-compliant; every new file needs an SPDX header).

## Layout

| Path | Contents |
|---|---|
| `cmd/server` | HTTP server entrypoint; route wiring, startup/shutdown, bootstrap admin |
| `cmd/oa` | CLI client |
| `cmd/resetpw` | Admin password reset tool |
| `internal/api` | REST handlers (`/api/v1/*`), upload handlers (`upload_{deb,rpm,npm,maven}.go`), `openapi.json` |
| `internal/format/<fmt>` | Native protocol handlers per package format (`/repos/{repo}/...`, `/v2/...`); the Go module format lives in `gomod` |
| `internal/repodispatch` | chi middleware in front of the per-format routes: hosted repos pass through, proxy and group repos are served by `internal/proxy` / `internal/group` |
| `internal/{group,repotype}` | Group member resolution + Maven metadata / npm packument / cargo index / Helm `index.yaml` / go `@v/list` / NuGet registration merge (`pkg/helm/index.go` parses, rewrites and merges indexes); `repotype` is the single policy of which formats may be proxy (maven, deb, rpm, docker, npm, pypi, cargo, go, helm, nuget, raw) or group (maven, docker, npm, pypi, cargo, go, helm, nuget, raw); `internal/pypisimple` is the PEP 503/691 parser/renderer behind the pypi proxy and group |
| `internal/ocipath` | Parses `/v2/<name>/...` into OpenArtifact repo + image; shared by RBAC and the docker handler |
| `internal/clientsetup` | Go port of `web/src/lib/installCommands.ts` (client snippets, used by MCP `client_setup`); keep both in sync |
| `internal/importmeta` | `X-OA-Import-Created-At` handling for `oa import` (honored only for `manage` principals) |
| `internal/netutil` | SSRF-safe outbound HTTP helpers (webhooks, proxy upstreams) |
| `internal/middleware` | RBAC, CSRF, audit, CORS, logging |
| `internal/auth` | Sessions, tokens, permission evaluation, OIDC/SAML, SSO provisioning, bootstrap |
| `internal/database` | `Database` interface (`interfaces.go`), Postgres impl (`postgres*.go`), sqlc code in `dbsqlc/`, queries in `queries/*.sql` |
| `internal/storage` | Blob stores: filesystem, S3, GCS, Azure, OCI; `SafeDeleteBlob` refcounting |
| `internal/worker` | Background workers: per-format metadata regeneration, cleanup, proxy cache, AI analysis |
| `internal/{backup,webhook,proxy,search,vuln,sbom,license,secrets,quarantine,quota,cron,ai,mcp,audit,metrics,config}` | Feature subsystems |
| `internal/crypto` | AES-256-GCM secret encryption at rest (`OPENARTIFACT_SECRET_KEY`) |
| `pkg/*` | Format parsers/libraries (deb, rpm, npm, nuget, maven, helm, rubymarshal, gpg, delta) plus shared `errors`/`logger` |
| `migrations/` | Embedded SQL migrations, applied at startup |
| `web/` | Vue 3 + TS + Tailwind SPA (bun), embedded via `web/embed.go` |
| `tests/` | Go integration/handler tests; `tests/integration/` uses testcontainers |
| `docker/` | Dockerfile (shell-less runtime: `RUNTIME=dhi` default, needs dhi.io login, or `distroless`; `-healthcheck` flag, no RUN in the final stage), compose, `.env.example` (compose credentials; the full env reference is the root `.env.example`) |
| `bench/` | Benchmark harness: `bench/run.sh` + `oabench` load generator (`make bench-load`) |
| `tools/` | Separate Go module (`tools/go.mod`) pinning sqlc, govulncheck, actionlint |
| `docs/` | User and developer documentation (index: `docs/README.md`); assets in `docs/assets/` |

## Commands

```sh
make build            # web + server binary → bin/
make build-cli        # bin/oa
make test             # go test -short -race ./...
make test-integration # -tags=integration tests (Postgres/S3 env or a container runtime)
make lint             # golangci-lint v2
make vet
make generate         # go tool -modfile=tools/go.mod sqlc generate (after editing internal/database/queries/*.sql)
make bench            # Go micro-benchmarks (-benchmem)
make bench-load       # bench/run.sh: seed a running instance and run HTTP load scenarios (OA_URL, OA_USER, OA_PASS, OA_PID)
cd web && bun install && bun run typecheck && bun test && bun run build
cd web && bun run generate-api   # regenerate src/api/generated from internal/api/openapi.json
make docker-up   # (OPENARTIFACT_RUNTIME=distroless without dhi.io access) compose up -d --build with VERSION/commit/build time stamped; needs docker/.env (copy from .env.example)
```

DB/S3-backed tests (behind `-tags=integration`) skip unless `TEST_DATABASE_URL` / `TEST_S3_ENDPOINT` (+ `TEST_S3_BUCKET`, `TEST_S3_ACCESS_KEY`, `TEST_S3_SECRET_KEY`, `TEST_S3_REGION`) are set; with any non-empty `CI` a missing variable fails instead of skipping. The S3 test backend is Garage (`dxflrs/garage`, region `garage`; MinIO no longer ships images) — see the `integration` job in `.github/workflows/ci.yml` for the bootstrap. `make test-integration` runs them; testcontainers under podman needs `DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true`.

Routing: every repo-scoped format handler registers relative routes on the single `/repos/{name}` subrouter in `cmd/server/main.go`. Never register an absolute `/repos/{name}/...` route on `authedAPI` — the mount shadows it (404) or it hijacks the subrouter. Shared catch-alls (`PUT /*`: npm publish + GOPROXY upload) are dispatched explicitly there.

Toolchain: Go 1.27.x, golangci-lint v2, bun. sqlc, govulncheck and actionlint are `tool` directives in `tools/go.mod` (separate module so Dependabot bumps them without bloating the main module): run them as `go tool -modfile=tools/go.mod <sqlc|govulncheck|actionlint> …` (`make generate` runs sqlc). CI actions are pinned by commit SHA and container images by `@sha256` digest; Dependabot updates both.

## Conventions and invariants

- **Migrations are forward-only.** Never edit an applied file (`001_initial_schema.sql`); the migrator aborts on checksum drift. Add `00N_<topic>.sql`. Keep transactional files short on large tables (metadata-only `ADD COLUMN`, `ADD CONSTRAINT … NOT VALID`, `SET LOCAL lock_timeout`); put index builds and `VALIDATE CONSTRAINT` in a following `00N_<topic>.no_tx.sql`, which the migrator splits and runs one statement at a time (so `CREATE INDEX CONCURRENTLY` works) and which must be re-runnable (`IF [NOT] EXISTS`, drop invalid indexes first), see `018_trash_indexes.no_tx.sql`.
- **sqlc:** edit `queries/*.sql`, run `make generate`; never hand-edit `dbsqlc/`.
- **Blobs are content-addressed by sha256.** Every `BlobStore.Put` must pass `{"sha256": <hex>}` metadata (S3/Azure/OCI require it). Blob IDs must be 64 lowercase hex — never pass user input or `blobs.id` UUIDs as storage keys. Delete blobs only via `storage.SafeDeleteBlob` (blobs are deduplicated across repos).
- **Repo scoping:** any handler that loads an entity by ID must check it belongs to the repo in the URL.
- **AuthZ:** RBAC in `middleware/rbac.go` is the gate. `/api/v1/*` maps to action `manage`; `/repos/{name}/...` to read/write by method. `/v2/<name>/...` (OCI) is parsed by `internal/ocipath` for both RBAC and the docker handler: the first segment of `<name>` is the OpenArtifact repo, the rest is the image (`/v2/repo/org/team/app/manifests/1` → repo `repo`, image `org/team/app`); tags/manifests are scoped per image, blobs per repo. SPA page bypass is GET/HEAD only. Format handlers must use the authenticated principal from context, never just the presence of an `Authorization` header.
- **Secrets** (webhook secrets, upstream passwords, SSO client secrets, AI keys, storage creds) are encrypted via `internal/crypto` and never returned in API responses.
- **Outbound URLs** (webhooks, proxy upstreams) must pass SSRF validation (no loopback/private/link-local, no unchecked redirects).
- **API JSON is snake_case.** Response models need explicit `json` tags; the frontend relies on them. Keep `internal/api/openapi.json` in sync with registered routes, then regenerate web types.
- **Frontend:** all API calls go through `web/src/api/client.ts` `request()` (handles CSRF header `X-CSRF-Token` from `oa_csrf` cookie and 401). No raw `fetch` for mutations. Strings go through `t()` / `web/src/i18n/en.ts`. Format icons come from `simple-icons` via `web/src/lib/formats.ts`; display formats with `FormatBadge`.
  - Dates: `formatDate`/`formatDateTime` from `web/src/lib/datetime.ts`; no per-page `toLocaleDateString` helpers.
  - Format colours: `formatColor`/`formatColorLowOnDark` for badges, `chartColor` (`CHART_PALETTE`) for charts; never hardcode hex per format.
  - Repo URLs and client snippets: `repoBaseUrl` / `clientSetup` in `web/src/lib/installCommands.ts`. Snippets must match real server routes (check `cmd/server/main.go` and the format handler), not another product's conventions.
  - Style: no gradients, glows, `backdrop-blur`, or letter-spaced uppercase labels; dashboards show only data the API actually returns.
- **Proxy/group repos:** only maven, deb, rpm, docker, npm, pypi, cargo, go, helm, nuget and raw can be proxies; only maven, docker, npm, pypi, cargo, go, helm, nuget and raw can be groups (`internal/repotype` is the single policy, enforced at creation). `internal/repodispatch` middleware serves them in front of the format handlers; hosted repos pass straight through. A new proxy/group format must be added to `repotype` and to the dispatch map in `cmd/server/main.go`. Docker is the exception to path mirroring: its proxies/groups are served only under `/v2` (`repodispatch.OCIMiddleware` → `proxy.ServeOCI` / `group.ServeOCI`) and `/repos/{name}` answers 404 for them; everything read-only goes through `ocipath.RejectNonPull`.
- **Projects are part of authorization** (`internal/auth/project_access.go`, role table in `project_roles.go`): members and granted groups of a project get access on repositories it owns (viewer → read, developer → read/write, maintainer/owner → read/write/delete; highest role wins), never `manage`, never from the system project (`models.SystemProjectID`), never for scoped tokens or anonymous. The control plane `/api/v1/projects/**` is gated by the same middleware: `routeScope.project` (`middleware/rbac.go`, route table in `middleware/rbac_projects.go`) is evaluated through `internal/auth/project_actions.go` `EvaluateProject` / `ProjectRole` (global admin = implicit owner; anonymous, `deploy` and scoped tokens get no project role). A non-member (or an unknown project) gets 404, a member lacking the role 403; unknown sub-paths and methods under `/api/v1/projects/**` fall to global `manage`. Repository administration is delegated to project maintainers/owners only through `routeScope.delegateRepo` + `Evaluator.EvaluateRepoAdmin` (`repo.admin`; handlers call `requireRepoAdmin`): never widen `Evaluate(manage)` for it, a repo-scoped `manage` target never implies it, and non-admin creation (`POST /projects/{p}/repositories`, shared `createRepository`) enforces the project policy while admins bypass it; the repository cap is checked in `CreateProjectRepository` under the project row lock. `POST /api/v1/projects` needs admin or the `projects.self_create` setting (`OPENARTIFACT_PROJECTS_SELF_CREATE`, admin-editable via `/api/v1/admin/projects/settings`; self-created projects take only name/display_name/description and get the default policy). Member and group mutations go through `PostgresDB.ChangeProjectMember` / `ChangeProjectGroup`, which lock the project row and run the escalation/last-owner guard callback; never write `project_members` / `project_groups` elsewhere. Changes go through `PostgresDB` methods that invalidate the auth cache; keep `docs/security/rbac.md` and `docs/security/projects.md` in sync.
- **Auth cache:** per-request auth lookups (token, user, groups, permission targets, repo by name) are cached for `DB_AUTH_CACHE_TTL_MS` (default 2000; 0 disables). Every write to those tables must live in a `PostgresDB` method that calls `defer p.authCache.invalidate()`; writes that bypass it (other replicas, `cmd/resetpw`) are seen only after the TTL.
- **pprof:** `OPENARTIFACT_PPROF_ENABLED` (off by default) mounts admin-only `/api/v1/admin/pprof/*`; never enable it by default or outside admin auth.
- **Workers** taking Postgres advisory locks must hold them on a pinned connection (or use xact locks), not the `*sql.DB` pool. Metadata regeneration (npm/maven/deb/rpm) goes through the coalescing `RegenQueue` (`internal/worker/regen_queue.go`: one running + one pending run per key, shared worker cap, lock-busy retry, drain on `Stop()`); never spawn a goroutine per upload.
- **Package listing:** `ListRepoComponents` is two-stage (each union branch takes its top `limit+offset` rows by `(created_at DESC, id)` before `blobs` is joined for the page); keep that shape and the matching `(repository_id, created_at DESC, id)` indexes when adding a branch.
- **HTTP caching:** metadata endpoints answer ETag/304 through `internal/format/base/cache.go` (after RBAC); `public` only for an anonymous read of a public repo, everything else `private, no-cache`. Never emit `public` for authenticated responses.
- **Audit list:** no `COUNT(*)`; keyset `cursor` + `has_more`, offset `total` is an estimate when large (`total_exact`).
- **Config:** every key settable by YAML must have an env override in `internal/config`, and be documented in `.env.example` and `openartifact.example.yaml`. Server read/write timeouts default to 0 (large uploads).
- Errors: use `writeError` with the shared codes; never leak `err.Error()` from DB/upstream to clients.
- Tests: assert behavior, not mock round-trips; no fixed sleeps — poll with deadlines.
- **Package listing (branches):** `ListRepoComponents` (`queries/components.sql`) unions every per-format table. Adding a format or a package table means adding a branch there (and to `CountRepoComponents`), or its packages silently vanish from the UI.
- **Timestamps:** DB upserts must not persist zero `time.Time`; default `created_at`/`updated_at` to now when the caller leaves them unset.
- **Anonymous access:** `repositories.public` grants anonymous `read` (never write) independent of `allowAnonymousRead`; the evaluator in `internal/auth/permission*.go` is the single place that decides it.
- **Client auth:** format routes accept API tokens (Bearer, or Basic with the token as password); account passwords work only for the UI login. Use tokens in examples and smoke tests.

## Verifying changes

- Web: `bun run typecheck && bun test`, then look at the page in the running app (dark and light theme).
- Go: `make lint` (gocyclo limit 20 applies to tests too; prefer table-driven subtests) and `make test`.
- End to end: rebuild with `docker compose -f docker/docker-compose.yml up -d --build openartifact`, log in (`POST /api/v1/auth/login`) and create a token via `POST /api/v1/auth/tokens` (`{"name":…,"kind":"access"}`) with the session cookie and `X-CSRF-Token` (tokens cannot mint tokens; see `docs/integrations/api.md`), and publish with the real client (`npm publish`, `curl -T` for raw, …) before claiming a format works.

## Review skills

`.agents/skills/` vendors Go skills from [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) and Vue skills from [vuejs-ai/skills](https://github.com/vuejs-ai/skills) (both MIT, pinned commits in `.agents/skills/README.md`). When to load them:

- `golang-security`, `golang-safety`, `golang-concurrency`, `golang-context`: **reviews** of security-sensitive code (proxies and groups, auth/RBAC/projects, upload and format handlers, anything serving upstream- or uploader-controlled bytes) and of workers, locks and goroutines.
- `golang-performance`, `golang-benchmark`: performance reviews and before optimizing hot paths (measure first with benchmarks/pprof).
- `golang-database`: writing or reviewing queries, transactions, row locks, advisory locks and migrations-adjacent Go code.
- `golang-observability`: metrics (label cardinality), slog logging, tracing.
- `golang-testing`: writing or reviewing tests; the repo uses the standard `testing` package (no testify) and the test rules in this file.
- `golang-troubleshooting`: debugging a concrete failure (flaky test, deadlock, leak, panic), not reviews.
- `vue-best-practices`, `vue-router-best-practices`, `vue-pinia-best-practices`: writing or reviewing `web/` components, route guards and stores (Composition API with `<script setup>`). They don't cover UI security: also check redirects (`next`), URL/shell-snippet interpolation and stale async responses yourself.

Skip them for routine implementation. Use their review mode only, not their sub-agent fan-out. This file wins on conflict: errors go through `writeError` (no `samber/oops`), DB access through sqlc/pgx, tests follow the rules above, and the repo's own conventions apply.

## CI and releases

`.github/workflows/ci.yml` runs on PRs, `main` and tags; treat every job as a required check: `go` (build, vet, `go mod tidy -diff`, govulncheck, race tests), `integration` (Postgres + Garage + testcontainers), `lint` (golangci-lint), `reuse`, `sqlc` (generate drift), `web` (typecheck, generated API client drift, tests, build, bundle size), `security` (Trivy image/fs/config gates on HIGH/CRITICAL, SARIF uploaded), `workflow-lint` (actionlint + zizmor, config in `.github/zizmor.yml`), `dependency-review` (PRs). `codeql.yml` (push, PRs, weekly), `scheduled-scan.yml` (weekly Trivy on the `latest` and newest stable image tags) and `scorecard.yml` run separately.

`release.yml` runs on `v*.*.*` tags: checks `VERSION` matches the tag, tests, then GoReleaser binaries (provenance attested) and a multi-arch image to `ghcr.io/openartifact/openartifact`. The image is Trivy-gated before push, carries `org.opencontainers.image.{title,description,vendor,authors,licenses,documentation}` labels/annotations, has SBOM and `provenance: mode=max` attached, is signed with cosign (keyless) and gets a build-provenance attestation. Verification commands: `docs/security/signing.md`.

## Documentation

Docs live in `docs/` (index `docs/README.md`); the README stays a short overview. A new feature, format, config key, CLI command or API area gets or updates a docs page in the same change; keep links relative and new `.md` files need the SPDX HTML comment header.

## Bootstrap

First start with an empty DB creates user `admin` with a random password printed to stderr (`OPENARTIFACT BOOTSTRAP` block); password change is forced on first login.
