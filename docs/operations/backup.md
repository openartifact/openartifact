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

The directory is created with mode `0755`; archives are written `0600` as `backup_<jobID>.tar.gz` (first to a `.tmp` file, then renamed). In the compose file it is the named volume `backups_data` mounted at `/var/lib/openartifact/backups`, and `make docker-run` mounts `openartifact_backups` there. Mount a persistent volume or bind mount in any other deployment, otherwise backups vanish with the container.

## What is in an archive

| Entry | Content |
|---|---|
| `<table>.jsonl` | one JSON array per table (despite the extension it is not JSON Lines) |
| `blobs/<sha256>` | one entry per stored blob, `full` backups only |
| `manifest.json` | written last: `version` (`1.0`), `created_at`, `type`, `table_counts` |

Backup types, passed as `type`:

- `full`: records and all blobs. Needs a backend that can list its blobs: filesystem, S3, GCS and Azure can; the OCI backend cannot, and a `full` backup on it fails with a clear error (use `metadata-only` plus your registry's own backup).
- `metadata-only`: records only. Restoring it needs the blobs to be present in the target store already.

There is no incremental backup: every archive is complete for its type, and any other `type` (including `incremental`) is refused with 400.

Soft-deleted data ([trash](trash.md): trashed repositories and package rows) is not part of a backup. The export reads through the same queries clients use, which treat trashed rows as nonexistent, so a trashed repository is skipped together with its contents and a restore brings back live data only. Restore anything you still need from the trash before taking the backup. The blob store is exported as it is, so the bytes of trashed packages are still in the archive until they are purged. Repositories carry `trash_retention_days` and `immutable_releases` through the backup.

Tables exported:

- Global: repositories, repository group members (member order is kept), users, groups, tokens, SSO providers, storage backend records, promotion pipelines, release bundles with their items and promotions, projects (with their policy settings), members and group grants, webhooks. Project tokens (and their service accounts) are left out: mint them again after a restore. Repositories keep their project on restore. Archives from older versions with `organizations` and `organization_members` are still accepted.
- Per format: Debian distributions, components and architectures with deb packages; rpm, Helm, PyPI, NuGet, Cargo, RubyGems, Conan, Ivy, sbt, raw, Docker (blobs, manifests, tags), npm (packages, versions, dist-tags), Maven, Conda and Go module records.
- Per repository: cleanup policies, SBOM, license, secret and vulnerability scan results, AI analyses.

Not included: permission targets and group membership ([RBAC](../security/rbac.md)), quota limits, artifact properties, sessions, the audit log, webhook delivery history, GPG keys and AI provider settings. Re-create them after a restore. A failed list call for any exported table (or an unreadable blob that is not simply missing) fails the job instead of producing an archive with silent gaps; the job's `error_message` names the table. A blob that disappeared between listing and reading is skipped. Blobs are enumerated in pages, so memory use does not grow with the blob count. The archive is flushed and closed before the job is marked `completed`; a failed write marks the job `failed` and removes the partial file.

### Secrets in the archive

- `users` and `tokens` carry the password hashes and API token hashes: treat the archive as sensitive and store it encrypted.
- Webhook signing secrets are removed, so restored webhooks have no secret; set them again. See [webhooks](webhooks.md).
- SSO OIDC `client_secret` and storage backend credential keys (anything containing secret, key, password or token) are dropped; re-enter them.
- A proxy repository's upstream password is exported as stored: ciphertext when `OPENARTIFACT_SECRET_KEY` is set. Use the same key on the restore target (see [storage](storage.md#stored-secrets)).

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

The export reads table after table; it is not a single database snapshot, so artifacts uploaded while a backup runs may be only partly captured.

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
- Records go through the normal create paths. IDs of repositories, users, Debian distributions and components, pipelines, bundles, projects and npm packages are regenerated and rows that point at them are remapped. A name that already exists (a repository, or the bootstrap `admin` user of a fresh server) is skipped with a warning, and rows depending on a skipped parent are skipped too. Restore into an empty instance for a clean result.
- Blobs are checked against the SHA-256 in their entry name, then written to the configured blob store.
- When the restore finishes, the repositories' [storage quota](../repositories/cleanup-and-quotas.md#keeping-the-counters-correct) usage counters are recounted from the restored packages, so quotas set afterwards start from the real usage. Quota limits are not part of the archive.
- Group repositories get their members back (remapped to the new repository ids). The generated metadata of hosted rpm, deb and maven repositories (repodata, `dists/`, `maven-metadata.xml`) is not in the archive; the restore queues its regeneration, so it is served shortly after the restore finishes.
- Entries a newer version wrote that this version does not know are ignored.

Procedure for a new instance:

1. Start the target with the same `OPENARTIFACT_SECRET_KEY` and the blob backend you want ([storage](storage.md)).
2. Upload the archive with the route above and watch the log until it finishes.
3. Log in, then re-create what is not in the archive (see above) and re-enter webhook secrets, SSO client secrets and storage credentials.
4. Check repositories, a download per format, and the blob count.

## Compose notes

The compose service runs with a read-only root filesystem and `tmpfs` on `/tmp`. The restore upload and its blob spool land in `/tmp`, so a large restore can exhaust that tmpfs; size it up for big archives. Compose credentials are in [`docker/.env.example`](../../docker/.env.example).

## Limits

- On S3-family backends (`s3`, `garage`, `seaweedfs`, `gcs`) the blob listing returns keys of the form `ab/cd/<sha256>`, which the blob-ID check rejects, so a `full` backup that reaches the first blob ends as `failed`. On `azure` and `oci` the blob listing is empty, so a `full` backup silently holds no blobs. Only the filesystem backend exports blobs correctly today. For the others, back up the bucket or registry with its own tooling and use `metadata-only` for the records.
- `oa backup restore <id>` sends JSON, but the server requires the multipart `file` upload described above, so it fails with 400. Use curl or the UI.
- `oa backup create --help` lists `full|metadata`; the server accepts `metadata-only`.
- Lists are read in one call of up to 100000 rows per table.
