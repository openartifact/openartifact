<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# CI and releases

Read this page if a CI job failed on your pull request, you maintain the workflows, or you cut a release. It describes what each job checks, how coverage is measured, the nightly client matrix, the documentation site build and the release pipeline.

The black-box suites (API fuzzing, end to end, visual regression, monkey walk, property tests) are described once, with their CI jobs, in [testing](testing.md). Fuzz targets are in [fuzzing](fuzzing.md).

## CI workflows

Workflows live in [`.github/workflows`](../../.github/workflows). All actions are pinned by commit SHA and all container images by `@sha256` digest.

[Dependabot](../../.github/dependabot.yml) bumps them weekly, with a 7-day cooldown and minor and patch updates grouped. It covers GitHub Actions, Go modules (`/` and `/tools`), web dependencies (bun, `/web`), and Dockerfile and compose images. Not covered, and bumped by hand: `GO_VERSION`, `BUN_VERSION`, `GOLANGCI_LINT_VERSION`, `TRIVY_VERSION`, and the Garage and Postgres images in the `integration-shard` job.

Image builds in CI and release log in to `dhi.io` with the `DHI_USERNAME` and `DHI_TOKEN` secrets. Dependabot needs the same two names as Dependabot secrets for the `dhi.io` base image. Fork PRs cannot read secrets and build with `RUNTIME=distroless`.

### `ci.yml`

Runs on pushes to `main`, tags `v*` and pull requests.

| Job | Checks |
|---|---|
| `go` | `go build`, `go vet`, `go mod tidy -diff` (root and `tools/`), `govulncheck ./...`, the [dead-code gate](README.md#dead-code-gate), `go test -race ./...` |
| `integration` | Aggregate check, green when every `integration shard` job is; it runs no tests |
| `integration-shard` | Four parallel integration jobs, see below |
| `lint` | golangci-lint with `.golangci.yaml`, including the formatter check and the 140-column limit |
| `reuse` | REUSE license compliance |
| `sqlc` | `sqlc generate` leaves no diff in `internal/database/dbsqlc` |
| `web` | `bun install --frozen-lockfile`, typecheck, generated API client up to date, `bun test` with coverage, build, bundle-size check |
| `coverage` | Merges the Go profiles, reports totals and compares with `main`, see [coverage](#coverage) |
| `security` | Builds the image, then runs Trivy image, filesystem and config scans |
| `docs` | `go run ./cmd/docgen -check`, then builds the docs site; a dead internal link fails the build |
| `workflow-lint` | `actionlint` and zizmor (rules in [`.github/zizmor.yml`](../../.github/zizmor.yml)) |
| `dependency-review` | Pull requests only; fails on high-severity dependency changes |

Notes on single jobs:

- **`integration`**: keep its name. The repository ruleset requires the check `integration (postgres + garage + testcontainers)`; add shards to the matrix instead of renaming it.
- **`integration-shard`**: the jobs are `integration shard (database)`, `(worker)`, `(tests)` and `(rest)`, each with its own Postgres and Garage. Each runs `go test -race -tags integration` over the packages that `scripts/integration-packages.sh <shard>` lists. Wall time is that of the slowest shard, about 10 minutes. To move a package between shards, edit `claim()` in the script. Each shard uploads `coverage-go-integration-<shard>`.
- **Shard contents**: `database` is `internal/database`; `worker` is `internal/worker` and `internal/federation`; `tests` is `./tests`; `rest` is every other package with an integration-tagged test, including `tests/integration`, so a new package needs no workflow edit.
- **`security`**: each Trivy scan runs twice. First a SARIF upload to code scanning, then a gate that fails on fixable HIGH and CRITICAL findings (the config gate fails on any HIGH or CRITICAL). Fork and Dependabot PRs skip the upload but keep the gate. Ignores live in `.trivyignore.yaml`.
- **`docs`**: also checks that `.env.example` and `openartifact.example.yaml` list every config key. The site build uses the VitePress project of [openartifact.github.io](https://github.com/openartifact/openartifact.github.io).

`coverage-comment.yml` posts the coverage comment on pull requests.

### Other security workflows

| Workflow | Trigger | Does |
|---|---|---|
| `codeql.yml` | Push to `main`, PRs, weekly (Mon) | CodeQL for Go and JavaScript/TypeScript |
| `scorecard.yml` | Push to `main`, branch-protection changes, weekly (Tue) | OpenSSF Scorecard, results published and uploaded as SARIF |
| `clusterfuzzlite-pr.yml`, `clusterfuzzlite-batch.yml` | PRs touching Go, daily | Fuzzing, see [fuzzing](fuzzing.md) |
| `scheduled-scan.yml` | Weekly (Mon), manual | Trivy scan of the published `latest` and latest stable semver images on ghcr.io, to notice new CVEs in released images |

## Coverage

CI measures coverage for Go and the web UI and reports it on every run. There is no fixed threshold, only a regression guard.

- **Go.** The `go` job writes a unit profile. Each `integration-shard` job writes an integration profile. All use `-race -covermode=atomic -coverpkg=./...`, so a package is credited for tests anywhere in the module. The `coverage` job merges them with `scripts/coverage/merge-go.sh`, which sums identical blocks and drops generated or non-product code (`internal/database/dbsqlc`, `bench/`, `cmd/docgen/testdata/`). A statement is covered when any run executed it.
- **Web.** `bun test --coverage` writes lcov. The number is line-weighted over product code (test files, `test/` and `src/api/generated` are excluded), so it differs from the per-file average in bun's own table. Files no test imports are not in bun's lcov and are not counted.
- **Where to read it.** The run's job summary has totals (Go unit, Go integration, Go merged, web lines and functions) and a per-area table. The `coverage-report` artifact has `coverage.json`, the annotated `go.html`, the per-file `web.html` and the merged profile. Pull requests get a sticky comment with totals and the change against `main`.
- **Baseline.** A push to `main` stores its `coverage.json` in the Actions cache (`coverage-base-<sha>`). Pull requests restore the newest one. If none exists, deltas show `n/a` and the guard is skipped.
- **Regression guard.** On pull requests the `coverage` job fails when the merged Go total or the web line coverage is more than 1 percentage point below `main` (`scripts/coverage/gate.sh`; `MAX_DROP` changes it). For a deliberate drop, such as deleting a well-tested feature, an admin merges with the numbers explained in the PR.
- **Fork pull requests.** `coverage-comment.yml` posts the comment on `workflow_run`, because a fork's token cannot write. It runs the default branch's code, never the PR's, and renders the CI artifact as data only.
- **Locally.** `make test-coverage` produces the filtered Go report (`coverage.html`). Integration coverage needs the variables from [testing](README.md#running-the-tests) and the same flags as the CI job.
- **No README badge.** A badge needs an external service or a generated file committed to `main` on every merge, and `main` is never pushed to directly.

## Client compatibility matrix

Every format is verified with its real client once, when it is added. `client-matrix.yml` repeats that every night (02:37 UTC, or on demand with an optional `formats` input) with the latest client of each format. A client release that changes its protocol shows up the night it ships, not in a user's CI.

### How it runs

- **One job per format.** The `image` job builds the image once (distroless runtime, no registry login) and shares it as an artifact. The matrix is whatever `scripts/smoke/*.sh` exists, minus `lib.sh`, so a new format needs no workflow edit.
- **Fresh stack per job.** Every `smoke (<format>)` job loads the image and starts its own compose stack with `scripts/smoke-stack.sh up`: random secrets, a bootstrap admin and a token from `scripts/dev-token.sh`. It runs `scripts/smoke/<format>.sh` and removes the stack. A failing format does not hide the others (`fail-fast: false`).
- **Failure report.** The `report` job opens one issue labelled `client-matrix` that lists the failed jobs. It comments while the issue stays open, and comments and closes it after the next green night. Only this job has `issues: write` and `actions: read`; the others have `contents: read`.

### One script per format

`scripts/smoke/<format>.sh` does five things:

1. Creates a hosted repository.
2. Runs the real client in a throwaway host-network container.
3. Publishes a tiny package with the client's own publish command, or the documented upload API where the client has none.
4. Installs or resolves the package back.
5. Compares the content and prints the client version.

The clients are apt, dnf, apk, pacman, npm, pip, mvn, docker, helm, cargo, go, dotnet, gem, composer, conda, terraform, ansible-galaxy, nix, conan, sbt (for both ivy and sbt) and curl. The shared plumbing (repository creation and cleanup, container start, image overrides) is `scripts/smoke/lib.sh`. The client logic exists only in the format's script.

### Run it locally

You need docker or podman, `curl` and `jq` on the host, and Internet access in the containers. No container is privileged; only `docker.sh` mounts the daemon socket.

```sh
eval "$(OA_STACK_PROJECT=oa-<topic> OA_STACK_PORT=18080 scripts/smoke-stack.sh up)"   # builds the image, exports OA_URL and OA_TOKEN
scripts/smoke-formats.sh                       # every format
FORMATS="npm cargo" scripts/smoke-formats.sh   # some formats
scripts/smoke/npm.sh                           # one format
scripts/smoke-stack.sh logs                    # server log
scripts/smoke-stack.sh down                    # remove the stack and its volumes
```

Variables:

- `<FORMAT>_IMAGE` overrides the client image; `KEEP=1` keeps the repositories.
- `OA_STACK_BUILD=0` reuses an existing `openartifact:<project>` image. The stack sets `OPENARTIFACT_VERSION=<project>`, so stacks of different projects and worktrees never share an image tag.
- `OA_STACK_BASE_URL=none` starts the stack without `BASE_URL`. Run `scripts/smoke/<format>.sh URL` against `http://127.0.0.1:PORT` and `http://localhost:PORT` in turn to prove the generated URLs follow the request host.
- `OA_STACK_TRUSTED_PROXIES` sets the trusted proxy CIDRs for a reverse-proxy run.

### Two phases

`SMOKE_MODE` selects what a script does:

| Mode | Behaviour |
|---|---|
| `both` (default) | Creates the repository, publishes, installs, deletes the repository |
| `publish` | Creates the repository and keeps it (implies `KEEP=1`) |
| `install` | Installs the package a publish run published, in a fresh client state, and deletes nothing |

The repository may have moved to another server between the two phases; this is how upgrade and import proofs work. `SMOKE_ID` ties the phases together (default: time and a random number). It names the repository `smoke-<format>-<id>` (`SMOKE_REPO_NAME` overrides it), the package `smoke<id>` and the version `1.0.0`. The script prints them on stderr as `smoke: repo=... pkg=... version=...`.

```sh
FORMATS="npm pypi" SMOKE_MODE=publish SMOKE_ID=42 scripts/smoke-formats.sh OLD_URL
SMOKE_MODE=install SMOKE_ID=42 scripts/smoke-formats.sh NEW_URL
```

### Client quirks the scripts encode

- `docker.sh` drives the host daemon through its socket (`DOCKER_SOCK`, default `/var/run/docker.sock`). `DOCKER_HOST` selects another daemon such as a `docker:dind` container. On a podman host use that: podman's API refuses plain http even on loopback, so the registry login fails.
- `go.sh` makes its repository public for the `go get` step, because the go command never sends credentials over http.
- `terraform.sh` puts a Caddy with a self-signed certificate in front of the server, because Terraform needs https and a dotted host name for a registry.
- `nuget.sh` allows the insecure (http) source.
- `cargo.sh` selects the `cargo:token` credential provider; newer cargo requires one for an authenticated registry.

## Documentation site

The site at <https://openartifact.github.io/> is built by the separate repository [openartifact.github.io](https://github.com/openartifact/openartifact.github.io). It renders this repository's `docs/` in place: `README.md` is a directory index, and links to files outside `docs/` become GitHub links. `/latest/` is `main`; `/vX.Y/` is the newest stable tag of each of the last five minor lines. The text, the generated reference and the drift and link checks stay here, versioned with the code.

A docs change goes live a few minutes after the merge:

1. `docs-site-trigger.yml` runs on pushes to `main` that touch `docs/` or `web/src/lib/installCommands.ts`, and on `v*.*.*` tags.
2. It force-pushes one trigger commit to the site repository's `rebuild` branch with a write deploy key (secret `SITE_DEPLOY_KEY`; the job is skipped without it).
3. The site's Pages workflow builds and deploys on that push.

The site also polls every 15 minutes and rebuilds when `docs/` changed or a tag appeared. That covers a missing key or a failed trigger. To publish immediately by hand:

```sh
gh workflow run pages.yml -R openartifact/openartifact.github.io
```

## Releases

`release.yml` runs on a `vX.Y.Z[-pre]` tag, or manually with an existing tag. Bump the top-level `VERSION` file first, together with the `ghcr.io/openartifact/oa-tekton:<version>` image defaults in `integrations/tekton/` and `scripts/smoke/oci-supplychain.sh`: the verify job fails when they differ. `tekton-catalog.yml` then publishes that image and the catalog bundles for the tag.

1. **ci-gate.** Waits up to 80 minutes for the `CI` workflow to succeed on the tagged commit. It fails when CI failed or never ran; nothing is published before that.
2. **verify.** The `VERSION` file must equal the tag without `v`. Runs `go vet` and `go test -race`. A tag with a `-suffix` is a prerelease and does not get the `latest` image tag. Neither does a stable tag that is not the highest stable `vX.Y.Z` tag of the repository, so re-releasing an older tag never moves `latest` back.
3. **binaries.** GoReleaser (`.goreleaser.yml`) builds the UI and publishes a GitHub release with `openartifact` and `resetpw` (linux amd64 and arm64, tar.gz), `oa` (linux, darwin and windows, amd64 and arm64) and `SHA256SUMS`. Archives and checksums get build-provenance attestations.
4. **image.** Builds `linux/amd64` and `linux/arm64` locally and runs the Trivy gate on both before anything is pushed. Then pushes the multi-arch image to `ghcr.io/openartifact/openartifact`, tagged `X.Y.Z`, `X.Y` and (stable only) `latest`, with OCI labels, a buildkit SBOM and `mode=max` provenance. The pushed digest is signed with cosign (keyless) and gets a build-provenance attestation in the registry.

All release caches are disabled, so a tag-triggered run never restores a cache written by another run. To verify signatures and attestations, see [signing](../security/signing.md).
