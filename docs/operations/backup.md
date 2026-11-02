<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Backup and restore

Export the database records, and optionally the blobs, to one `.tar.gz` archive, and restore that archive into an instance. Use it for periodic snapshots, migrations between storage backends and rebuilding an instance.

Everything is admin only (403 otherwise) and available from the UI (**Settings > Backup and restore**, `/settings/backup`), the `oa` CLI and the REST API. There is no built-in scheduler and no retention: backups run when asked, and archives stay in the backup directory until you remove them.

A backup is a snapshot. For a recovery point of minutes, and for what to do when the database, the blob store or a single repository is lost, see [disaster recovery](disaster-recovery.md) and [PostgreSQL point-in-time recovery](postgres-pitr.md).

## Take a backup

```sh
oa backup create --type full            # records and all blobs
oa backup create --type metadata-only   # records only
oa backup list
oa backup show <ID>
oa backup download <ID> backup.tar.gz
```

The same with curl:

```sh
curl -X POST HOST/api/v1/admin/backup \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"type":"full"}'
# 202 {"id":"<ID>","status":"pending","type":"full","created_at":"..."}

curl -H "Authorization: Bearer <TOKEN>" -o backup.tar.gz HOST/api/v1/admin/backup/<ID>/download
```

The job runs in the background (24 h limit). Poll it until `status` is `completed`. The other values are `pending`, `running` and `failed`; a failed job fills `error_message`. Download the archive and keep it off the host. The CLI is documented in [CLI](../integrations/cli.md).

The two types:

- `full`: records and all blobs. Blobs are read through the configured store (across [storage tiers](storage-tiering.md)), so it works on every backend, including S3, GCS, Azure and OCI.
- `metadata-only`: records only. A restore needs the blobs to be in the target store already.

There is no incremental backup. Any other `type` (including `incremental`) is refused with 400.

| Route | Purpose |
|---|---|
| `POST /api/v1/admin/backup` | start a job; `type` must be `full` or `metadata-only`, else 400 |
| `GET /api/v1/admin/backup` | latest 100 jobs, as `{"backups":[...]}` |
| `GET /api/v1/admin/backup/{id}` | one job: `id`, `status`, `type`, `size_bytes`, `created_at`, `updated_at`, `started_at`, `completed_at`, `error_message` |
| `GET /api/v1/admin/backup/{id}/download` | the archive (`application/gzip`); 400 until the job is `completed` |
| `POST /api/v1/admin/restore` | upload an archive, see [restore](#restore) |

### Schedule backups

Call the create route from an external scheduler and copy the archive off the host. A cron entry:

```sh
0 2 * * * curl -fsS -X POST HOST/api/v1/admin/backup -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" -d '{"type":"full"}'
```

Use a token owned by an admin user ([authentication](../security/authentication.md)). Prune old `backup_*.tar.gz` files from the backup directory yourself. There is no delete route, and job rows stay in the list.

## Restore

Restore into an empty instance for a clean result. Start the target with the same `OPENARTIFACT_SECRET_KEY` and the blob backend you want ([storage](storage.md)), then upload the archive:

```sh
oa backup restore backup.tar.gz
```

The same with curl:

```sh
curl -X POST HOST/api/v1/admin/restore \
  -H "Authorization: Bearer <TOKEN>" \
  -F file=@backup.tar.gz
# 202 {"message":"restore started","filename":"backup.tar.gz"}
```

The UI restore dialog does the same and asks for a confirmation phrase. The manifest `version` must be `1.0`.

The response only confirms the upload. The restore runs in the background (24 h limit) and the outcome is in the server log: `restore completed successfully`, `restore completed with failures` with a count, or `restore failed`.

Procedure for a new instance:

1. Start the target as described above.
2. Upload the archive and watch the log until it finishes.
3. Log in and re-create what is not in the archive (see [what is not included](#what-is-not-included)). Re-enter webhook secrets, SSO client secrets and storage credentials.
4. Check the repositories, a download per format and the blob count.

### What a restore does

- **Records** go through the normal create paths. IDs of repositories, users, Debian distributions and components, pipelines, bundles, projects and npm packages are regenerated, and rows that point at them are remapped.
- **Existing repository names** are skipped with a warning and count as failures. Rows under a skipped repository are skipped too.
- **Reserved repository names** (names that would shadow a UI page or an internal route) are skipped with a warning, as an API create would be refused.
- **Existing user names** keep the existing account. This is the case for the bootstrap `admin` user of a fresh server. It is logged at info level and is not a failure. The archived grants, group memberships, tokens and project memberships of that user are skipped with it.
- **Rows under any other skipped parent** are skipped as failures. A JSON `null` element in a table is ignored.
- **Blobs** are checked against the SHA-256 in their entry name, then written to the configured blob store. A blob entry larger than 64 GiB is refused.
- **Group repositories** get their members back, remapped to the new repository ids.
- **Generated metadata** of hosted deb, maven and rpm repositories (`dists/`, `maven-metadata.xml`, repodata) is archived with the other blobs. The restore also queues its regeneration, so it is served right after the restore and refreshed shortly after.
- **Permission targets** keep their name. A target that already exists (the built-in `admin-all` and `anonymous-read`) is kept as it is, and the archive's assignments are attached to it. Assignments and group memberships are remapped to the new user, group and target ids.
- **SSO-managed memberships** are not restored. The next SSO login derives them.
- **Storage quotas**: the limits (size, artifact count, warning threshold) are restored per repository, and the usage counters are recounted from the restored packages. See [quotas](../repositories/cleanup-and-quotas.md#keeping-the-counters-correct).
- **Unknown entries** that a newer version wrote are ignored.
- **Archives with a version below 2.0**, including those with `organizations` and `organization_members`, are refused with a pointer to `oa import`. See [upgrading](../getting-started/upgrading.md#0150).

Only one restore runs at a time, and a backup cannot run during a restore. A second upload gets `409`, and a backup job started during a restore ends as `failed`.

The upload is limited to 10 GiB (413 above) and is spooled to a `restore_*.tar.gz` file in the system temp directory before the restore starts.

## What is in an archive

| Entry | Content |
|---|---|
| `<table>.jsonl` | one JSON array per table (despite the extension it is not JSON Lines) |
| `blobs/<sha256>` | one entry per stored blob, `full` backups only |
| `manifest.json` | written last: `version` (`1.0`), `created_at`, `type`, `table_counts`, and for `full` backups `snapshot_at` and `blob_count` |

`snapshot_at` is when the database snapshot was taken. `blob_count` is the number of blobs the snapshot lists.

Tables exported:

- **Global**: repositories, repository group members (member order is kept), users, groups, tokens, SSO providers, storage backend records, promotion pipelines, release bundles with their items and promotions, projects (with their policy settings), members and group grants, webhooks, permission targets and their assignments ([RBAC](../security/rbac.md)), user-group memberships, and storage quota limits. Repositories keep their project, `trash_retention_days` and `immutable_releases`.
- **Per format**: Alpine (apk packages), Cargo, Conan, Conda, Debian distributions, components and architectures with deb packages; Docker (blobs, manifests, tags), Go module, Helm, Ivy, Maven, npm (packages, versions, dist-tags), NuGet, PyPI, raw, rpm, RubyGems, sbt and Terraform (modules and providers) records.
- **Per repository**: cleanup policies, SBOM, VEX decisions, license, secret and vulnerability scan results, AI analyses.

Lists are read in one call of up to 100000 rows per table.

### What is not included

- Artifact properties, sessions, the audit log, webhook delivery history, GPG keys and AI provider settings. Re-create them after a restore.
- Project tokens and their service accounts. Mint them again.
- [Trashed data](trash.md): trashed repositories and package rows. The export treats them as nonexistent, so a trashed repository is skipped together with its contents. Restore anything you still need from the trash before taking the backup. The blob store is exported as it is, so the bytes of trashed packages are still in the archive until they are purged.
- Managed GPG private keys. The API cannot export them. Keep your own copy of an imported key, or generate a new one and redistribute its public key. A database dump (`pg_dump`) carries them sealed with `OPENARTIFACT_SECRET_KEY`, which you must then restore too.

### Secrets in the archive

- `users` and `tokens` carry the password hashes and API token hashes. Treat the archive as sensitive and store it encrypted.
- Webhook signing secrets are removed, so restored webhooks have no secret. Set them again. See [webhooks](webhooks.md).
- SSO OIDC `client_secret` and storage backend credential keys (anything containing secret, key, password or token) are dropped. Re-enter them.
- A proxy repository's upstream password is not exported, only the URL and username. Set it again after a restore. An archive that does carry the stored value keeps it on restore; use the same `OPENARTIFACT_SECRET_KEY` on the target then (see [storage](storage.md#stored-secrets)).

### When a backup fails

A failed read of any exported table fails the job instead of producing an archive with silent gaps; `error_message` names the table. The job also fails on:

- a blob that is missing from the store;
- a blob whose bytes do not match its sha256;
- an archive that holds fewer blobs than the snapshot lists (`blob_count`).

Generated metadata (maven `maven-metadata.xml`, rpm `repodata`, deb `dists/`) has no `blobs` row, so it is not listed in `blob_count`; the restore regenerates it. Blobs are read in pages ordered by sha256, so memory use does not grow with the blob count. The archive is flushed and closed before the job is marked `completed`. A failed write marks the job `failed` and removes the partial file.

## Settings

| Environment variable | YAML key | Default |
|---|---|---|
| `OPENARTIFACT_BACKUP_DIR` | `backup.backupDir` | `/var/lib/openartifact/backups` |
| `OPENARTIFACT_BACKUP_LEASE_TTL` | `backup.leaseTtl` | `5m` |

`leaseTtl` must be positive. It matters only after a crash: the lease of the dead backup expires after this time, and deferred blob deletions can proceed. A running backup renews its [lease](#consistency-without-locks) every third of that period.

The directory is created with mode `0755`. Archives are written `0600` as `backup_<jobID>.tar.gz`, first to a `.tmp` file and then renamed.

Mount a persistent volume or bind mount at the backup directory, otherwise backups vanish with the container. The compose file uses the named volume `backups_data` at `/var/lib/openartifact/backups`, and `make docker-run` mounts `openartifact_backups` there.

The compose service runs with a read-only root filesystem and `tmpfs` on `/tmp`. The restore upload and its blob spool land in `/tmp`, so a large restore can exhaust that tmpfs. Size it up for big archives. Compose credentials are in [`docker/.env.example`](../../docker/.env.example).

## Consistency without locks

A backup reads all records from one PostgreSQL snapshot, so the tables agree with each other. The manifest's `snapshot_at` is the point in time the archive represents. Uploads, deletes and tier moves keep running meanwhile. Anything that happens after the snapshot is not in the archive.

The blobs the snapshot lists must still exist when the backup reads them, so physical blob deletion is deferred:

- A backup registers a **lease** with its snapshot, renews it while it runs and removes it when it ends, whether it succeeded or failed. A backup that crashed leaves its lease behind, and it expires after `backup.leaseTtl`.
- Deleting a blob (the last package referencing it was removed, a cleanup or purge) removes its database row at once but only queues the bytes. The same goes for the old copy left behind by a [tier move](storage-tiering.md#deferred-removal-of-the-old-copy).
- The blob deletion sweeper deletes queued bytes after the [grace period](storage.md#deferred-blob-deletion), and only when no live lease's snapshot can still list the blob. It runs on every replica. A blob uploaded again meanwhile keeps its bytes.

A backup holds a database transaction for its whole duration and uses up to five connections: one from the pool for the snapshot and up to four dedicated ones, outside the pool limit, that join the same snapshot.

Keep `idle_in_transaction_session_timeout` off (the PostgreSQL default) or longer than the longest backup. Otherwise the server terminates the snapshot and the job fails. Long backups also hold back the PostgreSQL vacuum horizon, as any long transaction does.

## Limits

- Blob bytes of deleted packages stay in the store for the [deletion grace period](storage.md#deferred-blob-deletion) (default 24 h), so storage is freed later than the delete.
- Restore does not compare the archive's blob count with `blob_count`. Each blob is checked against its sha256.
- `oa backup create --help` lists `--type full|metadata-only`.

See also: [rebuild from storage](rebuild-from-storage.md) when the database is lost and no backup is usable, and [blob replication](replication.md) for a second copy of the blob store on another backend.
