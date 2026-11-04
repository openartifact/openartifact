<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Storage

Artifact bytes live in one blob store per server process, and metadata lives in PostgreSQL. This page shows how to choose and configure the blob store, how blobs are named and deleted, how presigned transfers bypass the server, and how stored secrets are protected.

## Choose a backend

Pick one with `STORAGE_BACKEND` (or `storage.type`). `STORAGE_TYPE` is accepted as a fallback. An unknown value stops the server at startup (`unknown storage type`).

| `STORAGE_BACKEND` | Implementation |
|---|---|
| `filesystem` (or empty, the default) | Local directory at `STORAGE_PATH` |
| `s3` | AWS S3 or any S3-compatible endpoint (Ceph, MinIO and so on) |
| `garage`, `seaweedfs` | The S3 backend with path-style addressing forced on |
| `gcs` | Google Cloud Storage through its S3-compatible XML API (`https://storage.googleapis.com`) with HMAC keys |
| `azure` | Azure Blob Storage REST API with the account's Shared Key |
| `oci` | An OCI registry (Distribution spec v1.1) used as a blob store |

Precedence is defaults, then YAML, then environment, as described in [configuration](../getting-started/configuration.md). Full option lists are in [`.env.example`](../../.env.example) and [`openartifact.example.yaml`](../../openartifact.example.yaml).

New uploads always land on the backend configured here. To keep cold blobs on cheaper storage, register extra backends and map them to named tiers with age, idle-time or size policies: [storage tiers](storage-tiering.md). To keep a second copy for disaster recovery, see [replication](replication.md).

### Examples

Garage needs path-style addressing. `STORAGE_BACKEND=garage` sets it for you; with `s3` you set it yourself:

```sh
STORAGE_BACKEND=s3
STORAGE_ENDPOINT=http://localhost:3900
STORAGE_BUCKET=openartifact
STORAGE_REGION=garage
STORAGE_ACCESS_KEY=GK...        # from `garage key create`
STORAGE_SECRET_KEY=<SECRET>
STORAGE_S3_FORCE_PATH_STYLE=true
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

AWS S3 with an IAM role (no keys):

```sh
STORAGE_BACKEND=s3
STORAGE_BUCKET=openartifact
STORAGE_REGION=eu-west-1
```

## Settings

Defaults: type `filesystem`, bucket `artifacts`, region `us-east-1`, path `/var/lib/artifacts`.

| Environment variable | YAML key (`storage:`) | Applies to | Notes |
|---|---|---|---|
| `STORAGE_BACKEND` | `type` | all | see [Choose a backend](#choose-a-backend) |
| `STORAGE_PATH` | `path` | filesystem | directory, created with mode `0755` |
| `STORAGE_BUCKET` | `bucket` | s3, garage, seaweedfs | bucket name |
| `STORAGE_BUCKET` | `bucket` | oci | the repository name inside the registry |
| `STORAGE_REGION` | `region` | s3, garage, seaweedfs | |
| `STORAGE_ENDPOINT` | `endpoint` | s3, garage, seaweedfs, oci | S3 endpoint URL; for `oci` the registry URL |
| `STORAGE_ACCESS_KEY` | `accessKey` | s3, garage, seaweedfs | |
| `STORAGE_SECRET_KEY` | `secretKey` | s3, garage, seaweedfs | |
| `STORAGE_S3_FORCE_PATH_STYLE` | `forcePathStyle` | s3 | default `false`; always on for garage and seaweedfs |
| `OPENARTIFACT_GCS_BUCKET` | `gcsBucket` | gcs | required |
| `OPENARTIFACT_GCS_HMAC_KEY` | `gcsHmacKey` | gcs | required |
| `OPENARTIFACT_GCS_HMAC_SECRET` | `gcsHmacSecret` | gcs | required |
| `OPENARTIFACT_AZURE_ACCOUNT_NAME` | `azureAccountName` | azure | required |
| `OPENARTIFACT_AZURE_ACCOUNT_KEY` | `azureAccountKey` | azure | required, base64 as shown by Azure |
| `OPENARTIFACT_AZURE_CONTAINER_NAME` | `azureContainerName` | azure | required |
| `OPENARTIFACT_AZURE_ENDPOINT` | `azureEndpoint` | azure | blob service URL for Azurite or a sovereign cloud; empty means `https://<account>.blob.core.windows.net`. With Azurite use `http://host:10000/devstoreaccount1` |
| `STORAGE_OCI_USERNAME` | `ociUsername` | oci | Basic auth, sent only to the registry host |
| `STORAGE_OCI_PASSWORD` | `ociPassword` | oci | |
| `STORAGE_OCI_INSECURE` | `ociInsecure` | oci | allows an `http://` registry endpoint; without it such an endpoint is refused at startup. It never disables TLS verification |
| `STORAGE_SPOOL_PATH` | `spoolPath` | s3, azure, oci | directory where a blob is spooled while it is verified before upload. The server sweeps leftovers of a crash at startup. Default: the system temp dir (not swept) |
| `STORAGE_STAGING_PATH` | `stagingPath` | oci | spool directory for [server-relayed presigned uploads](#presigned-uploads). Required (use a persistent directory) while uploads are relayed, because it also holds the signing key |
| `STORAGE_PRESIGN_*`, `STORAGE_S3_PRESIGN_TTL` | `presign*` | s3, gcs | presigned URLs; see [Presigned URL settings](#presigned-url-settings) |

Azure and OCI clients use a 30 s HTTP timeout.

### Credentials

- **S3 family.** If both `accessKey` and `secretKey` are set, the server uses them as static credentials. Otherwise the AWS default credential chain applies: environment, shared config, instance metadata or IRSA.
- **Static credentials** in `STORAGE_*` and `storage:` are plain process configuration. The server does not encrypt them. Keep them in environment variables or a secrets store.

### Conditional requests

The server verifies a presigned upload and then promotes it with conditional requests (`If-Match`, `x-amz-copy-source-if-match`). Bytes overwritten through the still-valid upload URL therefore cannot be promoted.

Backends that ignore these preconditions (Garage 1.x does) are still safe. The server also compares the ETag reported by the read or copy and discards the copy on a mismatch. Backends that enforce the preconditions (Garage 2.x, AWS S3, MinIO-compatible servers) reject the request directly.

## Content-addressed layout

Every blob is named by the lowercase hex SHA-256 of its content (64 characters; other IDs are rejected). Each backend maps that name to a location:

| Backend | Location |
|---|---|
| filesystem | `<STORAGE_PATH>/<sha[0:2]>/<sha[2:]>`, plus `<blob>.meta`, a JSON sidecar with the upload metadata, size and computed digest |
| s3, garage, seaweedfs, gcs, azure | object key `blobs/<sha[0:2]>/<sha[2:4]>/<sha>` |
| oci | `<registry>/v2/<repository>/blobs/sha256:<sha>` |

Upload behaviour by backend:

- **S3, Azure and OCI** uploads must carry the digest (`sha256` metadata). The OCI store skips the upload when the blob already exists.
- **S3, Azure and OCI** uploads are spooled to a temporary file first (see `STORAGE_SPOOL_PATH`).
- **S3** uploads use multipart with 16 MiB parts and 4 parallel parts.
- **Filesystem** uploads go to a `blob-upload-*.tmp` file inside `STORAGE_PATH`. The server fsyncs it and renames it into place, so `STORAGE_PATH` must be a single filesystem. Leftover `.tmp` files are safe to delete while the server is stopped.

## Deduplication and deletion

Identical content is stored once, across all repositories and formats. Database rows reference blobs by digest, and the server removes a blob only when nothing references it. Because blobs are shared, deleting a repository frees disk only for content no other repository holds.

How content gets deleted:

- **Trash.** With the default [trash](trash.md) retention, deleting an artifact only marks its row as deleted. The row still references its blob, so nothing is freed until the item is purged, or the repository's retention is `0`.
- **Permanent delete.** The server deletes the artifact's row first. It removes the blob record only if nothing references the blob any more, and queues the bytes for [deferred deletion](#deferred-blob-deletion). A concurrent upload of the same content cannot lose its blob, because the check and the removal run under the same lock that uploads take. The lock is a PostgreSQL advisory lock held by a transaction that stays idle while the operation runs. That transaction switches off `idle_in_transaction_session_timeout` for itself. If its session still dies (a failover, a killed connection), the lock is gone and the operation fails with an error instead of reporting success.
- **Docker.** Manifests and layers that lose their tag are reclaimed by [registry garbage collection](../formats/docker.md#garbage-collection), which releases blobs through the same path.
- **Orphans.** The cleanup worker also collects unreferenced blobs older than 1 hour, in batches of 500. It scans the blob table in windows of 20 000 rows and continues from where the last pass stopped, so blobs that cannot be deleted never starve the rest. Only one server replica runs it at a time. The trash purge and a repository delete run the same collection after they finish. Each of the three keeps its own position in the blob table, so one never moves the scheduled pass past blobs it has not examined. The count in the log lines is the blobs actually removed; a blob that was referenced again in the meantime is kept and not counted.
- **Generated metadata.** Superseded and orphaned generated-metadata blobs (Maven `maven-metadata.xml`) are freed when they are replaced, or when their repository is deleted or purged. Their bytes go through the same deferred deletion queue.

Policies that decide which artifacts to delete are in [cleanup and quotas](../repositories/cleanup-and-quotas.md).

### Deferred blob deletion

Deleting a blob's record and deleting its bytes are separate steps. The bytes of an unreferenced blob are queued and removed later by the **blob deletion sweeper**. The same queue handles the old copy of a blob [moved between tiers](storage-tiering.md#deferred-removal-of-the-old-copy). A running [backup](backup.md#consistency-without-locks) therefore never finds a blob of its snapshot missing.

The sweeper deletes a queued entry when all of this holds:

- `blobDeletion.grace` has passed since it was queued. The default is 24 h, and `0` removes it as soon as the other conditions hold.
- No live backup lease belongs to a snapshot that may still list the blob. The lease of a crashed backup expires after `backup.leaseTtl`.
- The blob is still unreferenced, checked under the per-blob lock: no blob record exists again and no package, metadata or delta row references it. For an old tier copy, the blob must not be placed on that backend.

A blob uploaded again during the grace period keeps its bytes, and the entry is dropped. An object that is already gone counts as deleted. A failed delete leaves the entry queued, and the sweeper retries it after a few minutes. Every server replica runs the sweeper, and replicas never process an entry twice.

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_BLOB_DELETION_GRACE` | `blobDeletion.grace` | `24h` | minimum time between deleting a blob and removing its bytes; `0` means no grace; must not be negative |
| `OPENARTIFACT_BLOB_DELETION_SWEEP_INTERVAL` | `blobDeletion.sweepInterval` | `1m` | how often the sweeper runs; `0` disables it on this replica |
| `OPENARTIFACT_BLOB_DELETION_SWEEP_BATCH` | `blobDeletion.sweepBatch` | `200` | entries claimed per pass; must be positive |

What this means for you:

- Disk usage and bucket size lag behind a delete by the grace period.
- The repository [storage quota](../repositories/cleanup-and-quotas.md#what-counts) counts package rows, so it is released at the delete.
- `openartifact_blob_deletions_pending` exports the queue length ([monitoring](monitoring.md#metrics)).
- Bytes that no row references and that were never queued (a crashed upload) are found by the orphan collector, which queues them the same way.

## Presigned URLs

With an object-store backend, OpenArtifact can hand the byte transfer to the object store. Instead of streaming a blob, it answers `307 Temporary Redirect` with a presigned GET URL, and the client downloads directly from the bucket. Large artifacts then no longer flow through the server. The feature is opt-in and off by default.

Presigned downloads work on `s3` (including `garage` and `seaweedfs`), `gcs` (through its S3-compatible HMAC API, same code path) and `azure` (a read-only shared access signature). `filesystem` and `oci` have no download presigning, so downloads keep streaming. [Presigned uploads](#presigned-uploads) work on every backend.

To turn on redirected downloads for S3, set:

```sh
STORAGE_PRESIGN_DOWNLOADS=true
```

### Presigned URL settings

| Environment variable | YAML key (`storage:`) | Default | Meaning |
|---|---|---|---|
| `STORAGE_PRESIGN_DOWNLOADS` | `presignDownloads` | `false` | Redirect blob downloads to presigned URLs |
| `STORAGE_PRESIGN_MIN_SIZE` | `presignMinSize` | `1048576` (1 MiB) | Blobs smaller than this many bytes are streamed. `0` redirects everything. A negative value stops startup |
| `STORAGE_S3_PRESIGN_TTL` | `presignTtl` | `5m` | Lifetime of presigned GET URLs, clamped to `1m`..`1h` |
| `STORAGE_PRESIGN_ENDPOINT` | `presignEndpoint` | empty | Absolute `http(s)` URL without query or fragment, used only for signing; see [Presign endpoint](#presign-endpoint) |
| `STORAGE_PRESIGN_UPLOAD_TTL` | `presignUploadTtl` | `30m` | Lifetime of presigned PUT upload URLs, clamped to `1m`..`12h` |
| `STORAGE_PRESIGN_MAX_PENDING_UPLOADS` | `presignMaxPendingUploads` | `20` | Maximum outstanding (not completed, not expired) presigned uploads per user; `0` means unlimited. Exceeding it answers `429` with `Retry-After` |

### Per-repository override

Each repository has a `presign_downloads` field. You can set it on `POST /api/v1/repositories` and `PATCH /api/v1/repository/{name}`, and `GET` returns it.

- `true` always redirects.
- `false` always streams.
- `null` inherits `presignDownloads`.

In a PATCH, omitting the field leaves it unchanged, and `null` resets it to inherit. The web UI has it on the repository Settings tab as "Direct downloads (presigned URLs)" with the choices server default, always redirect and always stream. The `oa` CLI has no flag for it.

### How a redirected download works

```
client                OpenArtifact                      object store
  | GET /.../file.jar     |                                  |
  |---------------------->| authenticate + RBAC check        |
  |                       | blob >= presignMinSize?          |
  |                       | sign GET URL (presignTtl)        |
  |<-- 307 Location: URL -|                                  |
  | GET URL (no OpenArtifact credentials)                    |
  |--------------------------------------------------------->|
  |<----------------------------------------- bytes ---------|
```

- Authorisation always happens before the redirect. Clients without read permission never receive a URL.
- The redirect response has `Cache-Control: private, no-store`, `Referrer-Policy: no-referrer` and an empty body.
- The signed URL asks the object store to answer with `response-content-disposition` (`attachment; filename="..."`) and `response-content-type`, so the client gets the right file name and type.
- The server never logs the URL, because its query string is a credential.

### What is redirected

One shared hook covers the binary download of every format. The server redirects blobs only, never metadata.

| Redirected | Always streamed |
|---|---|
| apk, conan, conda, ivy and sbt artifacts; cargo crates; deb `pool/*.deb` files; OCI/Docker layers and configs; Go module zips; Helm charts; Maven jars, poms and other artifacts; npm tarballs; NuGet nupkg; PyPI files; raw files; rpm `*.rpm` files; RubyGems gems | `maven-metadata.xml`, checksum and `.asc` files; Docker manifests; deb `Packages`, `Release`, `InRelease`; rpm `repodata`; any other index or metadata response |

Further rules:

- `HEAD` requests are always answered locally.
- Group repositories delegate to the member that serves the file.
- Proxy repositories redirect on cache hits for blob paths only, after checking that the cached object exists. A stale copy served while the upstream is down (it carries a `Warning` header) is streamed.
- The server increments download counters (cargo, NuGet) before the redirect.

### When the server streams instead

The server streams the blob, and the download still succeeds, when any of these hold:

- the feature is off, globally or by the repository override;
- the backend does not support presigning;
- the blob is smaller than `presignMinSize`;
- the request is not a `GET`;
- signing fails, the repository lookup fails, or the object is missing.

### Metrics

`openartifact_blob_downloads_total{mode, reason}` counts blob downloads. `HEAD` and other non-`GET` requests are not counted. See [monitoring](monitoring.md).

| `mode` | `reason` | Meaning |
|---|---|---|
| `redirect` | `presigned` | answered with a `307` |
| `stream` | `disabled` | feature off globally or for the repository |
| `stream` | `unsupported` | backend cannot presign |
| `stream` | `below_min_size` | blob smaller than `presignMinSize` |
| `stream` | `sign_failed` | the signer returned an error |
| `stream` | `repo_lookup_failed` | the repository override could not be read |
| `stream` | `missing_object` | the object was not found or its size could not be read |

### Security model

- The authorisation check (authentication and [RBAC](../security/rbac.md)) runs before the redirect.
- The presigned URL is a bearer credential. Anyone holding it can read that single blob until it expires. Keep `presignTtl` short (the default is 5 minutes) and keep the bucket private. Presigned URLs work with private buckets.
- Redirect responses are marked `no-store` and `no-referrer`, and the URL is never written to logs.

### Presign endpoint

The server uses `endpoint` for its own reads and writes. That address can be cluster-internal and unreachable for clients, for example `http://otel-minio.observability.svc:9000`. Signed URLs would then point at an unreachable host. Set `presignEndpoint` to the public address, which the server uses only for signing:

```yaml
storage:
  type: s3
  endpoint: http://otel-minio.observability.svc:9000
  presignEndpoint: https://s3.example.com
  presignDownloads: true
```

- When `presignEndpoint` is empty, the server signs URLs against `endpoint`.
- The signature covers the `Host` header. The public URL must reach the same bucket with the same `Host` and path-style addressing, in the same region and with the same credentials.
- A reverse proxy in front of the object store must not rewrite `Host`.
- Addressing follows `forcePathStyle` for the public endpoint too. The AWS SDK falls back to virtual-hosted addressing (`<bucket>.<host>`) for a hostname endpoint, and uses a path only for an IP address. With a hostname such as `https://s3.example.com` on Garage, MinIO or SeaweedFS, keep `forcePathStyle: true` (it is implied for `garage` and `seaweedfs`). Otherwise the signed URL points at a host that does not exist.

### Client compatibility

A smoke test against Garage S3 (server binary, `presignEndpoint` on a different host name than `endpoint`) verified these clients. `curl -L` worked on an OCI layer and config blob, a Maven jar and a pom, and a raw file. `podman pull` of an image from a Docker repository followed the `307` for both the config and the layer blob.

Everything else in the table is expected behaviour from the clients' documented redirect handling and has not been individually verified.

| Client | Status | Notes |
|---|---|---|
| curl `-L` | tested | |
| podman | tested | `podman pull` completed and both blobs came from the object store |
| Maven, Gradle | expected to work | Apache HttpClient follows `307` on `GET`; credentials are scoped to the original host |
| npm | expected to work | follows redirects and drops `Authorization` across hosts; npm verifies integrity against the packument |
| pip | expected to work | `requests` follows the redirect and drops `Authorization` on a host change |
| docker, containerd, skopeo | expected to work | the registry spec allows blob redirects and distribution's S3 `redirect` feature does the same; registry credentials are not forwarded to the other host |
| Helm | expected to work | Go `net/http` strips `Authorization` on cross-domain redirects |
| apt | expected to work | follows redirects unless `Acquire::http::AllowRedirect=false`; an https endpoint needs `apt-transport-https`, and apt may refuse an https to http redirect |
| dnf, yum | expected to work | librepo/libcurl follows redirects |
| cargo, conan, conda, gem, `go` (GOPROXY), NuGet | expected to work | standard HTTP stacks that follow redirects |
| ivy, sbt | caveat | the Java `HttpURLConnection` handler does not follow redirects across protocols, so the redirect works only when the scheme stays the same |

For ivy and sbt, set `presign_downloads` to `false` on those repositories unless the object store uses the same scheme as OpenArtifact.

### Network and CORS requirements

- Clients must reach the presign endpoint host directly, not only the OpenArtifact server.
- Use TLS on the endpoint when OpenArtifact is served over https. Clients and browsers may block an https to http downgrade, and browsers block mixed content.
- Bucket CORS is needed only when browsers fetch artifacts with XHR or `fetch` from the web UI. CLI and package-manager clients do not need it.

This S3 JSON CORS rule allows the UI origin:

```json
[
  {
    "AllowedOrigins": ["https://artifacts.example.com"],
    "AllowedMethods": ["GET", "HEAD"],
    "AllowedHeaders": ["*"],
    "ExposeHeaders": ["ETag", "Content-Length", "Content-Disposition"],
    "MaxAgeSeconds": 3000
  }
]
```

Add `"PUT"` to `AllowedMethods` only for browser-based uploads. The CLI does not need it.

## Presigned uploads

Hosted raw repositories accept uploads that bypass the API path of the server. The client uploads the bytes straight to the backend. The server then reads the object back (internal traffic) to verify it, and records it. Other formats are not covered.

Files up to 5 GiB use one `PUT`. Larger files, up to 5 TiB, use a [multipart upload](#multipart-uploads). Every backend in this table supports both. If the configured store cannot stage uploads (for example a tiered store whose primary tier is not one of them), the calls answer `501` with the code `presign_unsupported` and record nothing; upload through the normal `PUT` instead.

| Backend | Upload URL | Notes |
|---|---|---|
| `s3`, `garage`, `seaweedfs` | presigned `PUT` (SigV4) | multipart through S3 `UploadPart`; promoting an object above 4 GiB uses `UploadPartCopy` because `CopyObject` refuses sources above 5 GiB |
| `gcs` | presigned `PUT` through the S3-compatible API | same code path as S3; parts are not verifiable against the real service here |
| `azure` | service SAS `PUT` (block blob) | Azure limits a single `PUT` to 5000 MiB, so larger files must use multipart (staged blocks, committed at `complete`). `azureEndpoint` points at Azurite or a sovereign cloud |
| `filesystem` | short-lived HMAC-signed URL served by OpenArtifact itself | the URL uses the request origin, so the client must reach OpenArtifact at the address it called (an explicit `BASE_URL` overrides it); staging lives in `<path>/staging` |
| `oci` (registry) | the same HMAC-signed URL | staging spool in `stagingPath` (`STORAGE_STAGING_PATH`, required: startup fails without it while uploads are relayed); the verified blob is then pushed to the registry |

An OCI registry has no pre-authenticated upload URLs. To use Oracle Cloud Object Storage with presigned URLs, configure it as `type: s3` through its S3-compatibility endpoint.

### Upload a file with presigned URLs

With the CLI, the three steps below are one command:

```sh
oa artifact upload --presigned [--path P] [--content-type T] <repo> <file>
```

With the API, the steps are these:

1. `POST /api/v1/repositories/{name}/uploads` with `{"path": "...", "size": N, "sha256": "...", "content_type": "..."}` (`content_type` is optional). You need write permission on the repository and on the path. It answers `201`:

   ```json
   {"id": "...", "upload_url": "https://...", "method": "PUT", "headers": {"Content-Length": "..."}, "expires_at": "..."}
   ```

2. `PUT` the bytes to `upload_url` with the returned `headers`, and without the OpenArtifact `Authorization` header. The URL lives for `presignUploadTtl`.
3. `POST /api/v1/repositories/{name}/uploads/{id}/complete`. It answers `201 {"id", "path", "sha256", "size"}`.

```sh
FILE=app.tar.gz
SHA=$(sha256sum "$FILE" | cut -d' ' -f1)
SIZE=$(stat -c %s "$FILE")

# 1. ask for an upload URL
curl -sS -X POST https://artifacts.example.com/api/v1/repositories/files/uploads \
  -H "Authorization: Bearer $OA_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"path\":\"$FILE\",\"size\":$SIZE,\"sha256\":\"$SHA\"}" > upload.json

# 2. PUT the bytes to the object store (no OpenArtifact credentials)
curl -sS -X PUT "$(jq -r .upload_url upload.json)" -H "Content-Length: $SIZE" --data-binary @"$FILE"

# 3. complete
curl -sS -X POST "https://artifacts.example.com/api/v1/repositories/files/uploads/$(jq -r .id upload.json)/complete" \
  -H "Authorization: Bearer $OA_TOKEN"
```

Add any other keys from the returned `headers` object as `-H` options in step 2.

Rules for step 1:

- `size` is at most 5 GiB for a single `PUT`. Above that, set `multipart` (see [Multipart uploads](#multipart-uploads)).
- `Content-Length` is signed into the URL, so the backend rejects any other size.
- The response carries `Cache-Control: no-store`, because `upload_url` is a bearer credential.
- Each user may have at most `presignMaxPendingUploads` outstanding uploads (default 20, `0` means unlimited). An outstanding upload is one not yet completed and not expired. Beyond that limit the call answers `429` with a `Retry-After` header.

### What `complete` does

1. It checks the staging object's size.
2. It re-hashes the staging object against the declared sha256 (internal traffic between the server and the backend).
3. It copies the object server-side to its content-addressed key.
4. It records the artifact exactly like a raw `PUT`: the path is overwritten, the `artifact.uploaded` webhook fires, and a replaced blob is cleaned up.
5. It deletes the staging object.

Details:

- Only the user who created the upload, or an admin, can complete it (`403` otherwise).
- Verification and commit run detached from the HTTP request, so a client disconnect or timeout does not abort them. Their timeout is 30 minutes plus one second per 20 MiB of the declared size, at most 6 hours.
- `complete` is idempotent. While a verification runs, another `complete` answers `409` with code `upload_in_progress` and a `Retry-After` header. After a success, a retry answers `200` with the same body (the first success is `201`).
- The upload row is kept, marked completed, until the cleanup worker removes it after `expires_at`.
- After `presignUploadTtl` expires, an upload can still be completed during a 1 hour grace period.
- If a second `PUT` overwrites the staging object during or after verification, the server detects it through an ETag precondition on the read and on the copy. `complete` answers `409` `upload_changed`, deletes the staging object and records nothing.
- With [storage tiers](storage-tiering.md#writes-that-race-a-move), completing an upload of a blob already placed on a cold tier does not copy it back onto the primary store.

Errors from `complete`:

| Status | Cause |
|---|---|
| `400` | repository is not raw/hosted, or bad input |
| `403` | caller is neither the creator of the upload nor an admin |
| `404` | unknown upload |
| `409` | see the codes below |
| `410` | the upload expired |
| `422` | size or sha256 mismatch; the server discards the staging object and the upload and records nothing |
| `501` | the backend cannot do it |

The `409` codes:

- `upload_missing`: nothing was uploaded to the staging key yet.
- `upload_incomplete`: a part of a multipart upload is missing or has the wrong size. The message names the parts. Upload them and call `complete` again.
- `upload_in_progress`: a verification is already running. Retry after `Retry-After`.
- `upload_changed`: "staging object changed during verification".

### How filesystem and OCI uploads are signed

For the filesystem and OCI backends the URL is `<origin>/upload-relay/<id>/<part>?exp=<unix>&sig=<hex>`.

- `<origin>` is the origin of the request that created the upload (scheme and host). The server believes `X-Forwarded-*` headers only from `OPENARTIFACT_TRUSTED_PROXIES`. If `BASE_URL` is set, it always wins.
- The HMAC-SHA256 signature covers the upload id, part number, size and expiry. A random key kept next to the staging data (`<staging>/.signing-key`) signs it. Replicas on a shared volume share that key.
- The route needs no `Authorization` header. The signature is the credential, exactly like a presigned bucket URL.
- A new `PUT` replaces a part that was already uploaded. A finalized object is never overwritten.
- Once `complete` starts, or the last part arrives, the upload is sealed. Relay `PUT`s then answer `409`, `complete` waits for `PUT`s that are still writing, and the published object never changes afterwards. The bytes that were hashed are therefore the bytes that get promoted.
- On the filesystem backend, the staging directory must belong to one server, or to replicas that share the volume with working file locks.

### Multipart uploads

Use multipart for a file above 5 GiB, or any file you choose, up to 5 TiB. The part size is 5 MiB to 4000 MiB, with at most 10000 parts. When you omit `part_size`, the server picks one of at least 64 MiB that keeps the count within the limit. The server stores the part size with the upload, so the plan cannot change between calls.

With the CLI, `oa artifact upload --presigned` switches to multipart on its own above 5 GiB. `--multipart` forces it, `--part-size` sets the part size, and `--parallel` sets the number of parts in flight. Each part is retried with backoff, and the CLI aborts the upload when a part fails for good. See the [CLI reference](../integrations/cli.md).

With the API:

1. `POST /api/v1/repositories/{name}/uploads` with `"multipart": true` (and optionally `"part_size": N`) added to the body. It answers `201 {"id", "multipart": true, "part_size", "part_count", "expires_at"}` and no `upload_url`. A size above 5 GiB without `multipart` answers `400 invalid_size`. An empty file cannot be multipart.
2. `POST /api/v1/repositories/{name}/uploads/{id}/parts` with `{"part_numbers": [1, 2, 3]}` (1 to 100 distinct numbers within `part_count`). It answers `200 {"parts": [{"part_number", "upload_url", "method": "PUT", "headers", "size"}], "expires_at"}`. Every part is exactly `part_size` bytes except the last, and the size is signed into the URL. Repeat the call for more parts or to re-sign a part. Each call extends the upload's deadline to a full URL lifetime plus the grace period, so an upload that makes progress does not expire however long it takes.
3. `PUT` every part, in any order and in parallel. Retry a failed part with a fresh `PUT`. You do not need to record the returned `ETag`s, because `complete` lists the parts the backend holds.
4. `POST .../complete` as for a single `PUT`. The server first assembles the parts into the staging object and answers `409 upload_incomplete` if one is missing. It then runs the same size check, sha256 re-hash and promotion. The server always computes the sha256 over the assembled bytes, so each blob still holds exactly the bytes with its hash.
5. `POST .../uploads/{id}/abort` (creator or admin, `204`) discards a multipart upload you will not complete: unfinished parts, staging object and row. Use it instead of waiting for the expiry sweep.

```sh
# 1. plan (SIZE above 5 GiB)
curl -sS -X POST https://artifacts.example.com/api/v1/repositories/files/uploads \
  -H "Authorization: Bearer $OA_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"path\":\"$FILE\",\"size\":$SIZE,\"sha256\":\"$SHA\",\"multipart\":true}" > upload.json
ID=$(jq -r .id upload.json); PS=$(jq -r .part_size upload.json); N=$(jq -r .part_count upload.json)

# 2-3. sign and upload part 1 (repeat for 2..N)
curl -sS -X POST "https://artifacts.example.com/api/v1/repositories/files/uploads/$ID/parts" \
  -H "Authorization: Bearer $OA_TOKEN" -H 'Content-Type: application/json' \
  -d '{"part_numbers":[1]}' > part1.json
dd if="$FILE" bs="$PS" skip=0 count=1 iflag=fullblock status=none |
  curl -sS -X PUT "$(jq -r '.parts[0].upload_url' part1.json)" \
    -H "Content-Length: $(jq -r '.parts[0].size' part1.json)" --data-binary @-

# 4. complete
curl -sS -X POST "https://artifacts.example.com/api/v1/repositories/files/uploads/$ID/complete" \
  -H "Authorization: Bearer $OA_TOKEN"
```

Object-store limits to keep in mind when the server promotes a large object:

- **S3 and Garage** cap a copy source at 5 GiB. The server handles this with ranged `UploadPartCopy`.
- **GCS** has no `UploadPartCopy`, and a plain copy is one synchronous request whose duration grows with the object. The server composes the staged object instead (a one-component XML API `compose`, pinned to the verified generation). No bytes are rewritten, and the time does not depend on the size.

### Staging and cleanup

Uploads land under the key prefix `staging/<uuid>` in the bucket (`<path>/staging` on the filesystem, the spool directory for OCI). The backend also tracks open multipart uploads. On S3, abandoned `CreateMultipartUpload` sessions bill for their parts. On Azure, uncommitted blocks expire after a week by themselves.

A cleanup worker runs every 5 minutes. It:

- deletes uploads (database rows and staging objects) that were never completed, once expiry plus the grace period has passed;
- removes the rows of completed uploads after `expires_at`;
- sweeps the `staging/` prefix: objects older than `presignUploadTtl` plus the 1 hour grace period that have no upload row are deleted, for example after a re-`PUT` following cleanup, a mismatch, or a repository delete cascade.

The sweep and the abort endpoint also abort the backend's in-progress multipart upload. As defence in depth, add a bucket lifecycle rule that expires the `staging/` prefix after 1 to 2 days, plus an `AbortIncompleteMultipartUpload` rule (`DaysAfterInitiation: 2`).

### Quota, license and secret scanning

The raw `PUT` does not run license policy or secret scanning inline, because they are on-demand scan endpoints. The presigned raw upload behaves the same.

The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced on both paths:

- The raw `PUT` reserves the quota before storing.
- A presigned upload cannot be checked before the bytes land in the bucket. Only the declared `size` is bounded before that. The server reserves the quota at `complete`, from the verified size.
- An over-quota completion answers `507`, records nothing and leaves the upload pending. Complete it again once space is free. Otherwise it expires like any pending upload.

### Why OCI blob uploads are not presigned

Docker and OCI pushes stay on the registry. In the distribution flow (`POST /v2/<name>/blobs/uploads/`, `PATCH` chunks, `PUT ?digest=`) the registry holds the upload session and verifies or computes the digest itself.

Clients such as docker, containerd and skopeo send the `PATCH` and `PUT` to the `Location` the registry returns, with registry credentials. They cannot be pointed at an object-store presigned URL, for three reasons:

- A chunked `PATCH` of arbitrary chunks has no object-store equivalent.
- The final `PUT` must carry a digest the registry verifies.
- Cross-repository mounts need registry state.

Blob downloads (pulls) are redirected, as with distribution's S3 `redirect` option.

## Stored secrets

The server encrypts credentials kept in the database with AES-256-GCM. The key comes from the environment variable `OPENARTIFACT_SECRET_KEY` only, not from YAML. It must be at least 16 bytes. Stored values look like `enc:v2:<base64 nonce+ciphertext>`.

The AES-256 key is derived from `OPENARTIFACT_SECRET_KEY` with HKDF-SHA256 (fixed salt and context string, so the same secret yields the same key on every replica). HKDF does not slow down guessing, so use a high-entropy value such as the `openssl` command below, not a human-chosen passphrase. Values written by earlier releases carry the `enc:v1:` prefix (key = plain SHA-256 of the secret). They stay readable with no migration, and the server rewrites them as `enc:v2:` the next time each record is saved.

**Rollback:** a release that predates `enc:v2:` cannot read `enc:v2:` values. After upgrading, rolling back to such a release makes every secret written or re-saved since the upgrade unreadable until you re-enter it. Take a database backup before upgrading, and keep it if you may roll back.

Generate a key and set the same one on every replica:

```sh
OPENARTIFACT_SECRET_KEY="$(openssl rand -base64 32)"
```

Encrypted fields:

| Field | Where |
|---|---|
| webhook signing secret | [webhooks](webhooks.md) |
| proxy repository upstream password | [hosted, proxy, group](../repositories/hosted-proxy-group.md) |
| SSO client secret | [authentication](../security/authentication.md) |
| AI provider API key | [AI analysis](../integrations/ai-analysis.md) |
| credentials of storage backends created under **Settings > Storage backends** | see [Storage backend records](#storage-backend-records) |

Rules:

- Without the key, the server starts but stores these values as plaintext and logs a warning (`OPENARTIFACT_SECRET_KEY not set ...`). A key shorter than 16 bytes aborts startup.
- The API rejects a credential that itself starts with `enc:v1:` or `enc:v2:` (`400 validation_failed`). A submitted value is always plaintext and always sealed, so a create or update cannot replay a stored ciphertext to read it back.
- Values without an `enc:v1:` or `enc:v2:` prefix are read as plaintext. Enabling the key later therefore keeps old rows working, and the server re-encrypts them when they are saved again.
- Changing or losing the key makes existing ciphertexts unreadable. Restore the old key, or re-enter the secrets.

### Storage backend records

Named backend definitions are kept through the admin API (admin only) and under **Settings > Storage backends** (`/settings/backends`):

- `GET /api/v1/admin/storage` and `POST /api/v1/admin/storage`
- `GET`, `PATCH` and `DELETE /api/v1/admin/storage/{name}`
- `POST /api/v1/admin/storage/{name}/test`

Behaviour:

- Credential keys in `config` (`access_key`, `secret_key`, `password`, `account_key`, `hmac_key`, `hmac_secret` and similar) are encrypted at rest and never returned. The API returns a `has_<key>` flag instead, and keeps the stored value when an update omits the key.
- `test` checks connectivity for every kind (`filesystem`, `s3`, `garage`, `seaweedfs`, `gcs`, `azure` and `oci`). Object stores get a bucket or container check with the stored credentials. `test` records the status as `healthy` or `unreachable`.
- The records do not route traffic. The server always reads and writes blobs through the single backend configured with `STORAGE_*`, plus the [tiers](storage-tiering.md) that map a record to a placement target.
- A record has no "default" flag. The default store is the one configured on this page.

The server checks the endpoints of these records twice:

- The save and test calls refuse an endpoint that resolves to a loopback, private, link-local or cloud-metadata address. `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` allows the private ones, for MinIO or Garage on a private network. Metadata addresses stay blocked.
- The storage client then makes every connection through the same guard, so a host name that resolves differently later (DNS rebinding) is refused too.

This covers the S3 family, GCS, Azure (`endpoint`) and OCI backends. A configured `HTTP_PROXY` or `HTTPS_PROXY` is still honoured and is trusted as the egress policy. The primary store (`STORAGE_*` in the config file) is written by the operator and dials unrestricted.

An Azure `account` must be a real storage account name (3-24 lowercase letters and digits), because it becomes the host in `https://<account>.blob.core.windows.net`. The test answer for GCS and Azure gives a fixed message for a bad configuration, and the cause is only logged.

### The primary store in the admin API

The store configured with `STORAGE_*` is not a record, but the API shows it. `GET /api/v1/admin/storage` returns it as a `primary` object next to `items`, and it is not part of `items` or `total`. `GET /api/v1/admin/storage/primary` returns the same object:

```json
{
  "name": "primary", "kind": "s3", "source": "config",
  "location": {"endpoint": "https://s3.example.com", "bucket": "artifacts", "region": "eu-west-1"},
  "status": "healthy", "status_message": null, "last_checked_at": "2026-10-27T09:00:00Z",
  "usage": {"blob_count": 1204, "size_bytes": 3407872}
}
```

- `location` is where the blobs live: `path`, or `endpoint`, `bucket` and `region`, or `account` and `container`, or `repository`, depending on the kind. It never holds a credential. Access and secret keys, HMAC and account keys and the OCI password are not part of it, and an endpoint URL loses its user info, query and fragment.
- `status` is a live health check through the store that serves every request: a write probe of the directory for `filesystem`, a bucket or container check for the object stores, `GET /v2/` for `oci`. The values are `healthy`, `unreachable` (with a fixed `status_message`; the raw error is only logged) or `unknown`. Reads reuse the last probe for 10 seconds.
- `usage` counts the blobs placed on the primary store, that is those not moved to a [tier](storage-tiering.md).
- `POST /api/v1/admin/storage/primary/test` probes now, refreshes the status and answers like the test of a record: `{"ok": true}`, or `{"ok": false, "error": "connection_failed", "message": ...}`.
- The primary store is read-only. `PATCH` and `DELETE` on `/api/v1/admin/storage/primary` answer `405 read_only`. Change `STORAGE_*` and restart instead.
- The names `primary` and `replication` are reserved, because they are static paths under `/api/v1/admin/storage` that win over `/{name}`. You cannot create or rename a record to either.

**Settings > Storage backends** lists the primary store first, with a *configuration* badge.

## Health

`/health` reports the real state of the configured store. So do the `status` of `GET /api/v1/system/info` and the `storage_status` of `GET /api/v1/admin/system/info`. The check depends on the backend:

- a bucket check for `s3`, `garage`, `seaweedfs` and `gcs`;
- a container check for `azure`;
- `GET /v2/` for `oci`;
- a write probe of the blob directory for `filesystem`.

The probe is cached for 10 seconds and limited to 3 seconds. A failing store shows as `"status":"error"` without any detail in the response, and the cause is in the server log. `/ready` probes only S3-family stores, on every call. See [monitoring](monitoring.md).

## Moving data between backends

There is no built-in migration command. You have two options:

- **Restore a full backup.** A `full` backup taken on any backend can be restored into a server configured with another backend. The server writes blobs to whichever backend is configured at restore time. See [backup and restore](backup.md#restore).
- **Replicate, then swap.** [Replication](replication.md) copies blobs to a replica backend of any type. You can then [make the replica the new primary](replication.md#make-the-replica-the-new-primary).
