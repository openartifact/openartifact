---
description: Review a PR, branch or commit range with read-only reviewers split by area
argument-hint: "<PR number | branch | base..head>"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Review: $ARGUMENTS. Read-only; edit and push nothing.

- Get the diff (`gh pr diff <n>`, `git diff origin/main...<branch>` or `<base>..<head>`).
- Split files by area; spawn one `reviewer` per area in one batch, each told to read `AGENTS.md`, the matching `.agents/rules/*.md` and the skills from `.agents/rules/review-skills.md`.
- Verify every finding against the code; drop what you cannot confirm.
- Report findings by severity as `path:line`, failing scenario, fix; then areas checked clean and what was not covered.

Hard rules: never push `main`; never commit `web/dist`; use a temporary worktree (`../openartifact-review-<id>`) if you need the revision checked out, and remove it.
