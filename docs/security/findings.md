<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Security findings

One list of what the [scans](scanning.md) found, across all repositories and all four finding types (vulnerabilities, licenses, secrets, AI analysis), plus a security health summary for administrators and a counts-only summary per repository or package. They read the stored scan results: nothing here starts a scan.

| Route | Who | Returns |
|---|---|---|
| `GET /api/v1/security/findings` | global admins, project maintainers and owners | the [findings list](#the-findings-list) |
| `GET /api/v1/security/findings/summary` | the same | counts per type for the same scope |
| `GET /api/v1/security/findings/export` | the same | [CSV, JSON or VEX](#exporting) |
| `GET /api/v1/security/health` | global admins | [scanner and integration health](#health) |
| `GET /api/v1/repositories/{name}/security/summary` | anyone who can read the repository | [counts only](#the-repository-summary) |

## Who sees what

- **Global admins** see the findings of every repository.
- **Project maintainers and owners** see the findings of the repositories of the projects where they hold one of those roles, nothing else. The role is the effective one: recorded ownership, membership and group grants.
- **Developers, viewers, users without a project role, anonymous callers, scoped tokens, deploy tokens and project-token service accounts** get `403 forbidden` on the findings routes. They can read the counts of a repository they may read through the [repository summary](#the-repository-summary).
- `/security/health` needs the global `manage` permission; a maintainer gets `403`.

The scope is applied inside the query. The `repo` and `project` filters narrow it and never widen it: asking for a repository outside the scope returns an empty list, not an error.

## What a finding is

A finding is **one row per stored scan result**, that is per package version and type, not per CVE. Open the row's scan (`GET …/vuln/scans/{id}` and the equivalents) for the individual vulnerabilities. A result that reports nothing is not a finding and is not listed or counted.

| Type | It is a finding when | Counts |
|---|---|---|
| `vuln` | the scan found at least one vulnerability | the stored critical, high, medium and low counts. Only the newest scan of an artifact counts: an artifact scanned by several scanner sets is one finding |
| `license` | a blocking [license policy](scanning.md#blocking-licenses) quarantined it, an admin approved it after such a quarantine, or its licenses are not SPDX expressions | a blocking quarantine is one high; each unresolved license is one low |
| `secret` | the scan found at least one secret, or it is quarantined | one count per secret at its severity; an unknown severity counts as low |
| `ai` | the analysis recommended quarantine, or requires a license review | a quarantine recommendation is one high, a license review one medium |

The row's `severity` is its worst non-zero count (`critical`, `high`, `medium`, `low`) or `none` when every count is zero. `counts` carries all four numbers.

`status` is one of:

- `quarantined`: the result's quarantine flag is set. AI findings are never `quarantined`: an AI recommendation does not quarantine anything.
- `suppressed`: a vulnerability result whose every vulnerability has a [VEX](sbom.md) decision `not_affected` or `fixed`, or a license result an admin approved.
- `open`: everything else.

`vex_status` appears on vulnerability findings that have VEX decisions. It is the least resolved one: `affected`, `under_investigation`, `fixed` or `not_affected`.

## The findings list

`GET /api/v1/security/findings` returns `{findings, has_more, next_cursor}`. A finding has `id`, `type`, `repository`, `format`, `package_name`, `package_version`, `artifact_id`, `severity`, `counts` (`critical`, `high`, `medium`, `low`), `title`, `scanner`, `status`, `quarantined`, `vex_status` (when set) and `scanned_at`. `package_name` and `package_version` are the scanned package's name and version; a scan started from a package page, which records the package row id as `artifact_id`, resolves to the package's name and version, and a package that no longer exists leaves both empty (an id is never returned as a name). The `title` names what was found: the id of the worst vulnerability of a vulnerability finding, the unresolved licenses (else the licenses) of a license finding, the description of the worst secret of a secret finding and the first line of the notes (at most 160 characters) of an AI finding.

| Query | Meaning |
|---|---|
| `type` | comma-separated list of `vuln`, `license`, `secret`, `ai`; default all four |
| `repo` | only this repository (by name) |
| `project` | only the repositories of this project (by name) |
| `severity` | `critical`, `high`, `medium` or `low`: findings whose **worst** severity is exactly that, so the numbers match the [summary](#the-summary) |
| `status` | `open`, `quarantined` or `suppressed` |
| `q` | text matched against `name@version` and the repository name (at most 255 characters) |
| `sort` | `severity` (default) or `priority`: [exploitation priority](exploit-signals.md#priority) first, then the usual order |
| `kev` | `true`: vulnerability findings with a KEV vulnerability; `false`: none |
| `epss_min` | 0 to 1: findings whose highest EPSS score is at least that |
| `priority` | `kev`, `epss`, `critical`, `high`, `medium` or `low`: findings whose priority is exactly that |
| `limit` | page size, default 50, at most 200 |
| `cursor` | the `next_cursor` of the previous page; send the same filters with it |

Findings also carry `kev_count`, `max_epss` and `priority` ([Exploitation signals](exploit-signals.md#fields)); they are `0`, `null` and the severity for findings that are not vulnerabilities. `GET /api/v1/security/exploit-feeds` reports the [feed status](exploit-signals.md#feed-status).

By default the order is fixed: worst severity first, then newest scan, then id. Paging is keyset: no row is counted and a deep page costs what the first costs. An unknown type, severity or status, an over-long value or a malformed cursor is `400 validation_failed`.

The `severity` filter is an exact match, unlike the `severity` filter of the [vulnerability overview](scanning.md#the-overview-across-repositories), which means "at least".

## The summary

`GET /api/v1/security/findings/summary` (optional `repo` and `project`) returns:

```json
{
  "by_type": {
    "vuln":    {"total": 9, "critical": 1, "high": 2, "medium": 3, "low": 3, "quarantined": 4},
    "license": {"total": 0, "critical": 0, "high": 0, "medium": 0, "low": 0, "quarantined": 0},
    "secret":  {"total": 0, "critical": 0, "high": 0, "medium": 0, "low": 0, "quarantined": 0},
    "ai":      {"total": 0, "critical": 0, "high": 0, "medium": 0, "low": 0, "quarantined": 0}
  },
  "scanned_repositories": 5,
  "total_repositories": 7,
  "last_scan_at": "2026-05-06T07:08:09Z"
}
```

Per type, `critical`, `high`, `medium` and `low` count findings by their worst severity, so they add up to `total`; `quarantined` counts the findings with the quarantine flag. Every type is present, also with zeros.

`total_repositories` counts the repositories in the scope that can hold artifacts, which is every repository but the group ones. A repository is **scanned** when at least one vulnerability, license, secret or AI scan of one of its artifacts completed (also partially); the others are **unscanned**. `last_scan_at` is when the last such scan finished, or `null`.

## Exporting

`GET /api/v1/security/findings/export?format=csv|json|vex` takes the list's filters (`type`, `repo`, `project`, `severity`, `status`, `q`; `limit` and `cursor` are ignored) and the same scope. Responses are attachments. The default format is `json`; any other value is `400 validation_failed`.

- **csv** has one row per finding with the columns `type, repository, format, package_name, package_version, artifact_id, severity, critical, high, medium, low, title, scanner, status, quarantined, vex_status, scanned_at, id`. A cell that starts with `=`, `+`, `-`, `@`, a tab or a carriage return is prefixed with `'`, so a spreadsheet does not evaluate it.
- **json** is `{"findings": [...], "truncated": false}` with the list's finding objects.
- **vex** is a CycloneDX VEX document of the **vulnerability** findings: the vulnerabilities of each selected artifact with their VEX decisions. The `type` filter is forced to `vuln`. The filters pick the artifacts, and each artifact's document holds all of its vulnerabilities. A package that appears in several repositories (the same package URL) is listed once.

At most 10000 findings are read for csv and json and at most 500 artifacts for vex. A larger selection is cut, `X-Export-Truncated: true` is set and, for json, `truncated` is `true`. Narrow the filters to export the rest.

## Health

`GET /api/v1/security/health` (global admins) returns:

```json
{
  "scanners": [
    {"name": "osv", "ready": true},
    {"name": "trivy", "ready": false, "message": "what to do to make it ready", "docs_url": "https://…"}
  ],
  "last_scan_at": "2026-05-06T07:08:09Z",
  "scans_pending": 3,
  "scans_failed_24h": 2,
  "dependency_track": {"enabled": true, "last_push_ok": false, "last_push_at": "2026-05-06T07:08:09Z"},
  "ai": {"enabled": true},
  "unscanned_repositories": 4
}
```

- `scanners` has one entry per vulnerability scanner (`osv`, `trivy`, `grype`; see [checking scanner readiness](scanning.md#checking-scanner-readiness)); `message` and `docs_url` are present when it is not ready.
- `scans_pending` counts vulnerability, license, secret and AI scans that are pending or running; `scans_failed_24h` those that failed in the last 24 hours.
- `last_scan_at` is when the last scan finished (`null` before the first), `unscanned_repositories` how many repositories have no completed scan, as [defined above](#the-summary).
- `dependency_track.enabled` is the global [Dependency-Track](dependency-track.md) switch. `last_push_ok` and `last_push_at` describe the latest push that finished (`done` or `failed`) and are absent until there is one.
- `ai.enabled` is whether [AI analysis](../integrations/ai-analysis.md) is on, stored setting over environment.

## The repository summary

`GET /api/v1/repositories/{name}/security/summary` needs read permission on the repository, which includes every project role. Optionally `package_name` narrows it to one package and `version` (only valid with `package_name`) to one version. Both are at most 255 characters. An unknown repository is `404`.

```json
{
  "scanned_at": "2026-05-06T07:08:09Z",
  "vuln": {"critical": 1, "high": 2, "medium": 3, "low": 4},
  "license": {"violations": 5},
  "secret": {"count": 6},
  "ai": {"quarantine_recommended": 7, "review_required": 8},
  "quarantined": true
}
```

It carries **numbers, a time and one flag only**: no package names, no vulnerability ids, no titles, so a viewer can see how a repository stands without the findings. `vuln` sums the counts of the repository's results; `license.violations` counts license findings, `secret.count` secret findings, `ai` the two AI outcomes. `quarantined` is `true` when at least one result in the selection is quarantined. `scanned_at` is the latest scan that finished in the selection, `null` when nothing was scanned.

Anonymous callers can read it for a public repository, the same rule as for the repository itself.
