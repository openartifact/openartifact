---
description: "Which vendored review skills (.agents/skills) to load for which kind of code or review"
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Review skills

`.agents/skills/` vendors Go skills from [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) and Vue skills from [vuejs-ai/skills](https://github.com/vuejs-ai/skills) (both MIT, pinned commits in `.agents/skills/README.md`). When to load them:

- `golang-security`, `golang-safety`, `golang-concurrency`, `golang-context`: **reviews** of security-sensitive code (proxies and groups, auth/RBAC/projects, upload and format handlers, anything serving upstream- or uploader-controlled bytes) and of workers, locks and goroutines.
- `golang-performance`, `golang-benchmark`: performance reviews and before optimizing hot paths (measure first with benchmarks/pprof).
- `golang-database`: writing or reviewing queries, transactions, row locks, advisory locks and migrations-adjacent Go code.
- `golang-observability`: metrics (label cardinality), slog logging, tracing.
- `golang-testing`: writing or reviewing tests; the repo uses the standard `testing` package (no testify) and the test rules in this file.
- `golang-troubleshooting`: debugging a concrete failure (flaky test, deadlock, leak, panic), not reviews.
- `vue-best-practices`, `vue-router-best-practices`, `vue-pinia-best-practices`: writing or reviewing `web/` components, route guards and stores (Composition API with `<script setup>`). They don't cover UI security: also check redirects (`next`), URL/shell-snippet interpolation and stale async responses yourself.

Skip them for routine implementation. Use their review mode only, not their sub-agent fan-out. This file wins on conflict: errors go through `writeError` (no `samber/oops`), DB access through sqlc/pgx, tests follow the rules above, and the repo's own conventions apply.
