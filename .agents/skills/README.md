<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Agent skills

Vendored, unmodified, so updates are a plain re-copy:

- `golang-*`: [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) at commit `8e899e2`, MIT (`LICENSES/MIT.txt`, copyright Samuel Berthe). Only `SKILL.md` and `references/` are kept.
- `vue-*`: [vuejs-ai/skills](https://github.com/vuejs-ai/skills) at commit `f5d4b3b`, MIT (`LICENSES/MIT.txt`, copyright hyf0, SerKo). Whole skill directories are kept.

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
| `vue-best-practices` | Vue 3 Composition API, reactivity, components |
| `vue-router-best-practices` | Route guards, navigation, params |
| `vue-pinia-best-practices` | Pinia stores, state reset |

When to load them and how they rank against repo conventions: see "Review skills" in `AGENTS.md`.

Update: copy the skill from a reviewed upstream commit and bump the commit above. Do not track upstream `main` automatically.
