<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Storage

Artifact bytes live in one blob store per server process; metadata lives in PostgreSQL.

Supported backends: `filesystem` (default), `s3`, `gcs`, `azure`, `garage`, `seaweedfs`, `oci`. Pick one with `STORAGE_BACKEND` (or `storage.type`). Full option lists: [`.env.example`](../../.env.example) and [`openartifact.example.yaml`](../../openartifact.example.yaml). Precedence is defaults, then YAML, then environment, as described in [configuration](../getting-started/configuration.md).

## Backends

| `STORAGE_BACKEND` | Implementation |
|---|---|
| `filesystem` (or empty) | Local directory at `STORAGE_PATH` |
| `s3` | AWS S3 or any S3-compatible endpoint (Ceph, MinIO and so on) |
| `garage`, `seaweedfs` | The S3 backend with path-style addressing forced on |
| `gcs` | Google Cloud Storage through its S3-compatible XML API (`https://storage.googleapis.com`) with HMAC keys |
| `azure` | Azure Blob Storage REST API with the account's Shared Key |
| `oci` | An OCI registry (Distribution spec v1.1) used as a blob store |

An unknown value stops the server at startup (`unknown storage type`). `STORAGE_TYPE` is accepted as a fallback for `STORAGE_BACKEND`.

## Settings

Defaults: type `filesystem`, bucket `artifacts`, region `us-east-1`, path `/var/lib/artifacts`.

| Environment variable | YAML key (`storage:`) | Applies to | Notes |
|---|---|---|---|
| `STORAGE_BACKEND` | `type` | all | see above |
| `STORAGE_PATH` | `path` | filesystem | directory, created with mode `0755` |
| `STORAGE_BUCKET` | `bucket` | s3, garage, seaweedfs | bucket name |
| `STORAGE_REGION` | `region` | s3, garage, seaweedfs | |
| `STORAGE_ENDPOINT` | `endpoint` | s3, garage, seaweedfs, oci | S3 endpoint URL; for `oci` the registry URL |
| `STORAGE_ACCESS_KEY` | `accessKey` | s3, garage, seaweedfs | |
| `STORAGE_SECRET_KEY` | `secretKey` | s3, garage, seaweedfs | |
| `STORAGE_S3_FORCE_PATH_STYLE` | `forcePathStyle` | s3 | default `false`; always on for garage and seaweedfs |
| `STORAGE_S3_PRESIGN_TTL` | `presignTtl` | s3 | Go duration, default `5m`; see [Presigned URLs](#presigned-urls) |
| `OPENARTIFACT_GCS_BUCKET` | `gcsBucket` | gcs | required |
| `OPENARTIFACT_GCS_HMAC_KEY` | `gcsHmacKey` | gcs | required |
| `OPENARTIFACT_GCS_HMAC_SECRET` | `gcsHmacSecret` | gcs | required |
| `OPENARTIFACT_AZURE_ACCOUNT_NAME` | `azureAccountName` | azure | required |
| `OPENARTIFACT_AZURE_ACCOUNT_KEY` | `azureAccountKey` | azure | required, base64 as shown by Azure |
| `OPENARTIFACT_AZURE_CONTAINER_NAME` | `azureContainerName` | azure | required |
| `STORAGE_BUCKET` | `bucket` | oci | the repository name inside the registry |
| `STORAGE_OCI_USERNAME` | `ociUsername` | oci | Basic auth, sent only to the registry host |
| `STORAGE_OCI_PASSWORD` | `ociPassword` | oci | |
| `STORAGE_OCI_INSECURE` | `ociInsecure` | oci | swaps in a plain HTTP transport for `http://` registries. It does not disable TLS verification |

The Azure account endpoint is derived as `https://<account>.blob.core.windows.net/<container>`; there is no endpoint setting. Azure and OCI clients use a 30 s HTTP timeout.

### Credentials

- S3 family: if both `accessKey` and `secretKey` are set, they are used as static credentials. Otherwise the AWS default credential chain applies (environment, shared config, instance metadata or IRSA).
- Static credentials in `STORAGE_*` and `storage:` are plain process configuration: they are not encrypted by the server. Keep them in environment variables or a secrets store.

### Examples

Garage (path-style is required; `STORAGE_BACKEND=garage` sets it for you):

```sh
STORAGE_BACKEND=s3
STORAGE_ENDPOINT=http://localhost:3900
STORAGE_BUCKET=openartifact
STORAGE_REGION=garage
STORAGE_ACCESS_KEY=GK...        # from `garage key create`
STORAGE_SECRET_KEY=<SECRET>
STORAGE_S3_FORCE_PATH_STYLE=true
```

AWS S3 with an IAM role (no keys):

```sh
STORAGE_BACKEND=s3
STORAGE_BUCKET=openartifact
STORAGE_REGION=eu-west-1
```

The same Garage setup in YAML:

```yaml
storage:
  type: s3
  endpoint: http://localhost:3900
  bucket: openartifact
  region: garage
  accessKey: GK...
  secretKey: <SECRET>
  forcePathStyle: true
```

## Content-addressed layout

Every blob is named by the lowercase hex SHA-256 of its content (64 characters; other IDs are rejected). Where it lands:

| Backend | Location |
|---|---|
| filesystem | `<STORAGE_PATH>/<sha[0:2]>/<sha[2:]>`, plus `<blob>.meta`, a JSON sidecar with the upload metadata, size and computed digest |
| s3, garage, seaweedfs, gcs, azure | object key `blobs/<sha[0:2]>/<sha[2:4]>/<sha>` |
| oci | `<registry>/v2/<repository>/blobs/sha256:<sha>` |

S3, Azure and OCI uploads must carry the digest (`sha256` metadata); the OCI store skips the upload when the blob already exists. S3 uploads use multipart with 64 MiB parts and 4 parallel parts. Azure uploads are spooled to a temporary file in the system temp directory first.

The filesystem store writes to a `blob-upload-*.tmp` file inside `STORAGE_PATH`, fsyncs it and renames it into place, so `STORAGE_PATH` must be a single filesystem. Leftover `.tmp` files are safe to delete when the server is stopped.

## Deduplication and deletion

Identical content is stored once, across all repositories and formats. Rows in the database reference blobs by digest, and a blob is removed only when nothing references it:

- Deleting an artifact deletes its database row first, then calls `storage.SafeDeleteBlob`, which removes the blob only if its reference count is zero. The check and the delete run under an advisory lock that uploaders also take, so a concurrent upload of the same content cannot lose its blob.
- The cleanup worker also collects orphans: unreferenced blobs older than 1 hour, in batches of 500. Policies that decide which artifacts to delete are in [cleanup and quotas](../repositories/cleanup-and-quotas.md).

Because blobs are shared, deleting a repository frees disk only for content no other repository holds.

## Presigned URLs

`STORAGE_S3_PRESIGN_TTL` / `presignTtl` sets the lifetime of presigned GET URLs from the S3 store, clamped to 1 minute to 1 hour (a value of zero or less becomes 5 minutes). Filesystem, Azure and OCI stores do not support presigning. No request handler calls it today: downloads are always streamed through the server, so the setting has no effect on clients.

## Stored secrets

Credentials kept in the database are encrypted with AES-256-GCM (`internal/crypto`). The key comes from the environment variable `OPENARTIFACT_SECRET_KEY` only (not YAML): at least 16 bytes, hashed with SHA-256 to a 32-byte key. Stored values look like `enc:v1:<base64 nonce+ciphertext>`.

Encrypted fields:

| Field | Where |
|---|---|
| webhook signing secret | [webhooks](webhooks.md) |
| proxy repository upstream password | [hosted, proxy, group](../repositories/hosted-proxy-group.md) |
| SSO client secret | [authentication](../security/authentication.md) |
| AI provider API key | [AI analysis](../integrations/ai-analysis.md) |
| credentials of storage backends created under Admin, Storage | see below |

Rules:

- Without the key the server starts but stores these values as plaintext and logs a warning (`OPENARTIFACT_SECRET_KEY not set ...`). A key shorter than 16 bytes aborts startup.
- Values without the `enc:v1:` prefix are read as legacy plaintext, so enabling the key later keeps old rows working; they are re-encrypted when saved again.
- Changing or losing the key makes existing ciphertexts unreadable. Restore the old key, or re-enter the secrets.
- Set the same key on every replica.

```sh
OPENARTIFACT_SECRET_KEY="$(openssl rand -base64 32)"
```

### Storage backend records

`GET/POST /api/v1/admin/storage`, `GET/PATCH/DELETE /api/v1/admin/storage/{name}` and `POST /api/v1/admin/storage/{name}/test` (admin only, also the Admin, Storage page) keep named backend definitions. Credential keys in `config` (`access_key`, `secret_key`, `password`, `account_key`, `hmac_key`, `hmac_secret` and similar) are encrypted at rest, never returned (a `has_<key>` flag is returned instead) and kept when an update omits them. `test` checks connectivity for `filesystem`, `s3`, `garage`, `seaweedfs` and `oci`, and records the status as `healthy` or `unreachable`; `gcs` and `azure` answer `not_implemented`. The records do not route traffic: blobs are always read and written through the single backend configured with `STORAGE_*`.

## Health

`/health` and `/ready` probe S3-family stores (`s3`, `garage`, `seaweedfs`, `gcs`) with a bucket check. Filesystem, Azure and OCI are always reported `ok`. See [monitoring](monitoring.md).

## Moving data between backends

There is no built-in migration. A `full` backup taken on a filesystem store can be restored into a server configured with another backend: blobs are written to whichever backend is configured at restore time. Full backups cannot be taken from the other backends, see [backup and restore](backup.md#limits).
