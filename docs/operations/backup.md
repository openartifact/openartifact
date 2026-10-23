<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Backup and restore

Admin-triggered export of the database records, and optionally the blobs, to one `.tar.gz` archive, and a matching restore.

Everything is admin only (403 otherwise) and available from the UI (Admin, Backup), the `oa` CLI and the REST API. There is no built-in scheduler and no retention: backups run when asked, and archives stay in the backup directory until you remove them.

## Settings

| Environment variable | YAML key | Default |
|---|---|---|
| `OPENARTIFACT_BACKUP_DIR` | `backup.backupDir` | `/var/lib/openartifact/backups` |
| `OPENARTIFACT_BACKUP_LEASE_TTL` | `backup.leaseTtl` | `5m` |

`leaseTtl` is how long a running backup's [lease](#consistency-without-locks) stays valid without a heartbeat. The backup renews it every third of that period; it only matters after a crash, when the lease of the dead backup expires after this time and deferred blob deletions can proceed. Must be positive.

The directory is created with mode `0755`; archives are written `0600` as `backup_<jobID>.tar.gz` (first to a `.tmp` file, then renamed). In the compose file it is the named volume `backups_data` mounted at `/var/lib/openartifact/backups`, and `make docker-run` mounts `openartifact_backups` there. Mount a persistent volume or bind mount in any other deployment, otherwise backups vanish with the container.

## What is in an archive

| Entry | Content |
|---|---|
| `<table>.jsonl` | one JSON array per table (despite the extension it is not JSON Lines) |
| `blobs/<sha256>` | one entry per stored blob, `full` backups only |
| `manifest.json` | written last: `version` (`1.0`), `created_at`, `type`, `table_counts`, and for `full` backups `snapshot_at` (when the database snapshot was taken) and `blob_count` (blobs the snapshot lists) |

Backup types, passed as `type`:

- `full`: records and all blobs. Blobs are taken from the `blobs` table of the backup's database snapshot and read through the configured store (across [storage tiers](storage-tiering.md)), so it works on every backend, including S3, GCS, Azure and OCI. The blob store is never listed.
- `metadata-only`: records only. Restoring it needs the blobs to be present in the target store already.

There is no incremental backup: every archive is complete for its type, and any other `type` (including `incremental`) is refused with 400.

Soft-deleted data ([trash](trash.md): trashed repositories and package rows) is not part of a backup. The export reads through the same queries clients use, which treat trashed rows as nonexistent, so a trashed repository is skipped together with its contents and a restore brings back live data only. Restore anything you still need from the trash before taking the backup. The blob store is exported as it is, so the bytes of trashed packages are still in the archive until they are purged. Repositories carry `trash_retention_days` and `immutable_releases` through the backup.

Tables exported:

- Global: repositories, repository group members (member order is kept), users, groups, tokens, SSO providers, storage backend records, promotion pipelines, release bundles with their items and promotions, projects (with their policy settings), members and group grants, webhooks, permission targets and their assignments ([RBAC](../security/rbac.md)), user-group memberships, and storage quota limits. Project tokens (and their service accounts) are left out: mint them again after a restore. Repositories keep their project on restore. Archives from older versions with `organizations` and `organization_members` are still accepted.
- Per format: Debian distributions, components and architectures with deb packages; rpm, Helm, PyPI, NuGet, Cargo, RubyGems, Conan, Ivy, sbt, raw, Docker (blobs, manifests, tags), npm (packages, versions, dist-tags), Maven, Conda and Go module records.
- Per repository: cleanup policies, SBOM, VEX decisions, license, secret and vulnerability scan results, AI analyses.

Not included: artifact properties, sessions, the audit log, webhook delivery history, GPG keys and AI provider settings. Re-create them after a restore. Managed GPG private keys cannot be exported by the API: keep your own copy of an imported key, or generate a new one and redistribute its public key; a database dump (`pg_dump`) carries them sealed with `OPENARTIFACT_SECRET_KEY`, which must then be restored too. A failed read of any exported table fails the job instead of producing an archive with silent gaps; the job's `error_message` names the table. So does a blob that is missing from the store, a blob whose bytes do not match their sha256, and an archive that holds fewer blobs than the snapshot lists (`blob_count`). Blobs are enumerated in pages ordered by sha256, so memory use does not grow with the blob count. Generated metadata (maven `maven-metadata.xml`, rpm `repodata`, deb `dists/`) has no `blobs` row and is not in the archive; the restore regenerates it. The archive is flushed and closed before the job is marked `completed`; a failed write marks the job `failed` and removes the partial file.

### Secrets in the archive

- `users` and `tokens` carry the password hashes and API token hashes: treat the archive as sensitive and store it encrypted.
- Webhook signing secrets are removed, so restored webhooks have no secret; set them again. See [webhooks](webhooks.md).
- SSO OIDC `client_secret` and storage backend credential keys (anything containing secret, key, password or token) are dropped; re-enter them.
- A proxy repository's upstream password is not exported (only the URL and username): set it again after a restore. Archives written by older versions still carry the stored value, and restoring one keeps it; use the same `OPENARTIFACT_SECRET_KEY` on the target then (see [storage](storage.md#stored-secrets)).

## Create a backup

```sh
curl -X POST HOST/api/v1/admin/backup \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"type":"full"}'
# 202 {"id":"<ID>","status":"pending","type":"full","created_at":"..."}

oa backup create --type metadata-only
```

The job runs in the background (24 h limit). Poll it until `status` is `completed` (other values: `pending`, `running`, `failed`; failures fill `error_message`).

| Route | Purpose |
|---|---|
| `POST /api/v1/admin/backup` | start a job; `type` must be `full` or `metadata-only`, else 400 |
| `GET /api/v1/admin/backup` | latest 100 jobs, as `{"backups":[...]}` |
| `GET /api/v1/admin/backup/{id}` | one job: `id`, `status`, `type`, `size_bytes`, `created_at`, `updated_at`, `started_at`, `completed_at`, `error_message` |
| `GET /api/v1/admin/backup/{id}/download` | the archive (`application/gzip`); 400 until the job is `completed` |
| `POST /api/v1/admin/restore` | upload an archive, see below |

```sh
oa backup list
oa backup show <ID>
oa backup download <ID> backup.tar.gz
```

Equivalent with curl: `curl -H "Authorization: Bearer <TOKEN>" -o backup.tar.gz HOST/api/v1/admin/backup/<ID>/download`. The CLI is documented in [CLI](../integrations/cli.md).

## Schedule

Call the create route from an external scheduler and copy the archive off the host. A cron entry:

```sh
0 2 * * * curl -fsS -X POST HOST/api/v1/admin/backup -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" -d '{"type":"full"}'
```

Use a token owned by an admin user ([authentication](../security/authentication.md)). Prune old `backup_*.tar.gz` files from the backup directory yourself: there is no delete route, and job rows stay in the list.

## Consistency without locks

A `full` or `metadata-only` backup reads all records from one PostgreSQL snapshot (a `REPEATABLE READ`, read-only transaction exported with `pg_export_snapshot`), so the tables agree with each other and the manifest's `snapshot_at` is the point in time the archive represents. Uploads, deletes and tier moves keep running while it works: nothing is locked, and anything that happens after the snapshot is simply not in the archive.

The blobs the snapshot lists must still exist when the backup reads them, so physical blob deletion is deferred:

- A backup registers a **lease** (`backup_leases`) with its snapshot, renews it while it runs and removes it when it ends, whether it succeeded or failed. A backup that crashed leaves its lease behind; it expires after `backup.leaseTtl`.
- Deleting a blob (the last package referencing it was removed, a cleanup or purge) removes its database row at once but only **queues** the bytes (`blob_deletions`). The same goes for the old copy left behind by a [tier move](storage-tiering.md#deferred-removal-of-the-old-copy).
- The blob deletion sweeper deletes queued bytes after the [grace period](storage.md#deferred-blob-deletion) and only when no live lease's snapshot can still list the blob. It runs on every replica without a global lock (rows are claimed with `FOR UPDATE SKIP LOCKED`) and re-checks, under the per-blob lock, that the blob is still unreferenced before it deletes anything. A blob uploaded again meanwhile keeps its bytes.

A backup holds a database transaction for its whole duration and uses up to five connections: one taken from the pool for the snapshot and up to four dedicated ones, outside the pool limit, that join the same snapshot. Keep `idle_in_transaction_session_timeout` off (the PostgreSQL default) or longer than the longest backup, otherwise the server terminates the snapshot and the job fails. Long backups also hold back the PostgreSQL vacuum horizon, as any long transaction does.

## Restore

Upload an archive made by this version family (manifest `version` must be `1.0`):

```sh
curl -X POST HOST/api/v1/admin/restore \
  -H "Authorization: Bearer <TOKEN>" \
  -F file=@backup_<ID>.tar.gz
# 202 {"message":"restore started","filename":"backup_<ID>.tar.gz"}
```

The UI restore dialog does the same and asks for a confirmation phrase. The upload is limited to 10 GiB (413 above) and is spooled to a `restore_*.tar.gz` file in the system temp directory; restore then runs in the background (24 h limit).

Behaviour:

- The response only confirms the upload. The outcome is in the server log: `restore completed successfully`, or `restore completed with failures` with a count, or `restore failed`.
- Records go through the normal create paths. IDs of repositories, users, Debian distributions and components, pipelines, bundles, projects and npm packages are regenerated and rows that point at them are remapped. A repository name that already exists is skipped with a warning and counts as a failure. A user name that already exists (the bootstrap `admin` user of a fresh server) keeps the existing account: it is logged at info level and is not a failure, and its archived grants, group memberships, tokens and project memberships are skipped with it (the bootstrap grant exists already). Rows depending on any other skipped parent are skipped too, as failures. Restore into an empty instance for a clean result. A repository whose name is reserved (it would shadow a UI page or an internal route) is skipped with a warning, like an API create would be refused. A JSON `null` element in a table is ignored.
- Blobs are checked against the SHA-256 in their entry name, then written to the configured blob store.
- When the restore finishes, the repositories' [storage quota](../repositories/cleanup-and-quotas.md#keeping-the-counters-correct) usage counters are recounted from the restored packages, so quotas set afterwards start from the real usage. 
- Group repositories get their members back (remapped to the new repository ids). The generated metadata of hosted rpm, deb and maven repositories (repodata, `dists/`, `maven-metadata.xml`) is archived with the other blobs (it is read from `repository_metadata`, which has no `blobs` row), and the restore also queues its regeneration, so it is served right after the restore finishes and refreshed shortly after.
- Permission targets keep their name: a target that already exists on the instance (the built-in `admin-all` and `anonymous-read`) is kept as it is and the archive's assignments are attached to it. Assignments and group memberships are remapped to the new user, group and target ids. Memberships that an SSO provider manages are not restored; the next SSO login derives them. Quota limits (size, artifact count, warning threshold) are restored per repository.
- Only one restore runs at a time, and a backup cannot run while a restore does: a second upload gets `409` and a backup job started during a restore ends as `failed`. A blob entry larger than 64 GiB is refused.
- Entries a newer version wrote that this version does not know are ignored.

Procedure for a new instance:

1. Start the target with the same `OPENARTIFACT_SECRET_KEY` and the blob backend you want ([storage](storage.md)).
2. Upload the archive with the route above and watch the log until it finishes.
3. Log in, then re-create what is not in the archive (see above) and re-enter webhook secrets, SSO client secrets and storage credentials.
4. Check repositories, a download per format, and the blob count.

## Compose notes

The compose service runs with a read-only root filesystem and `tmpfs` on `/tmp`. The restore upload and its blob spool land in `/tmp`, so a large restore can exhaust that tmpfs; size it up for big archives. Compose credentials are in [`docker/.env.example`](../../docker/.env.example).

## Limits

- Blob bytes of deleted packages stay in the store for the [deletion grace period](storage.md#deferred-blob-deletion) (default 24 h), so storage is freed later than the delete.
- Restore does not compare the archive's blob count with `blob_count`; each blob is checked against its sha256 as before.
- `oa backup restore <id>` sends JSON, but the server requires the multipart `file` upload described above, so it fails with 400. Use curl or the UI.
- `oa backup create --help` lists `full|metadata`; the server accepts `metadata-only`.
- Lists are read in one call of up to 100000 rows per table.
