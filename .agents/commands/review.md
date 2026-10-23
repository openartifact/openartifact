---
description: Review a PR, branch or commit range with read-only reviewers split by area
argument-hint: "<PR number | branch | base..head>"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Review this change: $ARGUMENTS

Read-only: do not edit, commit or push anything in the reviewed checkout.

## 1. Resolve the diff

- PR number: `gh pr view <n> --json baseRefName,headRefName,title,body` and `gh pr diff <n>` (or `pr://<n>/diff`).
- Branch: `git diff origin/main...<branch>`.
- Range: `git diff <base>..<head>`.

If a worktree is needed to read files at the reviewed revision, create a temporary one (`git worktree add --detach ../openartifact-review-<id> <rev>`) and remove it afterwards; never switch another checkout.

## 2. Split by area and fan out

Group changed files by area and spawn one read-only reviewer (`reviewer` agent) per non-empty area in a single batch. Tell each one the diff range, its file list, to read `AGENTS.md` and the matching `.agents/rules/*.md`, and to load the skills below (review mode only; AGENTS.md wins on conflict: errors through `writeError`, sqlc/pgx, standard `testing`).

| Area (paths) | Skills |
|---|---|
| auth, RBAC, projects, middleware, proxies/groups, upload and format handlers, anything serving upstream- or uploader-controlled bytes (`internal/auth`, `internal/middleware`, `internal/proxy`, `internal/group`, `internal/repodispatch`, `internal/format`, `internal/api/upload_*`) | `golang-security`, `golang-safety`, `golang-concurrency`, `golang-context` |
| workers, locks, goroutines (`internal/worker`, `internal/cron`, `internal/backup`) | `golang-concurrency`, `golang-safety`, `golang-context` |
| queries, transactions, migrations (`internal/database`, `migrations`) | `golang-database`, `golang-safety` |
| hot paths, benchmarks | `golang-performance`, `golang-benchmark` |
| metrics, logging, tracing | `golang-observability` |
| tests | `golang-testing` |
| `web/**` | `vue-best-practices`, `vue-router-best-practices`, `vue-pinia-best-practices`; also check redirects (`next`), URL/shell-snippet interpolation and stale async responses yourself |
| docs, CI workflows, config | no skill; check docs/config/`.env.example`/example YAML consistency and workflow pinning |

(`.agents/rules/review-skills.md` is the source of truth for this mapping.)

## 3. Report

Merge the reviewers' output, dedupe, verify each finding against the code yourself (read the cited lines), and drop what you cannot confirm. Output:

1. **Findings by severity** (blocker / high / medium / low): each as `path:line` — the failing scenario (inputs, who can trigger it, effect) — the concrete fix.
2. **Checked and clean**: the areas and properties examined that have no findings, so the reader knows what was covered.
3. **Not covered**: what was out of reach (no running stack, no integration DB) and what would cover it.

No praise, no style nits already enforced by `make lint`. If asked to post it, use `gh pr review` / `gh pr comment`, otherwise just report here.
