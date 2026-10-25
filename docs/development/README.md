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
| sqlc, govulncheck, deadcode, actionlint | see `tools/go.mod` | `tool` directives in `tools/go.mod` |

`tools/` is a separate Go module so Dependabot can bump the tools without bloating the main module. Run them through the Go toolchain; nothing needs to be installed:

```sh
go tool -modfile=tools/go.mod sqlc generate     # = make generate
go tool -modfile=tools/go.mod govulncheck ./...
go tool -modfile=tools/go.mod deadcode -tags integration ./...   # = make deadcode, see below
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
| `make docs-gen` | Regenerates the pages under `docs/reference/` (configuration keys, metrics, CLI commands, REST API) from the code; run after changing config keys, metrics, `oa` help or `internal/api/openapi.json`, and commit the result. `make docs-check` fails when they are stale |
| `make deadcode` | Fails when Go code is unreachable from every `main` package see [Dead-code gate](#dead-code-gate) |
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

## Fuzzing

Parsers of untrusted input (uploaded archives and metadata, proxied upstream indexes, SBOM documents, headers) have native Go fuzz targets, `func FuzzX(f *testing.F)` in `fuzz_test.go` files next to the code. Under `make test` they run over their seed corpus only, so they double as regression tests.

Run one target locally with the Go fuzzer:

```sh
grep -rn --include='*_test.go' '^func Fuzz' .                     # list the targets
go test -run='^$' -fuzz='^FuzzParseTarball$' -fuzztime=60s ./pkg/npm
```

When the fuzzer finds a failing input it writes it to `testdata/fuzz/FuzzX/<hash>` of the package and the next plain `go test` fails on it. Fix the code, keep that file as a regression seed (commit it with the fix) and do not commit anything else the fuzzer generated. A target should assert invariants that must always hold (round trips, size caps, no path escaping the root), not only "does not panic", stay fast (a few ms per input) and bound memory itself: archives are expanded through the size-capped readers the production code uses.

[ClusterFuzzLite](https://google.github.io/clusterfuzzlite/) fuzzes the same targets in CI with libFuzzer and AddressSanitizer (the only sanitizer it supports for Go):

| Workflow | Trigger | Does |
|---|---|---|
| `clusterfuzzlite-pr.yml` | pull requests touching Go files, `go.mod`/`go.sum` or `.clusterfuzzlite/` | Builds all targets and fuzzes them for 10 minutes in `code-change` mode; a crash fails the check and is uploaded as an artifact, SARIF goes to code scanning (same-repository PRs) |
| `clusterfuzzlite-batch.yml` | weekly (Sun), manual | 1 hour of `batch` fuzzing of `main` that grows the corpus (kept in GitHub Actions artifacts), then corpus pruning |

The build is defined in [`.clusterfuzzlite/`](../../.clusterfuzzlite): `Dockerfile` (the OSS-Fuzz Go builder image, pinned by digest and bumped by Dependabot), `build.sh` and `project.yaml` (`language: go`). `build.sh` finds every `func FuzzX(f *testing.F)` in a non-build-tagged `*_test.go` file and compiles it with `compile_native_go_fuzzer_v2` into a binary named `<package path>_<FuzzX>`, so a new target needs no registration. The compiler needs each fuzz function to be defined in exactly one file of its package directory, and it compiles all test files of that directory into one package: `build.sh` therefore deletes the external-test-package files (`package x_test`) of those directories from the image's copy of the tree, so a fuzz target must not depend on them.

To build and run the exact CI binaries locally (docker or podman):

```sh
docker build -f .clusterfuzzlite/Dockerfile -t oa-cfl .
mkdir -p out && docker run --rm -e FUZZING_ENGINE=libfuzzer -e SANITIZER=address -e ARCHITECTURE=x86_64 \
  -e FUZZING_LANGUAGE=go -e PROJECT_NAME=openartifact -v "$PWD/out:/out" oa-cfl compile
mkdir -p corpus && ./out/pkg_npm_FuzzParseTarball corpus -max_total_time=60
```

A crash artifact from CI reproduces with `./out/<binary> <crash-file>`. Convert it into a regression seed by adding the input to the target's seed corpus (`f.Add`) or as a `testdata/fuzz/FuzzX/` file.

## Conventions

The full list lives in [AGENTS.md](../../AGENTS.md); the ones that most often matter:

- Migrations are forward-only (`migrations/00N_<topic>.sql`); never edit an applied file.
- Never hand-edit `internal/database/dbsqlc/`.
- Blobs are content-addressed by sha256; delete only via `storage.SafeDeleteBlob`.
- Every repo-scoped format handler registers relative routes on the single `/repos/{name}` subrouter ([architecture](architecture.md#routing-invariant)).
- API JSON is snake_case; keep `openapi.json` in sync with the registered routes.
- Every config key settable in YAML needs an env override in `internal/config` and entries in `.env.example` and `openartifact.example.yaml`.
- Every new file needs an SPDX header (the repository is REUSE-compliant).

## Dead-code gate

`make deadcode` (`scripts/deadcode-check.sh`, CI job `go`) runs [`deadcode`](https://pkg.go.dev/golang.org/x/tools/cmd/deadcode) (pinned in `tools/go.mod`) with `-tags integration` over every `main` package and fails on any function that none of them reaches. There is no allowlist: the expected result is zero findings. It deliberately runs **without** `-test`: with `-test` every test function is a root, so a production function that only tests call would count as live. Without it such functions are reported, so test-only helpers cannot hide in production files.

The report is reduced to `file  symbol` lines (sorted, line numbers dropped), so the output is deterministic.

### Test support packages

Helpers shared by tests of several packages cannot live in a `_test.go` file. Put them in a package whose directory name ends in `test`, following the standard library (`httptest`, `iotest`): `internal/database/databasetest`, `internal/format/formattest`, `internal/gpgkeys/gpgkeystest`, `internal/licensemeta/archivetest`, `internal/licensepolicy/policytest`. That naming is the only exclusion: the script skips a package of this module whose directory name ends in `test` and that production code does not import (`internal/attest` ends in `test` too, but production imports it, so it is checked). Production code must never import a `*test` package.

### Reading a failure

The failure lists `file  Symbol` lines (`Type.Method` for methods). Handle each one:

1. **Dead everywhere**: delete it, together with its tests.
2. **Only tests use it**: move it into a `_test.go` file of the same package, or into a `*test` package when several packages share it. If a test only needs to wait for or observe background work, poll the observable outcome with a deadline instead of exporting a hook, or use a production lifecycle API that already exists (for example `WithTasks(bgtask.Runner)` and `Shutdown`).
3. **Reached only through reflection, templates or `go:linkname`**: make the call visible to the analysis, or give the code a real production caller; there is no allowlist.

To see why a function is live or dead run `go tool -modfile=tools/go.mod deadcode -tags integration -whylive=<pkg>.<Func> ./...`.

## CI

Workflows live in [`.github/workflows`](../../.github/workflows). All actions are pinned by commit SHA and all container images by `@sha256` digest; Dependabot ([`.github/dependabot.yml`](../../.github/dependabot.yml)) bumps them weekly (7-day cooldown, minor/patch grouped) for GitHub Actions, Go modules (`/` and `/tools`), web dependencies (bun, `/web`), Dockerfile and compose images (the `dhi.io` base needs the `DHI_USERNAME`/`DHI_TOKEN` Dependabot secrets). Image builds in CI and release log in to `dhi.io` with the `DHI_USERNAME`/`DHI_TOKEN` secrets; fork PRs, which cannot read secrets, build with `RUNTIME=distroless`. Not covered by Dependabot: `GO_VERSION`, `BUN_VERSION`, `GOLANGCI_LINT_VERSION`, `TRIVY_VERSION`, and the Garage/Postgres images in the `integration` job.

### `ci.yml` (push to `main`, tags `v*`, pull requests)

| Job | Checks |
|---|---|
| `go` | `go build`, `go vet`, `go mod tidy -diff` (root and `tools/`), `govulncheck ./...`, [dead-code gate](#dead-code-gate), `go test -race ./...` |
| `integration` | Postgres + Garage service; `go test -race -tags integration ./tests/...` |
| `lint` | golangci-lint with `.golangci.yaml` (includes gofmt/goimports) |
| `reuse` | REUSE license compliance |
| `sqlc` | `sqlc generate` leaves no diff in `internal/database/dbsqlc` |
| `web` | `bun install --frozen-lockfile`, typecheck, generated API client up to date, `bun test`, build, bundle-size check |
| `security` | Builds the image, then Trivy: image, filesystem (Go and bun lockfiles, secrets) and config (Dockerfile, compose) scans. Each runs twice: a SARIF upload to code scanning, then a gate that fails on fixable HIGH/CRITICAL findings (config gate: any HIGH/CRITICAL). Fork and Dependabot PRs skip the upload but keep the gate. Ignores in `.trivyignore.yaml` |
| `docs` | `go run ./cmd/docgen -check` (generated reference up to date; `.env.example` and `openartifact.example.yaml` list every config key), then builds the docs site from this checkout with the VitePress project of [openartifact.github.io](https://github.com/openartifact/openartifact.github.io); a dead internal link fails the build |
| `workflow-lint` | `actionlint` and zizmor (rules in [`.github/zizmor.yml`](../../.github/zizmor.yml)) |
| `dependency-review` | PRs only; fails on high-severity dependency changes |

### Other security workflows

| Workflow | Trigger | Does |
|---|---|---|
| `codeql.yml` | push to `main`, PRs, weekly (Mon) | CodeQL for Go and JavaScript/TypeScript |
| `scorecard.yml` | push to `main`, branch-protection changes, weekly (Tue) | OpenSSF Scorecard, results published and uploaded as SARIF |
| `clusterfuzzlite-pr.yml`, `clusterfuzzlite-batch.yml` | PRs touching Go, weekly (Sun) | ClusterFuzzLite fuzzing of the Go fuzz targets, see [Fuzzing](#fuzzing) |
| `scheduled-scan.yml` | weekly (Mon), manual | Trivy scan of the *published* `latest` and latest stable semver images on ghcr.io, so new CVEs in released images are noticed |

### Documentation site

The site at <https://openartifact.github.io/> is built by the separate repository [openartifact.github.io](https://github.com/openartifact/openartifact.github.io), which renders this repository's `docs/` in place (README.md is a directory index; links to files outside `docs/` become GitHub links): `/latest/` is `main`, `/vX.Y/` the newest stable tag of each of the last five minor lines. The text, generated reference and the drift and link checks stay here, versioned with the code. `docs-site-trigger.yml` (on pushes to `main` that touch `docs/` or `web/src/lib/installCommands.ts`, and on `v*.*.*` tags) force-pushes one trigger commit to the site repository's `rebuild` branch with a write deploy key (secret `SITE_DEPLOY_KEY`; the job is skipped without it), and the site's Pages workflow builds and deploys on that push, so a docs change is live a few minutes after the merge. The site also polls every 15 minutes and rebuilds when `docs/` changed or a tag appeared, which covers a missing key or a failed trigger. To publish immediately by hand: `gh workflow run pages.yml -R openartifact/openartifact.github.io`.

### Releases (`release.yml`)

Triggered by a `vX.Y.Z[-pre]` tag, or manually with an existing tag. Bump the top-level `VERSION` file first.

1. **verify**: the `VERSION` file must equal the tag (without `v`); `go vet` and `go test -race`. A tag with a `-suffix` is a prerelease and does not get the `latest` image tag.
2. **binaries**: GoReleaser (`.goreleaser.yml`) builds the UI and publishes a GitHub release with `openartifact` + `resetpw` (linux amd64/arm64, tar.gz), `oa` (linux/darwin/windows, amd64/arm64) and `SHA256SUMS`. Archives and checksums get build-provenance attestations.
3. **image**: builds `linux/amd64` locally and runs the Trivy gate before anything is pushed; then pushes the multi-arch (`linux/amd64`, `linux/arm64`) image to `ghcr.io/openartifact/openartifact` tagged `X.Y.Z`, `X.Y` and (stable only) `latest`, with OCI labels and annotations, a buildkit SBOM and `mode=max` provenance. The pushed digest is signed with cosign (keyless) and gets a build-provenance attestation stored in the registry.

All release caches are disabled so the tag-triggered workflow never restores a cache written by another run. To verify signatures and attestations, see [signing](../security/signing.md).

Performance work: [performance](../operations/performance.md).
