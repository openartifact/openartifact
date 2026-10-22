<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Cleanup policies and storage quotas

Cleanup policies remove old package versions on a schedule (into the [trash](../operations/trash.md) by default); storage quotas cap the size and artifact count of a repository. Both are per repository and managed by admins through `/api/v1/repositories/{repo}/...`.

## Cleanup policies

A background worker checks every minute which enabled policies are due (their cron schedule has fired since the last run, or since creation if never run) and executes them. Each policy run takes a Postgres advisory lock, so a policy never runs twice at once, and times out after 30 minutes. The same worker also reclaims unreferenced blobs every 15 minutes.

### Supported formats

Policies select rows from the format's package table. Supported formats: **deb, rpm, helm, pypi, nuget, go**. For any other format (maven, npm, docker, cargo, rubygems, conda, conan, raw, ivy, sbt) the API accepts a policy, but every run fails with `unsupported format for cleanup: <format>` recorded in `last_run_error`. Docker has its own [registry garbage collection](#docker-registry-garbage-collection).

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
- Selected versions go through the same path as an API delete. With the default [trash](../operations/trash.md) retention they are **moved to the trash** (`deleted_at` is stamped; the audit entry says `moved to trash`): they disappear from the repository like any deleted item, can be restored, and keep their blob until the purge worker removes them. When the repository's `trash_retention_days` is `0` the row is deleted at once and each blob is removed from storage only if no other row still references it (blobs are deduplicated across repositories).
- A repository with `immutable_releases` enabled never loses a released version to a policy: release versions are skipped (see [immutable releases](../operations/trash.md#immutable-releases)); only pre-releases and snapshots are eligible.
- A package whose blob is referenced by a [release bundle](release-bundles.md) item is skipped and kept.
- For deb, rpm and helm repositories, whose uploads count against the [storage quota](#storage-quotas), every row deleted permanently also gives its usage (its blob's size and one artifact) back to the repository's counters, in one update at the end of the run. Rows moved to the trash keep counting until they are purged (see [trash and quotas](#trash-and-quotas)). A row that an API delete removed between selection and deletion is skipped, so nothing is released twice. pypi, nuget and go uploads are not counted, so their runs release nothing.
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

## Docker registry garbage collection

Deleting a Docker tag (a tag moved to the [trash](../operations/trash.md) included) leaves its manifest, and the layers only that manifest used, in storage. A separate worker reclaims them: it deletes untagged manifests older than a grace period (keeping children of tagged indexes and subject/referrer pairs), then the blob records no manifest references any more, freeing the storage when no other repository uses it.

| Setting | Environment variable | Default |
|---|---|---|
| `workers.dockerGcInterval` | `OPENARTIFACT_DOCKER_GC_INTERVAL` | `1h` (`0` disables the schedule) |
| `workers.dockerGcGracePeriod` | `OPENARTIFACT_DOCKER_GC_GRACE_PERIOD` | `24h` |

An admin can run it on demand, with a dry run, through `POST /api/v1/repositories/{repo}/registry-gc` (body `{"dry_run": bool, "grace_period_seconds": int}`, both optional; `409` while a run is in progress, `400` for a non-Docker repository). What is kept, the safety guarantees and the request and response format are in [Docker / OCI: garbage collection](../formats/docker.md#garbage-collection).

Docker pushes are not counted against the repository quota (see [enforcement](#enforcement)), so registry garbage collection does not change the usage counters.

## Storage quotas

Each repository has an optional quota: a maximum total size and a maximum artifact count. `0` means unlimited, which is the default for new repositories. Quotas are per repository (there is no organization-wide quota).

### Enforcement

On upload the server atomically reserves the artifact's size and one artifact slot against the repository's counters (`current_size_bytes`, `current_artifact_count`); if either limit would be exceeded the upload is refused and nothing is stored. A reservation is released again when the upload fails afterwards or the artifact is deleted.

The refusal is `507 Insufficient Storage`:

```json
{"error":"repository storage quota exceeded (bytes of trashed items count until purged; see the repository trash)","code":"quota_exceeded"}
```

Usage is the sum, over the repository's packages, of each package's blob size, plus one artifact per package. It is given back when a package is **permanently** deleted through the format or upload API (retention `0`, or when the [trash](#trash-and-quotas) purge worker removes it), when an upload is replaced (Maven snapshot overwrite) and when a [cleanup policy](#cleanup-policies) permanently deletes it. A blob that is shared by several packages (blobs are deduplicated) counts once per package, the same way the upload reserved it. A repository in the trash keeps its counters until it is purged, which removes them with it.

Quotas are enforced on upload paths of these formats only: **Maven** (`/repos/maven/REPO`), **npm**, **Debian**, **RPM**, **Helm** and **RubyGems**. Uploads to the other formats (including Ivy and sbt, Docker, PyPI, Go, Cargo, NuGet, Conda, Conan and raw) neither check nor count against the quota, and their counters stay at 0. A yanked RubyGems version keeps counting: yanking hides it but the file is stored until the repository is deleted.

### Trash and quotas

Soft-deleted bytes keep counting against the quota until purge: moving a package to the trash does not release its reservation, so deleting to make room does not work while the retention is greater than 0. Reconciliation counts trashed rows too. The purge worker (or an admin's manual purge) releases the usage when the row is finally deleted; restoring a package needs no new reservation. To free quota immediately, empty the repository trash (`DELETE /api/v1/repositories/{name}/trash`, repository `manage` or admin; `oa trash purge --repo NAME --all`), purge a single item from the trash (`DELETE /api/v1/trash/{kind}/{id}`, admin) or set `trash_retention_days` to `0` on the repository. Details: [Trash](../operations/trash.md#quota).

### Keeping the counters correct

The counters are updated incrementally with `synchronous_commit=off` (see [performance](../operations/performance.md#storage-quota)), so a PostgreSQL crash can lose the last few hundred milliseconds of changes, and backups do not include quota rows. Usage is therefore **reconciled**: recomputed from the package rows themselves (trashed rows included) and written over the counters, limits untouched.

- The cleanup worker reconciles every repository on its first tick after the server starts and then every 6 hours. A pass that finds drift logs `corrected drifted quota usage counters` with the number of repositories. Replicas take a database advisory lock, so only one recounts at a time.
- A restore from a [backup](../operations/backup.md) reconciles when it finishes, because restored packages were not reserved against any quota.
- An admin can reconcile one repository on demand with `POST /api/v1/repositories/{repo}/quota/reconcile`. The response is the quota, as for `GET`, with the recomputed `current_size_bytes` and `current_artifact_count`. A repository without a quota row gets an unlimited one.

The recount is a single statement over one snapshot of the tables. An upload or delete that is in flight at that moment (reserved but not yet stored, or removed but not yet released) can leave the result off by that one operation until the next reconcile.

### API

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /api/v1/repositories/{repo}/quota` | `read` on the repository | Limits and current usage. |
| `PUT /api/v1/repositories/{repo}/quota` | admin | Set `max_size_bytes`, `max_artifact_count`, `warn_threshold_percent` (partial: unset fields are unchanged). Negative limits and a threshold outside 0-100 are `400 validation_failed`. |
| `DELETE /api/v1/repositories/{repo}/quota` | admin | Reset both limits to unlimited (`204`); usage counters are kept. |
| `POST /api/v1/repositories/{repo}/quota/reconcile` | admin | Recompute the usage counters from the package rows (see [above](#keeping-the-counters-correct)); returns the quota. Limits are unchanged. |

```sh
curl -X PUT HOST/api/v1/repositories/REPO/quota \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"max_size_bytes":10737418240,"max_artifact_count":5000}'
```

Response fields: `id`, `repository_id`, `max_size_bytes`, `max_artifact_count`, `warn_threshold_percent` (default 80), `current_size_bytes`, `current_artifact_count`, `usage_percent` (of the size limit; 0 when unlimited), `is_exceeded` (true once either current value reaches its limit), `created_at`, `updated_at`.

`warn_threshold_percent` is stored and returned; the server takes no action on it.
