<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Rebuild from storage

If the database is lost and no usable backup exists, the blob store still holds every package byte. The row journal makes the *index* recoverable too: it mirrors each package row as a small JSON **sidecar** blob next to the content-addressed package blobs, and `openartifact rebuild` re-creates the rows of an empty database from those sidecars.

This is the last resort, not a replacement for [backups](backup.md): users, permissions, tokens, audit logs and configuration are not package data and are not in the sidecars (see [what is not recovered](#what-is-not-recovered)).

## How it works

1. **Triggers mark rows.** Migration 068 adds an `AFTER INSERT OR UPDATE OR DELETE` trigger to every journaled table (`internal/recovery/tables.go`, 32 tables: repositories, blobs, every format's package table, docker manifests and tags, artifact refs and properties, ...). The trigger only records the row key in `recovery_dirty`; it does not read or write the blob store.
2. **A worker writes sidecars.** One replica at a time (advisory lock `openartifact:recovery-journal`, pinned connection) claims dirty keys, loads the row image under `TIME ZONE 'UTC'`, and writes it as a JSON blob. A sidecar is a normal content-addressed blob (its id is the sha256 of its bytes) whose storage metadata carries `oakind=sidecar` and `oatable=<table>` (metadata names are lowercase letters only because S3 lower-cases them and Azure restricts them to identifiers). Rows whose image did not change since the last sidecar (same hash) are skipped, so counters and `last_*` updates cost nothing in storage.
3. **Superseded sidecars are deleted** after the new one is recorded. The tracking table `recovery_sidecars` stores which blob is current for each row, and a pending marker makes a crash between the write and the record leave nothing leaked. Sidecars have no `blobs` row, so they are removed with the blob store directly, never through the deferred blob deletion.
4. **A reconcile backfills.** When the journal is first enabled (and then once per `reconcileInterval`) a keyset-paged scan marks rows that have no sidecar and tracking rows whose row no longer exists. It pauses while the backlog is large, so upgrading a big installation spreads the backfill over time instead of flooding storage.
5. **`openartifact rebuild` reads them back.** It lists the blob store, picks the objects whose metadata marks them as sidecars, validates each (size limit, sha256 equals blob id, known table, key matches the row), keeps the newest per row, inserts the rows in foreign-key order, verifies that the blob bytes each row references exist, then recomputes storage usage and regenerates format metadata.

Sidecars go through the same blob store stack as every other blob, so they are tiered and replicated with everything else.

## Configuration

| YAML | Env | Default | Meaning |
|---|---|---|---|
| `recovery.journal.enabled` | `OPENARTIFACT_RECOVERY_JOURNAL_ENABLED` | `true` | Write sidecars. Disabling stops the worker and makes the triggers a no-op; re-enabling queues a full resync. |
| `recovery.journal.interval` | `OPENARTIFACT_RECOVERY_JOURNAL_INTERVAL` | `10s` | Pause between passes. This is the journal lag, hence the recovery point objective of a rebuild. |
| `recovery.journal.batch` | `OPENARTIFACT_RECOVERY_JOURNAL_BATCH` | `200` | Dirty keys claimed per step. |
| `recovery.journal.concurrency` | `OPENARTIFACT_RECOVERY_JOURNAL_CONCURRENCY` | `4` | Parallel sidecar writes. |
| `recovery.journal.reconcileInterval` | `OPENARTIFACT_RECOVERY_JOURNAL_RECONCILE_INTERVAL` | `24h` | How often the full consistency scan restarts. |

## Cost

- **Database:** one small `INSERT ... ON CONFLICT DO NOTHING` into `recovery_dirty` per changed package row, inside the writing transaction. Measured numbers are in the pull request that introduced the journal.
- **Storage:** one sidecar of 0.5-3 KiB per package row (an npm package with hundreds of versions is larger), written once per distinct row image. Object stores with a per-request price see roughly one extra PUT and one DELETE per row change.
- **Memory of a rebuild:** about 32 bytes per listed object plus about 200 bytes per valid sidecar. Ten million objects need a few hundred MiB. `--concurrency` only changes how many storage reads run at once; raise it for high-latency object stores.

## Monitoring

`GET /api/v1/admin/recovery/status` (admin only) returns the journal state: `enabled`, `backlog` (rows waiting for a sidecar, capped at `backlog_cap`), `oldest_dirty_age_seconds` (the current lag), `retrying_rows`, `sidecars_tracked`, `last_error`, `last_drain_at`, `last_reconcile_at`, `tables_reconciled`/`tables_total` and `schema_version`.

Metrics (see [monitoring](monitoring.md)):

| Metric | Meaning |
|---|---|
| `openartifact_recovery_journal_backlog` | Rows waiting for their sidecar |
| `openartifact_recovery_oldest_dirty_age_seconds` | Age of the oldest waiting row |
| `openartifact_recovery_sidecars_written_total` | Sidecars written |
| `openartifact_recovery_sidecars_deleted_total` | Superseded or orphaned sidecars deleted |
| `openartifact_recovery_failures_total` | Failed row writes (retried with backoff, 5 s up to 15 min) |
| `openartifact_recovery_reconcile_marked_total` | Rows queued by the reconcile |

Alert on a backlog that does not shrink or an oldest age far above `interval`.

## What each format needs from the sidecar

Package bytes are the content-addressed blobs; what the database adds is the mapping from names to those blobs. Several formats could be partly re-derived from the bytes alone, but none completely (repository names and types, publish times, yanked flags, properties, tags, upstream data and the rest are database state), so every package table is journaled as a whole row and restored from its sidecar.

| Format | Derivable from the blob alone | Only in the sidecar |
|---|---|---|
| deb, rpm | name, version, architecture, dependencies (control file / RPM header) | repository, distribution/component, upload time, signing state |
| npm | name, version, manifest (tarball `package.json`) | dist-tags, deprecation, publish time, packument ordering |
| maven, ivy, sbt | coordinates from the path | path, classifier, checksums recorded at upload, snapshot state |
| pypi, nuget, helm, cargo, rubygems, conda, conan | name and version from the archive | filename as uploaded, yanked/unlisted state, requires-python and similar index data |
| go | module path and version from the zip | `.mod` blob link, publish time |
| docker / OCI | manifest content (a blob) | tags, manifest-to-blob references, which repository an image belongs to |
| raw | nothing (the path is a database column) | path, repository |

## Procedure

1. **Stop every server instance** that points at the lost database; a rebuild must not race a live journal.
2. **Provision an empty database** and point the configuration at it. The blob store configuration is the same as before (`storage.*`). The rebuild applies migrations itself unless `MIGRATE_ON_START=false`.
3. **Dry run.** Nothing is written to any table:

   ```sh
   /app/openartifact rebuild --dry-run
   ```

   (In the shell-less image the binary is `/app/openartifact`; elsewhere `openartifact-server rebuild` or your built binary.) A dry run still applies migrations, because it needs the schema to tell which rows exist.
4. **Run it for real**, keeping the report:

   ```sh
   /app/openartifact rebuild --report /tmp/rebuild-report.json
   ```

5. **Check the report and the exit status** (below), then start the server. It regenerates the merged metadata of group repositories on startup. Because the users table is empty, the server also creates the bootstrap admin on first start (read its password from the log); create the other users with `oa apply` or restore the user tables from a database backup.
6. **Verify:** `GET /api/v1/repositories`, download a few packages through the clients, and look at `GET /api/v1/admin/recovery/status` (the journal adopts the recovered rows and rewrites each sidecar once).

The rebuild is **merge-only and idempotent**: it never updates or deletes an existing row, so running it twice, or on a partly populated database, is safe. A row that already exists (the system project, for example) is left as is and re-journaled.

### Flags

| Flag | Meaning |
|---|---|
| `--config <file>` | YAML config (default `$OPENARTIFACT_CONFIG`); env overrides apply as for the server |
| `--dry-run` | Classify and report only |
| `--concurrency N` | Parallel storage reads (default 8) |
| `--deep` | Also read the first bytes of objects whose backend metadata lost the sidecar marker (for example after a copy with a tool that drops user metadata). Costs one ranged read per unmarked object |
| `--prune-superseded` | Delete older sidecars of a row that has a newer one. Never in a dry run |
| `--report <file>` | Write the JSON report to a file |
| `--json` | Print the JSON report on stdout (logs and the text summary go to stderr) |

### Report and exit status

The report lists objects scanned, valid / invalid / unsupported / superseded sidecars, per-table `found`, `inserted`, `already_present`, `unrecoverable` and `failed` counts, the repositories recovered, unreferenced blobs, every problem item, the post steps that ran and the fixed list of what is not recoverable.

| Exit status | Meaning |
|---|---|
| `0` | Everything found was restored |
| `1` | Failures: invalid sidecars, rows the database rejected, or the run itself failed |
| `2` | Completed, but some rows are unrecoverable: the blob bytes a row references are missing from storage, its parent row has no sidecar, or a unique constraint collided |

Rows that reference missing blobs are *not* inserted; they are listed under `missing_blobs` and `unrecoverable`.

## Limits

- **Primary store lost too:** if the primary blob store is lost as well, make the replica the primary (see [replication](replication.md)) and rebuild from it.
- **Listing:** the rebuild lists the store it reads. The OCI registry backend cannot list and is refused with an explicit error. With tiering, only the objects of the store you point it at are seen, so a blob that lives only on a cold tier is reported missing; rebuild with the tier configuration that holds them.
- **RPO is the journal lag.** A change made less than `interval` (plus queueing time) before the disaster has no sidecar yet; watch `oldest_dirty_age_seconds`. A delete inside that window is not seen either, so the old sidecar can resurrect the deleted row.
- **Superseded sidecars and deletes.** A sidecar of a deleted row is removed by the worker, but one that outlived a crash or was written to a replica that has since been cut off can bring a deleted row back. Run once with `--prune-superseded` and review the report if that matters; trash items (soft deleted) come back as trash because the deleted marker is part of the row.
- **Generated metadata** (RPM and Debian indexes, Maven metadata, npm packuments) is regenerated by the rebuild's worker pass and takes a while on big repositories; group repositories regenerate at the next server start.
- **Docker tags** are restored even when the manifest they name could not be (its blob is missing); check `missing_blobs` for manifests and delete dangling tags.
- **Point-in-time recovery of the database** is a separate mechanism; when a database restore is possible it is preferable, because it also keeps users and settings. A rebuild can be run on top of a restored database to add rows the restore lacks (merge-only).

## What is not recovered

Users, roles, permissions, tokens and sessions (re-apply with `oa apply` or restore a database backup); audit logs, webhooks, cleanup and license policies and other configuration; upstream repository passwords; download counters and last-used timestamps; signing key assignments (the keys themselves are secrets that were never in the store); blobs that exist only on a tier or store the rebuild did not read.

## Adding a package table

A table with a foreign key to `blobs` or `repositories` needs an entry in `internal/recovery/tables.go` (`Tables`, parents first) and a `recovery_journal` trigger in a new forward-only migration, or an entry with a reason in `NotJournaled`. `TestRegistryCoversSchema` fails otherwise. See `.agents/rules/recovery-journal.md`.
