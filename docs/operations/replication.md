<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Blob replication

Blob replication keeps a second copy of every blob on a **replica backend** (another bucket, another provider, another directory) and serves reads from it when the primary store cannot. It protects against the loss of the primary store: a deleted bucket, a failed disk, a region outage. It does not replace [backups](backup.md): a replicated delete is a delete, and the database is not covered.

- [Model](#model)
- [Configuration](#configuration)
- [Examples](#examples)
- [Reads and the read fallback](#reads-and-the-read-fallback)
- [Deletes](#deletes)
- [Verify and repair](#verify-and-repair)
- [Admin API](#admin-api)
- [Metrics and alerts](#metrics-and-alerts)
- [Recovering from the loss of the primary store](#recovering-from-the-loss-of-the-primary-store)
- [Interplay with storage tiers](#interplay-with-storage-tiers)
- [Limitations](#limitations)

## Model

Replication is **asynchronous**. Uploads and deletes succeed as soon as the primary store has handled them; the operation is recorded in a durable queue (the `blob_replication` table, written in the same database as everything else) and a background worker applies it to the replica.

- The recovery point objective (RPO) is the replication lag: the blobs queued but not yet copied when the primary is lost. It is usually seconds; `openartifact_replication_oldest_pending_age_seconds` shows it.
- The queue is per blob: one row per sha256, the latest operation wins (a delete after a copy replaces it). Every replica of the server works the queue; rows are claimed with `FOR UPDATE SKIP LOCKED` and a lease, so replicas never copy the same blob twice and a crashed worker's rows come back after the lease (15 minutes).
- A failed attempt is retried with exponential backoff (5 s, doubling, capped at `maxBackoff`, with jitter). The row keeps its attempt count and last error, so `failing` in the status counts what needs attention. Nothing is dropped.
- Every copy is checked: the bytes are hashed while they stream to the replica and compared with the blob's sha256; a mismatching object is removed again and the attempt fails. A copy is made under the same per-blob lock uploads and deletes use, so it cannot resurrect a blob that was deleted meanwhile.
- With no `storage.replica.type` the feature is off and the storage stack is unchanged.

## Configuration

Replication is active when `storage.replica.type` is set. The block takes the same backend keys as the primary `storage:` block (`type`, `path`, `bucket`, `region`, `endpoint`, `accessKey`, `secretKey`, `forcePathStyle`, `gcsBucket`, `gcsHmacKey`, `gcsHmacSecret`, `azureAccountName`, `azureAccountKey`, `azureContainerName`, `azureEndpoint`, `ociUsername`, `ociPassword`, `ociInsecure`) plus the keys below. Every key has an `OPENARTIFACT_STORAGE_REPLICA_*` environment override; see the [configuration reference](../getting-started/configuration.md#blob-replication-storagereplica) for the full table.

| YAML key (under `storage.replica`) | Default | Meaning |
|---|---|---|
| `readFallback` | `notfound` | `notfound`: read from the replica when the primary answers "not found". `error`: also when the primary fails for any other reason (an outage). `off`: never read from the replica |
| `interval` | `5s` | Pause between queue passes (a pass also starts right after an upload enqueues work on the same replica) |
| `batch` | `100` | Operations claimed per pass |
| `concurrency` | `4` | Blobs copied at once |
| `verifyInterval` | `24h` | Pause between full [verify passes](#verify-and-repair); `0` runs one only when an admin asks |
| `verifyDeep` | `false` | Hash the bytes of both copies in verify passes instead of comparing sizes (expensive) |
| `maxBackoff` | `1h` | Longest retry delay of a failing operation |

Startup refuses a replica that is the same location as the primary store (the same directory or a nested one, the same bucket on the same endpoint), because a copy on the same storage is no copy. A [tier](storage-tiering.md) backend that points at the replica's location is marked unavailable (and logged) instead of being opened. An explicit `database.maxOpenConns` must be at least `2 * concurrency + 2`: a blob being copied or verified pins one connection for its lock and needs another to run its queries.

The replica's credentials are stored like the primary's (YAML or environment, see [Credentials](storage.md#credentials)); they are never returned by the API. Use a credential that can write and delete in the replica location only.

## Examples

Filesystem to a second directory or mount:

```yaml
storage:
  type: filesystem
  path: /var/lib/artifacts
  replica:
    type: filesystem
    path: /mnt/replica/artifacts
```

S3 to another bucket in another region (or account, or provider):

```yaml
storage:
  type: s3
  bucket: openartifact-eu-west-1
  region: eu-west-1
  replica:
    type: s3
    bucket: openartifact-eu-central-1
    region: eu-central-1
    accessKey: CHANGE_ME
    secretKey: CHANGE_ME
    readFallback: error
    verifyInterval: 12h
```

On-premises primary and a Garage cluster as replica, configured with environment variables:

```sh
OPENARTIFACT_STORAGE_REPLICA_TYPE=s3
OPENARTIFACT_STORAGE_REPLICA_ENDPOINT=http://garage:3900
OPENARTIFACT_STORAGE_REPLICA_BUCKET=openartifact-replica
OPENARTIFACT_STORAGE_REPLICA_REGION=garage
OPENARTIFACT_STORAGE_REPLICA_ACCESS_KEY=CHANGE_ME
OPENARTIFACT_STORAGE_REPLICA_SECRET_KEY=CHANGE_ME
OPENARTIFACT_STORAGE_REPLICA_FORCE_PATH_STYLE=true
```

Enabling replication on a server that already holds blobs needs no migration step: the first [verify pass](#verify-and-repair) (started automatically when `verifyInterval` is not `0`) finds every blob the replica lacks and queues its copy. Watch `openartifact_replication_backlog`.

## Reads and the read fallback

Reads go to the primary store (through the [tiers](storage-tiering.md), if any). When it cannot answer, the replica does, according to `readFallback`:

| Mode | Falls back when |
|---|---|
| `notfound` (default) | the primary says the blob does not exist: a lost object, a lost disk, an empty replacement bucket |
| `error` | the primary says "not found" **or** fails (timeout, access denied, backend down): reads survive a primary outage |
| `off` | never |

This covers downloads, size and existence checks, metadata reads and the internal reads of the metadata workers. A fallback read is counted in `openartifact_replication_fallback_reads_total{call}`.

- A blob with a queued delete is never served from the replica, and a fallback never resurrects it.
- A blob served from the replica is **repaired**: the server copies it back to the primary store in the background (once a minute per blob at most), under the blob lock and with its hash checked. A client reading a lost blob therefore also heals it.
- With [presigned downloads](storage.md#presigned-urls) on, the server first checks that the primary still holds the blob (one extra `HEAD`); a blob only the replica has is streamed through the server instead of redirected.
- Writes always go to the primary store only; the replica is never written by a client request.

## Deletes

Deleting a package queues the blob for the [deferred deletion](storage.md#deferred-blob-deletion) sweeper, which removes its bytes after `blobDeletion.grace` (default 24 h) and once no backup needs them. With a replica the sweeper deletes through the replicating store: the primary copy is removed and the removal on the replica is queued like a copy. So **the replica copy disappears after the same grace period**, later than the delete, never earlier. Before it removes a replica copy the worker checks, under the blob lock, that the blob is really unreferenced; a blob uploaded again meanwhile is kept and its delete turns into a copy.

Because the replica follows deletes, it is a second copy, not a backup. To keep deleted data recoverable, use [backups](backup.md) or object versioning and lifecycle rules on the replica bucket.

## Verify and repair

Queued operations only describe changes made while replication was on. A **verify pass** compares the whole store and repairs the difference in both directions. It runs on one replica of the server at a time (an advisory lock) and resumes where it stopped after a restart.

For every blob either side lists, it compares the primary with the replica (sizes; with `verifyDeep` the sha256 of both copies):

| Finding | Repair |
|---|---|
| on the primary, missing on the replica | a copy is queued |
| on the replica, missing on the primary, and the database references it | restored to the primary (hash checked, under the blob lock) |
| on the replica only, and nothing references it | counted as `unreferenced`; left alone (the sweeper owns deletes) |
| different sizes or content | the copy whose hash matches the sha256 key wins and overwrites the other; if neither matches, the blob is counted in `errors` and logged as corrupt |
| referenced by the database, on neither side | reported as `lost` |
| a queued delete exists | skipped |

A pass is started by the schedule (`verifyInterval`), automatically the first time replication is enabled, and by `POST /api/v1/admin/storage/replication/verify`. It lists the primary store with its own listing; stores that cannot list (the OCI backend) are walked through the `blobs` table instead (`listing: database` in the result), and when the replica cannot list, blobs that only it holds are not found. Per blob the cost is one size lookup on each side, bounded by `concurrency`; `verifyDeep` reads every blob in full from both backends, so use it sparingly on large stores and cloud egress.

Blobs without a `blobs` row (generated repository metadata and other internally written objects) are found by verify only through the stores' own listings; when the primary is walked through the database they are repaired by the read fallback and read repair instead.

## Admin API

Both endpoints are for administrators (403 otherwise).

- `GET /api/v1/admin/storage/replication` returns the status: `enabled`, `replica` (`type` and credential-free `location`), `read_fallback`, `backlog_copy`, `backlog_delete`, `oldest_pending_age_seconds`, `failing`, `replicated_total`, `replicated_bytes_total`, `last_error`, `last_error_at`, `verify_requested`, `verify_running` and `last_verify` (`started_at`, `finished_at`, `deep`, `listing`, `replica_listed`, `checked`, `missing_replica`, `missing_primary`, `mismatched`, `repaired`, `unreferenced`, `lost`, `errors`). Without a replica it answers `{"enabled": false, ...}`.
- `POST /api/v1/admin/storage/replication/verify` requests a verify pass and answers `202` with `{"requested": true}`. It answers `409` when a pass is already running or requested and `404` when no replica is configured.

The route is static, so it wins over `GET /api/v1/admin/storage/{name}`: a storage backend record literally named `replication` is shown by the list instead.

```sh
curl -s -H "Authorization: Bearer $TOKEN" https://artifacts.example.com/api/v1/admin/storage/replication
curl -s -X POST -H "Authorization: Bearer $TOKEN" https://artifacts.example.com/api/v1/admin/storage/replication/verify
```

## Metrics and alerts

| Metric | Type | Meaning |
|---|---|---|
| `openartifact_replication_backlog{op}` | gauge | Queued operations by `op` (`copy`, `delete`) |
| `openartifact_replication_oldest_pending_age_seconds` | gauge | Age of the oldest queued operation: the replication lag (0 when idle) |
| `openartifact_replication_replicated_total` | counter | Blobs copied to the replica |
| `openartifact_replication_replicated_bytes_total` | counter | Bytes copied |
| `openartifact_replication_failures_total{op}` | counter | Failed attempts (retried) |
| `openartifact_replication_enqueue_failures_total` | counter | Operations that could not be queued (the next verify pass catches them) |
| `openartifact_replication_missing_primary_total` | counter | Queued copies whose blob was not on the primary store |
| `openartifact_replication_fallback_reads_total{call}` | counter | Reads answered by the replica, by `get`, `size`, `exists`, `metadata` |
| `openartifact_replication_verify_checked_total` | counter | Blobs examined by verify |
| `openartifact_replication_verify_missing_total{side}` | counter | Blobs found missing on `replica` or `primary` |
| `openartifact_replication_verify_mismatched_total` | counter | Blobs whose copies differ |
| `openartifact_replication_verify_repaired_total{side}` | counter | Blobs fixed on `replica` or `primary` |
| `openartifact_replication_last_verify_timestamp_seconds` | gauge | Unix time the last verify finished (0 = never) |

The gauges are refreshed every 15 s by the replica that works the queue. Suggested alerts:

```yaml
- alert: OpenArtifactReplicationLag
  expr: openartifact_replication_oldest_pending_age_seconds > 900
  for: 15m
- alert: OpenArtifactReplicationFailing
  expr: increase(openartifact_replication_failures_total[30m]) > 20
- alert: OpenArtifactPrimaryReadsFromReplica
  expr: increase(openartifact_replication_fallback_reads_total[10m]) > 0
- alert: OpenArtifactReplicaNeverVerified
  expr: time() - openartifact_replication_last_verify_timestamp_seconds > 3 * 86400
```

## Recovering from the loss of the primary store

Keep `storage.replica` in the configuration while you recover, so the fallback keeps serving.

1. **Point the primary at a new, empty location** (a new bucket or directory) and keep the old one as the replica, or keep the replica where it is: reads that find nothing on the new primary are served from the replica and repair themselves back (`readFallback: notfound`).
2. **Run a verify pass** (`POST /api/v1/admin/storage/replication/verify`) to restore everything the database references, not only what clients happen to read. Follow `last_verify` (`missing_primary`, `repaired`, `lost`).
3. A `lost` blob exists on neither side. Restore it from a [backup](backup.md) or re-publish it.

To make the replica the new primary instead, stop the server, swap the two `storage` blocks (the replica's keys become `storage:`), and start it with a new replica or none. The layout is identical on every backend (`<sha[:2]>/<sha[2:]>`), so nothing has to be converted.

If the **database** is lost too, see [rebuild from storage](rebuild-from-storage.md).

## Interplay with storage tiers

Replication wraps the whole tiered store: a write lands on the primary tier and is queued for the replica; a read finds the blob on whichever tier has it before the replica is tried; a deletion through the sweeper removes the copies on every tier and the replica. The [tier mover](storage-tiering.md) is unaffected, because moving a blob between tiers changes where the primary side keeps it, not whether it exists. Verify lists every tier's backend. A tier backend at the replica's location is made unavailable.

## Limitations

- Asynchronous: the last seconds before a loss can be missing from the replica. There is no synchronous mode.
- The replica follows deletes after the grace period; it is not a backup and does not keep history.
- Replication needs the same PostgreSQL the server uses for its queue; the database is not replicated by this feature (see the [backup](backup.md) and PITR guides).
- The OCI backend cannot list its objects; verify then walks the `blobs` table and cannot see blobs without a row, nor replica-only blobs.
- Blobs without a `blobs` row (generated metadata, objects written by the rebuild tool) are replicated when written, but verify only repairs them when the backend can list.
- With presigned downloads on, each presigned redirect costs one extra `HEAD` on the primary store.
- A second copy doubles the storage cost and, on cloud backends, the egress of the initial backfill and of `verifyDeep`.
