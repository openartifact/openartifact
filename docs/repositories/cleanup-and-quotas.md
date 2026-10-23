<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Cleanup policies and storage quotas

Cleanup policies remove old package versions on a schedule (into the [trash](../operations/trash.md) by default); storage quotas cap the size and artifact count of a repository, and of a [project](../security/projects.md) as a whole. Both are set per repository (admins, or maintainers and owners of its project) through `/api/v1/repositories/{repo}/...`; a project adds [default cleanup policies](#project-default-cleanup-policies) and a [project quota](#project-quotas) through `/api/v1/projects/{project}/...`.

## Cleanup policies

A background worker checks every minute which enabled policies are due (their cron schedule has fired since the last run, or since creation if never run) and executes them. Each policy run takes a Postgres advisory lock, so a policy never runs twice at once, and times out after 30 minutes. The same worker also reclaims unreferenced blobs every 15 minutes.

### Supported formats

Policies run on **every hosted format**: deb, rpm, helm, pypi, nuget, go, npm, maven, ivy, sbt, cargo, rubygems, conda, conan, raw and docker. Proxy and group repositories are never touched (a proxy cache has its own expiry). Creating or updating a policy for a format outside this set answers `400 UNSUPPORTED_FORMAT`. What a "version" is, and what a run does besides deleting rows, depends on the format:

| Format | What is ranked and removed | After a run |
|---|---|---|
| deb, rpm, helm, pypi, nuget, go | Versions per package (see below). | deb and rpm indexes are regenerated. |
| npm | Versions per package. A version a **dist-tag** points at (`latest`, `next`, ...) is never removed, so no tag is left dangling. | A package that lost its last version is dropped; the others get a new packument validator and `latest` is recomputed. |
| maven | Releases are ranked per `groupId:artifactId`; the timestamped builds of each `-SNAPSHOT` version are ranked against each other. The **newest build of a snapshot is always kept**, so `-SNAPSHOT` keeps resolving. All files of a version (jar, pom, checksums, classifiers) count as one version. | `maven-metadata.xml` of the artifact (and of its snapshot versions) is regenerated. |
| ivy, sbt | Revisions per `organisation:module` (sbt: per group, artifact and Scala version). | Indexes are computed live. |
| cargo | Versions per crate. Cleanup **deletes** a version (to hide a version without deleting it, [yank](../formats/cargo.md) it instead). | The sparse index is computed live. |
| rubygems | Versions per gem and platform. | Specs and the compact index are computed live. |
| conda | Builds per package and platform (`version-build_string`); the `.tar.bz2` and `.conda` files of one build count together. | `repodata.json` is computed live. |
| conan | Recipe versions (`version@user/channel`) per package; all revisions of a version count together. | Computed live. |
| raw | Files ranked against the other files of **their directory**, newest first (a raw file has no version; `name_pattern` is matched against the whole path and restricts what is removed, not how files are ranked). | none |
| docker | **Tags** per image, aged by their last update (re-tagging counts as activity). | When tags were removed permanently, [registry garbage collection](#docker-registry-garbage-collection) reclaims the manifests and layers they left unreferenced (subject to its grace period). Tags moved to the trash keep their manifests until the purge. |

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
- A **quarantined** package (a scan quarantined it) is skipped and kept: cleanup never removes the evidence of a finding.
- Every row deleted permanently also gives its usage (its blob's size and one artifact) back to the repository's counters, in one update at the end of the run. Rows moved to the trash keep counting until they are purged (see [trash and quotas](#trash-and-quotas)). A row that an API delete removed between selection and deletion is skipped, so nothing is released twice. A Go module releases its zip and its `go.mod` together as one artifact.
- A [dry run](#api) lists what a run would remove, applying all the protections above, without removing anything. The policy list has a preview button (eye icon) that shows it, for repository policies and for project default policies (listed per repository).

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
| `POST /api/v1/repositories/{repo}/cleanup-policies/{id}/dry-run` | Evaluate the policy now and answer `200` with `{repository, format, dry_run, rows, items:[{id,name,version}], truncated}`: the rows a run would remove (or move to the trash), at most 500 listed. Nothing is changed. A version made of several files counts one row per file. |
| `POST /api/v1/projects/{project}/cleanup-policies/{id}/dry-run` | Same for a project default policy (owners and admins): `200` with `{dry_run, rows, repositories:[<repository dry run>]}`, one entry per repository the policy would cover |

```sh
curl -X POST HOST/api/v1/repositories/REPO/cleanup-policies \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"keep-10","enabled":true,"keep_last_n":10,"keep_since_days":30,
       "name_pattern":"^snapshot-","schedule_cron":"0 3 * * 0"}'

curl -X POST HOST/api/v1/repositories/REPO/cleanup-policies/<ID>/run \
  -H "Authorization: Bearer <TOKEN>"
```

### Project default cleanup policies

A [project](../security/projects.md) can define default cleanup policies that cover all of its repositories, so a retention rule does not have to be copied into each one. They have the same fields, validation and schedule as the repository policies above (`name`, `enabled`, `keep_last_n`, `keep_since_days`, `name_pattern`, `schedule_cron`); the response carries `project_id` and an empty `repo_id`.

A default policy applies to every live **hosted** repository of the project that has **no enabled policy of its own**. A repository's own enabled policy overrides the defaults: it replaces them, it does not add to them. Disabling or deleting the repository's policy makes the repository fall back to the defaults again. Proxy and group repositories are never touched. A run applies the policy to each covered repository with that repository's format rules, and a failure in one repository does not stop the others.

| Method and path | Who | Purpose |
|---|---|---|
| `GET /api/v1/projects/{project}/cleanup-policies` | any project member | List (`{"policies": [...]}`). |
| `POST /api/v1/projects/{project}/cleanup-policies` | project owner, admin | Create; `201`. |
| `PUT /api/v1/projects/{project}/cleanup-policies/{id}` | project owner, admin | Partial update. |
| `DELETE /api/v1/projects/{project}/cleanup-policies/{id}` | project owner, admin | Delete; `204`. |

```sh
curl -X POST HOST/api/v1/projects/PROJECT/cleanup-policies \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"keep-10","enabled":true,"keep_last_n":10}'
```

In the web UI they are in the project's **Settings** section under **Cleanup defaults**, next to a note that a repository's own enabled policy overrides them.

## Docker registry garbage collection

Deleting a Docker tag (a tag moved to the [trash](../operations/trash.md) included) leaves its manifest, and the layers only that manifest used, in storage. A separate worker reclaims them: it deletes untagged manifests older than a grace period (keeping children of tagged indexes and subject/referrer pairs), then the blob records no manifest references any more, freeing the storage when no other repository uses it.

| Setting | Environment variable | Default |
|---|---|---|
| `workers.dockerGcInterval` | `OPENARTIFACT_DOCKER_GC_INTERVAL` | `1h` (`0` disables the schedule) |
| `workers.dockerGcGracePeriod` | `OPENARTIFACT_DOCKER_GC_GRACE_PERIOD` | `24h` |

An admin can run it on demand, with a dry run, through `POST /api/v1/repositories/{repo}/registry-gc` (body `{"dry_run": bool, "grace_period_seconds": int}`, both optional; `409` while a run is in progress, `400` for a non-Docker repository). What is kept, the safety guarantees and the request and response format are in [Docker / OCI: garbage collection](../formats/docker.md#garbage-collection).

Docker pushes are counted against the repository quota (see [what counts](#what-counts)), so registry garbage collection gives back the bytes of the blobs and the manifests it deletes.

## Storage quotas

Each repository has an optional quota: a maximum total size and a maximum artifact count. `0` means unlimited, which is the default for new repositories. Quotas are set per repository; a project can additionally cap the total of its repositories (see [project quotas](#project-quotas)).

### Enforcement

On upload the server atomically reserves the artifact's size and one artifact slot against the repository's counters (`current_size_bytes`, `current_artifact_count`); if either limit would be exceeded the upload is refused and nothing is stored. A reservation is released again when the upload fails afterwards or the artifact is deleted.

The refusal is `507 Insufficient Storage`:

```json
{"error":"repository storage quota exceeded (bytes of trashed items count until purged; see the repository trash)","code":"quota_exceeded"}
```

Usage is the sum, over the repository's packages, of each package's blob size, plus one artifact per package. It is given back when a package is **permanently** deleted through the format or upload API (retention `0`, or when the [trash](#trash-and-quotas) purge worker removes it), when an upload is replaced (Maven snapshot overwrite) and when a [cleanup policy](#cleanup-policies) permanently deletes it. A blob that is shared by several packages (blobs are deduplicated) counts once per package, the same way the upload reserved it. A repository in the trash keeps its counters until it is purged, which removes them with it.

### What counts

Quotas are enforced on the upload paths of every **hosted** format: Maven (and Ivy and sbt), npm, Debian, RPM, Helm, RubyGems, PyPI, Cargo, Go, NuGet, Conda, Conan, raw and Docker. The unit is one stored row and the size of its blob:

| Format | Counted as |
|---|---|
| Maven, Ivy, sbt, npm, deb, rpm, Helm, RubyGems, PyPI, Cargo, NuGet, Conda, raw | one artifact per file/version row, with its file size. Replacing a raw file, a Maven snapshot or a Conan file releases the previous content. |
| Conan | one artifact per stored file (recipe files and package files each count). |
| Go | one artifact per module version, with the size of its zip plus its `go.mod`. |
| Docker | the bytes of every blob (layer or config) the repository holds, **without** an artifact slot, plus one artifact with the manifest's size per manifest. An image with many layers is therefore one artifact, and a layer shared by several images of the repository, or pushed or mounted again into it, counts once. A blob mounted from another repository counts in the destination repository. Tags are free. |

Docker bytes are reserved when a blob upload completes (or a mount creates the repository's blob record) and when a manifest is first stored; an upload session that never completes reserves nothing. They are released when a manifest is deleted for good (retention `0`, or the [trash](#trash-and-quotas) purge), when a blob is deleted through the blob API, and when [registry garbage collection](#docker-registry-garbage-collection) removes untagged manifests and unreferenced blobs. A manifest moved to the trash keeps its usage, like every other format.

Not metered: **proxy** repositories (their cached upstream content is a cache, evicted by its own policies) and **group** repositories (they hold nothing). Only hosted repositories have counters. A yanked RubyGems version or Cargo crate keeps counting: yanking hides it but the file is stored until the repository is deleted. Raw files uploaded with [presigned uploads](../operations/storage.md#presigned-uploads) are counted when the upload is completed, i.e. after the bytes reached the bucket; an over-quota completion is refused (`507`), records nothing and leaves the upload pending until it expires.

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

`warn_threshold_percent` is stored and returned; the server takes no action on it. The web UI uses it to colour the usage bar of the [project quota](#project-quotas).

## Project quotas

Quotas have two levels. Every repository keeps its own limit (above), and a [project](../security/projects.md) can set a **ceiling** over the sum of all repositories it owns: `max_size_bytes` and `max_artifact_count`, `0` meaning unlimited (the default), plus a `warn_threshold_percent` (default 80).

- **Upload:** an upload must fit the repository's limit **and** the project's. The server reserves the artifact against both counters; if either would be exceeded the upload is refused with `507 Insufficient Storage` (`quota_exceeded`) and nothing is stored. Releases (delete, purge, cleanup, failed upload) give the usage back to both.
- **Ordering of limits:** only a global admin sets the project ceiling. A repository limit must be less than or equal to the project limit: `PUT /api/v1/repositories/{repo}/quota` with a larger value is `400 validation_failed` (`max_size_bytes must not exceed the project limit (...)`). `0` is always accepted; the project ceiling then bounds the repository.
- **Counters:** project counters are maintained **only while a project limit is set**. Setting a limit initialises them from the repositories' current usage in the same transaction; clearing it (all limits `0`) zeroes them. Without a limit the usage endpoint still reports the sums of the repositories' own counters.
- **Reconcile:** drift is repaired by the same [reconcile](#keeping-the-counters-correct) that repairs repository counters: the periodic cleanup-worker pass, the pass after a backup restore, and `POST /api/v1/repositories/{repo}/quota/reconcile` also rewrite the counters of every project that has a limit from its repositories' counters.
- **Not metered:** proxy and group repositories, as for repository quotas. They are listed in the usage view with `quota_enforced: false`.
- **Transfer:** [moving a repository](../security/projects.md#repository-self-service) to another project moves its usage with it (subtracted from the source's counters, added to the target's). If the target's ceiling cannot take it, the transfer fails with `409 project_quota_exceeded` and nothing changes.

| Method and path | Who | Purpose |
|---|---|---|
| `GET /api/v1/projects/{project}/usage` | any project member | Totals (`size_bytes`, `artifact_count`), the project `quota` and one entry per live repository in `repositories` (`name`, `format`, `repo_type`, `size_bytes`, `artifact_count`, `max_size_bytes`, `max_artifact_count`, `warn_threshold_percent`, `quota_enforced`), largest first. |
| `GET /api/v1/projects/{project}/quota` | any project member | The ceiling and live usage: `max_size_bytes`, `max_artifact_count`, `warn_threshold_percent`, `current_size_bytes`, `current_artifact_count`, `usage_percent`, `enforced` (a limit is set), `updated_at`. |
| `PUT /api/v1/projects/{project}/quota` | **global admin only** (owners get `403`) | Set `max_size_bytes`, `max_artifact_count`, `warn_threshold_percent`; omitted fields keep their value. Negative limits and a threshold outside 0-100 are `400 validation_failed`. |

```sh
curl -X PUT HOST/api/v1/projects/PROJECT/quota \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"max_size_bytes":107374182400,"max_artifact_count":0,"warn_threshold_percent":85}'
```

### In the web UI

Every member sees the project's **Usage** section: stored size and artifact count, the project quota as a bar that turns amber at the warning threshold and red at the limit, and a table with the size, artifact count and limits of each repository (proxy and group rows read "Not metered"). The project's **Settings** section shows the quota: global admins edit it there, owners and maintainers see it read-only.
