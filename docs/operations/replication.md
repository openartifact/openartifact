<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Blob replication

Blob replication keeps a second copy of every blob on a **replica backend** (another bucket, provider or directory) and serves reads from it when the primary store cannot. Use it to survive a deleted bucket, a failed disk or a region outage.

Replication is not a [backup](backup.md). A delete is replicated too, and the database is not covered.

## Enable replication

Set `storage.replica.type` and the usual backend keys under `storage.replica`. With no `storage.replica.type`, replication is off and nothing changes.

Filesystem to a second directory or mount:

```yaml
storage:
  type: filesystem
  path: /var/lib/artifacts
  replica:
    type: filesystem
    path: /mnt/replica/artifacts
```

S3 to another bucket in another region, account or provider:

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

An on-premises primary with a Garage cluster as replica, configured with environment variables:

```sh
OPENARTIFACT_STORAGE_REPLICA_TYPE=s3
OPENARTIFACT_STORAGE_REPLICA_ENDPOINT=http://garage:3900
OPENARTIFACT_STORAGE_REPLICA_BUCKET=openartifact-replica
OPENARTIFACT_STORAGE_REPLICA_REGION=garage
OPENARTIFACT_STORAGE_REPLICA_ACCESS_KEY=CHANGE_ME
OPENARTIFACT_STORAGE_REPLICA_SECRET_KEY=CHANGE_ME
OPENARTIFACT_STORAGE_REPLICA_FORCE_PATH_STYLE=true
```

Enabling replication on a server that already holds blobs needs no migration step. The first [verify pass](#verify-and-repair) finds every blob the replica lacks and queues its copy. It starts automatically unless `verifyInterval` is `0`. Watch `openartifact_replication_backlog` while it drains.

### Settings

The `storage.replica` block takes the same backend keys as the primary `storage:` block (see [storage settings](storage.md#settings)), plus the keys below. Every key has an `OPENARTIFACT_STORAGE_REPLICA_*` environment override. The [configuration reference](../getting-started/configuration.md#blob-replication-storagereplica) lists them all.

| YAML key (under `storage.replica`) | Default | Meaning |
|---|---|---|
| `readFallback` | `notfound` | When reads go to the replica: `notfound`, `error` or `off` (see [Reads](#reads-and-the-read-fallback)) |
| `interval` | `5s` | Pause between queue passes |
| `batch` | `100` | Operations claimed per pass |
| `concurrency` | `4` | Blobs copied at once |
| `verifyInterval` | `24h` | Pause between full [verify passes](#verify-and-repair); `0` runs one only when an admin asks |
| `verifyDeep` | `false` | Hash both copies in verify passes instead of comparing sizes (expensive) |
| `maxBackoff` | `1h` | Longest retry delay of a failing operation |

A pass also starts right after an upload queues work.

### Rules the server enforces at startup

- The replica must not be the same location as the primary store: the same directory, a nested directory, or the same bucket on the same endpoint. A copy on the same storage is no copy.
- A [storage tier](storage-tiering.md) that points at the replica's location is marked unavailable and logged instead of being opened.
- An explicit `database.maxOpenConns` must be at least `2 * concurrency + 2`. A blob being copied or verified holds one connection for its lock and needs another for its queries.

### Credentials

The replica's credentials are stored like the primary's, in YAML or the environment (see [Credentials](storage.md#credentials)). The API never returns them. Give the replica a credential that can write and delete in the replica location only.

## How replication works

Replication is **asynchronous**. An upload or delete succeeds as soon as the primary store has handled it. The operation is recorded in a durable queue in the database, and a background worker applies it to the replica.

- **Lag.** The recovery point objective (RPO) is the replication lag: blobs queued but not yet copied when the primary is lost. It is usually seconds. `openartifact_replication_oldest_pending_age_seconds` shows it.
- **One entry per blob.** The queue holds the latest operation for each sha256. A delete queued after a copy replaces it.
- **Several replicas of the server.** Every server replica works the queue and never copies the same blob twice. If a worker crashes, its entries return to the queue after 15 minutes.
- **Retries.** A failed attempt is retried with exponential backoff: 5 s, doubling, capped at `maxBackoff`, with jitter. The entry keeps its attempt count and last error, and `failing` in the status counts what needs attention. Nothing is dropped.
- **Integrity.** The server hashes the bytes while it streams them to the replica and compares the result with the blob's sha256. A mismatching object is removed and the attempt fails.
- **Safety against deletes.** A copy runs under the same per-blob lock that uploads and deletes use, so it cannot resurrect a blob deleted meanwhile.

## Reads and the read fallback

Reads go to the primary store, through the [tiers](storage-tiering.md) if you have any. When the primary cannot answer, `readFallback` decides whether the replica does:

| Mode | The replica answers when |
|---|---|
| `notfound` (default) | the primary says the blob does not exist: a lost object, a lost disk, an empty replacement bucket |
| `error` | the primary says "not found" or fails (timeout, access denied, backend down), so reads survive a primary outage |
| `off` | never |

The fallback covers downloads, size and existence checks, metadata reads and the internal reads of the metadata workers. `openartifact_replication_fallback_reads_total{call}` counts each fallback read.

- The server never serves a blob with a queued delete from the replica.
- A blob served from the replica is **repaired**: the server copies it back to the primary in the background, at most once a minute per blob, under the blob lock and with its hash checked. A client that reads a lost blob also heals it.
- Writes always go to the primary only. A client request never writes to the replica.
- With [presigned downloads](storage.md#presigned-urls) on, the server first checks that the primary still holds the blob, which costs one extra `HEAD`. A blob only the replica has is streamed through the server instead of redirected.

## Deletes

Deleting a package queues its blob for the [deferred deletion](storage.md#deferred-blob-deletion) sweeper. The sweeper removes the bytes after `blobDeletion.grace` (default 24 h) and once no backup needs them.

With a replica, the sweeper removes the primary copy and queues the removal on the replica like a copy. The replica copy therefore disappears after the same grace period, never earlier than the delete.

Before it removes a replica copy, the worker checks under the blob lock that the blob is still unreferenced. A blob uploaded again meanwhile is kept, and its delete turns into a copy.

Because the replica follows deletes, it is a second copy and not a backup. To keep deleted data recoverable, use [backups](backup.md), or object versioning and lifecycle rules on the replica bucket.

## Verify and repair

Queued operations only describe changes made while replication was on. A **verify pass** compares the whole store and repairs differences in both directions. It runs on one server replica at a time and resumes where it stopped after a restart.

A pass starts in three ways:

- on the schedule (`verifyInterval`);
- automatically the first time replication is enabled;
- on request with `POST /api/v1/admin/storage/replication/verify`.

For every blob either side lists, the pass compares the primary with the replica. It compares sizes, or with `verifyDeep` the sha256 of both copies.

| Finding | Repair |
|---|---|
| on the primary, missing on the replica | a copy is queued |
| on the replica, missing on the primary, and the database references it | restored to the primary, hash checked, under the blob lock |
| on the replica only, and nothing references it | counted as `unreferenced` and left alone, because the sweeper owns deletes |
| different sizes or content | the copy whose hash matches the sha256 key overwrites the other; if neither matches, the blob is counted in `errors` and logged as corrupt |
| referenced by the database, on neither side | reported as `lost` |
| a queued delete exists | skipped |

A blob counts as referenced when a blob record, a generated repository metadata record, a package record or a current [disaster-recovery sidecar](rebuild-from-storage.md#how-it-works) points at it. The sidecars of a lost primary therefore come back with the packages.

### Cost and limits of verify

- Each blob costs one size lookup on each side, bounded by `concurrency`.
- `verifyDeep` reads every blob in full from both backends. Use it sparingly on large stores and on cloud egress.
- Verify lists the primary store with the store's own listing. The OCI backend cannot list, so verify walks the database's blob records instead (`listing: database` in the result).
- When the replica cannot list, blobs that only it holds are not found.
- Objects without a blob record (generated repository metadata and other internally written objects) are found only through the stores' own listings. When the primary is walked through the database, the read fallback and read repair fix them instead.

## Admin API

Both endpoints are for administrators (`403` otherwise).

```sh
curl -s -H "Authorization: Bearer $TOKEN" https://artifacts.example.com/api/v1/admin/storage/replication
curl -s -X POST -H "Authorization: Bearer $TOKEN" https://artifacts.example.com/api/v1/admin/storage/replication/verify
```

`GET /api/v1/admin/storage/replication` returns the status. Without a replica it answers `{"enabled": false, ...}`. The fields are:

- `enabled`, `replica` (`type` and a credential-free `location`) and `read_fallback`;
- `backlog_copy`, `backlog_delete`, `oldest_pending_age_seconds` and `failing`;
- `replicated_total`, `replicated_bytes_total`, `last_error` and `last_error_at`;
- `verify_requested`, `verify_running` and `last_verify`.

`last_verify` holds `started_at`, `finished_at`, `deep`, `listing`, `replica_listed`, `checked`, `missing_replica`, `missing_primary`, `mismatched`, `repaired`, `unreferenced`, `lost` and `errors`.

`POST /api/v1/admin/storage/replication/verify` requests a verify pass and answers `202` with `{"requested": true}`. It answers `409` when a pass is already running or requested, and `404` when no replica is configured.

The route `replication` wins over `GET /api/v1/admin/storage/{name}`. A storage backend record literally named `replication` appears only in the list.

## Metrics and alerts

| Metric | Type | Meaning |
|---|---|---|
| `openartifact_replication_backlog{op}` | gauge | Queued operations by `op` (`copy`, `delete`) |
| `openartifact_replication_oldest_pending_age_seconds` | gauge | Age of the oldest queued operation, the replication lag (0 when idle) |
| `openartifact_replication_replicated_total` | counter | Blobs copied to the replica |
| `openartifact_replication_replicated_bytes_total` | counter | Bytes copied |
| `openartifact_replication_failures_total{op}` | counter | Failed attempts, which are retried |
| `openartifact_replication_enqueue_failures_total` | counter | Operations that could not be queued; the next verify pass catches them |
| `openartifact_replication_missing_primary_total` | counter | Queued copies whose blob was not on the primary store |
| `openartifact_replication_fallback_reads_total{call}` | counter | Reads answered by the replica, by `get`, `size`, `exists`, `metadata` |
| `openartifact_replication_verify_checked_total` | counter | Blobs examined by verify |
| `openartifact_replication_verify_missing_total{side}` | counter | Blobs found missing on `replica` or `primary` |
| `openartifact_replication_verify_mismatched_total` | counter | Blobs whose copies differ |
| `openartifact_replication_verify_repaired_total{side}` | counter | Blobs fixed on `replica` or `primary` |
| `openartifact_replication_last_verify_timestamp_seconds` | gauge | Unix time the last verify finished (0 = never) |

The server replica that works the queue refreshes the gauges every 15 s. Suggested alerts:

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

1. **Point the primary at a new, empty location** (a new bucket or directory). Keep the old replica as the replica. Reads that find nothing on the new primary are served from the replica and repair themselves back (`readFallback: notfound`).
2. **Run a verify pass** with `POST /api/v1/admin/storage/replication/verify`. It restores everything the database references, not only what clients happen to read. Follow `last_verify` (`missing_primary`, `repaired`, `lost`).
3. **Handle `lost` blobs.** A `lost` blob exists on neither side. Restore it from a [backup](backup.md) or re-publish it.

If the **database** is lost too, see [rebuild from storage](rebuild-from-storage.md).

### Make the replica the new primary

1. Stop the server.
2. Swap the two `storage` blocks, so the replica's keys become `storage:`.
3. Start the server with a new replica or none.

No data conversion is needed. The server addresses every blob by its sha256, and each backend type maps that hash to its own object key or file path. Two backends can use different paths for the same blob:

| Backend | Where a blob lives |
|---|---|
| filesystem | `<sha[:2]>/<sha[2:]>` |
| s3, garage, seaweedfs, gcs, azure | `blobs/<sha[:2]>/<sha[2:4]>/<sha>` |
| oci | `blobs/sha256:<sha>` in the registry repository |

Replication copies each blob through the store interface: it reads the bytes by sha256 from one backend and writes them by sha256 to the other. The replica is therefore laid out the way its backend type expects for a primary store. A blob never needs renaming when you swap the blocks or replicate between backend types.

## Interplay with storage tiers

Replication wraps the whole tiered store.

- A write lands on the primary tier and is queued for the replica.
- A read finds the blob on whichever tier has it before the replica is tried.
- A deletion through the sweeper removes the copies on every tier and on the replica.
- The [tier mover](storage-tiering.md) is unaffected. Moving a blob between tiers changes where the primary side keeps it, not whether it exists.
- Verify lists every tier's backend.

## Limitations

- Replication is asynchronous. The last seconds before a loss can be missing from the replica. There is no synchronous mode.
- The replica follows deletes after the grace period. It is not a backup and keeps no history.
- The queue lives in the server's PostgreSQL database. Replication does not copy the database itself; see the [backup](backup.md) and [PITR](postgres-pitr.md) guides.
- With the OCI backend, verify cannot see objects without a blob record, nor replica-only blobs.
- Objects without a blob record (generated metadata, objects written by the rebuild tool) are replicated when written. Verify repairs them only when the backend can list.
- With presigned downloads on, each presigned redirect costs one extra `HEAD` on the primary store.
- A second copy doubles the storage cost. On cloud backends it also doubles the egress of the initial backfill and of `verifyDeep`.
