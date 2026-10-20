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

Toolchain: Go 1.27.x, sqlc v1.31.1, golangci-lint v2, bun.

## Conventions and invariants

- **Migrations are forward-only.** Never edit an applied file (`001_initial_schema.sql`); the migrator aborts on checksum drift. Add `00N_<topic>.sql`.
- **sqlc:** edit `queries/*.sql`, run `make generate`; never hand-edit `dbsqlc/`.
- **Blobs are content-addressed by sha256.** Every `BlobStore.Put` must pass `{"sha256": <hex>}` metadata (S3/Azure/OCI require it). Blob IDs must be 64 lowercase hex — never pass user input or `blobs.id` UUIDs as storage keys. Delete blobs only via `storage.SafeDeleteBlob` (blobs are deduplicated across repos).
- **Repo scoping:** any handler that loads an entity by ID must check it belongs to the repo in the URL.
- **AuthZ:** RBAC in `middleware/rbac.go` is the gate. `/api/v1/*` maps to action `manage`; `/repos/{name}/...` to read/write by method. SPA page bypass is GET/HEAD only. Format handlers must use the authenticated principal from context, never just the presence of an `Authorization` header.
- **Secrets** (webhook secrets, upstream passwords, SSO client secrets, AI keys, storage creds) are encrypted via `internal/crypto` and never returned in API responses.
- **Outbound URLs** (webhooks, proxy upstreams) must pass SSRF validation (no loopback/private/link-local, no unchecked redirects).
- **API JSON is snake_case.** Response models need explicit `json` tags; the frontend relies on them. Keep `internal/api/openapi.json` in sync with registered routes, then regenerate web types.
- **Frontend:** all API calls go through `web/src/api/client.ts` `request()` (handles CSRF header `X-CSRF-Token` from `oa_csrf` cookie and 401). No raw `fetch` for mutations. Strings go through `t()` / `web/src/i18n/en.ts`. Format icons come from `simple-icons` via `web/src/lib/formats.ts`; display formats with `FormatBadge`.
- **Workers** taking Postgres advisory locks must hold them on a pinned connection (or use xact locks), not the `*sql.DB` pool.
- **Config:** every key settable by YAML must have an env override in `internal/config`, and be documented in `.env.example` and `openartifact.example.yaml`. Server read/write timeouts default to 0 (large uploads).
- Errors: use `writeError` with the shared codes; never leak `err.Error()` from DB/upstream to clients.
- Tests: assert behavior, not mock round-trips; no fixed sleeps — poll with deadlines.

## Bootstrap

First start with an empty DB creates user `admin` with a random password printed to stderr (`OPENARTIFACT BOOTSTRAP` block); password change is forced on first login.
