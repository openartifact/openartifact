---
description: Run an isolated compose stack and exercise the changed path with the real client
argument-hint: "<topic> [what to exercise]"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Smoke-test: $ARGUMENTS (first word = topic). Run from the worktree holding the change.

- `OPENARTIFACT_RUNTIME=distroless OPENARTIFACT_PORT=<free port> docker compose -p oa-<topic> -f docker/docker-compose.yml --env-file docker/.env up -d --build` (drop the runtime if `dhi.io` is logged in).
- Read the bootstrap password from `docker compose -p oa-<topic> ... logs openartifact`.
- `OA_PASS=… OA_NEW_PASS=… scripts/dev-token.sh --change-password http://127.0.0.1:<port>` prints a token.
- Create a repo via the API and exercise the changed path with the real client (`npm`, `pip`, `docker`, `curl -T`, `oa`, …); check logs.
- Always tear down: `docker compose -p oa-<topic> ... down -v`.

Hard rules: never touch other compose projects or the default :8080 stack; never commit `docker/.env` or tokens; never push `main`.
