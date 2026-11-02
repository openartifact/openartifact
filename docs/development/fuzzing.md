<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Fuzzing

Read this page if you write or fix a parser of untrusted input, add a fuzz target, or have to reproduce a crash that the CI fuzzing reported. For the other test suites see [testing](testing.md); for the overall workflow see the [development guide](README.md).

Parsers of untrusted input have native Go fuzz targets: uploaded archives and metadata, proxied upstream indexes, SBOM documents and headers. A target is a `func FuzzX(f *testing.F)` in a `fuzz_test.go` file next to the code. Under `make test` the targets run over their seed corpus only, so they double as regression tests.

## Run a target locally

```sh
grep -rn --include='*_test.go' '^func Fuzz' .                     # list the targets
go test -run='^$' -fuzz='^FuzzParseTarball$' -fuzztime=60s ./pkg/npm
```

When the fuzzer finds a failing input, it writes the input to `testdata/fuzz/FuzzX/<hash>` in the package. The next plain `go test` then fails on that file.

1. Fix the code.
2. Commit the new file with the fix, as a regression seed.
3. Do not commit anything else the fuzzer generated.

## Write a good target

- Assert invariants that must always hold: round trips, size caps, no path escaping the root. "Does not panic" alone is weak.
- Stay fast, a few milliseconds per input.
- Bound memory yourself. Expand archives through the size-capped readers the production code uses.
- Keep each fuzz function in exactly one file of its package directory. The CI compiler builds all test files of a directory into one package, so a fuzz target must not depend on `package x_test` files; `build.sh` deletes those files from the image's copy of the tree.

## Fuzzing in CI

[ClusterFuzzLite](https://google.github.io/clusterfuzzlite/) fuzzes the same targets in CI with libFuzzer and AddressSanitizer, the only sanitizer it supports for Go. A new target needs no registration: `build.sh` finds every `func FuzzX(f *testing.F)` in a `*_test.go` file without build tags.

| Workflow | Trigger | Fuzzes |
|---|---|---|
| `clusterfuzzlite-pr.yml` | Pull requests touching Go files, `go.mod`/`go.sum` or `.clusterfuzzlite/` | The targets the PR can affect, 5 minutes |
| `clusterfuzzlite-batch.yml` | Daily at 02:41 UTC, manual | The full set, 1 hour |

### Pull request run

- It selects the targets the change can reach, builds only those and fuzzes them for 5 minutes in `code-change` mode, starting from the stored corpus. A run takes 8 to 15 minutes; the job times out after 30.
- A target is affected when its package, or a package it imports, contains a changed file. Non-Go files count for the closest package above them, for example seeds and embedded assets.
- A change no target sees skips the build and the run.
- A `go.mod` or `go.sum` change selects only the targets that link a package of a module whose requirement or checksum changed. A change to the `go` or `toolchain` directive, or to `.clusterfuzzlite/`, affects every target.
- At most `FUZZ_MAX_TARGETS` targets are built (30, set in the workflow; 0 means no limit). The nearest to the change win: the changed package first, then the packages importing it, then theirs. The job logs a warning with the number left out, and the daily run fuzzes those.
- The import graph is narrowed with the coverage of the last batch run. A target whose report lists no covered line of any changed Go file is dropped. New files, files the report does not know and non-Go files keep the target. Without a report the graph alone decides.
- A crash fails the check and is uploaded as an artifact. SARIF goes to code scanning for same-repository PRs.
- The run reads the [storage repository](#corpus-and-coverage-storage) anonymously, so it needs no secret and works for fork PRs.

The selection is `.clusterfuzzlite/select-targets.sh`, tested by `.clusterfuzzlite/select-targets_test.sh` in the CI job `go`. To dry-run it on a real diff:

```sh
git diff --name-only --no-renames -z origin/main... | FUZZ_MAX_TARGETS=30 bash .clusterfuzzlite/select-targets.sh
```

### Nightly run

Three jobs run in sequence on `main`. One concurrency group serializes them, so nothing else writes the storage repository meanwhile.

- `batch` builds every target, fuzzes for 1 hour and adds what it finds to the stored corpus.
- `prune` minimizes the corpus. Its 1 hour budget is shared by the targets and caps each merge; a budget that is too small fails the job with `Merging new testcases timed out`.
- `coverage` builds the coverage report that the pull request job uses to pick the affected targets.

### The build

The build is defined in [`.clusterfuzzlite/`](../../.clusterfuzzlite):

- `Dockerfile`: the OSS-Fuzz Go builder image, pinned by digest. It downloads the Go modules in a layer of its own, so a change that leaves `go.mod` and `go.sum` alone reuses it.
- `build.sh`: compiles each target with `compile_native_go_fuzzer_v2` into a binary named `<package path>_<FuzzX>`.
- `project.yaml` (`language: go`) and the `dicts/` directory.

Targets are compiled in parallel, at most `min(nproc, free memory / 1.5 GiB)` at a time. Set `FUZZ_BUILD_JOBS=<n>` to override it. The build is CPU-bound and takes 13 to 19 minutes on the 4-vCPU hosted runner whatever the number of jobs; more jobs help only on a bigger machine. Each target builds in its own scratch directory, merged into the output in sorted order. The first failure stops the remaining targets and fails the build.

## Seeds and dictionaries

The libFuzzer binaries do not read `f.Add` calls or `testdata/fuzz/`: the OSS-Fuzz Go builder replaces `*testing.F`, and its `Add` does nothing. Without help every target would start from an empty corpus. `build.sh` therefore puts two companion files next to each binary:

| File | Source | Effect |
|---|---|---|
| `<name>_seed_corpus.zip` | `<package>/testdata/fuzz/<FuzzX>/` | The fuzzer starts from these inputs |
| `<name>.dict` | `.clusterfuzzlite/dicts/<name>.dict`, else the package dictionary | libFuzzer splices these tokens into inputs |

The same seed files are the seeds of the plain `go test` run. `<name>` is `<package path with / as _>_<FuzzX>`, for example `pkg_npm_FuzzParseTarball`. The package dictionary is `.clusterfuzzlite/dicts/<package path with / as _>.dict`.

To add seeds, put files in the target's `testdata/fuzz/FuzzX/` directory in the `go test fuzz v1` format. This is the format the Go fuzzer writes when it finds a failure.

- Supported values are `string`, `[]byte`, `bool`, `intN`, `uintN`, `int`, `uint` and `floatN`, written in plain decimal.
- `build.sh` fails on any other form (`byte(...)`, `rune(...)`, hexadecimal) instead of dropping the seed silently.
- Keep seeds small, one per feature of the format.

A dictionary is a plain libFuzzer `.dict` file: one `"token"` per line (`\xNN` escapes allowed, 1 to 64 bytes), `#` comments, the SPDX header on top. Add tokens that the parser matches literally and that a mutator would take long to invent. Dictionaries are shared by package; add a per-target file only when the target needs different tokens.

## Corpus and coverage storage

ClusterFuzzLite keeps the corpus and the coverage reports in a git repository, [`openartifact/openartifact-fuzz-storage`](https://github.com/openartifact/openartifact-fuzz-storage), instead of in GitHub Actions artifacts that expire.

- Branch `main`: the corpus, `corpus/<target>/<input>`, written by the `batch` and `prune` jobs.
- Branch `gh-pages`: the coverage report, `coverage/`, written by the `coverage` job. The PR job reads it to learn which targets cover the lines a PR touches.

The repository is public, so the PR workflow reads it without credentials, also for fork PRs. Only `clusterfuzzlite-batch.yml` writes to it, through the `FUZZ_STORAGE_TOKEN` repository secret.

- The token is a fine-grained personal access token with resource owner `openartifact`, access to the `openartifact-fuzz-storage` repository only and the permission *Contents: read and write*.
- Every job of that workflow first checks the token against the GitHub API. It fails with an explicit message when the token is missing, holds whitespace (set it with `printf %s`), is rejected (expired, revoked, not granted to the repository) or cannot push.
- The check exists because ClusterFuzzLite only logs a failed upload and carries on. A bad token would otherwise cost a whole run and leave the storage empty.
- The PR job keeps working without the corpus; it then fuzzes every target from the seeds alone.
- Each upload is a commit and the corpus grows. When the repository gets large, squash its history: ClusterFuzzLite only uses the latest content.

## Run the CI binaries locally

To build and run the exact CI binaries (docker or podman):

```sh
docker build -f .clusterfuzzlite/Dockerfile -t oa-cfl .
mkdir -p out && docker run --rm -e FUZZING_ENGINE=libfuzzer -e SANITIZER=address -e ARCHITECTURE=x86_64 \
  -e FUZZING_LANGUAGE=go -e PROJECT_NAME=openartifact -v "$PWD/out:/out" oa-cfl compile
mkdir -p corpus && unzip -q -o out/pkg_npm_FuzzParseTarball_seed_corpus.zip -d corpus
./out/pkg_npm_FuzzParseTarball corpus -dict=out/pkg_npm_FuzzParseTarball.dict -max_total_time=60
```

- With rootless podman add `--pids-limit=-1` to `run`: the parallel build starts more processes than podman's default limit allows.
- Pass `-e FUZZ_BUILD_JOBS=2` to limit the parallelism on a small machine.
- Use `SANITIZER=coverage` to build the binaries of the `coverage` job instead.

### Reproduce a CI crash

A crash artifact from CI reproduces with `./out/<binary> <crash-file>`.

To keep it as a regression seed, add the decoded arguments as a `testdata/fuzz/FuzzX/` file in the `go test fuzz v1` form. The artifact holds the raw libFuzzer bytes, which the OSS-Fuzz shim splits into the arguments of the fuzz function: fixed-size values first, then one weight byte per `string` or `[]byte` argument, then their data.
