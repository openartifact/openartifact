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
| `internal/{group,repotype}` | Group member resolution + Maven metadata / npm packument / cargo index / Helm `index.yaml` / go `@v/list` / NuGet registration merge (`pkg/helm/index.go` parses, rewrites and merges indexes); `repotype` is the single policy of which formats may be proxy (maven, deb, rpm, docker, npm, pypi, cargo, go, helm, nuget, raw) or group (maven, deb, rpm, docker, npm, pypi, cargo, go, helm, nuget, raw; deb/rpm groups serve generated merged metadata, `internal/worker/group_metadata*.go`); `internal/pypisimple` is the PEP 503/691 parser/renderer behind the pypi proxy and group |
| `internal/ocipath` | Parses `/v2/<name>/...` into OpenArtifact repo + image; shared by RBAC and the docker handler |
| `internal/clientsetup` | Go port of `web/src/lib/installCommands.ts` (client snippets, used by MCP `client_setup`); keep both in sync (`testdata/web_parity.json` is checked by a Go test and `web/src/lib/__tests__/installCommands.parity.test.ts`; regenerate with `UPDATE_PARITY=1 go test ./internal/clientsetup -run Parity`; `internal/repotype` has the same guard for `web/src/lib/repoTypes.ts`) |
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
| `docker/` | Dockerfile (shell-less runtime: `RUNTIME=dhi` default, needs dhi.io login, or `distroless`; `-healthcheck` flag, no RUN in the final stage; optional `SCANNERS` build arg (none, trivy, grype or all) copies digest-pinned Trivy/Grype binaries in, default none; bumping a pinned scanner version also means `scripts/update-scanner-manifest.sh`, which refreshes the runtime-install checksums in `internal/vuln/scanners_manifest.json`), compose, `.env.example` (compose credentials; the full env reference is the root `.env.example`) |
| `bench/` | Benchmark harness: `bench/run.sh` + `oabench` load generator (`make bench-load`) |
| `tools/` | Separate Go module (`tools/go.mod`) pinning sqlc, govulncheck, deadcode, actionlint |
| `cmd/docgen` | Generator of `docs/reference/*` (config keys, metrics, `oa` CLI, REST API); `make docs-gen`, drift checked in CI (`make docs-check`) |
| `docs/` | User and developer documentation (index: `docs/README.md`); assets in `docs/assets/`; `docs/reference/` is generated, never hand-edited. Published at <https://openartifact.github.io/> by the separate openartifact.github.io repo (VitePress, versioned) |

## Commands

```sh
make build            # web + server binary → bin/
make build-cli        # bin/oa
make test             # go test -short -race ./...
make test-integration # -tags=integration tests (Postgres/S3 env or a container runtime)
make lint             # golangci-lint v2
make vet
make deadcode         # unreachable-code gate (scripts/deadcode-check.sh); docs/development/README.md#dead-code-gate
make generate         # go tool -modfile=tools/go.mod sqlc generate (after editing internal/database/queries/*.sql)
make bench            # Go micro-benchmarks (-benchmem)
make bench-load       # bench/run.sh: seed a running instance and run HTTP load scenarios (OA_URL, OA_USER, OA_PASS, OA_PID)
cd web && bun install && bun run typecheck && bun test && bun run build
cd web && bun run generate-api   # regenerate src/api/generated from internal/api/openapi.json
make docker-up   # (OPENARTIFACT_RUNTIME=distroless without dhi.io access) compose up -d --build with VERSION/commit/build time stamped; needs docker/.env (copy from .env.example)
scripts/dev-token.sh   # mint an access token for a running instance (OA_URL/OA_USER/OA_PASS, --change-password + OA_NEW_PASS for a fresh bootstrap admin)
```

DB/S3-backed tests (behind `-tags=integration`) skip unless `TEST_DATABASE_URL` / `TEST_S3_ENDPOINT` (+ `TEST_S3_BUCKET`, `TEST_S3_ACCESS_KEY`, `TEST_S3_SECRET_KEY`, `TEST_S3_REGION`) are set; with any non-empty `CI` a missing variable fails instead of skipping. The S3 test backend is Garage (`dxflrs/garage`, region `garage`; MinIO no longer ships images) — see the `integration` job in `.github/workflows/ci.yml` for the bootstrap. `make test-integration` runs them; testcontainers under podman needs `DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true`.

Routing: every repo-scoped format handler registers relative routes on the single `/repos/{name}` subrouter in `cmd/server/main.go`. Never register an absolute `/repos/{name}/...` route on `authedAPI` — the mount shadows it (404) or it hijacks the subrouter. Shared catch-alls (`PUT /*`: npm publish + GOPROXY upload) are dispatched explicitly there.

Toolchain: Go 1.27.x, golangci-lint v2, bun. sqlc, govulncheck, deadcode and actionlint are `tool` directives in `tools/go.mod` (separate module so Dependabot bumps them without bloating the main module): run them as `go tool -modfile=tools/go.mod <sqlc|govulncheck|deadcode|actionlint> …` (`make generate` runs sqlc, `make deadcode` the dead-code gate). CI actions are pinned by commit SHA and container images by `@sha256` digest; Dependabot updates both.

## Conventions and invariants

- **Migrations are forward-only; sqlc output is generated.** Add `00N_<topic>.sql` (large-table index builds go in `.no_tx.sql`), edit `queries/*.sql` and run `make generate`, never hand-edit `dbsqlc/`. Details: `.agents/rules/migrations-sqlc.md`.
- **Blobs are content-addressed by sha256.** Every `BlobStore.Put` must pass `{"sha256": <hex>}` metadata (S3/Azure/OCI require it). Blob IDs must be 64 lowercase hex — never pass user input or `blobs.id` UUIDs as storage keys. Delete blobs only via `storage.SafeDeleteBlob` (blobs are deduplicated across repos).
- **Repo scoping:** any handler that loads an entity by ID must check it belongs to the repo in the URL.
- **AuthZ:** RBAC in `middleware/rbac.go` is the gate. `/api/v1/*` maps to action `manage`; `/repos/{name}/...` to read/write by method. `/v2/<name>/...` (OCI) is parsed by `internal/ocipath` for both RBAC and the docker handler: the first segment of `<name>` is the OpenArtifact repo, the rest is the image (`/v2/repo/org/team/app/manifests/1` → repo `repo`, image `org/team/app`); tags/manifests are scoped per image, blobs per repo. SPA page bypass is GET/HEAD only. Format handlers must use the authenticated principal from context, never just the presence of an `Authorization` header.
- **Secrets** (webhook secrets, upstream passwords, SSO client secrets, AI keys, storage creds) are encrypted via `internal/crypto` and never returned in API responses.
- **Outbound URLs** (webhooks, proxy upstreams) must pass SSRF validation (no loopback/private/link-local, no unchecked redirects).
- **API JSON is snake_case.** Response models need explicit `json` tags; the frontend relies on them. Keep `internal/api/openapi.json` in sync with registered routes, then regenerate web types.
- **Frontend (`web/`):** API calls only through `web/src/api/client.ts` `request()`, strings through `t()`, shared helpers for dates/colours/icons/install snippets. Details: `.agents/rules/frontend.md`.
- **Proxy/group repos:** `internal/repotype` is the single policy of which formats may be proxy or group; `internal/repodispatch` serves them. Details: `.agents/rules/proxy-group.md`.
- **Projects, auth cache, anonymous access:** project roles are part of authorization and never grant `manage`; auth lookups are cached and every write must invalidate. Details: `.agents/rules/projects-rbac.md`.
- **OpenTelemetry** (`internal/telemetry`, off unless `telemetry.otlp.enabled`): span names are chi route templates, never raw paths; attributes never carry URLs beyond host/scheme, headers, SQL text/arguments or error messages (the pgx tracer names spans after the sqlc statement). Instrument a new outbound client with `telemetry.WrapClient`, never `otelhttp` (its `url.full` keeps query strings). `api.HTTPServer` middleware runs outside chi, so the route pattern is only readable because `telemetry.HTTPMiddleware` seeds the route context.
- **pprof:** `OPENARTIFACT_PPROF_ENABLED` (off by default) mounts admin-only `/api/v1/admin/pprof/*`; never enable it by default or outside admin auth.
- **Workers:** advisory locks on a pinned connection; metadata regeneration only through `RegenQueue`. Details: `.agents/rules/workers.md`.
- **Disaster-recovery journal:** a new package table needs a journal entry in `internal/recovery/tables.go` and a trigger migration; sidecars never carry secrets; `openartifact rebuild` is merge-only. Details: `.agents/rules/recovery-journal.md`.
- **Package listing:** `ListRepoComponents` is two-stage and unions every per-format table; a new package table needs a branch. Details: `.agents/rules/package-listing.md`.
- **HTTP caching:** metadata endpoints answer ETag/304; `public` only for anonymous reads of public repos. Details: `.agents/rules/http-caching.md`.
- **Audit list:** no `COUNT(*)`; keyset `cursor` + `has_more`, offset `total` is an estimate when large (`total_exact`).
- **Config:** every key settable by YAML must have an env override in `internal/config`, and be documented in `.env.example` and `openartifact.example.yaml`. Server read/write timeouts default to 0 (large uploads).
- Errors: use `writeError` with the shared codes; never leak `err.Error()` from DB/upstream to clients.
- Tests: assert behavior, not mock round-trips; no fixed sleeps — poll with deadlines.
- **No dead code:** `make deadcode` (CI `go` job) must report nothing, and there is no allowlist. Code only tests call goes in a `_test.go` file or a `*test` support package (`databasetest`, `formattest`, ...), never in a production file; don't export hooks for tests. Details: `docs/development/README.md#dead-code-gate`.
- **Timestamps:** DB upserts must not persist zero `time.Time`; default `created_at`/`updated_at` to now when the caller leaves them unset.
- **Client auth:** format routes accept API tokens (Bearer, or Basic with the token as password); account passwords work only for the UI login. Use tokens in examples and smoke tests.

## Verifying changes

- Web: `bun run typecheck && bun test`, then look at the page in the running app (dark and light theme).
- Go: `make lint` (gocyclo limit 20 applies to tests too; prefer table-driven subtests) and `make test`.
- Client round trips: with a running instance, `OA_TOKEN=$(OA_PASS=… scripts/dev-token.sh URL) scripts/smoke-formats.sh URL` publishes a tiny package per format (deb, rpm, npm, pypi; `FORMATS="deb rpm"` to pick) and installs it with the real client (apt, dnf, npm, pip) in throwaway host-network containers; it deletes its repositories afterwards.
- End to end, isolated (`.agents/commands/smoke.md`): start a separate compose project so several worktrees can run side by side: `OPENARTIFACT_RUNTIME=distroless OPENARTIFACT_PORT=18080 docker compose -p oa-<topic> -f docker/docker-compose.yml --env-file docker/.env up -d --build` (containers have no fixed names; `-p` isolates network and volumes), read the bootstrap password from `docker compose -p oa-<topic> -f docker/docker-compose.yml logs openartifact`, mint a token with `OA_PASS=… scripts/dev-token.sh --change-password http://127.0.0.1:18080` (logs in, handles the forced password change via `OA_NEW_PASS`, `POST /api/v1/auth/tokens` with the session cookie and `X-CSRF-Token`, prints only the token; tokens cannot mint tokens, see `docs/integrations/api.md`), and publish with the real client (`npm publish`, `curl -T` for raw, …) before claiming a format works. Tear down with `docker compose -p oa-<topic> -f docker/docker-compose.yml down -v`.

## Review skills

Which vendored skill to load for which kind of work (security, concurrency, database, performance, testing, observability, Vue): `.agents/rules/review-skills.md`. Skip them for routine implementation.

## Agent workflow

Area-specific rules live in `.agents/rules/*.md` (glob-scoped; each is pointed to from the matching invariant above). Slash commands in `.agents/commands/`: `/pr <topic>` (worktree → implement → checks → PR → watch CI), `/review <PR|branch|range>` (area-split read-only review), `/smoke <topic>` (isolated compose stack end to end), `/ci-fix <PR>` (fix failing checks). Never push `main`; work in a worktree `../openartifact-<topic>` from `origin/main`. `web/dist` is untracked build output (only `web/dist/.gitkeep` is tracked so `go build` works without bun); never commit it.

## CI and releases

`.github/workflows/ci.yml` runs on PRs, `main` and tags; treat every job as a required check: `go` (build, vet, `go mod tidy -diff`, govulncheck, deadcode gate, race tests), `integration` (Postgres + Garage + testcontainers), `lint` (golangci-lint), `reuse`, `sqlc` (generate drift), `web` (typecheck, generated API client drift, tests, build, bundle size), `security` (Trivy image/fs/config gates on HIGH/CRITICAL, SARIF uploaded), `docs` (generated reference drift, docs site build and dead-link check), `workflow-lint` (actionlint + zizmor, config in `.github/zizmor.yml`), `dependency-review` (PRs). `codeql.yml` (push, PRs, weekly), `scheduled-scan.yml` (weekly Trivy on the `latest` and newest stable image tags) and `scorecard.yml` run separately.

`release.yml` runs on `v*.*.*` tags: checks `VERSION` matches the tag, tests, then GoReleaser binaries (provenance attested) and a multi-arch image to `ghcr.io/openartifact/openartifact`. The image is Trivy-gated before push, carries `org.opencontainers.image.{title,description,vendor,authors,licenses,documentation}` labels/annotations, has SBOM and `provenance: mode=max` attached, is signed with cosign (keyless) and gets a build-provenance attestation. Verification commands: `docs/security/signing.md`.

## Documentation

Docs live in `docs/` (index `docs/README.md`); the README stays a short overview. A new feature, format, config key, CLI command or API area gets or updates a docs page in the same change; keep links relative and new `.md` files need the SPDX HTML comment header. `docs/reference/` is generated by `cmd/docgen`: after changing config keys, metrics, `oa` help or `internal/api/openapi.json` run `make docs-gen` and commit it (the `docs` CI job fails on drift, on a config key missing from `.env.example`/`openartifact.example.yaml`, and on dead internal links). The site renders `docs/` as plain markdown: no `{{ }}` or bare `<tags>` outside code spans.

## Bootstrap

First start with an empty DB creates user `admin`. Default: a random password printed to stderr (`OPENARTIFACT BOOTSTRAP` block), change forced on first login, rejected at login after `bootstrap.generatedPasswordTtl` (24h; column `auth_users.password_expires_at`, cleared by any password change and `cmd/resetpw`). `bootstrap.adminPasswordFile`/`adminPassword` and `bootstrap.adminTokenFile` supply the password and an access token named `bootstrap` instead (nothing printed); all of it is created in the `CreateBootstrapAdmin` transaction and ignored on a non-empty instance.
