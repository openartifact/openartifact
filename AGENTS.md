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
| `internal/format/<fmt>` | Native protocol handlers per package format (`/repos/{repo}/...`, `/v2/...`) |
| `internal/middleware` | RBAC, CSRF, audit, CORS, logging |
| `internal/auth` | Sessions, tokens, permission evaluation, OIDC/SAML, SSO provisioning, bootstrap |
| `internal/database` | `Database` interface (`interfaces.go`), Postgres impl (`postgres*.go`), sqlc code in `dbsqlc/`, queries in `queries/*.sql` |
| `internal/storage` | Blob stores: filesystem, S3, GCS, Azure, OCI; `SafeDeleteBlob` refcounting |
| `internal/worker` | Background workers: per-format metadata regeneration, cleanup, proxy cache, AI analysis |
| `internal/{backup,webhook,proxy,search,vuln,sbom,license,secrets,ai,mcp,audit,metrics,config}` | Feature subsystems |
| `internal/crypto` | AES-256-GCM secret encryption at rest (`OPENARTIFACT_SECRET_KEY`) |
| `pkg/*` | Format parsers/libraries (deb, rpm, npm, maven, helm, rubymarshal, gpg, delta) |
| `migrations/` | Embedded SQL migrations, applied at startup |
| `web/` | Vue 3 + TS + Tailwind SPA (bun), embedded via `web/embed.go` |
| `tests/` | Go integration/handler tests; `tests/integration/` uses testcontainers |
| `docker/` | Dockerfile, compose, `.env.example` |

## Commands

```sh
make build            # web + server binary → bin/
make build-cli        # bin/oa
make test             # go test -short -race ./...
make lint             # golangci-lint v2
make vet
make generate         # sqlc generate (after editing internal/database/queries/*.sql)
cd web && bun install && bun run typecheck && bun test && bun run build
cd web && bun run generate-api   # regenerate src/api/generated from internal/api/openapi.json
docker compose -f docker/docker-compose.yml up -d --build   # needs docker/.env (copy from .env.example)
```

DB/S3-backed tests skip unless `TEST_DATABASE_URL` / `TEST_S3_ENDPOINT` (+ `TEST_S3_BUCKET`, `TEST_S3_ACCESS_KEY`, `TEST_S3_SECRET_KEY`, `TEST_S3_REGION`) are set; with `CI=true` a missing variable fails instead of skipping. The S3 test backend is Garage (`dxflrs/garage`, region `garage`; MinIO no longer ships images) — see the `integration` job in `.github/workflows/ci.yml` for the bootstrap. Integration tests use `-tags=integration`; testcontainers under podman needs `DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true`.

Routing: every repo-scoped format handler registers relative routes on the single `/repos/{name}` subrouter in `cmd/server/main.go`. Never register an absolute `/repos/{name}/...` route on `authedAPI` — the mount shadows it (404) or it hijacks the subrouter. Shared catch-alls (`PUT /*`: npm publish + GOPROXY upload) are dispatched explicitly there.

Toolchain: Go 1.27.x, golangci-lint v2, bun. sqlc, govulncheck and actionlint are `tool` directives in `tools/go.mod` (separate module so Dependabot bumps them without bloating the main module): run `go tool -modfile=tools/go.mod sqlc generate` (`make generate`). CI actions are pinned by commit SHA and container images by `@sha256` digest; Dependabot updates both.

## Conventions and invariants

- **Migrations are forward-only.** Never edit an applied file (`001_initial_schema.sql`); the migrator aborts on checksum drift. Add `00N_<topic>.sql`.
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
- **Workers** taking Postgres advisory locks must hold them on a pinned connection (or use xact locks), not the `*sql.DB` pool.
- **Config:** every key settable by YAML must have an env override in `internal/config`, and be documented in `.env.example` and `openartifact.example.yaml`. Server read/write timeouts default to 0 (large uploads).
- Errors: use `writeError` with the shared codes; never leak `err.Error()` from DB/upstream to clients.
- Tests: assert behavior, not mock round-trips; no fixed sleeps — poll with deadlines.
- **Package listing:** `ListRepoComponents` (`queries/components.sql`) unions every per-format table. Adding a format or a package table means adding a branch there (and to `CountRepoComponents`), or its packages silently vanish from the UI.
- **Timestamps:** DB upserts must not persist zero `time.Time`; default `created_at`/`updated_at` to now when the caller leaves them unset.
- **Anonymous access:** `repositories.public` grants anonymous `read` (never write) independent of `allowAnonymousRead`; the evaluator in `internal/auth/permission*.go` is the single place that decides it.
- **Client auth:** format routes accept API tokens (Bearer, or Basic with the token as password); account passwords work only for the UI login. Use tokens in examples and smoke tests.

## Verifying changes

- Web: `bun run typecheck && bun test`, then look at the page in the running app (dark and light theme).
- Go: `make lint` (gocyclo limit 20 applies to tests too; prefer table-driven subtests) and `make test`.
- End to end: rebuild with `docker compose -f docker/docker-compose.yml up -d --build openartifact`, create a token via `POST /api/v1/auth/tokens` (`{"name":…,"kind":"access"}`), and publish with the real client (`npm publish`, `curl -T` for raw, …) before claiming a format works.

## Bootstrap

First start with an empty DB creates user `admin` with a random password printed to stderr (`OPENARTIFACT BOOTSTRAP` block); password change is forced on first login.
