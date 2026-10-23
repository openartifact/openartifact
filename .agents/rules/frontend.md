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

- **Frontend:** all API calls go through `web/src/api/client.ts` `request()` (handles CSRF header `X-CSRF-Token` from `oa_csrf` cookie and 401). No raw `fetch` for mutations. Strings go through `t()` / `web/src/i18n/en.ts`. Format icons come from `simple-icons` via `web/src/lib/formats.ts`; display formats with `FormatBadge`.
  - Dates: `formatDate`/`formatDateTime` from `web/src/lib/datetime.ts`; no per-page `toLocaleDateString` helpers.
  - Format colours: `formatColor`/`formatColorLowOnDark` for badges, `chartColor` (`CHART_PALETTE`) for charts; never hardcode hex per format.
  - Repo URLs and client snippets: `repoBaseUrl` / `clientSetup` in `web/src/lib/installCommands.ts`. Snippets must match real server routes (check `cmd/server/main.go` and the format handler), not another product's conventions.
  - Style: no gradients, glows, `backdrop-blur`, or letter-spaced uppercase labels; dashboards show only data the API actually returns.
