<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Storage tiers and lifecycle policies

A tier is a name for a storage backend. Lifecycle policies decide which blobs live on which tier, for example "move to the HDD tier after 30 days without a download" or "releases to S3 after 90 days". A background mover relocates blobs between tiers. Clients see no difference: downloads, uploads and deletes behave exactly as before, whichever tier holds the bytes.

- [Concepts](#concepts)
- [Design and protocol](#design-and-protocol)
- [Policies](#policies)
- [Throttling](#throttling)
- [Reads, promote-on-read and access tracking](#reads-promote-on-read-and-access-tracking)
- [Admin API](#admin-api)
- [Admin UI](#admin-ui)
- [Configuration](#configuration)
- [Metrics](#metrics)
- [Limitations](#limitations)

## Concepts

| Term | Meaning |
|---|---|
| Primary store | The blob store configured with `STORAGE_BACKEND` ([storage](storage.md)). All new uploads land here. |
| Storage backend | An additional backend registered in the admin UI ([storage backends](storage.md#settings), `/api/v1/admin/storage`). Filesystem, S3-compatible and the other kinds work. |
| Tier | A named entry that maps to one storage backend. The tier `hot` always exists and is the primary store; you cannot delete it. Every other tier maps to exactly one registered backend, and a backend can be a tier only once. |
| Placement | Where a blob's bytes are: `blobs.storage_backend_id` (`NULL` = the primary store). The database is the single authority. |
| Policy | A rule "blobs matching these conditions belong on this tier". |

Nothing moves until you add a tier besides `hot` and at least one enabled policy. With no cold tier the mover does nothing and reads take the plain single-store path.

## Design and protocol

### Goals

1. Bytes are only ever copied, never moved in place: at every instant the blob has one authoritative placement and its bytes are readable.
2. A crash (process kill, power loss, lost database connection, an unreachable tier) at any point leaves exactly one valid placement and loses nothing. The next pass finishes or rolls back the move on its own.
3. Blobs stay content addressed: the blob id is its sha256, and every `Put` on every tier carries the `{"sha256": "<hex>"}` metadata, so the copy is verifiable by anyone who holds it.
4. Deletes keep their existing semantics: a blob is removed only through the refcount-aware, lock-protected path ([deduplication and deletion](storage.md#deduplication-and-deletion)), and that removes every copy on every tier.

### The journal

Each move owns one row in `storage_tier_moves`, keyed by the blob's sha256, so a blob can have at most one move in flight (even across replicas). The row records the source and target backend, the reason (policy name or `promote-on-read`), the size, the state (`copying` or `flipped`), the attempt count and the last error. The row is the only state the mover keeps: recovery needs nothing else.

### State machine

```
            claim            copy+verify              flip                  clean            finish
 (no row) ---------> copying ------------> copying ---------> flipped ----------> flipped ---------> (no row)
                      step 1     step 2, 3   (placement      step 4 (one       step 5              row deleted
                                              unchanged)      transaction)
```

| Step | What happens | Placement after |
|---|---|---|
| 1 `claimed` | The journal row is inserted in state `copying` (`INSERT ... ON CONFLICT DO NOTHING`; an existing row for the blob is returned instead). | unchanged |
| 2 `copied` | The source bytes are streamed to the target backend through the throttle, hashing on the way. A hash mismatch (corrupt source) deletes the target object and fails the move. If the target already holds an object of the right size (a resumed move) the copy is skipped. | unchanged |
| 3 `verified` | The target object must exist with the recorded size, and, with `tiering.verifyReadback` (default on), is read back and its sha256 compared to the blob id. A bad copy is deleted so a retry writes it afresh. | unchanged |
| 4 `flipped` | One database transaction, taken under the same per-blob advisory lock uploaders and deleters use: re-check that the target copy still exists with the right size, `UPDATE blobs SET storage_backend_id = target, tier_changed_at = now()` only if the placement still equals the move's source, and set the journal row to `flipped`. | **target** |
| 5 `cleaned` | Under the blob lock, if the placement is still the target, delete the source copy. If the placement moved on or the blob is gone, leave the source alone. | target |
| finish | The journal row is deleted. | target |

The flip is the only operation that changes where a blob lives. Everything before it only adds an unreferenced copy; everything after it only removes a superseded copy.

### Crash analysis

"Crash" means the process stops right after the step. The blob is always readable, because readers try the placement's tier, then fall back to the other tiers when the object is missing there ([Reads](#reads-promote-on-read-and-access-tracking)).

| Crash after | State left behind | Authoritative placement | Recovery on the next pass |
|---|---|---|---|
| `claimed` | journal row `copying`, no target object | source | Resume copies, verifies and flips. |
| `copied` | row `copying`, unverified target object | source | Resume finds the object of the right size, verifies, flips. |
| `verified` | row `copying`, verified target object | source | Resume re-verifies cheaply (existence and size inside the flip) and flips. |
| `flipped` | row `flipped`, both copies present | target | Resume sees state `flipped`, deletes the source copy, finishes. |
| `cleaned` | row `flipped`, source already gone | target | Resume finds the source already missing (not an error), finishes. |

Exactly one placement is valid in every row: before the flip transaction commits it is the source; after, the target. There is no state in which the database points at a missing copy, because the flip transaction verifies the target copy while holding the lock that serializes it against deleters.

Other races, all resolved by the same rule "the placement in the database wins":

- The blob is deleted while it is being copied: the flip reports the blob gone, the target object is removed (unless the placement is the target), the row is dropped.
- The placement changes during the copy (another replica, a manual change): the flip refuses (`placement changed`), the unused target copy is removed, the row is dropped.
- The target backend is unreachable or full: the move fails, the attempt counter grows, the source stays authoritative.
- A move found with the wrong direction for the blob (the journal row's source differs from the placement and from the target) is abandoned the same way.

A journal row is retried by every pass while it has attempts left (`5` back to back), afterwards once per hour. `GET /api/v1/admin/tiering/status` lists the pending rows and the metric `openartifact_storage_tier_moves_pending` counts them.

### Concurrency

- One mover pass runs cluster-wide: the worker takes a session advisory lock (`openartifact:tier-mover`) on a pinned database connection, so a second replica's pass is skipped, not queued.
- Within a pass, `tiering.concurrency` blobs move at once. Two moves of the same blob are impossible: the journal's primary key makes the second claim return the first row.
- The flip, the old-copy removal and `SafeDeleteBlob` all hold the per-blob advisory lock, so a delete either completes before the flip (the flip reports the blob gone) or after (the delete removes every tier's copy).

## Policies

A policy says "blobs matching all of these conditions belong on `target_tier`". Policies are stored in `storage_policies` and evaluated in order of `position` (then creation time); the **first** enabled match decides. A blob already on its policy's target tier is never touched. Blobs matching no policy stay where they are; there is no implicit move back to the hot tier (use promote-on-read for that).

| Field | Meaning |
|---|---|
| `name` | Unique, shown in logs, plans and journal rows. |
| `enabled` | Disabled policies are ignored by the mover and by dry runs. |
| `position` | Evaluation order, lowest first. |
| `target_tier` | Tier name the matching blobs move to. Required. |
| `from_tier` | Optional: only blobs currently on this tier match. Empty means any other tier. |
| `min_age_seconds` | Blob age (`now - created_at`) at least this many seconds. |
| `min_idle_seconds` | Time since the last recorded download at least this many seconds. A blob never downloaded counts from its creation time. |
| `min_size_bytes`, `max_size_bytes` | Blob size bounds, inclusive. `min <= max` is required. |
| `repo_glob` | Repository name glob. |
| `formats` | Package formats (`maven`, `npm`, `docker`, ...); any of them matches. |
| `path_glob` | Glob on the artifact path inside the repository (for Maven the repository path, for npm the package name, for Docker the image name, and so on). |

At least one condition is required: a policy with only a target would move every blob.

Because blobs are deduplicated, one blob can be referenced from several repositories and formats. The repository, format and path conditions use **any reference** semantics: the policy matches if at least one reference matches. Age and idle apply to the blob itself.

Glob syntax: `*` matches any run of characters except `/`, `**` matches anything including `/` (a leading `**/` also matches no prefix at all), `?` matches one character except `/`; everything else, including multi-byte characters, is literal. Globs are at most 256 characters and compile to linear-time (RE2) expressions.

Example ("releases to the S3 tier after 90 days"):

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

`POST /api/v1/admin/tiering/dry-run` evaluates the saved policies, or one draft policy that you have not saved yet, against the current blobs and reports what a pass would move: the number of blobs and bytes, the totals per route (`from` tier, `to` tier) and per policy, and a sample of blobs (at most 200). It performs only reads: it touches no placement, no journal row and no object on any tier. The scan is bounded (200 000 blobs per request); `truncated` tells you when it stopped early and the totals are a lower bound.

The simulation uses the same matching code as the mover. It does not include blobs that already have a move in flight.

## Throttling

| Control | Default | Effect |
|---|---|---|
| `tiering.maxBytesPerSecond` | 64 MiB/s | One byte-rate limiter shared by every copy and read-back in the process, so total mover bandwidth stays under the cap however many blobs move at once. `0` removes the cap. |
| `tiering.concurrency` | 2 | Blobs copied simultaneously. |
| `tiering.batch` | 200 | The most blobs one pass moves. Each replica's scan cursor carries over between passes, so a large backlog drains over several passes instead of one long burst. |
| `tiering.interval` | 5 minutes | Pause between scheduled passes. `0` disables the schedule; `POST /api/v1/admin/tiering/run` still works. |

A pass is bounded to two hours and honors shutdown: an interrupted pass leaves journal rows that recovery finishes.

## Reads, promote-on-read and access tracking

### Transparent reads

All blob access goes through a tiered store that wraps the primary store. A read first tries the tier where the blob is believed to be (a one-minute in-memory cache of recent placements, otherwise the primary store). When that tier reports the object missing, the store asks the database for the placement and reads from there, and finally tries the remaining tiers. Only "not found" moves on to the next tier; any other error (a database failure while looking up the placement, a tier that this server cannot open, a backend error) is returned instead of being reported as a missing blob, so a broken cold tier answers `500`/`503`, never `404`. `Exists` follows the same rule. A flip that happens while a download starts is therefore invisible. The source copy is deleted only after the placement flips; a download that opened the source just before cleanup keeps reading on a filesystem tier (the open file survives the unlink), and on an object-store tier a download that loses that race fails like any interrupted transfer and succeeds on retry.

Uploads always write to the primary store. Deleting a blob removes it from every tier that has a copy; when the blob's placement points at a tier this server cannot resolve, the delete fails and keeps the row rather than orphaning the bytes. Listing and existence checks look at all tiers.

### Promote-on-read

Setting `promote_on_read` on a cold tier makes the mover pull a blob back to the primary tier when it is downloaded after having arrived on the cold tier. In each pass, blobs on promoting tiers whose last recorded download is newer than their last placement change become promotion candidates, and they move to the primary tier through the same journaled protocol (reason `promote-on-read`). A candidate is not promoted when an enabled policy would immediately send it back, so a "30 days idle" policy and promote-on-read together cannot loop. Promotion happens in the background at the next pass; the read that triggered it is served from the cold tier.

### Access tracking

Idle-based policies and promotion need to know when a blob was last downloaded. Tracking is active only while at least one tier besides the primary exists: with a single store nothing is recorded and no usage statistics are aggregated, so a plain installation pays nothing. History starts when the first cold tier is added, and until a blob has been read after that, an idle policy treats it as idle since creation. Only `GET` and `HEAD` requests to the native repository protocols (`/repos/...` and `/v2/...`) count; API calls, replication and the mover's own reads do not. The tracker coalesces downloads in memory and writes them in one batch every `tiering.accessFlushInterval` (30 seconds), and only advances `blobs.last_accessed_at` when the recorded time is more than an hour old, so a hot blob costs at most one database write per hour. A blob is also written when its recorded access is not newer than its last placement change and it has been read since, so the first read after a move is never lost. The batch is sorted and row-locked in that order, so concurrent flushes from several replicas cannot deadlock. `last_accessed_at` is part of the partial index that finds promotion candidates (`idx_blobs_tier_promote`, migration `034`), which makes those updates non-HOT: each one writes a new row version and index entries. The one-hour resolution bounds this to at most one such update per blob per hour, and only for blobs that are read. A crash loses at most the unflushed seconds of access times, which only makes a blob look slightly idler.

## Admin API

All endpoints are under `/api/v1/admin/tiering`, admin only (`manage`). JSON is snake_case. Errors use the standard `{"error", "code"}` shape. The schemas are in [`openapi.json`](../../internal/api/openapi.json) and [REST API](../integrations/api.md).

| Method and path | Purpose |
|---|---|
| `GET /tiers` | Tiers with placement stats: `blob_count` and `total_bytes` per tier, `available` (false when this server cannot open the tier's backend), `promote_on_read`, `rank`. |
| `POST /tiers` | Create a tier: `{"name", "backend", "rank", "promote_on_read"}`. `backend` is a registered storage backend name. `400 validation_failed` for an unknown backend or a bad name (1-64 characters: letters, digits, `.`, `_`, `-`), `409 conflict` when the name or backend is already a tier. |
| `PATCH /tiers/{name}` | Change `rank` and `promote_on_read`. The primary tier cannot promote. |
| `DELETE /tiers/{name}` | Delete a tier. `400 cannot_delete_primary` for `hot`; `409 conflict` while blobs are placed on it, a policy names it or a move involves it. The check and the delete run in one transaction that locks the tier and its backend row. |
| `GET /policies`, `POST /policies` | List, create. |
| `GET`, `PUT`, `DELETE /policies/{id}` | Read, replace (full replacement: omitted conditions are cleared) and delete. |
| `POST /dry-run` | `{"policy": {...}, "sample_limit": 20}`; both optional. Without `policy` the saved policies are simulated. Response: `scanned`, `truncated`, `would_move {count, bytes}`, `routes`, `policies`, `sample`. |
| `POST /run` | Ask the mover to start a pass now: `202 {"started": true}`. `409` when the mover is disabled in the configuration. A pass that cannot take the cluster lock because another replica is running one is skipped. |
| `GET /status` | `enabled`, `running`, `interval_seconds`, the last pass (`moved`, `promoted`, `recovered`, `failed`, `skipped`, `bytes`, `scanned`, `aborted`) and the pending journal rows. |

Deleting a storage backend that is a tier, or is the source or target of an in-flight move, is refused by `DELETE /api/v1/admin/storage/{name}` with `409 conflict`. `blobs.storage_backend_id` is a `RESTRICT` foreign key, so the database refuses to delete a backend that still holds blobs. `PATCH /api/v1/admin/storage/{name}` likewise answers `409 conflict` when it would change the `kind` or `config` of a backend that holds blobs, serves a tier or is named by a move; renaming and `is_default` stay allowed.

Example, end to end:

```sh
# register a second filesystem backend, make it a tier
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/storage" \
  -d '{"name":"hdd","kind":"filesystem","config":{"path":"/var/lib/artifacts-cold"}}'
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/tiering/tiers" -d '{"name":"cold","backend":"hdd","rank":100}'

# "after 30 days without a download, move to cold"
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/tiering/policies" \
  -d '{"name":"cooldown","target_tier":"cold","min_idle_seconds":2592000}'

# what would move?
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST "$OA/api/v1/admin/tiering/dry-run" -d '{}'

# run the mover now
curl -sS -H "Authorization: Bearer $TOKEN" -X POST "$OA/api/v1/admin/tiering/run"
```

## Admin UI

Storage tiers (`/admin/storage/tiers`, under Storage in the sidebar):

- Placement: one row per tier with its backend, availability, blob count and bytes, and the promote-on-read switch. Add and delete tiers.
- Policies: create, edit, enable or disable and delete policies; conditions are described in plain words (age and idle in days, sizes in units).
- Simulation: run a dry run on all saved policies, or on the policy you are editing before saving it, and see counts, bytes, the route totals and a sample of blobs.
- Mover: status, last pass, pending moves, and a "Run now" button.

## Configuration

Settings live under `tiering:` in the YAML file or in the environment ([configuration](../getting-started/configuration.md)). Tiers and policies are data, managed through the API.

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_TIERING_ENABLED` | `tiering.enabled` | `true` | Run the background mover. Off: nothing moves (reads still work from every tier, and `POST /run` returns `409`). |
| `OPENARTIFACT_TIERING_INTERVAL` | `tiering.interval` | `5m` | Pause between passes; `0` disables the schedule. |
| `OPENARTIFACT_TIERING_BATCH` | `tiering.batch` | `200` | Most blobs moved per pass (at least 1). |
| `OPENARTIFACT_TIERING_CONCURRENCY` | `tiering.concurrency` | `2` | Parallel moves (at least 1). |
| `OPENARTIFACT_TIERING_MAX_BYTES_PER_SECOND` | `tiering.maxBytesPerSecond` | `67108864` | Shared copy bandwidth cap; `0` unlimited. |
| `OPENARTIFACT_TIERING_VERIFY_READBACK` | `tiering.verifyReadback` | `true` | Re-hash each copy from the target tier before the flip. |
| `OPENARTIFACT_TIERING_ACCESS_FLUSH_INTERVAL` | `tiering.accessFlushInterval` | `30s` | How often recorded downloads are written. |
| `OPENARTIFACT_TIERING_REFRESH_INTERVAL` | `tiering.refreshInterval` | `30s` | How often each replica reloads tiers and backends from the database. |

In a multi-replica deployment every replica must be able to reach every tier's backend. A replica that cannot open a tier marks it unavailable (visible in the tier list) and falls back to its other tiers; the mover only moves to and from tiers it can open.

Every tier must have its own storage. Two tiers that resolve to the same place would make a "copy" a no-op and the cleanup step delete the only copy, so a tier that aliases the primary store or another tier is marked unavailable (the tier list shows `available: false` and the server logs the reason once). Identity is compared on the canonical location, per backend kind:

  - filesystem: the real path after resolving symlinks, `.` and `..`; equal paths, a path inside another tier's directory, a directory containing another tier's, and two paths that are the same inode all conflict;
  - S3-compatible (S3, Garage, SeaweedFS): endpoint (scheme, case and trailing slash ignored) plus bucket;
  - GCS: bucket; Azure: account plus container; OCI: endpoint plus repository.

  Different prefixes inside one bucket are not distinguished: use separate buckets. The check runs before the backend is opened, so no directory is created under an aliasing path, and the mover re-checks it before it journals a move. A backend that fails to open (for example a mount that is not there yet) is retried on every refresh and recovers by itself.

With `tiering.enabled` the mover holds one pooled database connection for the duration of a pass (the cluster lock, up to two hours) and each blob-lock transaction needs two more, so `database.maxOpenConns` must be `0` (driver default) or at least `4`; smaller values are rejected at startup.

## Metrics

Labels are tier names and fixed result strings, so cardinality is bounded by the number of tiers.

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `openartifact_storage_tier_blobs` | gauge | `tier` | Blobs placed on the tier. |
| `openartifact_storage_tier_bytes` | gauge | `tier` | Bytes placed on the tier. |
| `openartifact_storage_tier_reads_total` | counter | `tier` | Blob reads served by the tier. |
| `openartifact_storage_tier_moves_total` | counter | `from`, `to`, `result` | `result` is `moved`, `skipped` or `failed`. |
| `openartifact_storage_tier_moved_bytes_total` | counter | `from`, `to` | Bytes moved. |
| `openartifact_storage_tier_moves_pending` | gauge | | Journal rows awaiting recovery. Should return to 0; a value that stays above 0 means a move keeps failing (the server log has a `storage tier move failed` warning with the error, and `storage_tier_moves.last_error` holds the latest one). |

The usage gauges come from a whole-table aggregate that is cached: it is recomputed after a pass that moved or promoted blobs, when a tier appears, and at most every 30 minutes otherwise, so an idle installation does not scan `blobs` on every pass. Series are updated in place (they do not vanish between refreshes) and removed when their tier is. The gauges and the pending-moves gauge are published by the replica that runs a pass, so they are leader-only: scrape every replica and take the latest value (for example `max by (tier)`), not the sum. With no cold tier no usage gauges are published.

## Limitations

- **Unlocked writers can leave a stray copy.** The uploaders that bypass the per-blob lock (and S3 staging promotion) always write to the primary store. If such a write races a move of the same blob, a copy can remain on the primary store after the blob moved to a cold tier. It is never read (the placement points at the cold tier), it is bytes-identical by content addressing, and deleting the blob removes it. It only costs space.
- **Any-reference matching.** A deduplicated blob matches a repository, format or path condition if any of its references does.
- **Stats are computed live** (`GROUP BY storage_backend_id`) when you open the tier list; on very large installations that is a scan of the blobs table.
- **Access tracking is for native protocol downloads only**, at one-hour resolution; blobs read only through the admin API or by presigned URL are not counted as accessed. Presigned and redirect downloads that go straight to a cold backend work only when that backend supports presigning; otherwise the server streams the object.
- **Moves are one blob at a time.** Large blobs hold their copy slot for as long as the throttle allows; raise `tiering.concurrency` and `tiering.maxBytesPerSecond` together when moving many large blobs.
- **Listing during a move.** A blob mid-move can appear on two tiers for the duration of the copy; the tiered `List` may report it twice. Backups remove such repeats from their export, but a listing walks tiers one after another, so a blob that moves from a tier already walked to one not yet walked can still be missed. Run full backups while the mover is idle (for example set `tiering.interval` to `0` around the backup window) when a complete archive matters.
- **`POST /run` lands on one replica.** It is skipped when another replica already holds the mover lock; the scheduled passes continue as usual.
- **Backends are configured once.** Tier changes made on one replica reach the others within `tiering.refreshInterval`.
