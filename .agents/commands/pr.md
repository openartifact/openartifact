---
description: Implement a topic in its own worktree, check, open a PR and watch CI
argument-hint: "<topic> <what to implement>"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Implement as a PR: $ARGUMENTS (first word = topic slug). Read `AGENTS.md` and the matching `.agents/rules/*.md` first.

- `git fetch origin && git worktree add -b <type>/<topic> ../openartifact-<topic> origin/main`; work only there.
- Implement; new files get an SPDX header; update docs in the same change.
- Run the checks the diff needs: `make generate`, `make lint`, `make test`, web `typecheck`/`test`/`build`, `reuse lint`.
- Commit (conventional subject), `git push -u origin <branch>`, `gh pr create --fill`.
- `gh pr checks --watch`; on red, use `/ci-fix`.

Hard rules: never push `main`; never commit `web/dist`; one worktree per topic; use a roomy `TMPDIR` if `/tmp` is full.
