---
description: Diagnose and fix the failing CI checks of a PR from its own worktree
argument-hint: "<PR number>"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Fix failing checks of PR $ARGUMENTS.

- `gh pr checks $ARGUMENTS`, then `gh run view <run-id> --log-failed` for each red job.
- Classify: real (code, lint, drift, REUSE, tests) vs environment (network, registry, runner). Retry environment failures once with `gh run rerun <id> --failed`.
- Fix real ones in the PR's worktree (`git worktree list`); never skip tests or loosen gates.
- Rerun the equivalent command locally (see the `ci.yml` job), commit, push, `gh pr checks $ARGUMENTS --watch`.

Hard rules: never push `main`; never commit `web/dist`; never force-push unless asked; work in the PR's worktree.
