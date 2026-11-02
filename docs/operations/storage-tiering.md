<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Storage tiers and lifecycle policies

Use storage tiers to keep cold blobs on cheaper storage. A tier is a name for a storage backend. Lifecycle policies decide which blobs live on which tier, for example "move to the HDD tier after 30 days without a download" or "releases to S3 after 90 days". A background mover relocates blobs between tiers.

Clients see no difference. Downloads, uploads and deletes behave exactly as before, whichever tier holds the bytes.

## Set up a cold tier

You need an admin token. The steps below register a second backend, make it a tier, add a policy, preview it and run the mover.

```sh
# register a second filesystem backend and make it a tier
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/storage" \
  -d '{"name":"hdd","kind":"filesystem","config":{"path":"/var/lib/artifacts-cold"}}'
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/tiering/tiers" -d '{"name":"cold","backend":"hdd","rank":100}'

# policy: after 30 days without a download, move to cold
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/tiering/policies" \
  -d '{"name":"cooldown","target_tier":"cold","min_idle_seconds":2592000}'

# preview what would move
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/tiering/dry-run" -d '{}'

# run the mover now instead of waiting for the next pass
curl -sS -H "Authorization: Bearer $TOKEN" -X POST "$OA/api/v1/admin/tiering/run"
```

You can do the same in the [admin UI](#admin-ui). Nothing moves until you add a tier besides `hot` and at least one enabled policy. With no cold tier the mover does nothing, and reads take the plain single-store path.

## Concepts

| Term | Meaning |
|---|---|
| Primary store | The blob store configured with `STORAGE_BACKEND` ([storage](storage.md)). All new uploads land here. |
| Storage backend | An additional backend registered in **Settings > Storage backends** (`/api/v1/admin/storage`, see [storage backends](storage.md#settings)). Filesystem, S3-compatible and the other kinds work. |
| Tier | A named entry that maps to one registered backend. A backend can be a tier only once. |
| `hot` | The tier that always exists and is the primary store. You cannot delete it. |
| Placement | Where a blob's bytes are. The database is the single authority. |
| Policy | A rule "blobs matching these conditions belong on this tier". |

## Policies

A policy says "blobs matching all of these conditions belong on `target_tier`". The server evaluates policies by `position`, then creation time. The **first** enabled match decides.

- A blob already on its policy's target tier is never touched.
- A blob that matches no policy stays where it is.
- There is no implicit move back to the hot tier. Use [promote-on-read](#promote-on-read) for that.

| Field | Meaning |
|---|---|
| `name` | Unique. Shown in logs, plans and journal rows. |
| `enabled` | The mover and dry runs ignore disabled policies. |
| `position` | Evaluation order, lowest first. |
| `target_tier` | Tier name the matching blobs move to. Required. |
| `from_tier` | Optional. Only blobs currently on this tier match. Empty means any other tier. |
| `min_age_seconds` | Blob age (`now - created_at`) is at least this many seconds. |
| `min_idle_seconds` | Time since the last recorded download is at least this many seconds. A blob never downloaded counts from its creation time. |
| `min_size_bytes`, `max_size_bytes` | Blob size bounds, inclusive. `min <= max` is required. |
| `repo_glob` | Repository name glob. |
| `formats` | Package formats (`docker`, `maven`, `npm`, ...). Any of them matches. |
| `path_glob` | Glob on the artifact path inside the repository: the image name for Docker, the repository path for Maven, the package name for npm. |

A policy needs at least one condition. A policy with only a target would move every blob.

Blobs are deduplicated, so one blob can be referenced from several repositories and formats. The repository, format and path conditions match if **any** reference matches. Age and idle apply to the blob itself.

Glob syntax:

- `*` matches any run of characters except `/`.
- `**` matches anything including `/`. A leading `**/` also matches no prefix at all.
- `?` matches one character except `/`.
- Everything else, including multi-byte characters, is literal.
- A glob has at most 256 characters.

Example, "releases to the S3 tier after 90 days":

```json
{
  "name": "releases-to-s3",
  "target_tier": "archive",
  "min_age_seconds": 7776000,
  "repo_glob": "*-releases",
  "formats": ["maven"]
}
```

### Dry run

`POST /api/v1/admin/tiering/dry-run` shows what a pass would move. It evaluates the saved policies, or one draft policy you have not saved, against the current blobs. The report has:

- the number of blobs and bytes;
- totals per route (`from` tier, `to` tier) and per policy;
- a sample of at most 200 blobs.

A dry run only reads. It changes no placement, no journal entry and no object. It uses the same matching code as the mover and leaves out blobs that already have a move in flight. The scan covers at most 200 000 blobs per request. When `truncated` is true, the totals are a lower bound.

## Throttling

| Control | Default | Effect |
|---|---|---|
| `tiering.maxBytesPerSecond` | 64 MiB/s | One rate limiter shared by every copy and read-back in the process. Total mover bandwidth stays under the cap however many blobs move at once. `0` removes the cap. |
| `tiering.concurrency` | 2 | Blobs copied simultaneously. |
| `tiering.batch` | 200 | The most blobs one pass moves. Each server replica's scan position carries over between passes, so a large backlog drains over several passes. |
| `tiering.interval` | 5 minutes | Pause between scheduled passes. `0` disables the schedule, and `POST /api/v1/admin/tiering/run` still works. |

A pass is bounded to two hours and honors shutdown. An interrupted pass leaves journal entries that the next pass finishes.

## Configuration

Settings live under `tiering:` in the YAML file or in the environment ([configuration](../getting-started/configuration.md)). Tiers and policies are data, managed through the API.

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_TIERING_ENABLED` | `tiering.enabled` | `true` | Run the background mover. When off, nothing moves, reads still work from every tier, and `POST /run` returns `409`. |
| `OPENARTIFACT_TIERING_INTERVAL` | `tiering.interval` | `5m` | Pause between passes. `0` disables the schedule. |
| `OPENARTIFACT_TIERING_BATCH` | `tiering.batch` | `200` | Most blobs moved per pass (at least 1). |
| `OPENARTIFACT_TIERING_CONCURRENCY` | `tiering.concurrency` | `2` | Parallel moves (at least 1). |
| `OPENARTIFACT_TIERING_MAX_BYTES_PER_SECOND` | `tiering.maxBytesPerSecond` | `67108864` | Shared copy bandwidth cap. `0` is unlimited. |
| `OPENARTIFACT_TIERING_VERIFY_READBACK` | `tiering.verifyReadback` | `true` | Re-hash each copy from the target tier before the move completes. |
| `OPENARTIFACT_TIERING_ACCESS_FLUSH_INTERVAL` | `tiering.accessFlushInterval` | `30s` | How often recorded downloads are written. |
| `OPENARTIFACT_TIERING_REFRESH_INTERVAL` | `tiering.refreshInterval` | `30s` | How often each server replica reloads tiers and backends from the database. |

With `tiering.enabled`, the mover holds one pooled database connection for the whole pass (up to two hours), and each blob-lock transaction needs two more. Set `database.maxOpenConns` to `0` (the driver default) or at least `4`. The server rejects smaller values at startup.

### Several server replicas

Every server replica must be able to reach every tier's backend. A replica that cannot open a tier marks it unavailable, which the tier list shows. It falls back to its other tiers. The mover only moves to and from tiers it can open. A backend that fails to open, for example a mount that is not there yet, is retried on every refresh and recovers by itself.

### Every tier needs its own storage

Two tiers that resolve to the same place would make a copy a no-op, and the cleanup step would delete the only copy. A tier that aliases the primary store or another tier is therefore marked unavailable. The tier list shows `available: false`, and the server logs the reason once.

The server compares the canonical location of each backend kind:

- **filesystem:** the real path after resolving symlinks, `.` and `..`. Equal paths, a path inside another tier's directory, a directory containing another tier's, and two paths that are the same inode all conflict.
- **S3-compatible (S3, Garage, SeaweedFS):** endpoint plus bucket. Scheme, case and a trailing slash are ignored.
- **GCS:** bucket.
- **Azure:** account plus container.
- **OCI:** endpoint plus repository.

Different prefixes inside one bucket are not distinguished, so use separate buckets. The check runs before the backend is opened, so no directory is created under an aliasing path. The mover checks again before it records a move.

## Reads, promote-on-read and access tracking

### Transparent reads

All blob access goes through a tiered store that wraps the primary store. A read tries these places in order:

1. The tier where the blob is believed to be. This is a one-minute in-memory cache of recent placements, otherwise the primary store.
2. The placement the database reports, when the first tier says the object is missing.
3. The remaining tiers.

Only "not found" moves on to the next tier. Any other error is returned as an error: a database failure while looking up the placement, a tier this server cannot open, or a backend error. A broken cold tier therefore answers `500` or `503`, never `404`. An existence check follows the same rule.

A move that completes while a download starts is invisible to the client. The source copy is deleted only after the placement changes. A download that opened the source just before cleanup keeps reading on a filesystem tier, because the open file survives deletion. On an object-store tier, a download that loses that race fails like any interrupted transfer and succeeds on retry.

### Writes and deletes

A new blob is written to the primary store. A re-upload of a blob that already has a record is written to the tier that record places it on, so it never creates a second copy. See [writes that race a move](#writes-that-race-a-move).

Deletes keep their usual semantics: a blob is removed only through the lock-protected, reference-counted path described in [deduplication and deletion](storage.md#deduplication-and-deletion). That removes every copy on every tier. Deleting a blob removes its record at once and queues the bytes of every tier that has a copy for [deferred removal](#deferred-removal-of-the-old-copy). If the blob's placement points at a tier this server cannot resolve, the delete fails and keeps the record, so the bytes are never orphaned. Listing and existence checks look at all tiers.

### Promote-on-read

Set `promote_on_read` on a cold tier to make the mover pull a blob back to the primary tier after it is downloaded.

- In each pass, blobs on promoting tiers whose last recorded download is newer than their last placement change become promotion candidates. They move to the primary tier through the same [journaled protocol](#how-a-move-works) (reason `promote-on-read`).
- A candidate is not promoted when an enabled policy would immediately send it back. A "30 days idle" policy and promote-on-read together therefore cannot loop.
- Promotion happens in the background at the next pass. The read that triggered it is served from the cold tier.

### Access tracking

Idle-based policies and promotion need to know when a blob was last downloaded.

- **When it is active.** Tracking runs only while at least one tier besides the primary exists. With a single store nothing is recorded, so a plain installation pays nothing.
- **History.** It starts when you add the first cold tier. Until a blob is read after that, an idle policy treats it as idle since creation.
- **What counts.** Only `GET` and `HEAD` requests to the native repository protocols (`/repos/...` and `/v2/...`). API calls, replication and the mover's own reads do not count.
- **Cost.** The tracker batches downloads in memory and writes them every `tiering.accessFlushInterval` (30 seconds). It advances a blob's last-access time only when the recorded time is more than an hour old, so a hot blob costs at most one database write per hour.
- **First read after a move.** The server also records a read when the recorded access is not newer than the last placement change, so that read is never lost.
- **Crash.** A crash loses at most the unflushed seconds of access times. A blob then looks slightly idler.

## How a move works

A move copies the bytes first and changes the placement last. At every instant the blob has one authoritative placement and its bytes are readable. A crash at any point leaves exactly one valid placement and loses nothing. The next pass finishes or rolls back the move on its own.

Blobs stay content addressed. The blob id is its sha256, and every copy on every tier carries `{"sha256": "<hex>"}` metadata, so anyone who holds a copy can verify it.

Each move owns one journal entry, keyed by the blob's sha256. A blob has at most one move in flight, even across server replicas. The entry records the source and target backend, the reason (policy name or `promote-on-read`), the size, the state (`copying` or `flipped`), the attempt count and the last error. It is the only state the mover keeps.

| Step | What happens | Placement after |
|---|---|---|
| 1 Claim | The journal entry is created in state `copying`. If one already exists for the blob, the mover uses that one. | source |
| 2 Copy | The source bytes stream to the target backend through the throttle, hashing on the way. A hash mismatch (corrupt source) deletes the target object and fails the move. If the target already holds an object of the right size (a resumed move), the copy is skipped. | source |
| 3 Verify | The target object must exist with the recorded size. With `tiering.verifyReadback` (default on) it is also read back and its sha256 compared to the blob id. A bad copy is deleted so a retry writes it afresh. | source |
| 4 Flip | Under the per-blob lock that uploaders and deleters use, the server re-checks that the target copy still exists with the right size. It then changes the placement to the target, but only if the placement is still the move's source, and marks the entry `flipped`. All of this is one database transaction. | **target** |
| 5 Clean | Under the blob lock, if the placement is still the target, the server queues the source copy for [deferred removal](#deferred-removal-of-the-old-copy). It does the same for any copy on a tier that is neither source nor target ([stray copies](#writes-that-race-a-move)). If the placement moved on or the blob is gone, it leaves the source alone. | target |
| Finish | The journal entry is deleted. | target |

The flip is the only step that changes where a blob lives. Everything before it only adds an unreferenced copy. Everything after it only removes a superseded copy.

### What happens when the server stops mid-move

The blob is always readable, because readers try the placement's tier and then the other tiers ([Reads](#transparent-reads)).

| The server stops | Placement | The next pass |
|---|---|---|
| before the flip (after claim, copy or verify) | source | resumes: copies if needed, verifies, flips |
| after the flip (during or after clean) | target | queues the source copy for removal again (safe to repeat) and finishes |

The database never points at a missing copy, because the flip checks the target copy while holding the lock that serializes it against deleters.

Other races resolve by the same rule: the placement in the database wins.

- **The blob is deleted while it is being copied.** The flip reports the blob gone. The target object is removed (unless the placement is the target) and the entry is dropped.
- **The placement changes during the copy** (another server replica, a manual change). The flip refuses with `placement changed`, the unused target copy is removed and the entry is dropped.
- **The target backend is unreachable or full.** The move fails, the attempt counter grows, and the source stays authoritative.
- **An entry has the wrong direction** (its source differs from both the placement and the target). The mover abandons it the same way.

Every pass retries an entry while it has attempts left (5 back to back). After that it retries once per hour. `GET /api/v1/admin/tiering/status` lists the pending entries, and `openartifact_storage_tier_moves_pending` counts them.

### Concurrency

- **One pass cluster-wide.** A second server replica's pass is skipped, not queued.
- **`tiering.concurrency` blobs move at once.** Two moves of the same blob are impossible, because the second claim returns the first entry.
- **Deletes and flips are ordered.** The flip, the old-copy removal and the blob delete all hold the per-blob lock. A delete therefore either completes before the flip, which then reports the blob gone, or after it, which then queues every tier's copy for removal.

### Writes that race a move

Writers store the bytes first. They then take the per-blob lock, check that the object is still there (storing it again if a delete removed it) and create the record. The lock covers database work only.

Some writers do not take the lock around the record at all: the generated deb and rpm metadata, and a backup restore. A write reads the blob's placement and then streams its bytes. A move can flip the blob in between, and the bytes land on the tier the blob just left.

The guarantee therefore does not depend on who holds the lock. The tiered store enforces it for every write that goes through it:

- **Write, then verify.** After every write, and after promoting a [presigned upload](storage.md#presigned-uploads), the store re-reads the placement. Suppose the blob has a record and the record now points at a different tier than the one written. The store then queues the copy just written for removal on that tier's backend, in the same deletion queue that step 5 uses. Queuing needs no lock. The sweeper re-checks the placement under the blob lock before it deletes a copy, and keeps the bytes if the placement points at that tier again. A blob without a record is a new upload, and its record is created on the primary store, so nothing is stray. If the store cannot re-read the placement or queue the copy, the write fails. It never leaves a copy nothing accounts for.
- **Presigned uploads follow the placement.** Staging lives on the primary store. Completing an upload of a blob placed on a cold tier never copies the bytes back onto the primary store. A tier that already holds the blob needs nothing, because content addressing makes the bytes identical. A tier that lost its copy gets the staged bytes streamed in, still conditional on the verified staging version.
- **A move cleans up behind itself.** Step 5 queues a copy on every tier other than the target and the source, which retires stray copies. A tier that cannot be reached at that moment is skipped with a warning, so one unreachable tier does not stall moves that do not involve it.

A copy that is not the blob's placement is therefore always either being moved (journaled) or queued for deletion. Once the [sweeper](storage.md#deferred-blob-deletion) has run after `blobDeletion.grace`, the blob's bytes are on its placement tier only. A queued stray is never read, because the placement points elsewhere, and deleting the blob removes it.

A server replica never takes writes without knowing its tiers. The server does not start when the first tier load fails. `/ready` answers `503` (`storage tiers not loaded`) until a load has succeeded, and again when no reload has succeeded for three `tiering.refreshInterval`s.

One narrow case stays outside the guarantee. A tier created through another server replica is picked up within `tiering.refreshInterval` (30 seconds by default). A blob stored by a replica that does not know the tier yet goes straight to the primary store, without the check.

### Deferred removal of the old copy

Step 5 does not delete the source copy. It queues an entry for it in the deletion queue, scoped to the source backend or the primary store.

The [blob deletion sweeper](storage.md#deferred-blob-deletion) deletes the entry after `blobDeletion.grace`, once no running [backup](backup.md#consistency-without-locks) can still need it. It deletes only if the blob's placement is not that backend any more, so a blob moved back to it keeps its bytes. A missing object counts as removed.

Until then, the source tier holds the bytes twice. Plan the capacity of a tier you drain accordingly, or lower `blobDeletion.grace`.

The only copy still deleted at once is a target copy the mover itself wrote and then had to abandon, because the flip was refused or the blob vanished. No backup can have seen it.

## Admin API

All endpoints live under `/api/v1/admin/tiering` and need admin (`manage`) rights. JSON is snake_case. Errors use the standard `{"error", "code"}` shape. The schemas are in [`openapi.json`](../../internal/api/openapi.json) and the [REST API](../integrations/api.md).

| Method and path | Purpose |
|---|---|
| `GET /tiers` | Tiers with placement stats: `blob_count` and `total_bytes` per tier, `available` (false when this server cannot open the tier's backend), `promote_on_read` and `rank`. |
| `POST /tiers` | Create a tier: `{"name", "backend", "rank", "promote_on_read"}`. `backend` is a registered storage backend name. |
| `PATCH /tiers/{name}` | Change `rank` and `promote_on_read`. The primary tier cannot promote. |
| `DELETE /tiers/{name}` | Delete a tier. |
| `GET /policies`, `POST /policies` | List and create policies. |
| `GET`, `PUT`, `DELETE /policies/{id}` | Read, replace and delete a policy. A replace is a full replacement: omitted conditions are cleared. |
| `POST /dry-run` | Body `{"policy": {...}, "sample_limit": 20}`, both optional. Without `policy` the saved policies are simulated. The response has `scanned`, `truncated`, `would_move {count, bytes}`, `routes`, `policies` and `sample`. |
| `POST /run` | Ask the mover to start a pass now: `202 {"started": true}`. |
| `GET /status` | `enabled`, `running`, `interval_seconds`, the last pass (`moved`, `promoted`, `recovered`, `failed`, `skipped`, `bytes`, `scanned`, `aborted`) and the pending journal entries. |

Error cases:

- `POST /tiers` answers `400 validation_failed` for an unknown backend or a bad name (1-64 characters: letters, digits, `.`, `_`, `-`). It answers `409 conflict` when the name or backend is already a tier.
- `DELETE /tiers/{name}` answers `400 cannot_delete_primary` for `hot`. It answers `409 conflict` while blobs are placed on the tier, a policy names it or a move involves it. The check and the delete run in one transaction.
- `POST /run` answers `409` when the mover is disabled in the configuration. A pass that cannot take the cluster lock, because another server replica is running one, is skipped.

### Changing storage backends that tiers use

- `DELETE /api/v1/admin/storage/{name}` answers `409 conflict` for a backend that is a tier, or the source or target of an in-flight move. The database also refuses to delete a backend that still holds blobs.
- `PATCH /api/v1/admin/storage/{name}` answers `409 conflict` when it would change the `kind` or `config` of a backend that holds blobs, serves a tier or is named by a move. Renaming stays allowed.

## Admin UI

**Settings > Storage tiers** (`/settings/tiers`, in the Storage group of the settings hub) has four parts:

- **Placement.** One row per tier with its backend, availability, blob count and bytes, and the promote-on-read switch. You add and delete tiers here.
- **Policies.** Create, edit, enable or disable and delete policies. Conditions are described in plain words: age and idle in days, sizes in units.
- **Simulation.** Run a dry run on all saved policies, or on the policy you are editing before saving it. It shows counts, bytes, the route totals and a sample of blobs.
- **Mover.** Status, last pass, pending moves, and a "Run now" button.

## Metrics

Labels are tier names and fixed result strings, so cardinality is bounded by the number of tiers.

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `openartifact_storage_tier_blobs` | gauge | `tier` | Blobs placed on the tier. |
| `openartifact_storage_tier_bytes` | gauge | `tier` | Bytes placed on the tier. |
| `openartifact_storage_tier_reads_total` | counter | `tier` | Blob reads served by the tier. |
| `openartifact_storage_tier_moves_total` | counter | `from`, `to`, `result` | `result` is `moved`, `skipped` or `failed`. |
| `openartifact_storage_tier_moved_bytes_total` | counter | `from`, `to` | Bytes moved. |
| `openartifact_storage_tier_moves_pending` | gauge | | Journal entries awaiting recovery. |

`openartifact_storage_tier_moves_pending` should return to 0. A value that stays above 0 means a move keeps failing. The server log has a `storage tier move failed` warning with the error, and the journal entry holds the latest error.

The usage gauges come from a cached whole-table aggregate. The server recomputes it after a pass that moved or promoted blobs, when a tier appears, and at most every 30 minutes otherwise, so an idle installation does not scan the blob table on every pass. Series update in place and are removed when their tier is. With no cold tier no usage gauges are published.

The gauges and the pending-moves gauge are published by the server replica that runs a pass. Scrape every replica and take the latest value, for example `max by (tier)`, not the sum.

## Limitations

- **Any-reference matching.** A deduplicated blob matches a repository, format or path condition if any of its references does.
- **Live stats.** The tier list computes its stats when you open it. On very large installations that is a scan of the blob table.
- **Access tracking covers native protocol downloads only**, at one-hour resolution. Blobs read only through the admin API or by presigned URL are not counted as accessed.
- **Presigned downloads from a cold backend.** Presigned and redirect downloads that go straight to a cold backend work only when that backend supports presigning. Otherwise the server streams the object.
- **Moves are one blob at a time.** Large blobs hold their copy slot for as long as the throttle allows. Raise `tiering.concurrency` and `tiering.maxBytesPerSecond` together when moving many large blobs.
- **Backups and moves.** A full backup reads the blob records of its database snapshot and fetches each blob through the tiered store. A blob that moves while the backup runs is read from wherever it lives then, and its old copy is not removed until the backup is over. You do not need to pause the mover around a backup.
- **Old copies live for the deletion grace period.** Until the sweeper removes them, a moved blob's old copy still occupies the source backend ([details](#deferred-removal-of-the-old-copy)).
- **`POST /run` lands on one server replica.** It is skipped when another replica already runs a pass. Scheduled passes continue as usual.
- **Tier changes take up to `tiering.refreshInterval`** to reach the other server replicas.
