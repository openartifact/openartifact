---
description: Start a topic end to end in its own worktree, check, open a PR and watch CI
argument-hint: "<topic> <what to implement>"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Work on this request as a pull request: $ARGUMENTS

The first word is the topic slug (kebab-case); the rest is the task. Read `AGENTS.md` first and the `.agents/rules/*.md` files matching the paths you touch.

## 1. Worktree

Never work in the main checkout and never push `main`.

```sh
git fetch origin
git worktree add -b <type>/<topic> ../openartifact-<topic> origin/main   # type: feat|fix|chore|docs|refactor
cd ../openartifact-<topic>
```

If the worktree or branch already exists, reuse it (`git worktree list`) after `git fetch`; do not touch other checkouts. Set `TMPDIR` to a directory with free space if `/tmp` is full (for example `$HOME/.cache/oatmp`).

## 2. Implement

Follow the repo conventions (AGENTS.md invariants, SPDX header on every new file, docs updated in the same change, `openapi.json` + generated web types in sync). Delegate independent slices to subagents when it pays off; the main agent runs the checks once at the end.

## 3. Checks (only the ones the diff needs)

Look at `git diff --name-only origin/main` and run:

| Touched | Run |
|---|---|
| `internal/database/queries/*.sql` | `make generate` and commit the sqlc output |
| `internal/api/openapi.json` | `cd web && bun install --frozen-lockfile && bun run generate-api` and commit `web/src/api/generated` |
| any Go | `make lint` and `make test` (single package: `go test -short ./internal/<pkg>/...`); `go mod tidy -diff` if `go.mod` changed |
| `web/**` | `cd web && bun install --frozen-lockfile && bun run typecheck && bun test && bun run build && bun run ../web/scripts/check-bundle-size.js` |
| `.github/workflows/**` | `go tool -modfile=tools/go.mod actionlint` |
| a package-format or protocol path | the `/smoke` flow with the real client |
| anything | `reuse lint` |

`bun run build` writes `web/dist`; it is git-ignored. **Never commit `web/dist`** (only `web/dist/.gitkeep` is tracked).

## 4. Commit, push, PR

- Conventional commit subject (`feat(scope): ...`); stage explicit paths, check `git status` shows no `web/dist`, no `.env`, no tokens.
- `git push -u origin <type>/<topic>`
- `gh pr create --fill` with a body: what changed, why, how it was verified (commands actually run), docs touched.

## 5. Watch CI

`gh pr checks --watch`. If a check fails, follow `/ci-fix` (classify environment vs real failure, fix in this worktree, rerun the equivalent command locally, push). Report the PR URL and the final check state.
