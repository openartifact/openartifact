<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Disaster recovery

A runbook: what protects what, and for each way of losing data, the procedure, the recovery point (RPO) and time (RTO) of each option, and the commands that prove it worked. Read the first two sections now, the rest during an incident.

## What there is to lose

An OpenArtifact installation keeps its state in four places, and each is protected differently:

| State | Holds | Protected by |
|---|---|---|
| PostgreSQL | Repositories, package records, users, tokens, permissions, projects, webhooks, SSO providers, settings, the [trash](trash.md) | [Point-in-time recovery](postgres-pitr.md) (continuous), a [backup](backup.md) or `pg_dump` (periodic) |
| Blob store | The bytes of every artifact, plus the [sidecar records](rebuild-from-storage.md) that let the database be rebuilt | [Replication](replication.md) to a second backend, bucket-level versioning or snapshots |
| Secrets and configuration | `OPENARTIFACT_SECRET_KEY`, `AUTH_HMAC_KEY`, SAML key pair, storage credentials, `BASE_URL`, GPG key passphrases | Your secret manager; see [secrets to preserve](../getting-started/upgrading.md#secrets-to-preserve) |
| Declarative configuration | Users, groups, projects, repositories, permission targets, SSO providers, webhooks, tiers | `oa export` into Git, restored with `oa apply` ([declarative configuration](../integrations/declarative.md)) |

Without `OPENARTIFACT_SECRET_KEY` a restored database keeps its encrypted values (upstream passwords, webhook secrets, SSO client secrets, storage credentials) but cannot read them. Store it apart from the database and the blob store, or no other backup is usable.

## What to set up before the disaster

| Setup | Protects against | RPO | RTO | Cost |
|---|---|---|---|---|
| WAL archiving and base backups ([postgres-pitr.md](postgres-pitr.md)) | Database lost, corrupted, or damaged by a bad change | 5 minutes or less | Base backup restore plus WAL replay: minutes for a metadata-sized database, measured by a [drill](postgres-pitr.md#restore-drills) | An object store; one `ScheduledBackup` |
| Nightly `pg_dump` or an [OpenArtifact backup](backup.md) | Database lost | The backup interval, 24 h for a nightly job | The restore time of the dump | A cron job and off-site copies |
| Sidecar records and `openartifact rebuild` ([rebuild-from-storage.md](rebuild-from-storage.md)) | Database lost and no usable database backup, or a backup older than the data | The journal lag (`oldest_dirty_age_seconds` of `GET /api/v1/admin/recovery/status`), 10 seconds by default plus queueing | One pass over the blob store: grows with the number of objects | The journal writes one small blob per package record |
| Blob replication ([replication.md](replication.md)) | Blob store lost, one blob lost, a corrupted object | The replication lag (a metric), usually seconds | Immediate for reads (the server falls back to the replica); the repair of the primary runs in the background | A second backend and its egress |
| Bucket versioning or provider snapshots of the blob store | A deleted or overwritten object, including by the application | The snapshot interval, or zero with versioning | Depends on the provider | Storage for old versions |
| [Trash](trash.md) (on by default, 7 days) | A repository, package or version deleted by a person or a cleanup policy | 0 until the retention ends | Seconds | Space until purge |
| `oa export` into Git | Configuration lost | The last commit | `oa apply` takes seconds | None |

Pick at least one line per row of the first table. The cheapest set that leaves no single point of failure is PITR for PostgreSQL, replication for the blob store, `oa export` in Git, and the secrets in a manager.

A protection that has stopped working is the usual reason a recovery fails, so alert on each of them ([monitoring](monitoring.md)):

| Protection | Alert on |
|---|---|
| WAL archiving | The archiver's `failed_count` rising, or no new `last_archived_wal` for longer than `archive_timeout` plus a few minutes ([postgres-pitr.md](postgres-pitr.md#check-that-it-works)) |
| Sidecar journal | `openartifact_recovery_oldest_dirty_age_seconds` far above `recovery.journal.interval`, or `openartifact_recovery_journal_backlog` that does not shrink |
| Replication | `openartifact_replication_oldest_pending_age_seconds` growing, `openartifact_replication_failures_total` rising, `failing` above zero in `GET /api/v1/admin/storage/replication` |

The options combine. After restoring an older database, `rebuild` adds back what was written after it from the sidecar records; after losing the primary blob store, the replica serves reads while the primary is refilled.

## First: find out what is gone

```sh
curl -sS HOST/ready                        # 503 "database unavailable" or "storage unavailable" names the layer
curl -sS HOST/health | jq .                # per-component status: database, storage
oa system info                             # version, database and storage status
oa trash list                              # anything recently deleted that is still restorable (admin)
oa audit list --action artifact.purged --limit 20     # something purged for good?
```

Then pick the procedure:

| What happened | Procedure |
|---|---|
| A repository, a version or a file was deleted | [Deleted repository or package](#a-repository-or-package-was-deleted) |
| PostgreSQL is gone, or its content is wrong | [Database lost or damaged](#the-database-is-lost-or-damaged) |
| The blob store lost objects or is gone | [Blob store lost or damaged](#the-blob-store-is-lost-or-damaged) |
| Both, or the whole site | [Everything lost](#everything-is-lost) |
| An upgrade went wrong | [Rollback](../getting-started/upgrading.md#rollback): restore the database taken before the upgrade, or point-in-time recover to just before it |

Before changing anything, stop writers that make it worse: scale the application to zero if it is serving wrong data or running a purge or cleanup you do not trust, and pause CI publishing. Keep the damaged database and volumes until the recovery is verified (rename or snapshot them) unless you must free the space.

## A repository or package was deleted

Deleting an item, a version, a Docker tag or a whole repository moves it to the [trash](trash.md) for its retention period (7 days by default, `trash_retention_days` per repository). It is hidden from every client but its record and its bytes are untouched, so restoring it is exact.

**Procedure (within the retention):**

```sh
oa trash list                                  # admin: every trashed item, repositories included
oa trash list --repo releases --kind maven_artifact
oa trash restore repository <id>               # a whole repository (admin)
oa trash restore maven_artifact <id> --repo releases
```

The same restore is `POST /api/v1/trash/{kind}/{id}/restore` (admin) or `POST /api/v1/repositories/{name}/trash/{kind}/{id}/restore` (the `delete` permission on that repository), and a button in the UI. A `409 trash_conflict` means the path or version was published again meanwhile; delete or move the new copy first. A `409 repository_in_trash` means restore the repository first. See [restore conflicts](trash.md#restore).

**Verify:**

```sh
oa repo list | grep releases                                    # the repository is back
curl -fsS -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer <TOKEN>" HOST/repos/raw/releases/<KNOWN FILE>   # 200
oa trash list --repo releases                                   # the item left the trash
```

Generated indexes (npm packuments, Maven metadata, Debian `Packages`, RPM repodata, Helm `index.yaml`) are rebuilt on restore; request one and check it lists the version.

| | RPO | RTO |
|---|---|---|
| Trash restore | 0 | Seconds |

**Tested by:** `TestTrashPolicies_RepositoryDeleteConfirmationAndTrash` (`tests/integration/trash_policies_test.go`) deletes a repository through the API, shows that it is hidden and its name reserved, restores it as an admin, and downloads its files again; `TestTrashRepositoryHidesAndRestores` (`internal/database/trash_integration_test.go`) covers the database layer, and `TestTrashProtocols_Maven`, `TestTrashProtocols_NPMUnpublishVersion` and `TestTrashProtocols_Deb` (`tests/integration/trash_protocols_test.go`) delete and restore items and check the files and generated metadata the protocol serves.

**The trash does not have it** when the retention ran out, when the repository had `trash_retention_days: 0`, or when someone purged it (`artifact.purged` or `repository.purged` in the [audit log](monitoring.md#audit-log)). A purge removes the record and the bytes that nothing else references, so recovering needs both back:

1. **An OpenArtifact [backup](backup.md) archive** that still holds it. A `full` archive carries rows and bytes. Restore it into the live instance with the [restore upload](backup.md#restore) (`oa backup restore` does not work, see the [limits](backup.md#limits)): repository names that already exist are skipped with a warning, counted as failures, and so are the rows under them, so only the missing repository and its packages are created, with new record IDs. Expect `restore completed with failures` in the server log and read the failures: they should name only the repositories that were never lost. A repository that was only in the trash when the backup ran is not in the archive, because [backups leave out trashed data](backup.md#what-is-in-an-archive).
2. **Point-in-time recovery plus the bytes, through a scratch instance.** Recover the database into a scratch cluster at a time before the purge (a [restore drill](postgres-pitr.md#restore-drills)), point a scratch OpenArtifact at it and at a copy of the blob store that still has the bytes, take a `full` backup there (`oa backup create --type full`), and restore that archive into production as in option 1. The bytes of a purged package stay in the blob store, on the primary and on the replica, for the [deletion grace period](storage.md#deferred-blob-deletion) (`blobDeletion.grace`, 24 hours by default); after that you need a provider snapshot or a versioned copy from before the purge. Copy the store before the grace period ends, and give the scratch instance its own copy, not the production bucket. It is long, but it uses only existing tools and leaves production untouched until the last step. To widen the window for next time, raise `OPENARTIFACT_BLOB_DELETION_GRACE` (it costs the space of deleted data for that long).
3. **Publish it again** from the build system, which is the source of truth for hosted artifacts. Proxy repositories refill from their upstream on demand, and group repositories are configuration (`oa apply`).

A [replica](replication.md) does not help here: it copies deletions too, after the deletion grace period.

## The database is lost or damaged

The blob store is intact, PostgreSQL is gone, unreachable, or holds wrong data (a bad delete, a bad migration, a corrupted volume). The options, best first:

| Option | Gets back | RPO | RTO |
|---|---|---|---|
| **A. Point-in-time recovery** | Everything, to the second: users, tokens, permissions, settings, the trash | 5 minutes or less | Base backup size plus WAL since it |
| **B. Restore a dump or OpenArtifact backup, then `rebuild`** | Everything in the dump; `rebuild` adds the packages written since | The dump interval for configuration and users; the journal lag for packages | Dump restore time plus one `rebuild` pass |
| **C. `rebuild` alone** | Repositories and packages, for every format the [sidecar records](rebuild-from-storage.md) cover | The journal lag | One pass over the blob store |

### A. Point-in-time recovery

Follow [postgres-pitr.md](postgres-pitr.md#restore): choose a target just before the damage, stop the application, create the recovered cluster, start the application, verify, and take a new base backup.

Then reconcile the blob store with the restored database ([what a recovery leaves behind](postgres-pitr.md#what-a-recovery-leaves-behind)). If the target was minutes or more before the incident, packages uploaded in between have bytes but no rows: [run the rebuild](#run-the-rebuild) (merge-only, it adds the rows that are missing and touches no existing one) and read its report.

### B. Restore a dump or backup, then rebuild

1. Start PostgreSQL and the application with the same `OPENARTIFACT_SECRET_KEY` and the same blob store. For a `pg_dump`: stop the application, `pg_restore` into an empty database ([commands](postgres-pitr.md#without-cloudnativepg)), start it. For an OpenArtifact archive: start a fresh instance and upload the archive ([restore](backup.md#restore)); it regenerates most record IDs and leaves out [trashed data, project tokens, GPG keys, sessions and the audit log](backup.md#what-is-in-an-archive).
2. [Run the rebuild](#run-the-rebuild) to add what the dump does not have. It is merge-only: a row that exists in the restored database is never updated, so a package changed since the dump keeps its old state, and a record deleted less than the journal interval before the loss can come back ([limits](rebuild-from-storage.md#limits)). Read the dry-run report before the real run.
3. Re-enter what no backup holds (webhook signing secrets, SSO client secrets, storage credentials, proxy upstream passwords); see [secrets in the archive](backup.md#secrets-in-the-archive).

### C. Rebuild from storage alone

Use this when no database backup is usable. It recovers what the blob store can say about itself, and nothing else: package records for every format, from the [sidecar records](rebuild-from-storage.md#how-it-works) the server writes next to the blobs. [What is not recovered](rebuild-from-storage.md#what-is-not-recovered): users, roles, permissions, tokens, sessions, audit log, webhooks, cleanup and license policies, upstream passwords, download counters, signing key assignments.

1. Stop every server that points at the lost database, and provision an empty PostgreSQL. Keep the **same blob store configuration and the same `OPENARTIFACT_SECRET_KEY`**.
2. [Run the rebuild](#run-the-rebuild). The rebuild applies the migrations itself.
3. Start the server. The users table is empty, so it creates the bootstrap `admin` with a [random password printed once](../getting-started/installation.md#first-login). Log in and set a new password.
4. Re-create what lives only in the database from your declarative configuration: `oa apply -f exported/ -R` for users, groups, projects, permission targets, SSO providers, webhooks and tiers. Run it after the rebuild: the rebuild brings repositories back with their original IDs, and `apply` [adopts](../integrations/declarative.md#ownership-adopting-and-pruning) them by name. A `User` manifest has no password field, so local users need a new password (or sign in through SSO). Tokens, sessions, GPG keys, the audit log and webhook delivery history are gone: mint new tokens, import the GPG keys again, and re-enter secrets.
5. Compare the report with what you expect; anything it lists as unrecoverable has to be re-published.

### Run the rebuild

With every server stopped (a rebuild must not race a live journal), against the target database and the blob store as the server sees them:

```sh
# binary or shell-less image: /app/openartifact; Docker Compose, from the checkout:
docker compose -f docker/docker-compose.yml run --rm openartifact /app/openartifact rebuild --dry-run
docker compose -f docker/docker-compose.yml run --rm openartifact /app/openartifact rebuild --report /tmp/rebuild-report.json
```

`--dry-run` writes no rows (it still applies the migrations) and prints what the real run would insert, and what it cannot recover. The run is idempotent: run it as often as you like. The exit status is `0` when everything found was restored, `1` on failures (invalid sidecars, rows the database rejected), and `2` when it completed but some rows are unrecoverable (their blob bytes are missing from the store, their parent row has no sidecar). Flags (`--deep`, `--prune-superseded`, `--json`, `--concurrency`) are in [rebuild from storage](rebuild-from-storage.md#flags).

On Kubernetes, scale the Deployment to zero and run the same command as a Job built from the Deployment's own pod spec, so it gets the same environment, Secret and volumes:

```sh
kubectl -n openartifact scale deployment/openartifact --replicas=0
kubectl -n openartifact get deployment openartifact -o json | jq '{
  apiVersion: "batch/v1", kind: "Job", metadata: {name: "openartifact-rebuild"},
  spec: {backoffLimit: 0, template: {spec: (.spec.template.spec
    | .restartPolicy = "Never" | del(.initContainers)
    | .containers[0] |= (del(.livenessProbe, .readinessProbe, .startupProbe)
        | .command = ["/app/openartifact", "rebuild"]))}}}' | kubectl -n openartifact apply -f -
kubectl -n openartifact wait --for=condition=complete job/openartifact-rebuild --timeout=6h
kubectl -n openartifact logs job/openartifact-rebuild
```

Add `"--dry-run"` to the command for the first pass, and delete the Job (`kubectl delete job openartifact-rebuild`) between runs. The job fails on exit status 1 or 2; the log has the report either way. Scale the Deployment back up when it is done.

### Verify (all three)

```sh
curl -fsS HOST/ready && echo ready
oa system info                                    # database and storage "ok"
oa repo list | wc -l                              # the number of repositories you expect
oa search --limit 5 <a package name you know>
for f in <KNOWN FILE 1> <KNOWN FILE 2>; do       # a known artifact per important format, compared with a recorded checksum
  curl -fsS -H "Authorization: Bearer <TOKEN>" "HOST/repos/raw/<REPO>/$f" | sha256sum
done
```

Then use the native client of each format that matters: `npm view <package> versions`, `mvn dependency:get`, `apt-get update`, `docker pull`. Served metadata (packument, `maven-metadata.xml`, `Packages`) is regenerated from the rows, so it must list the same versions as before; compare against a saved copy if you have one. Check the application log for `restore completed` (backup) or the rebuild report (rebuild), and the [Prometheus metrics](monitoring.md) for rising error rates.

## The blob store is lost or damaged

The database is intact but objects are missing (a deleted bucket, a failed disk, a corrupted object). Downloads of the affected files fail, and `/ready` may report `storage unavailable`.

| Option | Gets back | RPO | RTO |
|---|---|---|---|
| **A. Replica** (`storage.replica`) | Everything that had been replicated | The replication lag | Reads immediately; full repair at copy speed |
| **B. Bucket versioning, snapshot or provider backup** | Whatever the snapshot holds | The snapshot interval | The provider's restore time |
| **C. Re-publish** | Hosted artifacts from CI; proxy repositories refill from upstream by themselves | Whatever CI can rebuild | Depends on the pipeline |

### A. Replica

With [replication](replication.md) configured the server already serves a missing object from the replica (`readFallback: notfound` covers a lost object or bucket, `error` also covers an outage), and every blob a client reads from the replica is copied back to the primary. Put the primary back and repair all of it, not just what clients happen to read:

1. Point the primary at a new, empty location (a new bucket or directory) and keep `storage.replica` as it is; the keys are content hashes, so nothing in the database changes. If the primary is gone for good, make the replica the primary instead ([recovering from the loss of the primary store](replication.md#recovering-from-the-loss-of-the-primary-store)).
2. Start a verification and repair pass and read the result:

   ```sh
   curl -fsS -X POST -H "Authorization: Bearer <ADMIN_TOKEN>" HOST/api/v1/admin/storage/replication/verify   # 202; 409 if one is already running
   curl -fsS -H "Authorization: Bearer <ADMIN_TOKEN>" HOST/api/v1/admin/storage/replication | jq '{backlog_copy, backlog_delete, oldest_pending_age_seconds, failing, verify_running, last_verify}'
   ```

   `last_verify` counts the blobs it checked, found missing on either side (`missing_primary`, `missing_replica`), `repaired`, and `lost`.
3. Wait until `verify_running` is false, the backlogs are empty and `last_verify.missing_primary` equals `last_verify.repaired`.

A blob that the database references and that is on neither side is counted in `lost`: that file has to come from option B or C. A delete is replicated too, after the [deletion grace period](storage.md#deferred-blob-deletion) (24 h by default), so a replica is a second copy and not a way back from an accidental delete.

### B. Versioning, snapshot or provider backup

Restore the bucket (or the filesystem volume) to the newest snapshot, restart the application, and run the replication verification (if you have a replica) or the checks below to find what the snapshot lacks. Objects uploaded after the snapshot are missing: their rows exist, their bytes do not. Re-publish those artifacts.

### C. Re-publish

Find out which artifacts are affected, then publish them again. A row whose bytes are gone serves an error until then. Where the format refuses to publish a version that exists, delete the damaged item first: it moves to the [trash](trash.md), and publishing the same coordinates is allowed while the old copy is there. Purge the damaged copy afterwards (`oa trash purge <kind> <id> --yes`), since restoring it later would conflict. A repository with `immutable_releases` refuses to delete or overwrite a released version unless an admin passes `?force=true` ([immutable releases](trash.md#immutable-releases)).

### Verify

```sh
curl -fsS HOST/ready && echo ready
oa system info
curl -fsS -H "Authorization: Bearer <TOKEN>" HOST/repos/raw/<REPO>/<KNOWN FILE> | sha256sum   # compare with a recorded checksum
curl -fsS -H "Authorization: Bearer <ADMIN_TOKEN>" HOST/api/v1/admin/storage/replication | jq '{enabled, backlog_copy, backlog_delete, oldest_pending_age_seconds, failing, last_verify}'
```

and, with replication on, the `openartifact_replication_*` metrics: backlog and failures at zero.

## Everything is lost

The site, the cluster, and the volumes. What survives is whatever you kept off site. Rebuild in this order, and stop at the first step that fails:

1. **Secrets and configuration:** `OPENARTIFACT_SECRET_KEY`, `AUTH_HMAC_KEY`, SAML key pair, storage credentials, `BASE_URL`, your Helm values or compose files. Without the secret key the encrypted values in a restored database cannot be read.
2. **Blob store:** from the replica backend (make it the new primary: swap the `storage` blocks as [replication](replication.md#recovering-from-the-loss-of-the-primary-store) describes; the layout is the same on every backend), or from a provider snapshot. Verify that the bucket opens and holds objects. Sidecar records are blobs like the others, so they are on the replica too.
3. **Database:** [point-in-time recovery](postgres-pitr.md) from the archive object store into the new cluster, or the newest dump or backup. If there is none, [rebuild from storage](#c-rebuild-from-storage-alone) from the blob store you just restored.
4. **Application:** deploy the same version as the backup (or newer: migrations run forward), with the same `BASE_URL`, then run the checks above.
5. **Declarative configuration:** `oa apply -f exported/ -R` if the database did not come back with it.

RTO is the sum of the steps, dominated by the data copy; the recovery point is the older of the database and blob store copies, and [what a recovery leaves behind](postgres-pitr.md#what-a-recovery-leaves-behind) applies to the gap between them.

## Drills

Recovery procedures that have never run do not work. Quarterly, and after any change to backups or storage:

| Drill | How | Pass when |
|---|---|---|
| Database | A [restore drill](postgres-pitr.md#restore-drills) into a scratch cluster | The cluster is healthy and a known repository and package are present; the time is your measured RTO |
| Rebuild | `openartifact rebuild --dry-run` against a copy of the blob store and an empty database | The report matches the repository and package counts of production |
| Replica | Delete one object from the primary of a staging instance, download it, run the verification | The download succeeds and the verification repairs the primary |
| Trash | Delete a test repository, restore it | Its files download again |

## What is tested

| Claim | Test |
|---|---|
| WAL archiving, base backup and point-in-time recovery of the OpenArtifact database bring back repositories, rows written after the base backup, and tokens, and the artifacts download with their original checksum | `scripts/pitr-smoke.sh` ([postgres-pitr.md](postgres-pitr.md#how-this-was-tested)) |
| A deleted repository is restored from the trash with its content | `TestTrashPolicies_RepositoryDeleteConfirmationAndTrash`, `TestTrashRepositoryHidesAndRestores` |
| Rebuilding from the blob store after the database was wiped brings repositories and packages back for every covered format, and the served npm packument, Maven metadata and Debian `Packages` match the pre-wipe output | the integration tests of [rebuild from storage](rebuild-from-storage.md) |
| An upload reaches the replica, a missing primary blob is served from the replica and repaired by verification, and a failed copy is retried | the integration tests of [replication](replication.md) |
