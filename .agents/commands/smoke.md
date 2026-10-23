---
description: Run an isolated end-to-end stack (own compose project and port) and exercise the changed path with the real client
argument-hint: "<topic> [what to exercise]"
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

Smoke-test this on an isolated stack: $ARGUMENTS

The first word is the topic (compose project `oa-<topic>`); the rest says which path to exercise. Run from the worktree that holds the change. Do not touch other compose projects or the default `:8080` stack.

## 1. Start

Pick a free port (`ss -ltn | grep :1808`; use `18080` or another unused one) and an env file:

```sh
test -f docker/.env || cp docker/.env.example docker/.env      # set POSTGRES_PASSWORD (openssl rand -hex 24); never commit it
export OPENARTIFACT_PORT=18080 BASE_URL=http://127.0.0.1:18080
# No `docker login dhi.io`? use the anonymous runtime:
export OPENARTIFACT_RUNTIME=distroless
docker compose -p oa-<topic> -f docker/docker-compose.yml --env-file docker/.env up -d --build
```

`BASE_URL` is only passed to the server if it is set in the environment/`docker/.env`. Wait until `curl -fsS http://127.0.0.1:18080/health` returns 200 (`docker compose -p oa-<topic> ... ps` shows healthy).

## 2. Bootstrap admin and token

```sh
docker compose -p oa-<topic> -f docker/docker-compose.yml logs openartifact | grep -A3 'OPENARTIFACT BOOTSTRAP'
export OA_PASS='<bootstrap password>' OA_NEW_PASS='<a new password, 12+ chars>'
TOKEN=$(scripts/dev-token.sh --change-password http://127.0.0.1:18080)
```

`scripts/dev-token.sh` logs in, performs the forced password change, and mints an access token (`POST /api/v1/auth/tokens` with session cookie + `X-CSRF-Token`); it prints only the token. Keep it in a shell variable; do not echo it into logs or commit it. A second run needs `OA_PASS` = the new password.

## 3. Exercise the changed path

Create what you need through the API with `Authorization: Bearer $TOKEN` (`POST /api/v1/repositories`, see `docs/integrations/api.md`) and use the real client for the format under test: `npm publish`/`npm install`, `pip`/`twine`, `mvn deploy`, `docker push/pull`, `helm`, `cargo`, `go` with GOPROXY, `curl -T` for raw, `apt`/`dnf`, or the `oa` CLI. Check the UI page when the change is in `web/`. Assert observable behavior (status codes, installed package contents, list/search results), and check `docker compose -p oa-<topic> ... logs openartifact` for errors.

## 4. Tear down (always, also on failure)

```sh
docker compose -p oa-<topic> -f docker/docker-compose.yml --env-file docker/.env down -v
```

Report: the commands actually run, what each returned, and any defect found (with the log line). Remove anything you created outside the project (temp dirs, local npm/pip configs).
