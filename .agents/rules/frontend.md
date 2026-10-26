---
description: "OpenArtifact web UI conventions: API client, i18n, dates, format colours and icons, install snippets, style limits"
globs:
  - web/**
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Frontend (web/)

- **Frontend:** all API calls go through `web/src/api/client.ts` `request()` (handles CSRF header `X-CSRF-Token` from `oa_csrf` cookie and 401). No raw `fetch` for mutations. Strings go through `t()` / `tp()` / `tpn()` from `web/src/i18n`; English lives in `web/src/i18n/locales/en/*.json`, the other languages are translations edited by pull request (see `docs/development/translations.md`). Format icons come from `simple-icons` via `web/src/lib/formats.ts`; display formats with `FormatBadge`.
  - Layering: pages and components never hold `/api/v1/...` URLs; requests live in typed `web/src/api/*.ts` functions. Placeholders in catalog strings are filled with `tp(key, { name })`, never `.replace('{name}', ...)`. Keep pages and components under about 400 lines: static data goes to `web/src/lib/`, state and actions to `web/src/composables/`.
  - Tabs: a tab panel other than the default one is loaded with `asyncTab(() => import('./XTab.vue'))` (`web/src/lib/asyncTab.ts`, skeleton fallback) so the page chunk carries only what is visible. English is bundled and eager (`t()` is synchronous); every other language is a separate `locale-<tag>` chunk loaded before the app renders in it (`setLocale`), so a language switch never flashes keys.
  - Dates: `formatDate`/`formatDateTime` from `web/src/lib/datetime.ts`; no per-page `toLocaleDateString` helpers. Numbers go through `formatNumber` and user-visible name sorting through `compareText` (`web/src/lib/intl.ts`), never a hard-coded locale or `localeCompare`.
  - i18n: no `t()` at module scope (the catalog changes with the language; the `moduleEval` test fails on it), a plural is `<key>_one` / `<key>_other` read with `tpn(key, n)`, and a new key goes into the English catalog only. `cd web && bun run i18n:check` must pass; it also fails on English keys no code uses.
  - Format colours: `formatColor`/`formatColorLowOnDark` for badges, `chartColor` (`CHART_PALETTE`) for charts; never hardcode hex per format.
  - Repo URLs and client snippets: `repoBaseUrl` / `clientSetup` in `web/src/lib/installCommands.ts`. Snippets must match real server routes (check `cmd/server/main.go` and the format handler), not another product's conventions.
  - Style: no gradients, glows, `backdrop-blur`, or letter-spaced uppercase labels; dashboards show only data the API actually returns.
  - PWA: the service worker (`web/src/pwa/sw.ts`, built to `dist/sw.js` by `web/vite.pwa.ts`) precaches only hashed assets and the static shell and never answers `/api`, `/repos`, `/v2`, `/metrics`, `/health`, MCP or auth/SSO paths; that policy lives in `web/src/pwa/swRoutes.ts` (unit-tested), so a new top-level server route that returns data must be added to its `BYPASS_PREFIXES`. Updates are offered by a toast (`pwa/register.ts`), never auto-applied. Icons are generated with `web/scripts/generate-pwa-icons.sh`.
