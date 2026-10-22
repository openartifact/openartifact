<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Agent skills

Vendored from [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) at commit `8e899e2`, MIT licensed (`LICENSES/MIT.txt`, copyright Samuel Berthe). Only `SKILL.md` and `references/` are kept; files are unmodified so updates are a plain re-copy.

| Skill | Use for |
|---|---|
| `golang-security` | Trust boundaries, SSRF, injection, XSS, secrets, resource limits |
| `golang-safety` | Nil, slice/map aliasing, numeric overflow |
| `golang-concurrency` | Goroutine lifecycle, locks, channels, worker pools |
| `golang-context` | Cancellation, deadlines, request-scoped values |
| `golang-performance` | Allocation, I/O, hot paths; optimization patterns |
| `golang-benchmark` | Benchmarks, benchstat, pprof, trace |
| `golang-database` | Queries, transactions, locks, pool, N+1 |
| `golang-observability` | Metrics, logging, tracing |
| `golang-testing` | Test design, fuzzing, races, flaky tests |
| `golang-troubleshooting` | Debugging concrete failures |

When to load them and how they rank against repo conventions: see "Review skills" in `AGENTS.md`.

Update: copy `skills/<name>/{SKILL.md,references}` from a reviewed upstream commit and bump the commit above. Do not track upstream `main` automatically.
