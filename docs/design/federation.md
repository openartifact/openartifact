<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Design: federated repositories

Status: proposed (issue [#33](https://github.com/openartifact/openartifact/issues/33)). This document precedes the implementation; the implementation PRs follow it and will update it if reality forces a change.

A **federated repository** is a set of *member* repositories, one per OpenArtifact instance, that converge to the same content. Any member accepts writes; changes flow in both directions. The goal is multi-site replication that survives network partitions and the loss of a whole member, without a coordinator and without a single point of failure.

## Goals and non-goals

Goals:

- Symmetric peer-to-peer replication of a hosted repository between two or more instances.
- Convergence after concurrent writes on several members and after partitions of any length.
- At-least-once delivery, idempotent apply, retries with backoff, catch-up by cursor.
- Deterministic conflict resolution that needs no operator and no coordination.
- Visibility: lag, backlog, last error per peer; manual and full resync; rebuild of a lost member.
- Authentication between instances with scoped tokens (optional extra transport protection), SSRF rules for every outbound call, RBAC that cannot be bypassed through the federation endpoints.

Non-goals (v1):

- Synchronous replication or read-your-writes across members. Replication is asynchronous and eventually consistent.
- Strong consistency on mutable references. Concurrent moves of one tag converge to one winner; the other write is discarded and counted as a conflict.
- Replicating proxy caches, group repositories, users, permissions, repository settings, scan results, or quotas. Each member keeps its own.
- Edge-node / signed release-bundle distribution, see [Legal constraint](#legal-constraint).
- Debian, RPM, RubyGems, Conda, Conan, Ivy and sbt repositories, see [Formats](#formats).

## Legal constraint

JFrog holds US 12,505,255 B2, which covers secure distribution of release bundles to edge nodes with a checksum handshake before the transfer. This design is therefore deliberately **symmetric peer repository synchronisation of a change log**:

- Every member is an equal, writable peer. There are no edge nodes, no hub that publishes to them, and no *release bundle* object is signed, packaged or distributed.
- Transfer is demand-driven by the receiver (pull), per content unit, not a bundle pushed to a node after a prior checksum negotiation. A blob's sha256 is verified **after** it is received and before it is committed (content addressing), not exchanged as a pre-transfer handshake that authorises the transfer.

Before anyone adds an edge/bundle distribution mode (signed bundles, edge nodes, pre-transfer checksum handshake, promotion of release bundles to remote sites), a legal review is advised. The existing [release bundles](../repositories/release-bundles.md) feature stays local to one instance and is not extended by federation.

## Model

```text
  instance A                              instance B
  repo "libs" (federated)                 repo "libs-b" (federated)
  ┌────────────────────┐   B pulls A's    ┌────────────────────┐
  │ artifacts          │ ◄─────log+blobs──│ artifacts          │
  │ unit log (feed)    │                  │ unit log (feed)    │
  │                    │──log+blobs─────► │                    │
  └────────────────────┘   A pulls B's    └────────────────────┘
```

- A member is an ordinary **hosted** repository with the flag `federated = true`. It is served by the normal format handlers; clients see no difference. Proxy and group repositories cannot be federated (a proxy cache is not authoritative content; a group is a read-only view). `internal/repotype` gains `FederationSupported(format)` and repository creation rejects `federated` for unsupported formats or non-hosted types.
- Each member has a **peer** list. A peer is another member that this member pulls from (URL, an outbound token, optional CA pin / client certificate). Replication is symmetric when both sides list each other; a one-way mirror is simply a member with a peer and no inbound token on the other side.
- Member repository names may differ per instance; the formats must match (checked in the handshake).
- Topology is arbitrary: full mesh, chain or star all converge, because every member serves its *whole* state (including state it received from others) in its feed and applies are idempotent (see [Loop prevention](#loop-prevention)).

### Content units

Replication works on **units**: the smallest piece of repository state that is created, replaced or deleted as a whole. A unit has a format-specific *unit key*, and a Codec per format that knows how to read it from and write it to the format's tables (see [Formats](#formats)).

| Kind | What it is | Examples of unit key |
|---|---|---|
| `artifact` | A content-addressed object plus its metadata row | raw `path`; maven `repo_path`; npm `@scope/name@1.2.3`; pypi `name/version/filename`; cargo `name@version`; go `module@version`; helm `name@version`; nuget `id@version`; docker manifest `m:<image>@sha256:…` |
| `ref` | A mutable pointer to another unit | npm dist-tag `name#tag`; docker tag `t:<image>:<tag>` |

Blobs are content-addressed (sha256) and immutable, so they never conflict. Everything that can conflict is a unit *row* (which blob a path or tag points to, plus its metadata).

## Event log

Every member keeps a per-repository **log**. It is *compacted by unit key*: a row per unit holding the unit's last change, so the log is bounded by the number of units (plus recent tombstones), not by the number of writes, and the same structure is the complete state used for full resync. A later event for a unit supersedes the earlier ones; intermediate states (a tag moved five times) are not replayed, only the last one. Delivery is *state transfer driven by a change feed*: the feed tells the receiver which units changed, the receiver fetches the unit's current state.

Event types (the `event` column, informational and used for metrics):

| Event | Emitted when |
|---|---|
| `artifact_added` | a live `artifact` row is inserted or restored from the trash |
| `artifact_deleted` | a live `artifact` row is soft-deleted (trashed) or hard-deleted; the unit becomes a tombstone |
| `ref_moved` | a `ref` row is inserted, re-pointed or deleted |
| `metadata_changed` | an existing `artifact` row's replicated columns change without its blob changing (npm deprecation, cargo yank, …) |

### Schema

Forward-only migrations `070`+ (numbers are indicative until merge).

```sql
-- the identity of this instance, generated once
federation_instance (singleton bool PK, instance_id uuid)

repositories.federated boolean NOT NULL DEFAULT false

-- one row per federated repo: the log head and the hybrid logical clock
federation_repos (
  repo_id uuid PK -> repositories ON DELETE CASCADE,
  log_id uuid,                 -- changes if the log is ever reset (cursor reset on peers)
  head_seq bigint,             -- last allocated sequence number
  hlc_wall bigint, hlc_counter int,
  created_at timestamptz)

-- the compacted log AND the state
federation_units (
  repo_id uuid -> federation_repos ON DELETE CASCADE,
  unit_key text,
  kind text,                   -- artifact | ref
  event text,                  -- see above
  seq bigint,                  -- position in the feed; reassigned on every change
  hlc_wall bigint, hlc_counter int, origin uuid,   -- the stamp of the winning write
  tombstone boolean,
  content_ref text,            -- sha256 of the primary blob, or the ref target
  changed_at timestamptz,
  PRIMARY KEY (repo_id, unit_key));
INDEX (repo_id, seq)

federation_peers (id, repo_id, name, base_url, token_enc, ca_pem, client_cert_enc, client_key_enc,
  enabled, remote_instance_id, remote_log_id, cursor_seq, remote_head_seq,
  last_pull_at, last_success_at, last_error, last_error_at, consecutive_failures, next_attempt_at,
  oldest_pending_at, applied_total, conflicts_total, resync_requested, created_at, updated_at)

federation_deferred (peer_id, unit_key, reason, attempts, next_attempt_at, first_failed_at)

federation_peer_tokens (id, repo_id, name, token_hash, created_at, last_used_at, last_seen_cursor, revoked_at)
```

### Capture: written in the same transaction as the change

Events are captured by **row triggers** (`AFTER INSERT OR UPDATE OR DELETE`) on the per-format unit tables (`raw_artifacts`, `maven_artifacts`, `npm_versions`, `npm_dist_tags`, `pypi_packages`, `helm_charts`, `cargo_crates`, `gomod_modules`, `nuget_packages`, `docker_manifests`, `docker_tags`). A trigger fires inside the transaction of the statement that changed the row, so a change and its log entry commit or roll back together, for **every** code path: the upload handlers, the API, trash delete and restore, cleanup policies, the declarative apply, the docker garbage collector. No handler has to remember to emit an event, and a crash cannot leave a change without its event. (A Go-level emitter after the DB call, or the webhook publisher, is not transactional and would need a repair job.)

Each trigger first checks whether the row's repository has a `federation_repos` row (one primary-key lookup, done for every row change of every repository; the cost on non-federated repositories is that lookup). When it does, and the change is replicated state:

- Updates that only touch volatile columns (`updated_at`, `download_count`, generated search columns, scan state) are ignored (the trigger compares the row with the volatile columns removed).
- A live row inserted, or `deleted_at` going from set to `NULL` → `artifact_added` (or `ref_moved` for refs).
- `deleted_at` going from `NULL` to set, or a hard `DELETE` of a live row → tombstone (`artifact_deleted`).
- A hard `DELETE` of a row already in the trash (the retention purge) → ignored: the tombstone already exists.
- Deleting a whole repository never emits unit deletes: the repository delete is a soft delete, and a purge cascades `federation_repos` away. A member removing its repository is a membership change, not an artifact deletion, and must not delete the data of the other members. Peers just see the member's feed disappear (`404`), reported as a peer error.

The trigger upserts the unit row, assigning:

- `seq`: `UPDATE federation_repos SET head_seq = head_seq + 1 … RETURNING head_seq`. The row lock is held until the transaction ends, so sequence numbers are allocated in **commit order**: a reader that sees seq *n* has seen every seq below *n*. The price is that concurrent writers to the same federated repository serialise on that row for the duration of their transaction (uploads are short, autocommit transactions; heavy parallel ingestion into a federated repo gets less parallelism than into a plain one). A multi-statement transaction touching units of one repo in different orders can deadlock like any row-lock cycle; Postgres aborts one of them and the client retries.
- the **stamp** (hybrid logical clock value, see [Conflicts](#conflicts)).

Enabling federation on an existing repository runs a backfill in one transaction: it creates the `federation_repos` row and one unit per live row, stamped with the row's `updated_at`. Disabling federation deletes the `federation_repos` row (units and peers cascade); triggers become no-ops again. The other members are not affected.

## Replication

### Pull, not push

Each member **pulls** from its peers by cursor. Push (the origin posting each change to every peer) is rejected for v1 because:

- a partitioned or slow peer would require the origin to maintain per-peer outbound queues, retries and dead-letter handling in addition to the log;
- the receiver's position is the receiver's state: with a cursor there is nothing to lose if either side is down, restarted, restored or rebuilt;
- inbound-only reachability is enough (a member behind NAT or firewall can pull);
- the receiver can pace the apply (bandwidth, disk, quota).

The price is latency of up to one poll interval (default 30 s). An `If-None-Match`-style fast path (the feed answers `304`/empty when `head_seq` equals the cursor) keeps an idle poll to one cheap request.

### Protocol

All endpoints live under `/federation/v1/` and authenticate with a **peer token** that is bound to one repository (see [Security](#security)); the repository is therefore implied by the token and never appears in the URL.

| Endpoint | Purpose |
|---|---|
| `GET /federation/v1/info` | Handshake: `{protocol, instance_id, log_id, head_seq, repo: {name, format}}` |
| `GET /federation/v1/log?after=<seq>&limit=<n>` | Feed: units with `seq > after` in seq order: `{log_id, head_seq, entries: [{seq, unit_key, kind, event, tombstone, stamp, content_ref, changed_at}], has_more}` |
| `GET /federation/v1/unit?key=<unit_key>` | The unit's *current* state: `{unit_key, stamp, tombstone, data, blobs: [{sha256, size}], deps: [unit_key]}` |
| `GET /federation/v1/blob?unit=<unit_key>&sha256=<hex>` | Stream one blob of that unit. Only blobs listed in the unit's `blobs` are served |

`data` is the Codec's JSON for the unit: the row's replicated columns with local ids and blob ids replaced by sha256 values. The server never serves a blob that is not part of a live unit of the token's repository, so the token cannot be used to read other repositories or arbitrary storage keys.

### Pull loop (per peer)

1. Take a per-peer advisory lock on a pinned connection (several replicas of one instance: one pulls at a time).
2. `GET /info`. If `log_id` differs from the stored `remote_log_id`, or `head_seq` is below our cursor (the peer was rebuilt or restored), reset the cursor to 0: a full merge follows. A `format` mismatch is a configuration error.
3. `GET /log?after=cursor`, in batches.
4. For each entry in order: compare the entry's stamp with the local unit's stamp.
   - Local stamp ≥ entry stamp: nothing to do (idempotent; this is also what an *echo* of our own change looks like).
   - Otherwise fetch `GET /unit?key=` and apply it (below). The fetched state may be newer than the entry (the unit changed again meanwhile); applying the newest state is correct.
   - After the apply succeeds, advance the cursor to the entry's `seq` (persisted per entry or per batch).
5. Repeat while `has_more`; then sleep for the poll interval.

### Apply

Apply is **one local database transaction per unit**, run by the same Codec code paths that define the unit:

1. Fetch the blobs that are missing locally (`blobs` row *and* object in the store) into a spool file while hashing; the sha256 must match the unit's declared hash, else the unit fails. The staged blobs are `Put` (with the mandatory `sha256` metadata) before any lock is taken, by `storage.CommitBlobs`; the per-blob locks are then taken only for the database work, and under them the presence of every blob of the unit is checked again (a staged blob a concurrent delete removed is stored again from its spool file; a blob that was already local and has vanished since fails the unit with `storage.ErrBlobGone`, which is deferred as `blob_gone` and retried on the unit's backoff). So a concurrent `SafeDeleteBlob` cannot remove a blob before the rows referencing it exist, and an object-store `PUT` never holds a lock slot.
2. Apply `deps` first (a dist-tag needs its version, a docker tag needs its manifest, an OCI index needs its child manifests; such units are fetched by key from the same peer and applied recursively).
3. In one transaction with `SET LOCAL oa.federation_apply = 'on'` (which makes the capture triggers return immediately): ensure `blobs` rows, write or soft-delete the unit rows, and write the unit's log row with the **remote stamp** and a fresh local `seq` (so this member's own feed now contains the unit and other peers can pull it from here), observing the remote stamp into the local clock.
4. After commit: reserve/release quota (`internal/quota`; an exceeded quota fails the unit), release replaced blobs with `storage.SafeDeleteBlob`, notify `RegenQueue` consumers (Maven metadata, npm packuments) and publish `artifact.uploaded` / `artifact.deleted` webhooks so scans, SBOM and AI analysis see the new content exactly as for a local upload.

Because apply reuses the unit tables, the existing read paths, listings, search and trash handle replicated content with no special cases. A unit applied from a peer is indistinguishable from a local upload except for its `deleted_by` (`federation:<peer name>`) if it is later trashed by a replicated delete.

### At-least-once and idempotency

The cursor advances only after the unit is applied and committed. A crash between commit and cursor update re-delivers the entry; the stamp comparison makes the second apply a no-op. Re-delivery, duplicate delivery and reordering across batches are all harmless because apply is a *stamp-ordered upsert of a state*, never an increment.

### Failures, retries, backoff

- A failed pull (network, `5xx`, token rejected) records `last_error`, `last_error_at`, increments `consecutive_failures` and sets `next_attempt_at = now + min(maxBackoff, 5 s · 2^n) ± 20 % jitter`. The worker skips the peer until then. The first success resets the counters. Backoff never gives up: a peer that is down for a week is retried every `maxBackoff` (default 15 min) and catches up when it returns.
- A **unit** that cannot be applied (quarantined at the origin, quota exceeded, blob missing on the origin, validation error) must not block the rest of the feed. After 3 immediate attempts the entry is parked in `federation_deferred` with the reason, the cursor moves on, and the unit is retried with its own exponential backoff (cap 1 h) on every cycle. Deferred units are listed in the health view with reason and attempt count. A manual resync retries them at once.
- Blob transfers use a per-transfer idle timeout, not a total one, so very large blobs on slow links still complete.

### Conflicts

Conflicts exist only on unit rows; blobs never conflict.

**Rule: last writer wins per unit, ordered by a hybrid logical clock; ties broken by origin instance id.** Every write to a unit carries a stamp `(wall, counter, origin)`:

- `wall`: unix microseconds of the database clock of the writing instance, never lower than any stamp the instance has seen for the repository.
- `counter`: logical counter that increases when `wall` does not advance (several writes in one microsecond, or the local clock is behind a stamp received from a peer).
- `origin`: the instance id of the writer.

Stamps are totally ordered by `(wall, counter, origin)` (origin compared as the 16 bytes of its uuid). When a member applies a remote stamp it raises its own repository clock to at least that value, so any later local write is ordered after everything the member has seen: causally later writes always win; only truly concurrent writes are decided by time then origin, identically on every member. All members therefore compute the same winner without talking to each other.

Why not the alternatives:

- **A declared primary per ref.** It needs per-ref configuration, makes every non-primary write either impossible while the primary is unreachable (breaks write availability, the reason to federate) or a guess to be reconciled later, and needs a failover story. LWW needs no configuration and keeps all members writable during a partition.
- **Vector clocks / version vectors.** They *detect* concurrency but do not resolve it; a deterministic tie-break is still needed, and with it the result equals LWW. They cost O(members) per unit and a membership registry. For "keep the newer of two states" they add storage and complexity without a better outcome. The conflict is detected cheaply instead (below).
- **Keeping both writers.** Impossible for a path or tag by definition.

Properties and limits, stated plainly:

- Clock skew between instances can make a physically earlier write win. The HLC bounds the damage (a member that learns of a far-future stamp does not keep issuing lower ones) but not eliminates it. Keep instance clocks in sync (NTP).
- One of two concurrent writes is discarded. The losing write is **not** lost silently: when applying a remote stamp replaces a *local* live unit with a different `content_ref`, or when a local unit is skipped because a newer remote one exists with a different `content_ref`, the peer's `conflicts_total` is incremented, a warning is logged and `openartifact_federation_conflicts_total` is bumped. The superseded blob is released by normal refcounting (it remains in the member's trash if the supersede was a soft replace).
- "Immutable release" repositories: a concurrent publish of the same version on two members with different content is exactly a conflict. Federation converges it by the same rule rather than leaving the members permanently divergent; `immutable_releases` continues to protect against local overwrite attempts, but a replicated overwrite decided by the rule is applied and recorded as a conflict.

### Deletes and tombstones

A delete is an event, not an absence. The unit row stays with `tombstone = true`, its own stamp and a new `seq`, so that:

- a peer that was partitioned during the delete learns of it from the feed;
- an older concurrent upload of the same unit (lower stamp) does not resurrect it, and a later upload (higher stamp) correctly re-creates it.

A replicated delete is applied like a local delete in that repository: soft delete into the trash (`deleted_by = federation:<peer>`) with the repository's retention window, or hard delete when retention is 0, then `SafeDeleteBlob`. A trash *restore* on one member is `artifact_added` for the others. A trash *purge* emits nothing.

Tombstones are garbage-collected after `federation.tombstoneRetention` (default 30 days) by a periodic job. A member that stays offline longer than that and then resyncs cannot learn the deletes that were collected; it still holds the artifact, and the next sync *resurrects* it on the other members. This is the standard trade-off of tombstone expiry. Set the retention above the longest partition you intend to survive; the health view shows how long each peer has been behind.

### Loop prevention

- Capture triggers are disabled inside apply transactions (`oa.federation_apply`), so applying a remote change never creates a *new local* write with a fresh stamp.
- The apply writes the unit with the **origin's** stamp. The unit appears in this member's feed (so that a third member can pull it from here), and the member it came from sees an entry whose stamp equals its own and does nothing. The loop terminates after one metadata-only round trip and no blob moves.
- A pull never fetches blobs for a unit that is already at or above the entry's stamp and whose blobs are present.

### Resync and rebuild

- **Resync** (manual button/API, or automatic on a `log_id` change): reset the peer's cursor to 0 and run a full pass over the peer's feed. It is a merge, idempotent and safe to run any time: units already up to date cost one comparison; for units that compare equal it also verifies the blobs exist in the local store and refetches missing ones; it retries every deferred unit. It never deletes a local unit merely because the peer lacks it (the peer may simply not have it yet, and tombstones, not absence, express deletion).
- **Rebuilding a deleted member**: create the repository on the replacement instance with `federated`, add the surviving member as a peer, issue a token on the survivor for it, and start the pull. The new member has a new `instance_id`/`log_id`; its cursor starts at 0, the feed contains every unit and tombstone, and the new member ends up with the full content. The survivors see a changed `log_id` on the replacement and reset their cursor for it (nothing to import yet). A member **restored from a backup** keeps its `log_id` but its sequence may be behind what peers already passed; after a restore run *Resync* on every peer (the health view shows `head_seq < cursor` as an error until then).

## Security

### Authentication: scoped peer tokens

A peer token is a separate credential type, *not* an API token:

- Format `oafed_<43 base64url chars>` (256 bits). Only its SHA-256 is stored (`federation_peer_tokens.token_hash`); the plaintext is shown once at creation.
- It is bound to **one repository** and grants exactly the four `/federation/v1/*` read endpoints above for that repository. It cannot reach the REST API, any format endpoint, or any other repository; the user/API tokens, sessions and the legacy key are rejected on `/federation/v1/*`. The federation routes are mounted outside the RBAC middleware chain with their own authentication middleware: they require this dedicated *peer scope*, which no user, group or permission target can acquire.
- Tokens can be revoked and listed (name, created, last used, last cursor seen) by an admin (`manage`) through the REST API; every issue and revoke is audited.
- The **outbound** token a member uses to talk to a peer is stored in `federation_peers.token_enc`, encrypted with `internal/crypto` (`OPENARTIFACT_SECRET_KEY`) and never returned by the API.

### Transport and mTLS

- Peers must use `https` URLs; plain `http` is accepted only when private addresses are allowed (`OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`, development and closed networks).
- A peer entry may carry a **CA bundle to pin** (`ca_pem`) for private PKIs, and an optional **client certificate and key** (`client_cert_enc`, `client_key_enc`, encrypted) presented to the peer. Inbound mTLS is terminated by the reverse proxy in front of the peer instance (the usual deployment of OpenArtifact); the proxy decides which client certificates may reach `/federation/`. The token is still required behind it, so mTLS is defence in depth, not a replacement. Native inbound client-certificate verification in the server is not part of v1.

### SSRF

Peer URLs are user-supplied outbound targets. They are validated on write with `netutil.ValidateOutboundURL`, and every request is made through the `internal/netutil` SSRF-safe client (address checked at dial time, so DNS rebinding and redirects to private addresses are blocked; redirects are not followed with credentials). Private and loopback targets need `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`, like webhooks and proxies.

### RBAC

- Configuring peers, issuing tokens, resync and the health view are `/api/v1/repositories/{name}/federation/...` routes: the RBAC middleware maps `/api/v1/*` to `manage`, so only an administrator of the repository (global `manage`, or repository-scoped manage / project owner) can use them.
- Federation never grants a read or write path to artifacts: replication moves data between instances, while client access to each member stays governed by that member's RBAC.
- The apply path acts as the system, not as a user: it is not subject to the repository's write permission targets but is subject to quota, and a repository that is `blocked` refuses it.

## Formats

v1 supports the formats whose state is *immutable blobs plus simple, row-shaped mutable references* and whose served metadata is either derived on read or regenerated by `RegenQueue`:

| Format | Units | Derived metadata after apply |
|---|---|---|
| raw | `path` → blob | none |
| maven | one unit per file (`repo_path`) | `maven-metadata.xml`: Maven regen queue |
| npm | version (tarball + manifest), dist-tag (`ref`) | packument: npm regen queue |
| docker / OCI | manifest (with config and layers), tag (`ref`) | none (served from rows) |
| helm | chart `name@version` | `index.yaml` rendered on read |
| pypi | file `name/version/filename` | simple index rendered on read |
| cargo | crate `name@version` (yank is a column) | sparse index rendered on read |
| go | module `module@version` (zip + mod) | `@v/list` rendered on read |
| nuget | package `id@version` | registration rendered on read |

Excluded in v1, with reasons:

- **Debian and RPM**: repository metadata is *signed* and generated per distribution/component with the instance's own GPG key, merged with group state and tied to per-repo regeneration; replicating packages is easy but replicating or recomputing consistent signed indexes across members is a design of its own (which member signs, key distribution, `InRelease` freshness).
- **RubyGems, Conda, Conan**: Conda's `repodata` and RubyGems' marshalled specs are regenerated whole; Conan's recipe/package revisions and checksums form a graph with its own concurrency model. Each needs its own unit design and tests.
- **Ivy and sbt**: path files like maven; deferred only to keep the first test matrix small, not for a technical reason.
- **Proxy and group repositories**: not authoritative content.

## Interaction with other features

- **RegenQueue**: apply never regenerates inline; it calls the same `NotifyStale` entry points the upload handlers use, so concurrent applies coalesce (one running plus one pending run per key). Regenerated metadata (`repository_metadata`, generated indexes) is **never replicated**; each member renders its own from the units, which is also why members converge on identical *content* but not necessarily on byte-identical metadata files.
- **Quarantine**: quarantine is local state (scan results are per member). A unit that is currently quarantined at the origin is **not served** (`423` on the unit and blob endpoints) and is deferred on the receiver until released. Applied units are scanned by the receiver under its own policies via the `artifact.uploaded` event. A receiver's quarantine is not propagated back.
- **Trash and immutable releases**: see [Deletes](#deletes-and-tombstones) and [Conflicts](#conflicts). A trashed unit is a tombstone.
- **Quotas**: apply reserves quota like an upload; exceeding it parks the unit in `federation_deferred` with the reason `quota`.
- **Storage tiering, presigned downloads, backups**: unaffected; replicated blobs are ordinary blobs. The federation tables are part of the database backup.

## Observability

Per peer, in the API and the repository settings page: `enabled`, `last_success_at`, `last_error` / `last_error_at`, `consecutive_failures`, `cursor_seq` vs `remote_head_seq` (**backlog** in units), **lag** (age of the oldest unapplied entry, 0 when caught up; derived from `oldest_pending_at`), `deferred` units with reasons, `conflicts_total`, `applied_total`.

Prometheus metrics (labels `repo`, `peer`): `openartifact_federation_lag_seconds`, `openartifact_federation_backlog_units`, `openartifact_federation_applied_total{result}`, `openartifact_federation_conflicts_total`, `openartifact_federation_pull_errors_total`, `openartifact_federation_last_success_timestamp_seconds`.

## Configuration

| YAML key | Env | Default |
|---|---|---|
| `federation.enabled` | `OPENARTIFACT_FEDERATION_ENABLED` | `false` (the `/federation/` routes and the pull workers exist only when true; a repository cannot be created or flagged `federated` while it is false) |
| `federation.pollInterval` | `OPENARTIFACT_FEDERATION_POLL_INTERVAL` | `30s` |
| `federation.batchSize` | `OPENARTIFACT_FEDERATION_BATCH_SIZE` | `200` entries per feed request |
| `federation.maxBackoff` | `OPENARTIFACT_FEDERATION_MAX_BACKOFF` | `15m` |
| `federation.requestTimeout` | `OPENARTIFACT_FEDERATION_REQUEST_TIMEOUT` | `30s` (API calls) |
| `federation.tombstoneRetention` | `OPENARTIFACT_FEDERATION_TOMBSTONE_RETENTION` | `720h` |

## Test plan

Unit tests: stamp ordering and the clock receive rule; backoff schedule; codec round trips; token hashing and scope checks.

Integration tests (Postgres, two real server instances in one process with two databases and two blob stores):

1. Concurrent uploads of different artifacts on both members converge to the union.
2. Partition: stop the peer's server; write on both sides; bring it back; both catch up from their cursors; lag and `last_error` are visible during the partition and clear afterwards.
3. A tag moved concurrently on both members resolves to the same winner on both, and the loser is counted as a conflict.
4. A delete propagates; an older concurrent re-upload does not resurrect it.
5. A deleted member is rebuilt from scratch by a full resync and holds every unit and blob.
6. Federation endpoints reject user tokens, other repositories' peer tokens and anonymous callers; SSRF rejects loopback peers by default.

A smoke test with two real binaries and real clients is recorded in the implementation PR.

## Future work (not v1)

Push-assisted wakeups (a peer nudges the other when its head moves, reducing latency while keeping pull as the source of truth), scheduled anti-entropy, Debian/RPM with a designated signing member, per-member read-only mode, native inbound mTLS, edge distribution only after the legal review described above.
