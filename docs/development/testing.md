<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Black-box and property tests

Besides the Go unit and integration tests (see the [development guide](README.md)), four suites exercise a running server or pure frontend functions. All of them are cheap to run locally except where noted; CI is the reference run.

| Suite | CI job (workflow) | When | Local entry point |
|---|---|---|---|
| API fuzzing (Schemathesis) | `api-fuzz` (`api-fuzz.yml`) | PRs (bounded, fixed seed), nightly (bigger, random seed) | `scripts/api-fuzz.sh` |
| End to end (Lightpanda over CDP) | `e2e` (`e2e.yml`) | PRs and `main` | `cd web && bun run e2e` |
| Visual regression and colour contrast (Chromium) | `e2e` (`e2e.yml`, extra steps) | PRs and `main` | `scripts/e2e-chromium.sh start`, then `cd web && bun run e2e:chromium` |
| Monkey walk | `monkey` (`e2e.yml`) | nightly, manual dispatch with `seed`/`steps` | `cd web && bun run monkey` |
| Property tests (fast-check) | `web build` (`ci.yml`) | every `bun test` | `cd web && bun test src/lib/__tests__/*.property.test.ts` |

## Test server

`scripts/testenv.sh start` starts a built server against a Postgres, seeds it and writes `$OA_TESTENV_DIR/env`. The first admin comes from `bootstrap.adminPasswordFile` and `bootstrap.adminTokenFile`; the script also creates the user `dev` (read, write and delete on every repository, no `manage`: the developer role) and the repositories `seed-docker`, `seed-helm`, `seed-maven`, `seed-npm` (public), `seed-npm-private`, `seed-pypi` and `seed-raw` (public, one file).

```sh
podman run -d --name oa-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=oa -p 55999:5432 docker.io/postgres:18-alpine
go build -o "$TMPDIR/oa-server" ./cmd/server          # e2e needs the SPA first: cd web && bun run build
export OA_BIN=$TMPDIR/oa-server OA_TESTENV_DIR=$TMPDIR/te OA_PORT=18080 \
  DB_HOST=127.0.0.1 DB_PORT=55999 DB_PASSWORD=pw DB_NAME=oa
scripts/testenv.sh start && . "$OA_TESTENV_DIR/env"    # OA_URL, OA_ADMIN_TOKEN, OA_ADMIN_PASS, OA_DEV_USER, OA_DEV_PASS
scripts/testenv.sh stop
```

The script wipes `$OA_TESTENV_DIR` but not the database: drop and recreate it between runs (the seed fails on an existing user).

## API fuzzing

`scripts/api-fuzz.sh [pr|nightly]` runs Schemathesis (version pinned, container image by digest) against `internal/api/openapi.json` in three passes: admin (Bearer token), developer (no admin route may answer 2xx) and anonymous (only operations the spec marks `security: []` and public-repository reads may answer 2xx). Checks: `not_a_server_error`, `response_schema_conformance`, `status_code_conformance`, plus the authentication checks in `tests/apifuzz/hooks.py`. Operations that would break the run (restore, logout, password change) or call out to the network (webhook, storage, SSO and AI tests) are disabled in `tests/apifuzz/schemathesis.toml`, each with a reason; known deferred issues live in the same file.

```sh
. "$OA_TESTENV_DIR/env"
SCHEMATHESIS='uvx schemathesis@4.32.5' OA_FUZZ_EXAMPLES=5 scripts/api-fuzz.sh   # quick, no container
scripts/api-fuzz.sh nightly                                                        # full budget, container
OA_FUZZ_SEED=123 scripts/api-fuzz.sh nightly                                       # replay a nightly failure
```

A failure prints the seed and a curl reproduction; the CI artifact `api-fuzz-report` has the reports and the server log. A finding is either a server bug (fix it with a Go regression test) or a spec mismatch (fix `openapi.json`, then `cd web && bun run generate-api` and `make docs-gen`).

## End to end and monkey walk

The browser is [Lightpanda](https://github.com/lightpanda-io/browser) (headless, no rendering), driven over CDP by `puppeteer-core` under `bun test`; no browser is downloaded. The binary is pinned by version and sha256 in `.github/workflows/e2e.yml`.

```sh
lightpanda serve --host 127.0.0.1 --port 9222 --load-resources iframe --load-resources stylesheet \
  --timezone UTC --locale en-US --watchdog-ms 30000 --http-timeout 15000 --cdp-max-message-size 8388608 &
. "$OA_TESTENV_DIR/env"        # server built with the SPA, see above
cd web
bun run e2e                                      # specs in web/e2e/specs, about 30 s
MONKEY_SEED=3 MONKEY_STEPS=300 bun run monkey    # seeded random walk; seed is printed and written to web/monkey-seed.txt
```

`LIGHTPANDA_CDP_URL` overrides `http://127.0.0.1:9222`. A failing test writes `web/test-results/<test>/` (`failure.txt` with the error, URL, page errors and `LP.getPageStatus`, `console.log`, `page.html`); CI uploads it as `e2e-failure` or `monkey-failure`. The e2e specs cover login, repository creation, a real `npm publish` with an API token, the install command, quarantine blocking downloads, legacy URL redirects, role visibility (admin, developer, anonymous), breadcrumbs, and an axe-core scan of every visited page in light and dark theme (serious and critical impacts fail). The monkey walk picks links and safe controls with a seeded PRNG as admin and as developer and fails on uncaught exceptions, console errors, 5xx responses, or reaching the not-found page through an in-app link; destructive controls are on a deny-list (`web/e2e/monkey.e2e.ts`). The replay is exact only when the data and timing match the original run.

Writing specs: use `testAs(role)` for a signed-in page, the locator helpers in `web/e2e/lib/` (`getByRole`, `getByTestId`, `expectLoc`, `eventually`); wait on selectors, URLs or conditions, never fixed sleeps; assert the expected URL after each navigation (Lightpanda can fall back to `about:blank`, [#3395](https://github.com/lightpanda-io/browser/issues/3395)), and wait for the whole expected location in one `expectUrl` call (path, query and hash): `shellReady` does not wait for the router, the header mounts before a legacy redirect or a lazy page has settled, and the old URL can already satisfy a partial check such as the hash; avoid drag and drop, contenteditable and `scrollIntoView` in nested scroll containers.

### Visual regression and colour contrast (Chromium)

Lightpanda paints nothing, so two kinds of check run in a real Chromium instead: pixel snapshots of the app shell (`web/e2e/chromium/visual.chromium.ts`) and the axe `color-contrast` rule (`contrast.chromium.ts`). They share the built server and seed of the `e2e` job and add about 40 s to it. The browser is the `chromedp/headless-shell` container pinned by tag and digest in `scripts/e2e-chromium.sh`, which CI and a local run both use, so the same engine, fonts and software rasteriser paint both. The specs live outside `specs/` and are named `*.chromium.ts`: `bun run e2e` never runs them.

```sh
scripts/e2e-chromium.sh start        # podman or docker; listens on 127.0.0.1:9223 (CHROMIUM_CDP_URL overrides)
. "$OA_TESTENV_DIR/env"              # the server must run on the host, built with the SPA
cd web
bun run e2e:chromium                 # snapshots and contrast, both themes
bun run e2e:visual:update            # rewrite the baselines after an intended change of the look
scripts/e2e-chromium.sh stop
```

- **Snapshots** cover the expanded sidebar, the collapsed rail, a rail tooltip, the header and a toast, each in the light and the dark theme, at 1366x768. Baselines are the PNGs in `web/e2e/chromium/__screenshots__`; review the changed images in the pull request like any other diff. The comparison (pixelmatch, `TOLERANCE` in `web/e2e/lib/visual.ts`) allows a per-pixel colour distance of 0.1 and 0.1 % of the pixels to differ: a stray glyph edge passes, a moved icon or a changed token does not. A failure writes `expected`, `actual` and `diff` PNGs to `web/test-results/visual/`, which CI uploads as `e2e-failure`. Animations and transitions are switched off and the web fonts awaited before a shot, and the pointer is parked away from every control. `expectMatchesBaseline` takes the shot itself, with `captureBeyondViewport: false`: puppeteer's default for a clipped or element shot makes Chromium resize the view to 1x1 for a moment, and a frame that lands in that window moves the pointer out of the page, so the open tooltip closes before the pixels are read (rare, and only on a loaded machine: it made `shell-rail-tooltip-*` fail in CI). Do not call `screenshot()` directly in a snapshot spec.
- **Contrast** scans the login page, the shell (expanded and collapsed rail), the dashboard, the repository list and detail, an artifact, the security page and the developer dashboard in both themes, plus three overlays while open (rail tooltip, user menu, toast). A failure names the element, the measured ratio, the ratio required and both colours. Fix the colour in the tokens of `web/src/style.css`, not in a component. A rule that needs layout (`target-size`, `scrollable-region-focusable`, `link-in-text-block`) is still skipped; only `color-contrast` moved to Chromium.
- After bumping the headless-shell tag, refresh the baselines (`bun run e2e:visual:update`) and commit them.

### Lightpanda gaps (1.0.2) and how the suites work around them

- **No layout or rendering.** No screenshots or visual checks; coordinate clicks are unreliable ([#4029](https://github.com/lightpanda-io/browser/issues/4029), [#4171](https://github.com/lightpanda-io/browser/issues/4171)), so clicks are DOM-level `element.click()`. axe-core runs without the layout-dependent rules `color-contrast`, `color-contrast-enhanced`, `target-size`, `scrollable-region-focusable` and `link-in-text-block`: colour contrast is covered by the Chromium specs below instead.
- **CDP message limit of 1 MB** breaks injecting axe-core (about 1.5 MB); raised with `--cdp-max-message-size` ([PR #2760](https://github.com/lightpanda-io/browser/pull/2760)).
- **One browser context per connection** ([#3076](https://github.com/lightpanda-io/browser/issues/3076), [#3182](https://github.com/lightpanda-io/browser/issues/3182)) and **one page per context** ([#1962](https://github.com/lightpanda-io/browser/issues/1962)): the suites run serially and close each context before opening the next; Playwright's own runner and `AxeBuilder.analyze()` need more and do not work.
- Playwright on Bun fails the CDP WebSocket handshake ([#2019](https://github.com/lightpanda-io/browser/issues/2019)), hence `puppeteer-core`.

## Property tests

`web/src/lib/__tests__/*.property.test.ts` use [fast-check](https://fast-check.dev) with a modest default of 100 runs: install commands (shell quoting, URLs under the repository base URL, every format), breadcrumb resolution for every route, URL state round trips, audit subjects, version comparison and grouping, and safe redirects. `FC_RUNS=5000` raises the budget; a failure prints the seed, replay it with `FC_SEED=<seed>`. Properties exclude inputs the server rejects (for example dot-segment repository names), each noted in the test.
