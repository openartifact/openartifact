---
description: Diagnose and fix the failing CI checks of a PR from its own worktree
argument-hint: "<PR number>"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Fix the failing CI checks of PR $ARGUMENTS.

## 1. Collect

```sh
gh pr view $ARGUMENTS --json headRefName,headRefOid,baseRefName,mergeStateStatus
gh pr checks $ARGUMENTS
gh run list --branch <headRefName> --limit 5
gh run view <run-id> --log-failed
```

Work only on checks that failed on the latest head commit. Locate the PR's worktree (`git worktree list`; branch = `headRefName`); if none exists, create `../openartifact-<topic>` on that branch (`git fetch origin <branch>`, `git worktree add ../openartifact-<topic> <branch>`). Never push `main`.

## 2. Classify each failure

- **Real**: reproducible from the code (compile/vet/lint error, failing or flaky-by-race test, generated-code drift, `go mod tidy -diff`, REUSE header, bundle size, actionlint/zizmor finding, Trivy HIGH/CRITICAL with a fixable package). Fix the cause; do not skip tests, loosen gates or add suppressions.
- **Environment**: runner/registry/network (docker pull rate limit, `dhi.io` login on fork PRs, GHCR/Trivy DB download, runner out of disk, timeout with a clean log tail). Re-run once with `gh run rerun <run-id> --failed`; if it fails the same way, treat it as real or report it as an infrastructure problem with the log line.
- Say which class each failure is and why, citing the log lines.

## 3. Fix and verify locally with the CI-equivalent command

| CI job | Local equivalent |
|---|---|
| `go` | `go build ./... && go vet ./... && go mod tidy -diff && go tool -modfile=tools/go.mod govulncheck ./... && go test -short -race ./...` (`make test`) |
| `integration` | `make test-integration` (Postgres/S3 env or a container runtime) |
| `lint` | `make lint` |
| `reuse` | `reuse lint` |
| `sqlc` | `make generate && git diff --exit-code` |
| `web` | `cd web && bun install --frozen-lockfile && bun run typecheck && bun run generate-api && git diff --exit-code -- src/api/generated && bun test && bun run build && bun run ../web/scripts/check-bundle-size.js` |
| `workflow-lint` | `go tool -modfile=tools/go.mod actionlint` |
| `security` | rebuild the image and read the Trivy finding; bump or replace the affected dependency |

Use `TMPDIR` on a disk with free space if `/tmp` is full.

## 4. Push and confirm

Commit with a conventional message naming the failure fixed, `git push` the PR branch (never `main`, never force-push unless the user asked), then `gh pr checks $ARGUMENTS --watch`. Report: failure → class → fix → local command that passed → final check state.
