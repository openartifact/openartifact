<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# PostgreSQL point-in-time recovery

OpenArtifact keeps every record (repositories, packages, users, tokens, permissions, settings) in PostgreSQL and the bytes in the [blob store](storage.md). The database is the part with the most value per byte and the only one that changes in place, so it is the part to protect continuously: archive its write-ahead log (WAL), take base backups, and you can bring it back to any moment inside the archive, for example the minute before someone deleted a project.

This page sets that up for a database run by [CloudNativePG](https://cloudnative-pg.io) (CNPG) on Kubernetes, which is what the official Helm chart can provision (`cnpg.enabled=true`), and says what to do for any other PostgreSQL. The whole procedure is part of the [disaster recovery runbook](disaster-recovery.md).

What point-in-time recovery (PITR) does and does not cover:

| Covered | Not covered |
|---|---|
| Every table, to the second: [trash](trash.md) contents, users, tokens, permission targets, webhooks, SSO providers, repository settings | The blob store. Bytes deleted after the recovery target (a [trash](trash.md) purge, a cleanup policy, an orphan sweep) stay deleted, and the restored rows that point at them answer `404`. See [what a recovery leaves behind](#what-a-recovery-leaves-behind) |
| Users, sessions and API tokens, which [rebuilding from storage](rebuild-from-storage.md) cannot bring back | `OPENARTIFACT_SECRET_KEY`, which encrypts stored credentials and lives in a Kubernetes Secret, not in the database |

## Versions

The manifests below were checked against the CloudNativePG 1.30.1 and Barman Cloud plugin 0.15.1 custom resource definitions (see [how this was tested](#how-this-was-tested)). CloudNativePG deprecated its built-in object store backup (`barmanObjectStore`) in 1.26 in favor of the [Barman Cloud plugin](https://cloudnative-pg.io/plugin-barman-cloud/), so everything here uses the plugin. It needs the operator, [cert-manager](https://cert-manager.io) and the plugin installed in the cluster; follow the plugin's [installation guide](https://cloudnative-pg.io/plugin-barman-cloud/docs/installation/).

## Where the backups go

Use an object store that is not the one holding your blobs, and preferably another account, region or provider: the point of the archive is that it survives the loss of everything else. Create the credentials, then an `ObjectStore`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: openartifact-db-backup-s3
  namespace: openartifact
type: Opaque
stringData:
  ACCESS_KEY_ID: <ACCESS_KEY_ID>
  ACCESS_SECRET_KEY: <SECRET_ACCESS_KEY>
---
apiVersion: barmancloud.cnpg.io/v1
kind: ObjectStore
metadata:
  name: openartifact-db
  namespace: openartifact
spec:
  # Base backups and WAL older than this are deleted by the plugin.
  retentionPolicy: "30d"
  configuration:
    destinationPath: s3://openartifact-db-backups/
    # endpointURL: https://s3.example.com   # S3-compatible stores (Garage, SeaweedFS, MinIO...)
    s3Credentials:
      accessKeyId:
        name: openartifact-db-backup-s3
        key: ACCESS_KEY_ID
      secretAccessKey:
        name: openartifact-db-backup-s3
        key: ACCESS_SECRET_KEY
    wal:
      compression: gzip
      maxParallel: 4
    data:
      compression: gzip
```

`retentionPolicy` is the recovery window: you can recover to any time from the oldest retained base backup up to the newest archived WAL. Azure and GCS stores take their own credential blocks, see the plugin's [object store providers](https://cloudnative-pg.io/plugin-barman-cloud/docs/object_stores/). Leave `spec.configuration.serverName` empty: the archive name is set on the cluster (below).

## Archive WAL and take base backups

### With the Helm chart

The chart renders the CNPG `Cluster` and merges `cnpg.extraSpec` into its `spec`, so the WAL archiver is a values change:

```yaml
cnpg:
  enabled: true
  instances: 3
  extraSpec:
    plugins:
      - name: barman-cloud.cloudnative-pg.io
        isWALArchiver: true
        parameters:
          barmanObjectName: openartifact-db
```

With the release name `openartifact` the cluster is named `openartifact-db` (`<release>-db`, shortened to 50 characters), its read-write service is `openartifact-db-rw` and the application credentials are in the Secret `openartifact-db-app`. The names used below follow from that; replace them if your release differs.

The chart has no template for the `ObjectStore`, its credentials or the `ScheduledBackup`: apply them yourself, next to the release:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata:
  name: openartifact-db-daily
  namespace: openartifact
spec:
  # Six fields, the first is seconds: every day at 03:00:00.
  schedule: "0 0 3 * * *"
  immediate: true
  backupOwnerReference: self
  method: plugin
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
  cluster:
    name: openartifact-db
```

Base backups are hot and full (no incremental copies on an object store); once a day to once a week is usual, and the interval is what bounds the [recovery time](#rpo-and-rto). An on-demand backup:

```sh
kubectl cnpg backup openartifact-db -n openartifact \
  --method=plugin --plugin-name=barman-cloud.cloudnative-pg.io
```

### Check that it works

Do this once after setting up, and put the same checks in your monitoring:

```sh
# WAL is being archived, and the last backup worked
kubectl -n openartifact get cluster openartifact-db \
  -o jsonpath='{range .status.conditions[?(@.type=="ContinuousArchiving")]}{.type}={.status} ({.reason}){"\n"}{end}{range .status.conditions[?(@.type=="LastBackupSucceeded")]}{.type}={.status}{"\n"}{end}'

# the window you can recover to: first point, and newest base backup
kubectl -n openartifact get cluster openartifact-db \
  -o jsonpath='first recoverability point: {.status.firstRecoverabilityPoint}{"\n"}last successful backup: {.status.lastSuccessfulBackup}{"\n"}'

kubectl -n openartifact get backup
kubectl cnpg status openartifact-db -n openartifact
```

`ContinuousArchiving=True` and a `firstRecoverabilityPoint` that is not empty are the signs of a healthy archive. An empty `firstRecoverabilityPoint` means there is no base backup yet: WAL alone cannot restore anything.

## Restore

Recovery in CNPG is never in place: it creates a new cluster from the archive. With the application's cluster that means: stop writing to the old one, remove it, and create a cluster with the same name that bootstraps from the archive, so the service and Secret names the chart's Deployment uses stay valid.

1. **Choose the target.** An RFC 3339 time in UTC (`2026-10-21T09:00:00Z`), strictly before the damage and not older than `firstRecoverabilityPoint`. Recovery stops before the first transaction committed after the target and fails if the archive holds none, so the target must lie before the newest archived WAL; leave `recoveryTarget` out to replay everything up to the last archived WAL. Other targets are `targetLSN`, `targetXID` (with `backupID`), `targetName` and `targetImmediate`.
2. **Stop the application,** so nothing writes to a database you are about to replace:

   ```sh
   kubectl -n openartifact scale deployment/openartifact --replicas=0
   ```

3. **Remove the damaged cluster.** This deletes its volumes; the archive in the object store is untouched.

   ```sh
   kubectl -n openartifact delete cluster openartifact-db
   ```

4. **Create the recovered cluster.** Two ways, with the same result:

   - [With the chart](#restore-with-the-chart) (Helm 3, or Helm 4 with `--server-side=false`).
   - [With a standalone `Cluster`](#restore-with-a-standalone-cluster), which works with any Helm version.

5. **Wait** for the recovery job to finish and the cluster to become healthy, then start the application:

   ```sh
   kubectl -n openartifact get cluster openartifact-db -w      # "Cluster in healthy state"
   kubectl -n openartifact rollout restart deployment/openartifact
   kubectl -n openartifact scale deployment/openartifact --replicas=<N>
   ```

   The operator creates a fresh `openartifact-db-app` Secret for the recovered cluster, so restart the pods rather than waiting for them to notice.
6. **Verify** with the [checks below](#verify-the-recovered-instance).
7. **Take a base backup straight away** (command above): the recovered cluster archives under its new `serverName`, which has no base backup yet.

The recovered cluster must not write into the archive path it was restored from: the operator refuses to start it (`WAL archive check failed ... Expected empty archive`), and bypassing that check can destroy the archive. That is why the `plugins` block of the recovered cluster names a **new** `serverName` (`openartifact-db-r1`), while `externalClusters` points at the old one (`openartifact-db`). Use a new suffix at every restore (`-r2`, ...) and name the latest one as the source of the next restore.

### Restore with the chart

`cnpg.extraSpec` is merged over the rendered spec, which contains `bootstrap.initdb`. A cluster takes only one bootstrap method, so the chart's `initdb` has to go: set it to `null`.

```yaml
# values-restore.yaml
cnpg:
  enabled: true
  instances: 3
  extraSpec:
    bootstrap:
      initdb: null            # drop the chart's initdb, recovery replaces it
      recovery:
        source: origin
        database: openartifact
        owner: openartifact
        recoveryTarget:
          targetTime: "2026-10-21T09:00:00Z"   # omit recoveryTarget to replay to the end of the archive
    externalClusters:
      - name: origin
        plugin:
          name: barman-cloud.cloudnative-pg.io
          parameters:
            barmanObjectName: openartifact-db
            serverName: openartifact-db        # the cluster that wrote the archive
    plugins:
      - name: barman-cloud.cloudnative-pg.io
        isWALArchiver: true
        parameters:
          barmanObjectName: openartifact-db
          serverName: openartifact-db-r1       # new name: the restored cluster must not write into the old archive
```

```sh
helm upgrade openartifact <chart> -n openartifact -f values.yaml -f values-restore.yaml --server-side=false
```

`database` and `owner` must be the ones the chart uses (`cnpg.database`, `cnpg.owner`, both `openartifact` by default); they keep the name and user the Deployment connects with.

`initdb: null` works with Helm 3 and with Helm 4 only when it applies client-side (`--server-side=false`). Helm 4 applies server-side by default, and the API server then rejects the manifest with `spec.bootstrap.initdb ... must be of type object: "null"`. If you cannot pass the flag, use the standalone cluster below. The chart should grow a setting that omits `initdb` instead (see [chart gaps](#chart-gaps)).

After a successful restore, keep the `plugins` block (with the new `serverName`) in your regular values and stop passing `values-restore.yaml`: the operator reads `bootstrap` only when it creates a cluster.

### Restore with a standalone Cluster

Switch the release to an externally managed database, then apply the recovery `Cluster` yourself. The Deployment keeps connecting to the same service and Secret.

```yaml
# values.yaml (changed once)
cnpg:
  enabled: false           # the Cluster is managed outside the chart
database:
  sslMode: require
  external:
    host: openartifact-db-rw
    name: openartifact
    user: openartifact
    existingSecret: openartifact-db-app
    existingSecretPasswordKey: password
```

```yaml
# cluster-restore.yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: openartifact-db
  namespace: openartifact
spec:
  instances: 3
  storage:
    size: 8Gi
  bootstrap:
    recovery:
      source: origin
      database: openartifact
      owner: openartifact
      recoveryTarget:
        targetTime: "2026-10-21T09:00:00Z"
  externalClusters:
    - name: origin
      plugin:
        name: barman-cloud.cloudnative-pg.io
        parameters:
          barmanObjectName: openartifact-db
          serverName: openartifact-db
  plugins:
    - name: barman-cloud.cloudnative-pg.io
      isWALArchiver: true
      parameters:
        barmanObjectName: openartifact-db
        serverName: openartifact-db-r1
```

```sh
helm upgrade openartifact <chart> -n openartifact -f values.yaml     # removes the chart-managed Cluster, if any is left
kubectl -n openartifact apply -f cluster-restore.yaml
```

The `Cluster` is yours from then on: size, instances, resources and parameters are in your manifest, not in the chart values.

### Verify the recovered instance

```sh
# the database came out of recovery and is at the time you asked for
kubectl -n openartifact exec openartifact-db-1 -c postgres -- \
  psql -U postgres -d openartifact -Atc "select pg_is_in_recovery(), now()"
kubectl -n openartifact logs openartifact-db-1 -c postgres | grep -E "recovery stopping|redo done"

# the application sees it
curl -fsS HOST/ready
oa system info                    # database and storage status
oa repo list
curl -fsS -H "Authorization: Bearer <TOKEN>" "HOST/repos/raw/<REPO>/<KNOWN FILE>" | sha256sum
```

The log line `recovery stopping before commit of transaction ..., time ...` names the first transaction that was left out, which should be just after your target. Compare a download against a checksum you recorded earlier, and look at the newest package in each important repository: it tells you how much of the last minutes the recovery left out.

## RPO and RTO

| | Value | What sets it |
|---|---|---|
| RPO | 5 minutes or less (CloudNativePG's documented figure with a WAL archive) | PostgreSQL archives each finished WAL segment and forces a segment switch every `archive_timeout`, which CNPG sets to 5 minutes; a busy server fills segments sooner. Lower it through `cnpg.extraSpec.postgresql.parameters` to tighten the bound, at the cost of more, smaller files. The transactions not yet archived are lost only if every instance and its volume are lost too |
| RTO | base backup restore plus WAL replay plus start-up | The size of the newest base backup before the target, and the amount of WAL between that backup and the target. More frequent base backups shorten the replay; `wal.maxParallel` on the `ObjectStore` fetches WAL in parallel. The database holds only metadata, so it is small compared with the blobs; measure it with a [restore drill](#restore-drills) instead of trusting a guess |

A healthy multi-instance cluster (`instances: 3`) survives the loss of a node without any restore; it does not protect against a bad delete, a bad migration or a corrupted volume, which replicate to every instance. That is what the archive is for.

## What a recovery leaves behind

Blob store and database drift apart between the recovery target and the moment you recover:

- **Uploads after the target** exist as bytes in the blob store, with no row. They do not appear in any listing and cost space until you remove them. If [rebuild from storage](rebuild-from-storage.md) is on, `openartifact rebuild` re-creates their rows from the sidecar records; otherwise publish them again (publishing the same bytes stores them once).
- **Deletions after the target** (a trash purge, a [cleanup policy](../repositories/cleanup-and-quotas.md#cleanup-policies), a repository delete with retention `0`) removed rows, and their bytes follow once the [deletion grace period](storage.md#deferred-blob-deletion) (24 hours by default) has passed. A recovery inside that period finds the bytes still in place; after it, the restored rows reference bytes that are gone and a download of such an item fails. Restore the bytes from bucket versioning or a snapshot of the blob bucket, or publish the artifact again. A [replica](replication.md) is no help once the deletion grace period (24 hours by default) has passed, because it applies deletions too. Choosing a target close to the incident, and recovering quickly, keeps this window small.
- **Soft-deleted rows** come back exactly as they were at the target, trashed or not.

The server's own background work (the trash purge, cleanup policies, the orphan blob collector) starts from the restored rows as soon as the application is back, and can delete more bytes. When the target is far in the past, or you are unsure how much was lost, recover into a scratch cluster first (another name, application scaled to zero, as in a [restore drill](#restore-drills)), compare it with the blob store, and only then cut over.

## Restore drills

An untested backup is a hope. Once per quarter, and after every change to the backup setup:

1. Restore the newest archive into a scratch namespace with a `Cluster` like the one above (another name, `recoveryTarget` set a few minutes in the past), without the `plugins` block so it does not archive.
2. Time it from `kubectl apply` to `Cluster in healthy state`: that is your measured RTO for the current database size.
3. Point a throwaway OpenArtifact at it (`DB_HOST=<scratch>-rw`, a copy of the blob store or none) and run the verification checks.
4. Delete the namespace.

## Without CloudNativePG

Any PostgreSQL can do the same with the tools that fit your platform: [pgBackRest](https://pgbackrest.org), [WAL-G](https://github.com/wal-g/wal-g), [Barman](https://pgbarman.org), a managed service's own point-in-time restore (RDS, Cloud SQL, Azure Database for PostgreSQL), or plain `archive_command` plus `pg_basebackup`. The requirements are the same: `wal_level = replica` or higher, `archive_mode = on` with a working `archive_command`, a base backup, and a place for both that is not the database server.

On Docker Compose with the bundled `postgres` service, if a nightly dump is enough (RPO of one day), a logical backup needs no setup:

```sh
# dump
docker compose -f docker/docker-compose.yml exec -T postgres \
  pg_dump -U postgres -Fc artifact_repo > artifact_repo-$(date +%F).dump

# restore into an empty database, with the application stopped
docker compose -f docker/docker-compose.yml stop openartifact
docker compose -f docker/docker-compose.yml exec -T postgres dropdb   -U postgres --if-exists artifact_repo
docker compose -f docker/docker-compose.yml exec -T postgres createdb -U postgres artifact_repo
docker compose -f docker/docker-compose.yml exec -T postgres \
  pg_restore -U postgres -d artifact_repo --no-owner --exit-on-error < artifact_repo-2026-10-21.dump
docker compose -f docker/docker-compose.yml start openartifact
```

Copy the dump off the host. A dump is a snapshot: everything written after it is gone unless you fill the gap with [rebuild from storage](rebuild-from-storage.md). For continuous archiving on Compose, mount an archive directory or run a WAL tool as a second container; the same `archive_command`/`restore_command` mechanics as in [the test script](#how-this-was-tested) apply.

The application's own [backup](backup.md) is a third option, with a different trade: it is portable between PostgreSQL versions and storage backends and goes through the application's create paths, but it re-generates record IDs, leaves out trashed data, the audit log and a few other tables, and has no point in time.

## How this was tested

`scripts/pitr-smoke.sh` runs the recovery with the tools CloudNativePG runs in its pods, against real binaries: PostgreSQL from the CloudNativePG operand image, WAL archiving with `barman-cloud-wal-archive`, a base backup with `barman-cloud-backup`, an S3 object store (Garage), and a recovery with `barman-cloud-restore`, a `restore_command` calling `barman-cloud-wal-restore`, and `recovery_target_time`. In between it runs a real OpenArtifact server on the database:

1. start the server, create a token, a repository and an artifact, take a base backup, upload a second artifact;
2. note the time, then wipe every repository from the database (`truncate repositories cascade`), switch the WAL segment and wait for it to be archived, and destroy the database;
3. restore into a fresh instance with the target time, start the server on it, and check that the repository is back, the token still authorizes requests, and both artifacts download with their original SHA-256.

```sh
go build -o /tmp/openartifact ./cmd/server
OA_BIN=/tmp/openartifact scripts/pitr-smoke.sh
```

It needs a container engine, `curl`, `jq` and `openssl`, and pulls two images. It does not run Kubernetes: the `Cluster`, `ObjectStore` and `ScheduledBackup` resources are the operator's wrapper around these commands, and what could be checked without a cluster was checked another way: the manifests on this page were applied with strict field validation to a Kubernetes API server with the CloudNativePG and plugin CRDs installed, and the chart was rendered and installed with `helm` against it, in both the backup and the restore configuration. A full run on a real cluster, with the operator and the plugin, is the [restore drill](#restore-drills) above.

## Chart gaps

The official chart provides the `Cluster` only. The following is missing or fragile and has to be done outside the chart, as described above:

| Missing | Today |
|---|---|
| A bootstrap switch (`initdb` or `recovery`) that omits `initdb` | `bootstrap.initdb: null` through `cnpg.extraSpec`, which Helm 4's default server-side apply rejects |
| `externalClusters` and recovery target values | `cnpg.extraSpec` |
| An `ObjectStore`, its credentials Secret and the `plugins` entry | Applied by hand; only the `plugins` entry fits `cnpg.extraSpec` |
| A `ScheduledBackup` | Applied by hand |
| Extra manifests (`extraObjects`) | Not available, so none of the above can ship with the release |
| `cnpg.superuserSecret`, `cnpg.enableSuperuserAccess` and an explicit app-credentials Secret, to keep the passwords stable across a restore | `cnpg.extraSpec` |
