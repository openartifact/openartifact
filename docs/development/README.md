<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Development

This page is the overview for contributors: the toolchain, the everyday commands, web UI development, build options and the conventions that CI enforces. Start here, then follow the links to the guide you need.

| You want to | Read |
|---|---|
| Understand where code lives and how a request flows | [architecture](architecture.md) |
| Add a package format | [adding a format](adding-a-format.md) |
| Run or write black-box suites (e2e, API fuzzing, property tests) | [testing](testing.md) |
| Write or reproduce a fuzz target | [fuzzing](fuzzing.md) |
| Understand CI jobs, coverage, the client matrix, docs site and releases | [CI and releases](ci-and-releases.md) |
| Add or fix a UI translation | [translations](translations.md) |
| Read the design records behind the format plugins and federation | [format plugins](../design/format-plugins.md), [federation](../design/federation.md) |

Contribution rules are in [CONTRIBUTING.md](../../CONTRIBUTING.md) and [AGENTS.md](../../AGENTS.md), which holds the conventions and invariants for humans and coding agents alike.

```sh
make test              # unit tests (race detector)
make test-integration  # integration tests (testcontainers; needs a container runtime)
make lint              # golangci-lint v2 (linters + formatter check)
make fmt               # golangci-lint fmt: gofmt, goimports, golines (140 columns)
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
| PostgreSQL | 18 | CI integration jobs and the compose file (`postgres:18-alpine`) |
| sqlc, govulncheck, deadcode, actionlint | see `tools/go.mod` | `tool` directives in `tools/go.mod` |

`tools/` is a separate Go module so Dependabot can bump the tools without bloating the main module. Run them through the Go toolchain; nothing needs to be installed:

```sh
go tool -modfile=tools/go.mod sqlc generate     # = make generate
go tool -modfile=tools/go.mod govulncheck ./...
go tool -modfile=tools/go.mod deadcode -tags integration ./...   # = make deadcode, see below
go tool -modfile=tools/go.mod actionlint
```

bun, golangci-lint and Trivy versions are not tracked by Dependabot; bump them by hand in the workflows.

The Go harness in `tests/integration` starts its own database with testcontainers and hard-codes `postgres:16-alpine` (`harness_helpers_test.go`). It is not bumped together with the PostgreSQL 18 used by CI and compose, so those tests run against 16. Nothing in the code gives a reason for the difference; when you change harness behaviour that depends on the server version, run it against both.

## Commands

| Command | Does |
|---|---|
| `make build` | Builds the web UI (and checks bundle size), then `bin/openartifact` |
| `make build-cli` | Builds the CLI to `bin/oa` |
| `make run` | `make build`, then runs the server |
| `make test` | `go test -short -race -timeout 5m ./...` |
| `make test-integration` | `go test -tags=integration -race -count=1 ./tests/...` (10 min timeout) |
| `make test-coverage` | Race tests with `-coverpkg=./...`, filtered like CI (see [coverage](ci-and-releases.md#coverage)), writes `coverage.html` and prints the total |
| `make lint` | `golangci-lint run ./...` (needs golangci-lint v2 on `PATH`); fails on unformatted code and on lines over 140 columns (`lll`) |
| `make fmt` | `golangci-lint fmt`: gofmt, goimports and golines at 140 columns; run it before committing |
| `make vet` | `go vet ./...` |
| `make check` | `fmt` + `vet` + `vet-tags` + `lint`; run before opening a PR |
| `make vet-tags` | `go vet` with the slim-build tags, see [build tags](#build-tags) |
| `make generate` | sqlc generate; run after editing `internal/database/queries/*.sql` or the schema, and commit `internal/database/dbsqlc/` |
| `make docs-gen` | Regenerates the pages under `docs/reference/` (configuration keys, metrics, CLI commands, REST API) from the code; run after changing config keys, metrics, `oa` help or `internal/api/openapi.json`, and commit the result. `make docs-check` fails when they are stale |
| `make deadcode` | Fails when Go code is unreachable from every `main` package; see [dead-code gate](#dead-code-gate) |
| `make migrate` | `go run ./cmd/server -migrate` (apply migrations and exit) |
| `make bench`, `make bench-load` | Benchmarks, see [performance](../operations/performance.md) and the note below |
| `make docker-build` | Builds `openartifact:latest` from `docker/Dockerfile` |
| `make docker-up` | Starts the full stack with `docker compose -f docker/docker-compose.yml up -d --build` |

Notes on single commands:

- **`make fmt`** wraps code, not comments, strings or SQL. Split those by hand; `make lint` flags them (`lll`). It rejects unformatted files.
- **`make docker-build`** takes the runtime base from `RUNTIME`: `dhi` (default, needs `docker login dhi.io`) or `distroless` (anonymous).
- **`make docker-up`** passes `VERSION`, the short SHA and the RFC 3339 build time as build args. It needs `docker/.env` copied from [`docker/.env.example`](../../docker/.env.example). Without dhi.io access set `OPENARTIFACT_RUNTIME=distroless`.
- **`make bench`** links every package's benchmark binary under `GOTMPDIR`. Go defaults that to `/tmp`, which on a small tmpfs fails with `link: cannot write /tmp/go-link-...: disk quota exceeded`. The target uses `$GOTMPDIR` when set, otherwise `$XDG_CACHE_HOME/openartifact-bench` (or `~/.cache/openartifact-bench`). Running `go test -bench` by hand on such a machine needs `GOTMPDIR` on a disk with a few GB free.

## Web UI development

Run these in `web/`:

```sh
bun install
bun run dev            # Vite dev server on :5173, proxies /api to localhost:8080
bun run typecheck      # vue-tsc, service-worker and e2e type checks
bun test               # unit and property tests
bun run i18n:check     # every locale has the keys of English (also run by build)
bun run build          # i18n check, vite build, precompress; output in web/dist
bun run generate-api   # regenerate src/api/generated from internal/api/openapi.json
bun run dup            # jscpd duplication gate
```

The browser suites are `bun run e2e`, `bun run monkey`, `bun run e2e:chromium` and `bun run e2e:visual:update`. They need a running test server; see [testing](testing.md) for the setup. Translation helpers (`i18n:missing`, `i18n:apply`) are in [translations](translations.md).

The UI registers its service worker only in a production build served by a stamped server (`make build` stamps the version; a plain `go build` reports `dev`), and removes a worker an earlier visit left in any other case, so a development instance does not offer an update after every rebuild.

Start the server on `:8080` for the dev server to talk to. Build the server with `-tags dev` and set `OPENARTIFACT_DEV=1` to make it proxy UI requests to `http://localhost:5173` instead of serving the embedded build (release binaries ignore the variable).

### Static demo build

`bun run build:demo` (or `make web-demo`) builds the UI with no server behind it into `web/dist-demo`. The project site serves it under `/demo/`. Set `OA_DEMO_BASE` to the path it is served from (default `/`):

```sh
OA_DEMO_BASE=/demo/ bun run build:demo
```

- **Backend.** `src/demo` answers every API call from memory, through the transport hook in `src/api/client.ts`. The data is seeded on load; writes change it until the page reloads. The fixtures and handlers are typed with the generated API types, so `bun run typecheck` fails when the API drifts. A call without a handler gets a 501 `demo_unavailable` error, which pages render like any other failure.
- **Normal builds.** They contain none of it: the demo code sits behind `import.meta.env.MODE === 'demo'` and dynamic imports.
- **Routing and service worker.** The demo routes by URL hash (`/demo/#/repos`), because static hosting has no SPA fallback. It registers no service worker or web manifest.
- **Curated pages.** These must work completely: the dashboard, repositories (list, detail, settings), packages (detail, versions, search), security, users, groups and projects. `src/demo/__tests__/coverage.test.ts` mounts the app on each of them and fails on any call the demo does not answer. When a curated page starts calling a new endpoint, add a handler under `src/demo/routes`.

### Binary size

`make build`, `docker/Dockerfile` and `.goreleaser.yml` produce the same shape of binary: `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"` (no symbol table, no DWARF) plus the version `-X` flags. The server is additionally built with `-tags grpcnotrace`, which drops gRPC's `net/trace` debug pages: `EnableTracing` is never set, and with the pages gone `html/template` leaves the binary and the linker can discard methods nothing calls (about 9 MB). Add the tag when you build the server by hand for a size comparison.

Three more rules keep the binaries small:

- `web/dist` is embedded once, gzipped: `web/scripts/precompress.js` (run by `bun run build` and the Dockerfile) replaces each compressible file with `name.gz`, `web/assets.go` streams that gzip from the binary as it is and expands it per request for clients that do not accept gzip. Startup only hashes the files; a build without the script (plain `vite build`) gzips each compressible asset on its first request, once, with a pooled writer, and keeps only that gzip in memory. Source maps are not emitted.
- `cmd/oa` (the package `internal/oacli`) and `cmd/resetpw` link no server code: no database, storage, telemetry, web or cloud-SDK packages (`internal/passwd` holds the password hashing `resetpw` needs; `internal/auth` pulls in the whole server). `tests/binary_deps_test.go` fails when an import brings one back, when the server links `golang.org/x/net/trace` again, and when `oa` or the server links text/template execution (a cobra help, usage or version template: it turns on the linker's keep-every-method mode and costs 1.9 MB).
- UPX is not used, and the official binaries and image carry every documented feature; the optional [build tags](#build-tags) below are for self-built slim servers.

To see where the bytes go, build the server without `-s -w` and analyse it with [go-size-analyzer](https://github.com/Zxilly/go-size-analyzer) (it needs the symbol table; stripped binaries are not analysed):

```sh
CGO_ENABLED=0 go build -trimpath -tags grpcnotrace -o /tmp/oa-server.sym ./cmd/server
go install github.com/Zxilly/go-size-analyzer/cmd/gsa@latest && gsa /tmp/oa-server.sym
```

### Build tags

Two negative tags compile optional features out of the server; the default build, without either tag, carries both.

| Tag | Removes |
|---|---|
| `noai` | AI analysis: the LLM client (`internal/ai`), the AI worker and the admin and per-repository AI handlers. `internal/ai/manifest` stays, it is shared with the dependency, license and SBOM code |
| `nomcp` | The MCP server (`internal/mcp`) and the `github.com/modelcontextprotocol/go-sdk` dependency tree |

Measured on a stripped server: `nomcp` saves about 2.25 MB, `noai` only about 0.11 MB (the LLM client is a thin HTTP layer), so `nomcp` is the size lever and `noai` is for removing the feature.

Build with `make build-slim` (both tags on top of `grpcnotrace`, output `bin/openartifact-slim`), by hand with `go build -tags grpcnotrace,noai,nomcp ./cmd/server`, or an image with `docker build --build-arg GO_TAGS=grpcnotrace,noai,nomcp -f docker/Dockerfile .` (the default is `GO_TAGS=grpcnotrace`).

- A binary without a feature refuses to start when the configuration turns it on: `ai.enabled: true` with `noai` or `mcp.enabled: true` with `nomcp` fails validation with an error naming the tag. Leave the setting off (the default) for a slim build.
- With `noai` the AI routes (`GET /api/v1/repositories/{name}/ai-analysis[...]`, `POST .../ai-analysis/{artifactID}/trigger`, `GET|PUT|POST /api/v1/admin/ai/{config,test,models}`) are still routed and answer `501` with code `feature_unavailable`, so the web UI shows a clear message instead of a 404; the security health page reports AI as off. With `noai` alone the MCP server stays and `trigger_ai_analysis` answers that AI analysis is not enabled.
- `make vet-tags` (part of `make check`) vets every package with `noai,nomcp`, `noai` and `nomcp`, so tag-only code stays compilable. Code and tests that only make sense with a feature carry the matching `//go:build !noai` / `!nomcp` constraint; `internal/buildtags` exports the compile-time constants `buildtags.AI` and `buildtags.MCP` for code that has to ask. `tests/binary_deps_test.go` checks that the slim server links neither `internal/ai`, `internal/mcp` nor the MCP SDK, and that the default build still does.

If you change the HTTP API, update `internal/api/openapi.json` and run `bun run generate-api`; CI fails when `web/src/api/generated` drifts.

## Running the tests

Unit tests need nothing external. For the browser, API-fuzz and property suites see [testing](testing.md); for fuzz targets see [fuzzing](fuzzing.md). Tests that need Postgres or S3 skip unless their variables are set:

| Variable | Used for |
|---|---|
| `TEST_DATABASE_URL` | Postgres DSN for DB-backed tests (e.g. `postgres://USER:PASS@localhost:5432/openartifact_test?sslmode=disable`) |
| `TEST_S3_ENDPOINT` | S3-compatible endpoint; unset skips the S3 tests |
| `TEST_S3_BUCKET`, `TEST_S3_ACCESS_KEY`, `TEST_S3_SECRET_KEY`, `TEST_S3_REGION` | Bucket and credentials (region `garage` for the CI backend) |

- With `CI` set (GitHub Actions sets it), a missing variable makes the test fail instead of skipping, so a misconfigured pipeline cannot silently skip integration tests.
- DB and S3 tests, and the migration runner tests, are behind the `integration` build tag: run them with `-tags=integration` (`make test-integration`).
- The S3 test backend is [Garage](https://garagehq.deuxfleurs.fr/) (`dxflrs/garage`); MinIO no longer ships images. The `integration-shard` job in [`ci.yml`](../../.github/workflows/ci.yml) shows how to bootstrap a single-node Garage (layout, bucket, imported key).
- `tests/integration/dex_sso_integration_test.go` signs in through Dex, a second Dex (an upstream OIDC IdP) and OpenLDAP containers, once per Dex connector, and also needs `TEST_DATABASE_URL`. It takes the images, the LDIF seed and the Dex config from the `dex` profile of `docker/docker-compose.yml` and `docker/dex/`, so a Dependabot bump of those images is tested by it.
- `tests/integration/` starts its own Postgres through testcontainers and needs a container runtime. Under rootless podman:

```sh
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
export TESTCONTAINERS_RYUK_DISABLED=true
make test-integration
```

Write tests that assert behavior, not mock round-trips; poll with deadlines instead of fixed sleeps. gocyclo limit 20 applies to tests too, so prefer table-driven subtests.

## Conventions

The full list lives in [AGENTS.md](../../AGENTS.md); the ones that most often matter:

- Migrations are forward-only (`migrations/NNN_<topic>.sql`); never edit an applied file. The next free number is one more than the highest in `ls migrations`.
- Never hand-edit `internal/database/dbsqlc/`.
- Blobs are content-addressed by sha256; delete only via `storage.SafeDeleteBlob`.
- Every repo-scoped format handler registers relative routes on the single `/repos/{name}` subrouter ([architecture](architecture.md#routing-invariant)).
- API JSON is snake_case; keep `openapi.json` in sync with the registered routes.
- Every config key settable in YAML needs an env override in `internal/config` and entries in `.env.example` and `openartifact.example.yaml`.
- Every new file needs an SPDX header (the repository is REUSE-compliant).


## Dead-code gate

`make deadcode` (`scripts/deadcode-check.sh`, CI job `go`) runs [`deadcode`](https://pkg.go.dev/golang.org/x/tools/cmd/deadcode) (pinned in `tools/go.mod`) with `-tags integration` over every `main` package and fails on any function that none of them reaches. There is no allowlist: the expected result is zero findings. It deliberately runs **without** `-test`: with `-test` every test function is a root, so a production function that only tests call would count as live. Without it such functions are reported, so test-only helpers cannot hide in production files.

The report is reduced to `file  symbol` lines (sorted, line numbers dropped), so the output is deterministic.

## Duplication gates

Duplication can only go down. Both gates run in CI (`dupl` in the `go` job, `jscpd` in the `web` job).

- Go: `make dup` (`scripts/dup-check.sh`) runs [`dupl`](https://github.com/mibk/dupl) (pinned in `tools/go.mod`) with `-t 75` over non-test, non-generated Go (`internal/database/dbsqlc`, `*_test.go` and files with a `Code generated` header are excluded). It compares the clone group count and the duplicated line count (union of the reported ranges) with `go_groups` and `go_lines` in `.github/dup-baseline.json` and fails if either grows.
- Web: `cd web && bun run dup` runs [`jscpd`](https://github.com/kucherenko/jscpd) (pinned in `web/package.json`) with `web/.jscpd.json` (50 tokens, 5 lines, generated API client, i18n and tests excluded) and fails on any clone (threshold 0).

When a change removes duplication, run `make dup-baseline` and commit the rewritten `.github/dup-baseline.json` so the lower number becomes the new limit. Never raise the baseline to make a PR pass: remove the duplicate code instead.

### Test support packages

Helpers shared by tests of several packages cannot live in a `_test.go` file. Put them in a package whose directory name ends in `test`, following the standard library (`httptest`, `iotest`):

- `internal/backup/backuptest`
- `internal/format/formattest`
- `internal/gpgkeys/gpgkeystest`
- `internal/licensemeta/archivetest`
- `internal/licensepolicy/policytest`

That naming is the only exclusion. The script skips a package of this module whose directory name ends in `test` and that production code does not import. `internal/attest` ends in `test` too, but production imports it, so it is checked. Production code must never import a `*test` package.

### Reading a failure

The failure lists `file  Symbol` lines (`Type.Method` for methods). Handle each one:

1. **Dead everywhere**: delete it, together with its tests.
2. **Only tests use it**: move it into a `_test.go` file of the same package, or into a `*test` package when several packages share it. If a test only needs to wait for or observe background work, poll the observable outcome with a deadline instead of exporting a hook, or use a production lifecycle API that already exists (for example `WithTasks(bgtask.Runner)` and `Shutdown`).
3. **Reached only through reflection, templates or `go:linkname`**: make the call visible to the analysis, or give the code a real production caller; there is no allowlist.

To see why a function is live or dead run `go tool -modfile=tools/go.mod deadcode -tags integration -whylive=<pkg>.<Func> ./...`.
