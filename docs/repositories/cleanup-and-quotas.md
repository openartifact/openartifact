<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Cleanup policies and storage quotas

Cleanup policies delete old package versions on a schedule; storage quotas cap the size and artifact count of a repository. Both are per repository and managed by admins through `/api/v1/repositories/{repo}/...`.

## Cleanup policies

A background worker checks every minute which enabled policies are due (their cron schedule has fired since the last run, or since creation if never run) and executes them. Each policy run takes a Postgres advisory lock, so a policy never runs twice at once, and times out after 30 minutes. The same worker also reclaims unreferenced blobs every 15 minutes.

### Supported formats

Policies delete rows from the format's package table. Supported formats: **deb, rpm, helm, pypi, nuget, go**. For any other format (maven, npm, docker, cargo, rubygems, conda, conan, raw, ivy, sbt) the API accepts a policy, but every run fails with `unsupported format for cleanup: <format>` recorded in `last_run_error`.

### Policy fields

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Required. |
| `enabled` | bool | Scheduled runs happen only when `true`. Defaults to `false` on create; manual runs ignore it. |
| `keep_last_n` | int ≥ 1 | Keep the newest N versions of each package. |
| `keep_since_days` | int ≥ 1 | Keep everything created in the last N days. Age is the row's `created_at` (for imported artifacts, the preserved original creation time; see [Nexus/Artifactory migration](../migration/nexus-artifactory.md)). |
| `keep_downloaded_since_days` | - | Rejected (`400`): download times are not tracked. |
| `name_pattern` | regex | Optional Go regular expression matched (unanchored) against the package name; only matching packages are deleted. Empty matches all. |
| `schedule_cron` | string | 5-field cron (`minute hour day-of-month month day-of-week`) or `@hourly`, `@daily`/`@midnight`, `@weekly`, `@monthly`, `@yearly`/`@annually`. Evaluated in **UTC**. Default (empty) `0 2 * * *`. An unparsable stored schedule falls back to a 24 h interval. |
| `last_run_at`, `last_run_deleted`, `last_run_error` | read-only | Outcome of the last run. |

At least one of `keep_last_n` or `keep_since_days` is required; zero or negative values are rejected (they would select everything). Violations are `400 validation_failed`.

### What gets deleted

- A version is deleted when it is outside the newest `keep_last_n` versions of its package **and** older than `keep_since_days` (a criterion that is not set does not protect anything). It is kept when either criterion protects it; `name_pattern` then restricts the selection.
- Versions are ranked per package: by name and architecture for rpm, by name, architecture and distribution for deb, by name (module path for go, package id for nuget) otherwise. A newer `arm64` build therefore never evicts the only `amd64` one. Files sharing a version (for example a PyPI sdist and wheel) count as one version.
- The database row is deleted first; each blob is then removed from storage only if no other row still references it (blobs are deduplicated across repositories).
- A package whose blob is referenced by a [release bundle](release-bundles.md) item is skipped and kept.
- There is no dry-run mode.

### API

All routes need an admin principal.

| Method and path | Purpose |
|---|---|
| `GET /api/v1/repositories/{repo}/cleanup-policies` | List (`{"policies": [...]}`). |
| `POST /api/v1/repositories/{repo}/cleanup-policies` | Create; `201`. |
| `GET /api/v1/repositories/{repo}/cleanup-policies/{id}` | Read one. |
| `PUT /api/v1/repositories/{repo}/cleanup-policies/{id}` | Partial update: only the fields sent change (an unset criterion cannot be cleared). The result must validate, except that disabling a policy is always allowed. |
| `DELETE /api/v1/repositories/{repo}/cleanup-policies/{id}` | Delete; `204`. |
| `POST /api/v1/repositories/{repo}/cleanup-policies/{id}/run` | Start an immediate asynchronous run; `202`. `409` if it is already running, `400` if the stored policy is invalid. |

```sh
curl -X POST HOST/api/v1/repositories/REPO/cleanup-policies \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"keep-10","enabled":true,"keep_last_n":10,"keep_since_days":30,
       "name_pattern":"^snapshot-","schedule_cron":"0 3 * * 0"}'

curl -X POST HOST/api/v1/repositories/REPO/cleanup-policies/<ID>/run \
  -H "Authorization: Bearer <TOKEN>"
```

## Storage quotas

Each repository has an optional quota: a maximum total size and a maximum artifact count. `0` means unlimited, which is the default for new repositories. Quotas are per repository (there is no organization-wide quota).

### Enforcement

On upload the server atomically reserves the artifact's size and one artifact slot against the repository's counters (`current_size_bytes`, `current_artifact_count`); if either limit would be exceeded the upload is refused and nothing is stored. A reservation is released again when the upload fails afterwards or the artifact is deleted.

The refusal is `507 Insufficient Storage`:

```json
{"error":"repository storage quota exceeded","code":"quota_exceeded"}
```

Deletions made by [cleanup policies](#cleanup-policies) do not decrement the usage counters (only deletes through the format or upload APIs do), so usage can drift upwards in repositories that rely on cleanup. The API cannot reset the counters; raise or remove the limits if a repository reaches its quota with fewer artifacts than counted.

Quotas are enforced on upload paths of these formats only: **Maven** (`/repos/maven/REPO`), **npm**, **Debian**, **RPM**, **Helm** and **RubyGems**. Uploads to the other formats (including Ivy and sbt, Docker, PyPI, Go, Cargo, NuGet, Conda, Conan and raw) neither check nor count against the quota.

### API

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /api/v1/repositories/{repo}/quota` | `read` on the repository | Limits and current usage. |
| `PUT /api/v1/repositories/{repo}/quota` | admin | Set `max_size_bytes`, `max_artifact_count`, `warn_threshold_percent` (partial: unset fields are unchanged). Negative limits and a threshold outside 0-100 are `400 validation_failed`. |
| `DELETE /api/v1/repositories/{repo}/quota` | admin | Reset both limits to unlimited (`204`); usage counters are kept. |

```sh
curl -X PUT HOST/api/v1/repositories/REPO/quota \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"max_size_bytes":10737418240,"max_artifact_count":5000}'
```

Response fields: `id`, `repository_id`, `max_size_bytes`, `max_artifact_count`, `warn_threshold_percent` (default 80), `current_size_bytes`, `current_artifact_count`, `usage_percent` (of the size limit; 0 when unlimited), `is_exceeded` (true once either current value reaches its limit), `created_at`, `updated_at`.

`warn_threshold_percent` is stored and returned; the server takes no action on it.
