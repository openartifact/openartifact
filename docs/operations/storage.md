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

To keep cold blobs on cheaper storage, register extra backends and map them to named tiers with age, idle-time or size policies: [storage tiers](storage-tiering.md). New uploads always land on the backend configured here.

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
| `STORAGE_S3_PRESIGN_TTL` | `presignTtl` | s3, gcs | Go duration, default `5m`, clamped to `1m`..`1h`: lifetime of presigned download URLs; see [Presigned URLs](#presigned-urls) |
| `STORAGE_PRESIGN_DOWNLOADS` | `presignDownloads` | s3, gcs | default `false`; answer blob downloads with a `307` redirect to a presigned URL |
| `STORAGE_PRESIGN_MIN_SIZE` | `presignMinSize` | s3, gcs | bytes, default `1048576`; smaller blobs are streamed |
| `STORAGE_PRESIGN_ENDPOINT` | `presignEndpoint` | s3, gcs | public base URL used only for signing |
| `STORAGE_PRESIGN_UPLOAD_TTL` | `presignUploadTtl` | s3, gcs | Go duration, default `30m`, clamped to `1m`..`12h`: lifetime of presigned upload URLs |
| `STORAGE_PRESIGN_MAX_PENDING_UPLOADS` | `presignMaxPendingUploads` | s3, gcs | default `20`, `0` = unlimited: outstanding presigned uploads allowed per user |
| `OPENARTIFACT_GCS_BUCKET` | `gcsBucket` | gcs | required |
| `OPENARTIFACT_GCS_HMAC_KEY` | `gcsHmacKey` | gcs | required |
| `OPENARTIFACT_GCS_HMAC_SECRET` | `gcsHmacSecret` | gcs | required |
| `OPENARTIFACT_AZURE_ACCOUNT_NAME` | `azureAccountName` | azure | required |
| `OPENARTIFACT_AZURE_ACCOUNT_KEY` | `azureAccountKey` | azure | required, base64 as shown by Azure |
| `OPENARTIFACT_AZURE_CONTAINER_NAME` | `azureContainerName` | azure | required |
| `OPENARTIFACT_AZURE_ENDPOINT` | `azureEndpoint` | azure | blob service URL for Azurite or a sovereign cloud; empty = `https://<account>.blob.core.windows.net`. With Azurite use `http://host:10000/devstoreaccount1` |
| `STORAGE_STAGING_PATH` | `stagingPath` | oci | spool directory for [server-relayed presigned uploads](#presigned-uploads); default `<system temp dir>/openartifact-staging` |
| `STORAGE_BUCKET` | `bucket` | oci | the repository name inside the registry |
| `STORAGE_OCI_USERNAME` | `ociUsername` | oci | Basic auth, sent only to the registry host |
| `STORAGE_OCI_PASSWORD` | `ociPassword` | oci | |
| `STORAGE_OCI_INSECURE` | `ociInsecure` | oci | swaps in a plain HTTP transport for `http://` registries. It does not disable TLS verification |

The Azure account endpoint defaults to `https://<account>.blob.core.windows.net/<container>`; `azureEndpoint` overrides the service URL (Azurite, sovereign clouds). Azure and OCI clients use a 30 s HTTP timeout.

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

S3, Azure and OCI uploads must carry the digest (`sha256` metadata); the OCI store skips the upload when the blob already exists. S3 uploads use multipart with 16 MiB parts and 4 parallel parts. Azure uploads are spooled to a temporary file in the system temp directory first.

The filesystem store writes to a `blob-upload-*.tmp` file inside `STORAGE_PATH`, fsyncs it and renames it into place, so `STORAGE_PATH` must be a single filesystem. Leftover `.tmp` files are safe to delete when the server is stopped.

## Deduplication and deletion

Identical content is stored once, across all repositories and formats. Rows in the database reference blobs by digest, and a blob is removed only when nothing references it:

- With the default [trash](trash.md) retention, deleting an artifact only marks its row as deleted: the row still references its blob, so nothing is freed until the item is purged (or the repository's retention is `0`). Purging then follows the path below.
- Permanently deleting an artifact deletes its database row first, then calls `storage.SafeDeleteBlob`, which removes the `blobs` row only if its reference count is zero and queues the bytes for [deferred deletion](#deferred-blob-deletion). The check and the row delete run in one transaction under an advisory lock that uploaders also take, so a concurrent upload of the same content cannot lose its blob.
- Docker manifests and layers that lose their tag are reclaimed by [registry garbage collection](../formats/docker.md#garbage-collection), which releases blobs through the same path.
- The cleanup worker also collects orphans: unreferenced blobs older than 1 hour, in batches of 500. The scan walks the `blobs` table in windows of 20 000 rows with a persistent cursor (a pass covers up to 50 windows, the next pass continues), checking references with an indexed anti-join, so blobs that cannot be deleted never starve the rest and large tables are not rescanned from the start. Only one replica runs it at a time. Superseded and orphaned generated-metadata blobs (maven `maven-metadata.xml`) are freed when they are replaced or when their repository is deleted or purged; their bytes go through the same [deferred deletion](#deferred-blob-deletion) queue. Policies that decide which artifacts to delete are in [cleanup and quotas](../repositories/cleanup-and-quotas.md).

Because blobs are shared, deleting a repository frees disk only for content no other repository holds.

### Deferred blob deletion

Deleting a blob's row and deleting its bytes are separate steps. The bytes of an unreferenced blob (and the old copy of a blob [moved between tiers](storage-tiering.md#deferred-removal-of-the-old-copy)) are queued in the `blob_deletions` table and removed by the **blob deletion sweeper**, so a running [backup](backup.md#consistency-without-locks) never finds a blob of its snapshot missing. An entry is deleted when all of this holds:

- `blobDeletion.grace` has passed since it was queued (default 24 h; `0` removes it as soon as the other conditions hold);
- no live backup lease belongs to a snapshot that may still list the blob (the lease of a crashed backup expires after `backup.leaseTtl`);
- under the per-blob lock the blob is still unreferenced: no `blobs` row exists again and no package, metadata or delta row references it, or, for an old tier copy, the blob is not placed on that backend. A blob uploaded again during the grace period keeps its bytes and the entry is dropped.

An object that is already gone counts as deleted. A failed delete leaves the entry queued and it is retried after its claim expires (a few minutes). Every replica runs the sweeper; entries are claimed in batches with `FOR UPDATE SKIP LOCKED`, so replicas never process an entry twice and nothing is globally locked.

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_BLOB_DELETION_GRACE` | `blobDeletion.grace` | `24h` | minimum time between deleting a blob and removing its bytes; `0` = no grace; must not be negative |
| `OPENARTIFACT_BLOB_DELETION_SWEEP_INTERVAL` | `blobDeletion.sweepInterval` | `1m` | how often the sweeper runs; `0` disables it on this replica |
| `OPENARTIFACT_BLOB_DELETION_SWEEP_BATCH` | `blobDeletion.sweepBatch` | `200` | entries claimed per pass; must be positive |

Consequences: storage is freed after the grace period rather than at the delete (disk usage and bucket size lag behind `delete`), and the repository [storage quota](../repositories/cleanup-and-quotas.md#what-counts), which counts package rows, is released at the delete as before. The queue length is exported as `openartifact_blob_deletions_pending` ([monitoring](monitoring.md#metrics)). Bytes that no row references and that were never queued (a crashed upload) are still found by the orphan collector, which queues them the same way.

## Presigned URLs

With an object-store backend, OpenArtifact can hand the byte transfer to the object store: instead of streaming a blob, it answers `307 Temporary Redirect` with a presigned GET URL and the client downloads directly from the bucket. Large artifacts then no longer flow through the server. It is opt-in and off by default.

Supported backends for presigned downloads: `s3` (including `garage` and `seaweedfs`) and `gcs` (through its S3-compatible HMAC API, same code path), and `azure` (a read-only shared access signature). `filesystem` and `oci` have no download presigning: downloads keep streaming. [Presigned uploads](#presigned-uploads) work on every backend.

### How it works

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

Authorisation always happens before the redirect. The redirect response has `Cache-Control: private, no-store`, `Referrer-Policy: no-referrer` and an empty body. The signed URL asks the object store to answer with `response-content-disposition` (`attachment; filename="..."`) and `response-content-type`, so the client gets the right file name and type. The URL is never logged, because its query string is a credential.

### Settings

| Environment variable | YAML key (`storage:`) | Default | Meaning |
|---|---|---|---|
| `STORAGE_PRESIGN_DOWNLOADS` | `presignDownloads` | `false` | Redirect blob downloads to presigned URLs |
| `STORAGE_PRESIGN_MIN_SIZE` | `presignMinSize` | `1048576` (1 MiB) | Blobs smaller than this many bytes are streamed. `0` redirects everything. A negative value stops startup |
| `STORAGE_S3_PRESIGN_TTL` | `presignTtl` | `5m` | Lifetime of presigned GET URLs, clamped to `1m`..`1h` |
| `STORAGE_PRESIGN_ENDPOINT` | `presignEndpoint` | empty | Absolute `http(s)` URL without query or fragment, used only for signing; see [below](#presign-endpoint) |
| `STORAGE_PRESIGN_UPLOAD_TTL` | `presignUploadTtl` | `30m` | Lifetime of presigned PUT upload URLs, clamped to `1m`..`12h` |
| `STORAGE_PRESIGN_MAX_PENDING_UPLOADS` | `presignMaxPendingUploads` | `20` | Maximum outstanding (not completed, not expired) presigned uploads per user; `0` = unlimited. Exceeding it answers `429` with `Retry-After` |

### Per-repository override

Each repository has a `presign_downloads` field (`POST /api/v1/repositories`, `PATCH /api/v1/repository/{name}`, returned by `GET`): `true` always redirects, `false` always streams, `null` inherits `presignDownloads`. In a PATCH, omitting the field leaves it unchanged and `null` resets it to inherit. The web UI has it on the repository Settings tab as "Direct downloads (presigned URLs)" (server default, always redirect, always stream). The `oa` CLI has no flag for it.

### What is redirected

One shared hook covers the binary download of every format. Only blobs are redirected, never metadata:

| Redirected | Always streamed |
|---|---|
| Maven jars, poms and other artifacts; npm tarballs; PyPI files; raw files; Helm charts; OCI/Docker layers and configs; deb `pool/*.deb` and rpm `*.rpm` files; cargo crates; NuGet nupkg; Go module zips; RubyGems gems; conda, conan, ivy and sbt artifacts | `maven-metadata.xml`, checksum and `.asc` files; Docker manifests; deb `Packages`, `Release`, `InRelease`; rpm `repodata`; any other index or metadata response |

Further rules:

- `HEAD` requests are always answered locally.
- Group repositories delegate to the member that serves the file.
- Proxy repositories redirect on cache hits for blob paths only, after checking that the cached object exists. A stale copy served while the upstream is down (it carries a `Warning` header) is streamed.
- Download counters (cargo, NuGet) are incremented before the redirect.

### Fallbacks to streaming

The server streams the blob as before when any of these hold: the feature is off (globally or by the repository override); the backend does not support presigning; the blob is smaller than `presignMinSize`; the request is not a `GET`; signing fails; the repository lookup fails; the object is missing. A failed presign never fails the download.

### Metrics

`openartifact_blob_downloads_total{mode, reason}` counts blob downloads:

| `mode` | `reason` | Meaning |
|---|---|---|
| `redirect` | `presigned` | answered with a `307` |
| `stream` | `disabled` | feature off globally or for the repository |
| `stream` | `unsupported` | backend cannot presign |
| `stream` | `below_min_size` | blob smaller than `presignMinSize` |
| `stream` | `sign_failed` | the signer returned an error |
| `stream` | `repo_lookup_failed` | the repository override could not be read |
| `stream` | `missing_object` | the object was not found or its size could not be read |

`HEAD` and other non-`GET` requests are not counted. See [monitoring](monitoring.md).

### Security model

- The authorisation check (authentication and [RBAC](../security/rbac.md)) runs before the redirect. Clients without read permission never receive a URL.
- The presigned URL is a bearer credential: anyone holding it can read that single blob until it expires. Keep `presignTtl` short (the default is 5 minutes) and keep the bucket private; presigned URLs work with private buckets.
- Redirect responses are marked `no-store` and `no-referrer`, and the URL is never written to logs.

### Presign endpoint

The server uses `endpoint` for its own reads and writes. When that address is cluster-internal and clients cannot reach it, for example `http://otel-minio.observability.svc:9000`, signed URLs would point at an unreachable host. Set `presignEndpoint` to the public address used only for signing:

```yaml
storage:
  type: s3
  endpoint: http://otel-minio.observability.svc:9000
  presignEndpoint: https://s3.example.com
  presignDownloads: true
```

The signature covers the `Host` header, so the public URL must reach the same bucket with the same `Host` and path-style addressing (same region, same credentials). A reverse proxy in front of the object store must not rewrite `Host`. When empty, URLs are signed against `endpoint`.

Addressing follows `forcePathStyle` for the public endpoint too. The AWS SDK falls back to virtual-hosted addressing (`<bucket>.<host>`) for a hostname endpoint and only uses a path for an IP address, so with a hostname such as `https://s3.example.com` on Garage, MinIO or SeaweedFS keep `forcePathStyle: true` (it is implied for `garage` and `seaweedfs`), or the signed URL points at a host that does not exist.

### Client compatibility

Verified here in a smoke test against Garage S3 (server binary, `presignEndpoint` on a different host name than `endpoint`): `curl -L` on a raw file, a Maven jar and a pom, an OCI layer and config blob, and `podman pull` of an image from a Docker repository, which followed the `307` for both the config and the layer blob. Everything else below is expected behaviour from the clients' documented redirect handling and has not been individually verified.

| Client | Status | Notes |
|---|---|---|
| curl `-L` | tested | |
| Maven, Gradle | expected to work | Apache HttpClient follows `307` on `GET`; credentials are scoped to the original host |
| npm | expected to work | follows redirects and drops `Authorization` across hosts; npm verifies integrity against the packument |
| pip | expected to work | `requests` follows the redirect and drops `Authorization` on a host change |
| podman | tested | `podman pull` completed and both blobs came from the object store |
| docker, containerd, skopeo | expected to work | the registry spec allows blob redirects and distribution's S3 `redirect` feature does the same; registry credentials are not forwarded to the other host |
| Helm | expected to work | Go `net/http` strips `Authorization` on cross-domain redirects |
| apt | expected to work | follows redirects unless `Acquire::http::AllowRedirect=false`; an https endpoint needs `apt-transport-https`, and apt may refuse an https to http redirect |
| dnf, yum | expected to work | librepo/libcurl follows redirects |
| cargo, NuGet, `go` (GOPROXY), gem, conda, conan | expected to work | standard HTTP stacks that follow redirects |
| ivy, sbt | caveat | the Java `HttpURLConnection` handler does not follow redirects across protocols; the redirect works only when the scheme stays the same. Set `presign_downloads` to `false` on ivy and sbt repositories unless the object store uses the same scheme as OpenArtifact |

### Network and CORS requirements

- Clients must reach the presign endpoint host directly, not only the OpenArtifact server.
- Use TLS on the endpoint when OpenArtifact is served over https: clients and browsers may block an https to http downgrade, and browsers block mixed content.
- Bucket CORS is needed only when browsers fetch artifacts with XHR or `fetch` from the web UI. CLI and package-manager clients do not need it. Example (S3 JSON form) allowing the UI origin:

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

  Add `"PUT"` to `AllowedMethods` only for browser-based uploads; the CLI does not need it.

### Presigned uploads

Hosted raw repositories take uploads that bypass the API path of the server: the client uploads the bytes straight to the backend, then the server reads the object back (internal traffic) to verify it and records it. Other formats are not covered. Files up to 5 GiB use one `PUT`; larger files (up to 5 TiB) use a [multipart upload](#multipart-uploads). Every backend supports both:

| Backend | Upload URL | Notes |
|---|---|---|
| `s3`, `garage`, `seaweedfs` | presigned `PUT` (SigV4) | multipart through S3 `UploadPart`; promoting an object above 4 GiB uses `UploadPartCopy` because `CopyObject` refuses sources above 5 GiB |
| `gcs` | presigned `PUT` through the S3-compatible API | same code path as S3; parts are not verifiable against the real service here, see the caveats below |
| `azure` | service SAS `PUT` (block blob) | a single `PUT` is limited to 5000 MiB by Azure, so larger files must use multipart (staged blocks, committed at `complete`). `azureEndpoint` / `OPENARTIFACT_AZURE_ENDPOINT` points at Azurite or a sovereign cloud |
| `filesystem` | short-lived HMAC-signed URL served by OpenArtifact itself | needs `baseUrl` (`BASE_URL`) to be an address the client can reach; staging lives in `<path>/staging` |
| `oci` (registry) | the same HMAC-signed URL | staging spool in `stagingPath` (`STORAGE_STAGING_PATH`, default `<tmp>/openartifact-staging`); the verified blob is then pushed to the registry. An OCI registry has no pre-authenticated upload URLs. To use Oracle Cloud Object Storage with presigned URLs, configure it as `type: s3` through its S3-compatibility endpoint |

For the filesystem and OCI backends the URL is `<baseUrl>/upload-relay/<id>/<part>?exp=<unix>&sig=<hex>`. The HMAC-SHA256 signature covers the upload id, part number, size and expiry and is keyed by a random key kept next to the staging data (`<staging>/.signing-key`, shared by replicas on a shared volume). The route needs no `Authorization` header: the signature is the credential, exactly like a presigned bucket URL. A part already uploaded is replaced by a new `PUT`; a finalized object is never overwritten. Without a usable `baseUrl` the create call answers `501 presign_unsupported`. Once `complete` starts (or the last part arrives) the upload is sealed: relay PUTs are answered `409`, `complete` waits for PUTs that are still writing, and the published object never changes afterwards, so the bytes that were hashed are the bytes that get promoted. On the filesystem backend the staging directory must belong to one server or to replicas sharing the volume with working `flock` locks.

1. `POST /api/v1/repositories/{name}/uploads` with `{"path": "...", "size": N, "sha256": "...", "content_type": "..."}` (`content_type` optional). Needs write permission on the repository and on the path. Answers `201`:

   ```json
   {"id": "...", "upload_url": "https://...", "method": "PUT", "headers": {"Content-Length": "..."}, "expires_at": "..."}
   ```

   `size` is at most 5 GiB for a single `PUT` (above it, set `multipart`, see below). `Content-Length` is signed into the URL, so the backend rejects any other size. The response carries `Cache-Control: no-store` because `upload_url` is a bearer credential. Each user may have at most `presignMaxPendingUploads` (default 20, `0` = unlimited) outstanding uploads, meaning not yet completed and not expired; beyond that the call answers `429` with a `Retry-After` header.
2. The client `PUT`s the bytes to `upload_url` with the returned `headers`, without the OpenArtifact `Authorization` header. The URL lives for `presignUploadTtl`.
3. `POST /api/v1/repositories/{name}/uploads/{id}/complete`. The server checks the staging object's size, re-hashes it (internal traffic between the server and the backend) against the declared sha256, copies it server-side to its content-addressed key, records the artifact exactly like a raw `PUT` (the path is overwritten, the `artifact.uploaded` webhook fires, a replaced blob is cleaned up) and deletes the staging object. Answers `201 {"id", "path", "sha256", "size"}`.

Only the user who created the upload, or an admin, can complete it (`403` otherwise). Verification and commit run detached from the HTTP request, so a client disconnect or timeout does not abort them. Their timeout is 30 minutes plus one second per 20 MiB of the declared size, at most 6 hours. `complete` is idempotent: while a verification is running, another `complete` answers `409` with code `upload_in_progress` and a `Retry-After` header; after a success, a retry answers `200` with the same `{"id", "path", "sha256", "size"}` body (the first success is `201`). The upload row is kept, marked completed, until the cleanup worker removes it after `expires_at`.

If the staging object is overwritten (a second `PUT`) during or after verification, this is detected through an ETag precondition on the read and on the server-side copy: `complete` answers `409` `upload_changed`, the staging object is deleted and nothing is recorded.

Errors from `complete`:

| Status | Cause |
|---|---|
| `400` | repository is not raw/hosted, or bad input |
| `403` | caller is neither the creator of the upload nor an admin |
| `404` | unknown upload |
| `409` | code `upload_missing`: nothing uploaded to the staging key yet; `upload_incomplete`: a part of a multipart upload is missing or has the wrong size (the message names the parts; upload them and call `complete` again); `upload_in_progress`: a verification is already running (retry after `Retry-After`); `upload_changed`: "staging object changed during verification" |
| `410` | the upload expired |
| `422` | size or sha256 mismatch; the staging object and the upload are discarded and nothing is recorded |
| `501` | the backend cannot do it |

After `presignUploadTtl` expires an upload can still be completed during a 1 hour grace period.

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

Add any other keys from the returned `headers` object as `-H` options in step 2. With the CLI the three steps are one command:

```sh
oa artifact upload --presigned [--path P] [--content-type T] <repo> <file>
```

#### Multipart uploads

A file above 5 GiB (and any file you choose, up to 5 TiB) is uploaded in parts. Part size is 5 MiB to 4000 MiB with at most 10000 parts; when `part_size` is omitted the server picks one of at least 64 MiB that keeps the count within the limit. The part size is stored with the upload, so the plan cannot change between calls.

1. `POST /api/v1/repositories/{name}/uploads` with `"multipart": true` (and optionally `"part_size": N`) added to the body. A size above 5 GiB without `multipart` answers `400 invalid_size`; an empty file cannot be multipart. Answers `201 {"id", "multipart": true, "part_size", "part_count", "expires_at"}` and no `upload_url`.
2. `POST /api/v1/repositories/{name}/uploads/{id}/parts` with `{"part_numbers": [1, 2, 3]}` (1 to 100 distinct numbers within `part_count`) answers `200 {"parts": [{"part_number", "upload_url", "method": "PUT", "headers", "size"}], "expires_at"}`. Every part is exactly `part_size` bytes except the last, and the size is signed into the URL. The call can be repeated for more parts or to re-sign a part, and each call extends the upload's deadline to a full URL lifetime plus the grace period, so an upload that makes progress does not expire however long it takes.
3. The client `PUT`s every part, in any order and in parallel, retrying a failed part with a fresh `PUT`. It does not need to record the returned `ETag`s: at `complete` the server lists the parts the backend holds.
4. `POST .../complete` as above. The server first assembles the parts into the staging object (S3 `CompleteMultipartUpload`, Azure block list commit, filesystem concatenation), answering `409 upload_incomplete` if one is missing, then runs the same size check, sha256 re-hash and promotion as for a single `PUT`. The sha256 is therefore always computed by the server over the assembled bytes, so the content-addressed invariant (`blobs/<sha256>` holds exactly the bytes with that hash) is unchanged.
5. `POST .../uploads/{id}/abort` (creator or admin, `204`) discards a multipart upload that will not be completed: unfinished parts, staging object and row. Use it instead of waiting for the expiry sweep.

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

`oa artifact upload --presigned` switches to multipart on its own above 5 GiB; `--multipart` forces it, `--part-size` sets the part size and `--parallel` the number of parts in flight (each part is retried with backoff, and the upload is aborted when a part fails for good). See the [CLI reference](../integrations/cli.md).

Keep the object store's own limits in mind: S3 and Garage cap a copy source at 5 GiB, which the server handles with ranged `UploadPartCopy` when it promotes a large object; GCS has no `UploadPartCopy` and a plain copy is one synchronous request whose duration grows with the object, so promotion composes the staged object (a one-component XML API `compose`, pinned to the verified generation) instead: no bytes are rewritten and the time does not depend on the size.

#### Staging and cleanup

Uploads land under the key prefix `staging/<uuid>` in the bucket (`<path>/staging` on the filesystem, the spool directory for OCI). Open multipart uploads are tracked by the backend as well: on S3, abandoned `CreateMultipartUpload` sessions bill for their parts, and on Azure uncommitted blocks expire after a week by themselves. A worker runs every 5 minutes. It deletes uploads (database rows and staging objects) that were never completed once expiry plus the grace period has passed, removes the rows of completed uploads after `expires_at`, and also sweeps the `staging/` prefix: objects older than `presignUploadTtl` plus the 1 hour grace period that have no upload row (for example a re-`PUT` after cleanup, a mismatch, or a repository delete cascade) are deleted. The sweep and the abort endpoint also abort the backend's in-progress multipart upload. A bucket lifecycle rule expiring the `staging/` prefix after 1 to 2 days, plus an `AbortIncompleteMultipartUpload` rule (`DaysAfterInitiation: 2`) on the bucket, is still recommended as defence in depth, but it is no longer the only mechanism.

#### Quota, license and secret scanning

The raw `PUT` does not run license policy or secret scanning inline (they are on-demand scan endpoints), and the presigned raw upload behaves the same. The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced on both: the `PUT` reserves it before storing; a presigned upload cannot be checked before the bytes land in the bucket, so the reservation is made at `complete` from the verified size, and an over-quota completion answers `507`, records nothing and leaves the upload pending (complete it again once space is free; otherwise it expires like any pending upload). Only the declared `size` is bounded before that.

#### Why OCI blob uploads are not presigned

Docker and OCI pushes stay on the registry. In the distribution flow (`POST /v2/<name>/blobs/uploads/`, `PATCH` chunks, `PUT ?digest=`) the registry holds the upload session and verifies or computes the digest itself. Clients such as docker, containerd and skopeo send the `PATCH` and `PUT` to the `Location` the registry returns, with registry credentials, and cannot be pointed at an object-store presigned URL: a chunked `PATCH` of arbitrary chunks has no object-store equivalent, the final `PUT` must carry a digest the registry verifies, and cross-repository mounts need registry state. Blob downloads (pulls) are redirected, as with distribution's S3 `redirect` option.

The database side is migration `016`, which adds `repositories.presign_downloads` and the `presigned_uploads` table.

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
- The API rejects a credential that itself starts with `enc:v1:` (`400 validation_failed`): a submitted value is always plaintext and is always sealed, so a stored ciphertext cannot be replayed through a create or update to read it back.
- Values without the `enc:v1:` prefix are read as legacy plaintext, so enabling the key later keeps old rows working; they are re-encrypted when saved again.
- Changing or losing the key makes existing ciphertexts unreadable. Restore the old key, or re-enter the secrets.
- Set the same key on every replica.

```sh
OPENARTIFACT_SECRET_KEY="$(openssl rand -base64 32)"
```

### Storage backend records

`GET/POST /api/v1/admin/storage`, `GET/PATCH/DELETE /api/v1/admin/storage/{name}` and `POST /api/v1/admin/storage/{name}/test` (admin only, also the Admin, Storage page) keep named backend definitions. Credential keys in `config` (`access_key`, `secret_key`, `password`, `account_key`, `hmac_key`, `hmac_secret` and similar) are encrypted at rest, never returned (a `has_<key>` flag is returned instead) and kept when an update omits them. `test` checks connectivity for every kind (`filesystem`, `s3`, `garage`, `seaweedfs`, `gcs`, `azure` and `oci`; object stores get a bucket or container check with the stored credentials), and records the status as `healthy` or `unreachable`. The records do not route traffic: blobs are always read and written through the single backend configured with `STORAGE_*`.

## Health

`/health` and `/ready` probe S3-family stores (`s3`, `garage`, `seaweedfs`, `gcs`) with a bucket check. Filesystem, Azure and OCI are always reported `ok`. See [monitoring](monitoring.md).

## Moving data between backends

There is no built-in migration. A `full` backup taken on a filesystem store can be restored into a server configured with another backend: blobs are written to whichever backend is configured at restore time. Full backups cannot be taken from the other backends, see [backup and restore](backup.md#limits).
