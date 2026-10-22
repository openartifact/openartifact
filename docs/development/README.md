<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Development

How to build, test and release OpenArtifact, and what CI enforces. For the code layout see [architecture](architecture.md); for contribution rules see [CONTRIBUTING.md](../../CONTRIBUTING.md) and [AGENTS.md](../../AGENTS.md) (conventions and invariants for humans and coding agents alike).

```sh
make test              # unit tests (race detector)
make test-integration  # integration tests (testcontainers; needs a container runtime)
make lint              # golangci-lint v2
make generate          # sqlc code generation
cd web && bun install && bun run dev
```

## Toolchain

| Tool | Version | Where it is pinned |
|---|---|---|
| Go | 1.27.x | `go.mod` (`go 1.27.0`); CI uses `GO_VERSION` (`1.27.3`) in `.github/workflows/*.yml` |
| bun | 1.4.4 | `BUN_VERSION` in `ci.yml` / `release.yml`; used for the web UI and its tests |
| golangci-lint | v2.15.0 | `GOLANGCI_LINT_VERSION` in `ci.yml`; config in `.golangci.yaml` |
| Trivy | v0.76.1 | `TRIVY_VERSION` in `ci.yml`, `release.yml`, `scheduled-scan.yml` (binary installed by `trivy-action`, which defaults to an older release) |
| PostgreSQL | 18 | CI integration job (`postgres:18-alpine`); the `tests/integration` harness starts `postgres:16-alpine` itself |
| sqlc, govulncheck, actionlint | see `tools/go.mod` | `tool` directives in `tools/go.mod` |

`tools/` is a separate Go module so Dependabot can bump the tools without bloating the main module. Run them through the Go toolchain; nothing needs to be installed:

```sh
go tool -modfile=tools/go.mod sqlc generate     # = make generate
go tool -modfile=tools/go.mod govulncheck ./...
go tool -modfile=tools/go.mod actionlint
```

bun, golangci-lint and Trivy versions are not tracked by Dependabot; bump them by hand in the workflows.

## Commands

| Command | Does |
|---|---|
| `make build` | Builds the web UI (and checks bundle size), then `bin/openartifact` |
| `make build-cli` | Builds the CLI to `bin/oa` |
| `make run` | `make build`, then runs the server |
| `make test` | `go test -short -race -timeout 5m ./...` |
| `make test-integration` | `go test -tags=integration -race -count=1 ./tests/...` (10 min timeout) |
| `make test-coverage` | Tests with `coverage.html` report |
| `make lint` | `golangci-lint run ./...` (needs golangci-lint v2 on `PATH`) |
| `make vet` | `go vet ./...` |
| `make check` | `fmt` + `vet` + `lint`; run before opening a PR |
| `make generate` | sqlc generate; run after editing `internal/database/queries/*.sql` or the schema, and commit `internal/database/dbsqlc/` |
| `make migrate` | `go run ./cmd/server -migrate` (apply migrations and exit) |
| `make bench`, `make bench-load` | Benchmarks, see [performance](../operations/performance.md) |
| `make docker-build` | Builds `openartifact:latest` from `docker/Dockerfile`; the runtime base is `RUNTIME=dhi` (default, needs `docker login dhi.io`) or `RUNTIME=distroless` (anonymous) |
| `make docker-up` | Full stack via `docker compose -f docker/docker-compose.yml up -d --build` with `VERSION`, short SHA and RFC 3339 build time passed as build args; needs `docker/.env` copied from [`docker/.env.example`](../../docker/.env.example); without dhi.io access set `OPENARTIFACT_RUNTIME=distroless` |

Web UI (run in `web/`):

```sh
bun install
bun run dev            # Vite dev server on :5173, proxies /api to localhost:8080
bun run typecheck      # vue-tsc --noEmit
bun test
bun run build          # → web/dist, embedded into the server binary
bun run generate-api   # regenerate src/api/generated from internal/api/openapi.json
```

Start the server on `:8080` for the dev server to talk to. Build the server with `-tags dev` and set `OPENARTIFACT_DEV=1` to make it proxy UI requests to `http://localhost:5173` instead of serving the embedded build (release binaries ignore the variable).

If you change the HTTP API, update `internal/api/openapi.json` and run `bun run generate-api`; CI fails when `web/src/api/generated` drifts.

## Testing

Unit tests need nothing external. Tests that need Postgres or S3 skip unless their variables are set:

| Variable | Used for |
|---|---|
| `TEST_DATABASE_URL` | Postgres DSN for DB-backed tests (e.g. `postgres://USER:PASS@localhost:5432/openartifact_test?sslmode=disable`) |
| `TEST_S3_ENDPOINT` | S3-compatible endpoint; unset skips the S3 tests |
| `TEST_S3_BUCKET`, `TEST_S3_ACCESS_KEY`, `TEST_S3_SECRET_KEY`, `TEST_S3_REGION` | Bucket and credentials (region `garage` for the CI backend) |

- With `CI` set (GitHub Actions sets it), a missing variable makes the test fail instead of skipping, so a misconfigured pipeline cannot silently skip integration tests.
- DB and S3 tests, and the migration runner tests, are behind the `integration` build tag: run them with `-tags=integration` (`make test-integration`).
- The S3 test backend is [Garage](https://garagehq.deuxfleurs.fr/) (`dxflrs/garage`); MinIO no longer ships images. The `integration` job in [`ci.yml`](../../.github/workflows/ci.yml) shows how to bootstrap a single-node Garage (layout, bucket, imported key).
- `tests/integration/` starts its own Postgres through testcontainers and needs a container runtime. Under rootless podman:

```sh
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
export TESTCONTAINERS_RYUK_DISABLED=true
make test-integration
```

Write tests that assert behavior, not mock round-trips; poll with deadlines instead of fixed sleeps. gocyclo limit 20 applies to tests too, so prefer table-driven subtests.

## Conventions

The full list lives in [AGENTS.md](../../AGENTS.md); the ones that most often matter:

- Migrations are forward-only (`migrations/00N_<topic>.sql`); never edit an applied file.
- Never hand-edit `internal/database/dbsqlc/`.
- Blobs are content-addressed by sha256; delete only via `storage.SafeDeleteBlob`.
- Every repo-scoped format handler registers relative routes on the single `/repos/{name}` subrouter ([architecture](architecture.md#routing-invariant)).
- API JSON is snake_case; keep `openapi.json` in sync with the registered routes.
- Every config key settable in YAML needs an env override in `internal/config` and entries in `.env.example` and `openartifact.example.yaml`.
- Every new file needs an SPDX header (the repository is REUSE-compliant).

## CI

Workflows live in [`.github/workflows`](../../.github/workflows). All actions are pinned by commit SHA and all container images by `@sha256` digest; Dependabot ([`.github/dependabot.yml`](../../.github/dependabot.yml)) bumps them weekly (7-day cooldown, minor/patch grouped) for GitHub Actions, Go modules (`/` and `/tools`), web dependencies (bun, `/web`), Dockerfile and compose images (the `dhi.io` base needs the `DHI_USERNAME`/`DHI_TOKEN` Dependabot secrets). Image builds in CI and release log in to `dhi.io` with the `DHI_USERNAME`/`DHI_TOKEN` secrets; fork PRs, which cannot read secrets, build with `RUNTIME=distroless`. Not covered by Dependabot: `GO_VERSION`, `BUN_VERSION`, `GOLANGCI_LINT_VERSION`, `TRIVY_VERSION`, and the Garage/Postgres images in the `integration` job.

### `ci.yml` (push to `main`, tags `v*`, pull requests)

| Job | Checks |
|---|---|
| `go` | `go build`, `go vet`, `go mod tidy -diff` (root and `tools/`), `govulncheck ./...`, `go test -race ./...` |
| `integration` | Postgres + Garage service; `go test -race -tags integration ./tests/...` |
| `lint` | golangci-lint with `.golangci.yaml` (includes gofmt/goimports) |
| `reuse` | REUSE license compliance |
| `sqlc` | `sqlc generate` leaves no diff in `internal/database/dbsqlc` |
| `web` | `bun install --frozen-lockfile`, typecheck, generated API client up to date, `bun test`, build, bundle-size check |
| `security` | Builds the image, then Trivy: image, filesystem (Go and bun lockfiles, secrets) and config (Dockerfile, compose) scans. Each runs twice: a SARIF upload to code scanning, then a gate that fails on fixable HIGH/CRITICAL findings (config gate: any HIGH/CRITICAL). Fork and Dependabot PRs skip the upload but keep the gate. Ignores in `.trivyignore.yaml` |
| `workflow-lint` | `actionlint` and zizmor (rules in [`.github/zizmor.yml`](../../.github/zizmor.yml)) |
| `dependency-review` | PRs only; fails on high-severity dependency changes |

### Other security workflows

| Workflow | Trigger | Does |
|---|---|---|
| `codeql.yml` | push to `main`, PRs, weekly (Mon) | CodeQL for Go and JavaScript/TypeScript |
| `scorecard.yml` | push to `main`, branch-protection changes, weekly (Tue) | OpenSSF Scorecard, results published and uploaded as SARIF |
| `scheduled-scan.yml` | weekly (Mon), manual | Trivy scan of the *published* `latest` and latest stable semver images on ghcr.io, so new CVEs in released images are noticed |

### Releases (`release.yml`)

Triggered by a `vX.Y.Z[-pre]` tag, or manually with an existing tag. Bump the top-level `VERSION` file first.

1. **verify**: the `VERSION` file must equal the tag (without `v`); `go vet` and `go test -race`. A tag with a `-suffix` is a prerelease and does not get the `latest` image tag.
2. **binaries**: GoReleaser (`.goreleaser.yml`) builds the UI and publishes a GitHub release with `openartifact` + `resetpw` (linux amd64/arm64, tar.gz), `oa` (linux/darwin/windows, amd64/arm64) and `SHA256SUMS`. Archives and checksums get build-provenance attestations.
3. **image**: builds `linux/amd64` locally and runs the Trivy gate before anything is pushed; then pushes the multi-arch (`linux/amd64`, `linux/arm64`) image to `ghcr.io/openartifact/openartifact` tagged `X.Y.Z`, `X.Y` and (stable only) `latest`, with OCI labels and annotations, a buildkit SBOM and `mode=max` provenance. The pushed digest is signed with cosign (keyless) and gets a build-provenance attestation stored in the registry.

All release caches are disabled so the tag-triggered workflow never restores a cache written by another run. To verify signatures and attestations, see [signing](../security/signing.md).

Performance work: [performance](../operations/performance.md).
