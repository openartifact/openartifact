<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Development

How to build, test and release OpenArtifact, and what CI enforces. For the code layout see [architecture](architecture.md); for the black-box suites (API fuzzing, e2e, monkey walk, property tests) see [testing](testing.md); for contribution rules see [CONTRIBUTING.md](../../CONTRIBUTING.md) and [AGENTS.md](../../AGENTS.md) (conventions and invariants for humans and coding agents alike).

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
| `make test-coverage` | Race tests with `-coverpkg=./...`, filtered like CI (see [Coverage](#coverage)), writes `coverage.html` and prints the total |
| `make lint` | `golangci-lint run ./...` (needs golangci-lint v2 on `PATH`); also fails on unformatted code and on lines over 140 columns (`lll`) |
| `make fmt` | `golangci-lint fmt`: gofmt, goimports and golines (wraps code at 140 columns, struct tags untouched); run it before committing, `make lint` rejects unformatted files. Comments, strings and SQL are not wrapped automatically: split them by hand (`lll` flags them) |
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

### Binary size

`make build`, `docker/Dockerfile` and `.goreleaser.yml` produce the same shape of binary: `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"` (no symbol table, no DWARF) plus the version `-X` flags. The server is additionally built with `-tags grpcnotrace`, which drops gRPC's `net/trace` debug pages: `EnableTracing` is never set, and with the pages gone `html/template` leaves the binary and the linker can discard methods nothing calls (about 9 MB). Add the tag when you build the server by hand for a size comparison.

Three more rules keep the binaries small:

- `web/dist` is embedded once, gzipped: `web/scripts/precompress.js` (run by `bun run build` and the Dockerfile) replaces each compressible file with `name.gz`, `web/assets.go` serves that gzip as it is and expands it once at startup for clients that do not accept gzip. Source maps are not emitted.
- `cmd/oa` and `cmd/resetpw` link no server code: no database, storage, telemetry, web or cloud-SDK packages (`internal/passwd` holds the password hashing `resetpw` needs; `internal/auth` pulls in the whole server). `tests/binary_deps_test.go` fails when an import brings one back, and when the server links `golang.org/x/net/trace` again.
- UPX and feature-removing build tags are not used: the official image carries every documented feature.

To see where the bytes go, build the server without `-s -w` and analyse it with [go-size-analyzer](https://github.com/Zxilly/go-size-analyzer) (it needs the symbol table; stripped binaries are not analysed):

```sh
CGO_ENABLED=0 go build -trimpath -tags grpcnotrace -o /tmp/oa-server.sym ./cmd/server
go install github.com/Zxilly/go-size-analyzer/cmd/gsa@latest && gsa /tmp/oa-server.sym
```

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

## Coverage

CI measures coverage for Go and the web UI and reports it on every run; there is no fixed threshold, only a regression guard.

- **Go.** The `go` job writes a unit profile and the `integration` job an integration profile, both `-race -covermode=atomic -coverpkg=./...`, so a package is credited for tests anywhere in the module. The `coverage` job merges them with `scripts/coverage/merge-go.sh`, which sums identical blocks and drops generated or non-product code (`internal/database/dbsqlc`, `bench/`, `cmd/docgen/testdata/`). A statement is covered when any run executed it.
- **Web.** `bun test --coverage` writes lcov. The number is line-weighted over product code (test files, `test/` and `src/api/generated` are excluded), so it differs from the per-file average in bun's own table. Files no test imports are not in bun's lcov and therefore not counted.
- **Where to read it.** The run's job summary has totals (Go unit, Go integration, Go merged, web lines and functions) and a per-area table (`internal/worker`, `cmd/server`, `src/pages`, ...). The `coverage-report` artifact has `coverage.json`, the annotated `go.html`, the per-file `web.html` and the merged profile. Pull requests get a sticky comment with totals and the change against `main`.
- **Baseline.** A push to `main` stores its `coverage.json` in the Actions cache (`coverage-base-<sha>`); pull requests restore the newest one. If none exists (first run, cache evicted after seven idle days) deltas show `n/a` and the guard is skipped.
- **Regression guard.** On pull requests the `coverage` job fails when the merged Go total or the web line coverage is more than 1 percentage point below `main` (`scripts/coverage/gate.sh`, `MAX_DROP` to change it). A deliberate drop, for example deleting a well-tested feature, is merged by an admin with the numbers explained in the PR.
- **Fork pull requests.** The comment is posted by `coverage-comment.yml` on `workflow_run`, because a fork's token cannot write. That workflow runs the default branch's code, never the PR's, and renders the CI artifact as data only; it takes effect once it is on `main`. The report itself needs no secrets or external service.
- **Locally.** `make test-coverage` produces the filtered Go report (`coverage.html`); integration coverage needs the variables from [Testing](#testing) and the same flags as the CI job.
- No README badge: a badge needs either an external service or a generated file committed to `main` on every merge, and `main` is never pushed to directly.

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
| `clusterfuzzlite-pr.yml` | pull requests touching Go files, `go.mod`/`go.sum` or `.clusterfuzzlite/` | Selects the targets the PR can affect, builds only those and fuzzes them for 5 minutes in `code-change` mode, starting from the stored corpus (typically 8-12 minutes in all, against about 30 for the full set). A target is affected when its package, or a package it imports, contains a changed file (non-Go files count for the closest package above them: seeds, embedded assets); a change to `go.mod`, `go.sum` or `.clusterfuzzlite/` builds every target, a change no target sees skips the build and the run. The import graph (`go list -e`, no module download) is narrowed with the coverage of the last batch run: a target whose report lists no covered line of any changed Go file is dropped (files unknown to the report, new files, and non-Go files keep the target; without a report the graph alone decides). ClusterFuzzLite's own coverage-based removal is off because it cannot match the file names of the report, which start with the coverage build's workspace. The selection is `.clusterfuzzlite/select-targets.sh` and reaches `build.sh` as `CFL_EXTRA_FUZZ_TARGETS`; 5 minutes rather than the batch's hour because the deep fuzzing runs daily on `main` and a PR only has to catch a regression in the code it touches. A crash fails the check and is uploaded as an artifact, SARIF goes to code scanning (same-repository PRs). Reads the [storage repository](#corpus-and-coverage-storage) anonymously, so it needs no secret and works for fork PRs |
| `clusterfuzzlite-batch.yml` | daily (02:41 UTC), manual | Three sequential jobs on `main`: `batch` fuzzes for 1 hour and adds what it finds to the stored corpus, `prune` minimizes the corpus (its 1 hour budget is shared by the targets and caps each merge: too small a budget fails the job with `Merging new testcases timed out`), `coverage` builds the coverage report that the PR job uses to pick the affected targets. One concurrency group serializes them, so nothing else writes the storage repository at the same time |

The build is defined in [`.clusterfuzzlite/`](../../.clusterfuzzlite): `Dockerfile` (the OSS-Fuzz Go builder image, pinned by digest and bumped by Dependabot; it downloads the Go modules in a layer of its own, so a change that leaves `go.mod`/`go.sum` alone reuses it), `build.sh`, `project.yaml` (`language: go`), and the `dicts/` directory. `build.sh` finds every `func FuzzX(f *testing.F)` in a non-build-tagged `*_test.go` file and compiles it with `compile_native_go_fuzzer_v2` into a binary named `<package path>_<FuzzX>`, so a new target needs no registration. Targets are compiled in parallel, at most `min(nproc, free memory / 1.5 GiB)` at a time (`FUZZ_BUILD_JOBS=<n>` overrides it; the build is CPU-bound, so it shortens a run only on a machine with more cores than the 4-vCPU hosted runner, where it takes 13 to 19 minutes with any number of jobs), each in its own scratch directory that is merged into the output in sorted order afterwards; the first failure stops the remaining targets and fails the build. The compiler needs each fuzz function to be defined in exactly one file of its package directory, and it compiles all test files of that directory into one package: `build.sh` therefore deletes the external-test-package files (`package x_test`) of those directories from the image's copy of the tree, so a fuzz target must not depend on them.

### Seeds and dictionaries

The libFuzzer binaries do not read `f.Add` calls or `testdata/fuzz/`: the OSS-Fuzz Go builder replaces `*testing.F`, and its `Add` does nothing. Without help every target would start from an empty corpus. `build.sh` therefore gives each binary two companion files next to it in the output:

| File | Source | Effect |
|---|---|---|
| `<name>_seed_corpus.zip` | `<package>/testdata/fuzz/<FuzzX>/`, encoded by the image's `addStdLibCorpusToFuzzer` | the fuzzer starts from these inputs; the same files are the seeds of the plain `go test` run |
| `<name>.dict` | `.clusterfuzzlite/dicts/<name>.dict`, else the package dictionary `.clusterfuzzlite/dicts/<package path with / as _>.dict` | libFuzzer splices these tokens (magic numbers, field names, format keywords) into inputs |

`<name>` is `<package path with / as _>_<FuzzX>`, for example `pkg_npm_FuzzParseTarball`. To add seeds, put files in the target's `testdata/fuzz/FuzzX/` directory in the `go test fuzz v1` format (the same files the Go fuzzer writes when it finds a failure). Only `string`, `[]byte`, `bool`, `intN`/`uintN`, `int`/`uint` and `floatN` values written in plain decimal are supported; `build.sh` fails when a seed uses another form (`byte(...)`, `rune(...)`, hexadecimal) instead of dropping it silently. Keep seeds small, one per feature of the format.

A dictionary is a plain libFuzzer `.dict` file: one `"token"` per line (`\xNN` escapes allowed, 1 to 64 bytes), `#` comments, the SPDX header on top. Add tokens that the parser matches literally and that a mutator would take long to invent. Dictionaries are shared by package, so add a per-target file only when the target needs different tokens.

### Corpus and coverage storage

ClusterFuzzLite keeps the corpus and the coverage reports in a git repository, [`openartifact/openartifact-fuzz-storage`](https://github.com/openartifact/openartifact-fuzz-storage), instead of in GitHub Actions artifacts that expire:

- branch `main`: the corpus, `corpus/<target>/<input>`, written by the `batch` and `prune` jobs;
- branch `gh-pages`: the coverage report, `coverage/`, written by the `coverage` job. The PR job reads it to learn which targets cover the lines a PR touches and fuzzes only those.

The repository is public, so the PR workflow reads it without credentials, also for pull requests from forks. Only `clusterfuzzlite-batch.yml` writes to it, through the `FUZZ_STORAGE_TOKEN` repository secret: a fine-grained personal access token with the resource owner `openartifact`, access to the `openartifact-fuzz-storage` repository only and the permission *Contents: read and write*. Every job of that workflow starts by checking the secret against the GitHub API and fails with an explicit message when it is missing, holds whitespace (set it with `printf %s`), is rejected (expired, revoked, not granted to the repository) or cannot push: ClusterFuzzLite itself only logs a failed upload and carries on, so without the check a bad token costs a whole run and leaves the storage empty. The PR job keeps working (it then fuzzes every target from the seeds alone). Each upload is a commit and the corpus grows, so when the repository gets large, squash its history: ClusterFuzzLite only uses the latest content.

### Running the targets locally

To build and run the exact CI binaries locally (docker or podman):

```sh
docker build -f .clusterfuzzlite/Dockerfile -t oa-cfl .
mkdir -p out && docker run --rm -e FUZZING_ENGINE=libfuzzer -e SANITIZER=address -e ARCHITECTURE=x86_64 \
  -e FUZZING_LANGUAGE=go -e PROJECT_NAME=openartifact -v "$PWD/out:/out" oa-cfl compile
mkdir -p corpus && unzip -q -o out/pkg_npm_FuzzParseTarball_seed_corpus.zip -d corpus
./out/pkg_npm_FuzzParseTarball corpus -dict=out/pkg_npm_FuzzParseTarball.dict -max_total_time=60
```

With rootless podman add `--pids-limit=-1` to `run`: the parallel build starts more processes than podman's default limit allows. `FUZZ_BUILD_JOBS=2` (as `-e`) limits the parallelism on a small machine; `SANITIZER=coverage` builds the binaries of the `coverage` job instead.

A crash artifact from CI reproduces with `./out/<binary> <crash-file>`. Keep it as a regression seed by adding the decoded arguments as a `testdata/fuzz/FuzzX/` file in the `go test fuzz v1` form: the artifact holds the raw libFuzzer bytes, which the OSS-Fuzz shim splits into the arguments of the fuzz function (fixed-size values first, then one weight byte per `string`/`[]byte` argument, then their data).

## Conventions

The full list lives in [AGENTS.md](../../AGENTS.md); the ones that most often matter:

- Migrations are forward-only (`migrations/00N_<topic>.sql`); never edit an applied file.
- Never hand-edit `internal/database/dbsqlc/`.
- Blobs are content-addressed by sha256; delete only via `storage.SafeDeleteBlob`.
- Every repo-scoped format handler registers relative routes on the single `/repos/{name}` subrouter ([architecture](architecture.md#routing-invariant)).
- API JSON is snake_case; keep `openapi.json` in sync with the registered routes.
- Every config key settable in YAML needs an env override in `internal/config` and entries in `.env.example` and `openartifact.example.yaml`.
- Every new file needs an SPDX header (the repository is REUSE-compliant).

## Adding a format

The registry in `internal/repotype/repotype.go` is the single list of package formats. Add one `Spec` there (its position is the picker order), then run `go test ./...`: the exhaustiveness tests fail once for every place that has no decision for the new format, naming it. Work through them:

1. **Registry:** the `Spec` fields (proxy, group and federation support, default upstream, signing, isolated URL namespace, version rules, Package URL type, OSV ecosystem, package tables) are the only place format facts live. Never add a new `switch` or slice of format names for such a fact.
2. **Database:** a migration that extends `chk_repo_format` (the latest one is checked by `TestLatestMigrationAcceptsEveryFormat`), and the `internal/api/openapi.json` format enums.
3. **Package tables:** list them in `Spec.PackageTables`, then add the branches to every union query the `TestPackageTablesInUnions` failure names (`ListRepoComponents`/`CountRepoComponents`, catalog, trash, quota, tiering, recovery journal, backup).
4. **Per-format code:** a switch whose branches run different code per format (router mounts, proxy and group serving, workers, SBOM and vuln scanning, client setup, license and dependency readers) stays a switch. Each has a `format_exhaustive_test.go` that lists the format as `handled` (and probes it) or `ignored` (falling through is intended), so add the branch or list the format as ignored.
5. **Web:** `web/src/lib/formats.ts` (`FORMAT_META`: label, color, icon), the install snippets in `web/src/lib/installCommands.ts` and `internal/clientsetup`, and the hand-kept format lists in `web/src/lib/repoTypes.ts` and `web/src/router/redirects.ts`.

## Dead-code gate

`make deadcode` (`scripts/deadcode-check.sh`, CI job `go`) runs [`deadcode`](https://pkg.go.dev/golang.org/x/tools/cmd/deadcode) (pinned in `tools/go.mod`) with `-tags integration` over every `main` package and fails on any function that none of them reaches. There is no allowlist: the expected result is zero findings. It deliberately runs **without** `-test`: with `-test` every test function is a root, so a production function that only tests call would count as live. Without it such functions are reported, so test-only helpers cannot hide in production files.

The report is reduced to `file  symbol` lines (sorted, line numbers dropped), so the output is deterministic.

## Duplication gates

Duplication can only go down. Both gates run in CI (`dupl` in the `go` job, `jscpd` in the `web` job).

- Go: `make dup` (`scripts/dup-check.sh`) runs [`dupl`](https://github.com/mibk/dupl) (pinned in `tools/go.mod`) with `-t 100` over non-test, non-generated Go (`internal/database/dbsqlc`, `*_test.go` and files with a `Code generated` header are excluded). It compares the clone group count and the duplicated line count (union of the reported ranges) with `go_groups` and `go_lines` in `.github/dup-baseline.json` and fails if either grows.
- Web: `cd web && bun run dup` runs [`jscpd`](https://github.com/kucherenko/jscpd) (pinned in `web/package.json`) with `web/.jscpd.json` (50 tokens, 5 lines, generated API client, i18n and tests excluded) and fails above the 0.5 % threshold.

When a change removes duplication, run `make dup-baseline` and commit the rewritten `.github/dup-baseline.json` so the lower number becomes the new limit. Never raise the baseline to make a PR pass: remove the duplicate code instead.

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
| `go` | `go build`, `go vet`, `go mod tidy -diff` (root and `tools/`), `govulncheck ./...`, [dead-code gate](#dead-code-gate), `go test -race ./...` (writes the unit coverage profile) |
| `integration` | Postgres + Garage service; `go test -race -tags integration` over every package with an integration-tagged test (writes the integration coverage profile) |
| `lint` | golangci-lint with `.golangci.yaml` (includes the gofmt/goimports/golines formatter check and the 140-column `lll` limit) |
| `reuse` | REUSE license compliance |
| `sqlc` | `sqlc generate` leaves no diff in `internal/database/dbsqlc` |
| `web` | `bun install --frozen-lockfile`, typecheck, generated API client up to date, `bun test` (with lcov coverage), build, bundle-size check |
| `coverage` | Merges the Go profiles, reports totals and per-area coverage and compares with `main`; see [Coverage](#coverage) |
| `security` | Builds the image, then Trivy: image, filesystem (Go and bun lockfiles, secrets) and config (Dockerfile, compose) scans. Each runs twice: a SARIF upload to code scanning, then a gate that fails on fixable HIGH/CRITICAL findings (config gate: any HIGH/CRITICAL). Fork and Dependabot PRs skip the upload but keep the gate. Ignores in `.trivyignore.yaml` |
| `docs` | `go run ./cmd/docgen -check` (generated reference up to date; `.env.example` and `openartifact.example.yaml` list every config key), then builds the docs site from this checkout with the VitePress project of [openartifact.github.io](https://github.com/openartifact/openartifact.github.io); a dead internal link fails the build |
| `workflow-lint` | `actionlint` and zizmor (rules in [`.github/zizmor.yml`](../../.github/zizmor.yml)) |
| `dependency-review` | PRs only; fails on high-severity dependency changes |

`coverage-comment.yml` posts the coverage comment on pull requests (see [Coverage](#coverage)).

### Other security workflows

| Workflow | Trigger | Does |
|---|---|---|
| `codeql.yml` | push to `main`, PRs, weekly (Mon) | CodeQL for Go and JavaScript/TypeScript |
| `scorecard.yml` | push to `main`, branch-protection changes, weekly (Tue) | OpenSSF Scorecard, results published and uploaded as SARIF |
| `clusterfuzzlite-pr.yml`, `clusterfuzzlite-batch.yml` | PRs touching Go, daily | ClusterFuzzLite fuzzing of the Go fuzz targets, see [Fuzzing](#fuzzing) |
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
