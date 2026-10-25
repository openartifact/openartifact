<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Configuration

OpenArtifact is configured with defaults, an optional YAML file and environment variables; every setting has an environment override.

Full annotated references: [`openartifact.example.yaml`](../../openartifact.example.yaml) (YAML) and [`.env.example`](../../.env.example) (environment). Compose credentials are in [`docker/.env.example`](../../docker/.env.example) ([installation](installation.md#docker-compose)).

## Load order

1. Built-in defaults.
2. YAML file: `-config path`, or `OPENARTIFACT_CONFIG` (the flag wins).
3. Environment variables, which take precedence over the file.

```sh
openartifact verify openartifact.yaml    # validate a config file (path optional if -config / OPENARTIFACT_CONFIG is set)
openartifact -config openartifact.yaml   # start with a config file
openartifact -migrate                    # apply database migrations and exit
openartifact -repair-gomod               # relink Go module zips damaged on <= 0.4.1, then exit (see upgrading)
```

- `verify` loads the file with the current environment applied and checks: port 1-65535, non-empty database driver, storage type and log level, TLS cert/key present when TLS is enabled, and `mcp.transport` equal to `http`. The YAML is parsed non-strictly: **unknown or misspelled keys are ignored**, so `verify` does not catch typos.
- Read and write timeouts default to `0` (unlimited) so large uploads and downloads are never truncated. Only `readHeaderTimeout` (default 20 s) bounds slow header reads.
- Migrations run at startup unless `MIGRATE_ON_START=false`; see [upgrading](upgrading.md#database-migrations).
- **Exit status.** The process exits `0` after a clean shutdown (`SIGINT`/`SIGTERM`, or `-migrate` and `verify` succeeding) and `1` when it cannot start or stops serving: invalid configuration or `OPENARTIFACT_SECRET_KEY`, no database connection, a failed migration or bootstrap, an unusable blob store, a listen address that is in use, an unreadable or invalid TLS certificate. The cause is logged as an `error` line (for example `failed to start server`) before the exit, so a container runtime or systemd sees the failure and restarts or reports it instead of treating the stopped process as a success.
- Boolean variables are parsed with Go's `strconv.ParseBool`: `1`, `t`, `T`, `TRUE`, `true`, `True`, `0`, `f`, `F`, `FALSE`, `false`, `False`. `yes`/`no`/`on`/`off` are invalid.
- An invalid integer, boolean or duration is ignored: a `WARN: ignoring invalid value for <KEY>` line is printed to stderr (without the value) and the previous setting stays.
- Integers are plain decimal. Durations use Go syntax (`5m`, `90s`). List variables are comma-separated and trimmed; an empty list value does not clear a list set in the file.
- With no `AUTH_HMAC_KEY` the server generates a random key once and persists it in `system_settings`, so it survives restarts and is shared by replicas.

## Environment-only variables

These are read directly from the environment, have no YAML key and are not in `internal/config`:

| Variable | Meaning |
|---|---|
| `OPENARTIFACT_SECRET_KEY` | Secret-encryption key (any string of at least 16 bytes; hashed to an AES-256-GCM key). Unset: stored credentials (including the generated SAML SP key) are kept in plaintext and a warning is logged. A shorter key aborts startup. Changing or losing it makes existing `enc:v1:` ciphertexts unreadable. Protects webhook secrets, upstream passwords, SSO client secrets, AI keys and storage credentials stored in the database |
| `METRICS_AUTH_TOKEN` | Bearer token required for `GET /metrics`. See [monitoring](../operations/monitoring.md) |
| `METRICS_ALLOW_ANONYMOUS` | Exactly `true` serves `/metrics` without a token. With neither variable set, `/metrics` answers `401` and a warning is logged |
| `MIGRATE_ON_START` | Exactly `false` (lowercase) skips automatic migrations; any other value, including `0` or `FALSE`, keeps them on |
| `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS` | `true` (case-insensitive) lets webhooks and proxy upstreams target private and loopback addresses; cloud metadata addresses (`169.254.0.0/16`, `fd00:ec2::/16`) stay blocked. See [webhooks](../operations/webhooks.md) and [proxy repositories](../repositories/hosted-proxy-group.md) |
| `OPENARTIFACT_CONFIG` | Default for the `-config` flag |

`cmd/resetpw` has its own variables (`OPENARTIFACT_RESET_PASSWORD`, `OPENARTIFACT_RESET_USERNAME`); see [installation](installation.md#reset-the-admin-password). The S3 backend without `STORAGE_ACCESS_KEY`/`STORAGE_SECRET_KEY` uses the standard AWS SDK credential chain (`AWS_*` variables, shared config, instance role).

## Settings

YAML keys are nested under the section named in each heading. Secret values (marked **secret**) belong in environment variables or a protected file.

### Top level

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `BASE_URL` | `baseUrl` | `http://localhost:8080` | Public URL; used for generated links and SSO callbacks (see [reverse proxy](installation.md#behind-a-reverse-proxy-and-tls)) |

### Server (`server`)

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `SERVER_HOST` | `host` | `0.0.0.0` | Listen address |
| `SERVER_PORT` | `port` | `8080` | Listen port |
| `SERVER_READ_TIMEOUT` | `readTimeout` | `0` | Seconds for a whole request; `0` = unlimited |
| `SERVER_WRITE_TIMEOUT` | `writeTimeout` | `0` | Seconds for writing a response; `0` = unlimited |
| `SERVER_READ_HEADER_TIMEOUT` | `readHeaderTimeout` | `20` | Seconds to read request headers |
| `SERVER_MAX_BODY_BYTES` | `maxBodyBytes` | `17179869184` | Maximum request body (bytes) accepted by any endpoint; `0` = unlimited. Format handlers enforce lower limits of their own |
| `SERVER_SHUTDOWN_TIMEOUT` | `shutdownTimeout` | `30` | Seconds allowed for graceful shutdown |
| `SERVER_TLS_ENABLED` | `tlsEnabled` | `false` | Serve HTTPS; requires cert and key; sends HSTS |
| `SERVER_TLS_CERT` | `tlsCert` | empty | TLS certificate file (PEM) |
| `SERVER_TLS_KEY` | `tlsKey` | empty | TLS private key file (PEM) |
| `CORS_ORIGINS` | `corsOrigins` | none | Allowed browser origins |
| `OPENARTIFACT_INSTANCE_ID` | `instanceId` | empty | Instance name reported by `/health` (omitted when empty; the hostname is never disclosed) |
| `OPENARTIFACT_IP_ALLOWLIST` | `ipAllowlist` | empty (allow all) | CIDRs allowed to connect |
| `OPENARTIFACT_RATE_LIMIT_RPS` | `rateLimitRps` | `0` (off) | Requests per second per client IP |
| `OPENARTIFACT_TRUSTED_PROXIES` | `trustedProxies` | none | CIDRs of reverse proxies whose `X-Forwarded-For`/`X-Real-IP` are honored |
| `OPENARTIFACT_PPROF_ENABLED` | `pprofEnabled` | `false` | Admin-only Go profiler at `/api/v1/admin/pprof/`; see [performance](../operations/performance.md) |

### Database (`database`)

PostgreSQL is the only supported database.

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `DB_DRIVER` | `driver` | `postgres` | Must be non-empty; the server always connects to PostgreSQL |
| `DB_HOST` | `host` | `localhost` | Host |
| `DB_PORT` | `port` | `5432` | Port |
| `DB_USER` | `user` | `postgres` | User |
| `DB_PASSWORD` | `password` | empty | Password (**secret**) |
| `DB_NAME` | `database` | `artifact_repo` | Database name |
| `DB_SSLMODE` | `sslMode` | `require` | PostgreSQL `sslmode`; `disable` only for local development |
| `DB_MAX_OPEN_CONNS` | `maxOpenConns` | `25` | Connection pool size; `0` or at least `2` (a blob-lock transaction pins one connection and its callback needs another), and at least `4` while `tiering.enabled` is true (the mover pins one more for its pass lock) |
| `DB_MAX_IDLE_CONNS` | `maxIdleConns` | `5` | Warm connections kept by the pgx pool |
| `DB_CONN_MAX_LIFETIME_MINUTES` | `connMaxLifetimeMinutes` | `30` | Maximum connection lifetime |
| `DB_CONNECT_TIMEOUT` | `connectTimeout` | `60s` | How long startup retries the initial database connection (capped backoff, each wait logged at info) before exiting; `0` fails at the first error. Replaces the Helm chart's wait-for-database init container |
| `DB_AUTH_CACHE_TTL_MS` | `authCacheTtlMs` | `2000` | In-memory cache of per-request auth lookups; changes made on this instance apply at once, other replicas see them within this time; `0` disables |
| `DB_OPTIONS` | `options` | none | Extra libpq connection parameters; env form is comma-separated `key=value` pairs (`DB_OPTIONS=connect_timeout=5,application_name=oa`), merged over YAML keys |

### Workers (`workers`)

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_METADATA_REGEN_CONCURRENCY` | `workers.metadataRegenConcurrency` | `4` | Max concurrent metadata regenerations (npm, maven, deb, rpm share it); each pins about two DB connections |
| `OPENARTIFACT_DOCKER_GC_INTERVAL` | `workers.dockerGcInterval` | `1h` | Pause between Docker registry garbage-collection passes over all docker repositories; `0` disables the schedule (an admin can still run it through the API). See [Docker registry GC](../formats/docker.md#garbage-collection) |
| `OPENARTIFACT_DOCKER_GC_GRACE_PERIOD` | `workers.dockerGcGracePeriod` | `24h` | How long an untagged manifest or an unreferenced blob must sit idle before GC may delete it; keep it longer than your slowest push and any digest-pinned reference to an untagged image. Must not be negative |
| `OPENARTIFACT_PROXY_DOCKER_DIGEST_TTL` | `workers.proxyDockerDigestTtl` | `720h` | How long a Docker proxy keeps a manifest or blob addressed by digest after its last pull; the proxy cache worker then evicts it (and the blob once no other entry references it) and the next pull fetches it from the upstream again. A pull in the second half of the period renews it. `0` keeps them until an admin purge or the repository's removal. Must not be negative. See [Docker proxy caching](../formats/docker.md#proxy-and-group-repositories) |
| `OPENARTIFACT_DOCKER_UPLOAD_TTL` | `workers.dockerUploadTtl` | `24h` | Age after which an unfinished Docker blob upload session and its staged file are removed; `0` disables the sweep. Must not be negative |
| `OPENARTIFACT_DOCKER_UPLOAD_DIR` | `workers.dockerUploadDir` | `/tmp/openartifact/docker-uploads` | Directory where in-progress Docker blob uploads are staged; use a persistent volume that all replicas handling one upload share |
| `OPENARTIFACT_WEBHOOK_DELIVERY_RETENTION_DAYS` | `workers.webhookDeliveryRetentionDays` | `30` | Days finished (success or failed) webhook delivery records are kept; the hourly purge deletes older ones. `0` keeps them forever. Must not be negative |

### Documentation links (`docs`)

| Environment variable | YAML key | Default | Description |
|---|---|---|---|
| `OPENARTIFACT_DOCS_BASE_URL` | `docs.baseUrl` | `https://openartifact.github.io/` | Root of the published documentation the web UI links to. The server appends `vX.Y/` for a release build (`MAJOR.MINOR.PATCH`) and `latest/` otherwise, and reports the result as `docs_url` in `GET /api/v1/system/info`. Point it at a mirror for air-gapped installs; empty hides every documentation link |

### Branding (`branding`)

White-label name, logos, colour and links; administrators can override each value at runtime (Administration > Branding), and a runtime value wins. Details, image rules and the attribution that cannot be hidden: [White-label branding](../operations/branding.md).

| Environment variable | YAML key | Default | Description |
|---|---|---|---|
| `OPENARTIFACT_BRANDING_PRODUCT_NAME` | `branding.productName` | empty (`OpenArtifact`) | Product name shown in the title, sidebar, login page and installed-app name; 1 to 64 characters |
| `OPENARTIFACT_BRANDING_LOGO_LIGHT` | `branding.logoLight` | empty | Path of a PNG or SVG logo (max 512 KiB) for light surfaces; used for both themes when `logoDark` is unset |
| `OPENARTIFACT_BRANDING_LOGO_DARK` | `branding.logoDark` | empty | Path of a PNG or SVG logo for dark surfaces |
| `OPENARTIFACT_BRANDING_FAVICON` | `branding.favicon` | empty | Path of a PNG or SVG favicon (at least 192x192 for an installable app) |
| `OPENARTIFACT_BRANDING_PRIMARY_COLOR` | `branding.primaryColor` | empty | Accent colour as `#rrggbb`; a contrast-adjusted variant is derived per theme |
| `OPENARTIFACT_BRANDING_LOGIN_MESSAGE` | `branding.loginMessage` | empty | Text shown on the login page (up to 2000 bytes; bold, italic, code and links only) |
| `OPENARTIFACT_BRANDING_SUPPORT_URL` | `branding.supportUrl` | empty | `http(s)` or `mailto:` link shown in the About dialog |
| `OPENARTIFACT_BRANDING_SOURCE_URL` | `branding.sourceUrl` | `https://github.com/openartifact/openartifact` | Target of the always-visible "Source code" link (AGPL-3.0 section 13): point it at the source of the build you run |

### Trash (`trash`)

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_TRASH_RETENTION` | `trash.retentionDays` | `7` | Days a deleted artifact, version or repository stays restorable before the purge worker removes it; repositories can override it (`trash_retention_days`); `0` disables the trash (deletes are immediate and permanent). Must not be negative. See [Trash](../operations/trash.md) |
| `OPENARTIFACT_TRASH_PURGE_INTERVAL` | `trash.purgeInterval` | `15m` | Pause between purge passes; `0` disables the schedule (admins can still purge by hand) |

### Audit log (`audit`)

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_AUDIT_RETENTION_DAYS` | `audit.retentionDays` | `0` | Days audit entries are kept before the retention worker deletes them in batches; `0` keeps them forever. Nothing is exported first: use `oa audit export`. Must not be negative. See [audit log retention](../operations/audit-retention.md) |
| `OPENARTIFACT_AUDIT_PURGE_INTERVAL` | `audit.purgeInterval` | `1h` | Pause between retention passes. Must be positive |
| `OPENARTIFACT_AUDIT_PURGE_BATCH_SIZE` | `audit.purgeBatchSize` | `5000` | Entries one delete statement removes. Must be positive |

### Federation (`federation`)

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_FEDERATION_ENABLED` | `federation.enabled` | `false` | Master switch of the pull workers and of the `/federation/v1` peer endpoints. Repositories are federated individually. See [Federation](../operations/federation.md) |
| `OPENARTIFACT_FEDERATION_POLL_INTERVAL` | `federation.pollInterval` | `30s` | Pause between pulls of one peer |
| `OPENARTIFACT_FEDERATION_BATCH_SIZE` | `federation.batchSize` | `200` | Log entries requested per `/log` call |
| `OPENARTIFACT_FEDERATION_MAX_BACKOFF` | `federation.maxBackoff` | `15m` | Ceiling of the retry backoff for a failing peer |
| `OPENARTIFACT_FEDERATION_REQUEST_TIMEOUT` | `federation.requestTimeout` | `30s` | Timeout of one peer request (blob transfers use an idle timeout instead) |
| `OPENARTIFACT_FEDERATION_SPOOL_DIR` | `federation.spoolDir` | empty | Directory where blobs pulled from peers are downloaded; an interrupted download stays and is resumed with a `Range` request. Empty uses a directory under the OS temp dir; set a persistent volume to resume across restarts, one per instance |
| `OPENARTIFACT_FEDERATION_MAX_PARTIAL_BYTES` | `federation.maxPartialBytes` | `10737418240` | Cap on the total size of kept interrupted downloads; the oldest are removed first |
| `OPENARTIFACT_FEDERATION_MAX_PARTIAL_AGE` | `federation.maxPartialAge` | `24h` | An interrupted download not written to for this long is removed |
| `OPENARTIFACT_FEDERATION_TOMBSTONE_RETENTION` | `federation.tombstoneRetention` | `720h` | How long delete tombstones are kept; set it above the longest partition a member must survive |

### Storage tiers (`tiering`)

Tuning of the background mover; tiers and policies are managed through the admin API ([storage tiers](../operations/storage-tiering.md#configuration)).

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_TIERING_ENABLED` | `tiering.enabled` | `true` | Run the mover. Off: nothing moves, reads still work from every tier |
| `OPENARTIFACT_TIERING_INTERVAL` | `tiering.interval` | `5m` | Pause between passes; `0` disables the schedule |
| `OPENARTIFACT_TIERING_BATCH` | `tiering.batch` | `200` | Most blobs moved per pass (at least 1) |
| `OPENARTIFACT_TIERING_CONCURRENCY` | `tiering.concurrency` | `2` | Blobs copied at once (at least 1) |
| `OPENARTIFACT_TIERING_MAX_BYTES_PER_SECOND` | `tiering.maxBytesPerSecond` | `67108864` | Shared copy bandwidth cap; `0` unlimited |
| `OPENARTIFACT_TIERING_VERIFY_READBACK` | `tiering.verifyReadback` | `true` | Re-hash each copy on the target tier before the placement flips |
| `OPENARTIFACT_TIERING_ACCESS_FLUSH_INTERVAL` | `tiering.accessFlushInterval` | `30s` | How often recorded downloads are written to the database |
| `OPENARTIFACT_TIERING_REFRESH_INTERVAL` | `tiering.refreshInterval` | `30s` | How often each replica reloads the tier list |

### Blob replication (`storage.replica`)

An optional second copy of every blob on another backend, with read fallback and verify/repair ([blob replication](../operations/replication.md)). Replication is active when `storage.replica.type` is set; the block nests under `storage:` and takes the primary backend's keys plus the tuning keys below. The environment variables use the `OPENARTIFACT_STORAGE_REPLICA_` prefix (the primary's `STORAGE_*` variables are unrelated). The replica may not be the same location as the primary.

| Environment variable | YAML key (under `storage.replica`) | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_STORAGE_REPLICA_TYPE` | `type` | empty | Replica backend (`filesystem`, `s3`, `gcs`, `azure`, `garage`, `seaweedfs`, `oci`). Empty turns replication off |
| `OPENARTIFACT_STORAGE_REPLICA_PATH` | `path` | empty | Directory (filesystem replica) |
| `OPENARTIFACT_STORAGE_REPLICA_ENDPOINT` | `endpoint` | empty | S3-compatible endpoint, or OCI registry URL |
| `OPENARTIFACT_STORAGE_REPLICA_BUCKET` | `bucket` | `artifacts` | Bucket (S3 family) or OCI repository prefix |
| `OPENARTIFACT_STORAGE_REPLICA_REGION` | `region` | `us-east-1` | Region |
| `OPENARTIFACT_STORAGE_REPLICA_ACCESS_KEY` | `accessKey` | empty | S3 access key; used only when both keys are set, otherwise the AWS credential chain applies |
| `OPENARTIFACT_STORAGE_REPLICA_SECRET_KEY` | `secretKey` | empty | S3 secret key (**secret**) |
| `OPENARTIFACT_STORAGE_REPLICA_FORCE_PATH_STYLE` | `forcePathStyle` | `false` | Path-style S3 addressing |
| `OPENARTIFACT_STORAGE_REPLICA_OPTIONS` | `options` | none | Free-form backend options as comma-separated `key=value` pairs |
| `OPENARTIFACT_STORAGE_REPLICA_GCS_BUCKET` | `gcsBucket` | empty | GCS bucket |
| `OPENARTIFACT_STORAGE_REPLICA_GCS_HMAC_KEY` | `gcsHmacKey` | empty | GCS HMAC access key |
| `OPENARTIFACT_STORAGE_REPLICA_GCS_HMAC_SECRET` | `gcsHmacSecret` | empty | GCS HMAC secret (**secret**) |
| `OPENARTIFACT_STORAGE_REPLICA_AZURE_ACCOUNT_NAME` | `azureAccountName` | empty | Azure storage account |
| `OPENARTIFACT_STORAGE_REPLICA_AZURE_ACCOUNT_KEY` | `azureAccountKey` | empty | Azure account key (**secret**) |
| `OPENARTIFACT_STORAGE_REPLICA_AZURE_CONTAINER_NAME` | `azureContainerName` | empty | Azure container |
| `OPENARTIFACT_STORAGE_REPLICA_AZURE_ENDPOINT` | `azureEndpoint` | empty | Blob service URL for Azurite or a sovereign cloud |
| `OPENARTIFACT_STORAGE_REPLICA_OCI_USERNAME` | `ociUsername` | empty | OCI registry user |
| `OPENARTIFACT_STORAGE_REPLICA_OCI_PASSWORD` | `ociPassword` | empty | OCI registry password (**secret**) |
| `OPENARTIFACT_STORAGE_REPLICA_OCI_INSECURE` | `ociInsecure` | `false` | Plain HTTP to the registry (dev/test only) |
| `OPENARTIFACT_STORAGE_REPLICA_READ_FALLBACK` | `readFallback` | `notfound` | When reads fall back to the replica: `notfound` (the primary says the blob is missing), `error` (also when the primary fails), `off` |
| `OPENARTIFACT_STORAGE_REPLICA_INTERVAL` | `interval` | `5s` | Pause between replication queue passes; must be positive |
| `OPENARTIFACT_STORAGE_REPLICA_BATCH` | `batch` | `100` | Queued operations claimed per pass (at least 1) |
| `OPENARTIFACT_STORAGE_REPLICA_CONCURRENCY` | `concurrency` | `4` | Blobs copied at once (at least 1). With an explicit `database.maxOpenConns`, that must be at least `2 * concurrency + 2` |
| `OPENARTIFACT_STORAGE_REPLICA_VERIFY_INTERVAL` | `verifyInterval` | `24h` | Pause between full verify passes; `0` runs one only when an admin requests it |
| `OPENARTIFACT_STORAGE_REPLICA_VERIFY_DEEP` | `verifyDeep` | `false` | Hash both copies in verify passes instead of comparing sizes (reads every blob from both backends) |
| `OPENARTIFACT_STORAGE_REPLICA_MAX_BACKOFF` | `maxBackoff` | `1h` | Longest retry delay of a failing replication operation; must be positive |

### Disaster-recovery journal (`recovery.journal`)

Row journal that lets `openartifact rebuild` re-index an empty database from the blob store ([rebuild from storage](../operations/rebuild-from-storage.md)).

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_RECOVERY_JOURNAL_ENABLED` | `recovery.journal.enabled` | `true` | Capture changed package rows and write their sidecar blobs. Off: the database triggers return immediately and nothing is written |
| `OPENARTIFACT_RECOVERY_JOURNAL_INTERVAL` | `recovery.journal.interval` | `10s` | Pause between worker passes; bounds how far sidecars lag behind the database |
| `OPENARTIFACT_RECOVERY_JOURNAL_BATCH` | `recovery.journal.batch` | `200` | Rows claimed per drain step (at least 1) |
| `OPENARTIFACT_RECOVERY_JOURNAL_CONCURRENCY` | `recovery.journal.concurrency` | `4` | Sidecar blobs written or deleted at once (at least 1) |
| `OPENARTIFACT_RECOVERY_JOURNAL_RECONCILE_INTERVAL` | `recovery.journal.reconcileInterval` | `24h` | How often every journaled table is re-checked for rows without a sidecar and sidecars without a row; `0` runs only the first complete pass |
### Vulnerability scanners (`scanners`)

Settings for the `trivy` and `grype` backends of vulnerability scans; `osv` needs none. The binaries are not in the release image; see [Installing Trivy and Grype](../security/scanning.md#installing-trivy-and-grype) for installation, egress, offline mode and mirrors.

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_SCANNER_CACHE_DIR` | `scanners.cacheDir` | `<user cache dir>/openartifact/scanners` (the image sets `/var/cache/openartifact/scanners`) | Absolute directory for the scanner databases (`trivy/` and `grype/` inside); must be writable by the server unless offline |
| `OPENARTIFACT_SCANNER_UPDATE_INTERVAL` | `scanners.updateInterval` | `6h` | Refresh interval of the background database download; at least `1h`, or `0` to download once and stop |
| `OPENARTIFACT_SCANNER_OFFLINE` | `scanners.offline` | `false` | Never download databases; they must already be in the cache directory |
| `OPENARTIFACT_TRIVY_PATH` | `scanners.trivy.path` | `trivy` (from `PATH`) | Command name or absolute path of the Trivy binary |
| `OPENARTIFACT_SCANNER_DOWNLOAD_BASE_URL` | `scanners.downloadBaseUrl` | `https://github.com` | `https` origin the pinned Trivy/Grype release archives are downloaded from by `install: auto`; a mirror must serve GitHub's `/<owner>/<repo>/releases/download/<tag>/<file>` paths. No credentials, query or fragment |
| `OPENARTIFACT_TRIVY_INSTALL` | `scanners.trivy.install` | `none` | `none` or `auto`. `auto` downloads the pinned, sha256-verified Trivy release into `<cacheDir>/bin` when no path is configured and `trivy` is not on `PATH` |
| `OPENARTIFACT_GRYPE_INSTALL` | `scanners.grype.install` | `none` | Same for Grype |
| `OPENARTIFACT_TRIVY_DB_REPOSITORY` | `scanners.trivy.dbRepository` | Trivy's built-in list (`mirror.gcr.io/aquasec/trivy-db:2`, then `ghcr.io/aquasecurity/trivy-db:2`) | OCI repository to download `trivy-db` from (a mirror) |
| `OPENARTIFACT_TRIVY_JAVA_DB_REPOSITORY` | `scanners.trivy.javaDbRepository` | empty | When set, the Java index database is downloaded from this repository too; empty skips it |
| `OPENARTIFACT_GRYPE_PATH` | `scanners.grype.path` | `grype` (from `PATH`) | Command name or absolute path of the Grype binary |
| `OPENARTIFACT_GRYPE_DB_URL` | `scanners.grype.dbUrl` | Grype's built-in | `http(s)` URL of a Grype database `listing.json` (a mirror), without credentials in the URL |

A path must be a bare command name or absolute (no relative paths), a repository must not contain whitespace or start with `-`, and the server refuses to start on an invalid value. The cache directory is created when the first download starts. The startup log reports each resolved binary and its database state.

### Storage (`storage`)

Backends: `filesystem`, `s3`, `gcs`, `azure`, `garage`, `seaweedfs`, `oci`. `garage` and `seaweedfs` reuse the S3 client with path-style addressing forced on. Details: [storage](../operations/storage.md).

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `STORAGE_BACKEND` (alias `STORAGE_TYPE`) | `type` | `filesystem` | Backend; `STORAGE_BACKEND` wins when both are set |
| `STORAGE_PATH` | `path` | `/var/lib/artifacts` | Directory (filesystem) |
| `STORAGE_OPTIONS` | `options` | none | Free-form backend options as comma-separated `key=value` pairs, merged over YAML keys; none of the built-in backends reads any key yet |
| `STORAGE_ENDPOINT` | `endpoint` | empty | S3-compatible endpoint, or OCI registry URL |
| `STORAGE_BUCKET` | `bucket` | `artifacts` | Bucket (S3 family) or OCI repository prefix |
| `STORAGE_REGION` | `region` | `us-east-1` | Region |
| `STORAGE_ACCESS_KEY` | `accessKey` | empty | S3 access key; used only when both keys are set, otherwise the AWS credential chain applies |
| `STORAGE_SECRET_KEY` | `secretKey` | empty | S3 secret key (**secret**) |
| `STORAGE_S3_FORCE_PATH_STYLE` | `forcePathStyle` | `false` | Path-style S3 addressing |
| `STORAGE_S3_PRESIGN_TTL` | `presignTtl` | `5m` | Lifetime of presigned download URLs (s3, gcs), clamped to 1 m - 1 h. Only used when `presignDownloads` is on |
| `STORAGE_PRESIGN_DOWNLOADS` | `presignDownloads` | `false` | Answer blob downloads with a `307` redirect to a presigned URL (s3, gcs only); per-repository override `presign_downloads`. See [Presigned URLs](../operations/storage.md#presigned-urls) |
| `STORAGE_PRESIGN_MIN_SIZE` | `presignMinSize` | `1048576` | Blobs smaller than this many bytes are streamed; `0` redirects everything; negative is rejected at startup |
| `STORAGE_PRESIGN_ENDPOINT` | `presignEndpoint` | empty | Public base URL (absolute `http(s)`, no query or fragment) used only for signing; set it when `endpoint` is not reachable by clients |
| `STORAGE_PRESIGN_UPLOAD_TTL` | `presignUploadTtl` | `30m` | Lifetime of presigned PUT upload URLs, clamped to 1 m - 12 h |
| `STORAGE_PRESIGN_MAX_PENDING_UPLOADS` | `presignMaxPendingUploads` | `20` | Max outstanding (not completed, not expired) presigned uploads per user; `0` = unlimited; beyond it creating an upload answers `429` |
| `OPENARTIFACT_GCS_BUCKET` | `gcsBucket` | empty | GCS bucket (via the S3-compatible XML API) |
| `OPENARTIFACT_GCS_HMAC_KEY` | `gcsHmacKey` | empty | GCS HMAC access key (required for `gcs`) |
| `OPENARTIFACT_GCS_HMAC_SECRET` | `gcsHmacSecret` | empty | GCS HMAC secret (**secret**) |
| `OPENARTIFACT_AZURE_ACCOUNT_NAME` | `azureAccountName` | empty | Azure storage account |
| `OPENARTIFACT_AZURE_ACCOUNT_KEY` | `azureAccountKey` | empty | Azure account key (**secret**) |
| `OPENARTIFACT_AZURE_CONTAINER_NAME` | `azureContainerName` | empty | Azure container |
| `OPENARTIFACT_AZURE_ENDPOINT` | `azureEndpoint` | empty | Azure blob service URL (Azurite, sovereign clouds); empty uses `https://<account>.blob.core.windows.net` |
| `STORAGE_STAGING_PATH` | `stagingPath` | empty | Spool directory for server-relayed presigned uploads on `oci` storage (default `<tmp>/openartifact-staging`) |
| `STORAGE_OCI_USERNAME` | `ociUsername` | empty | OCI registry user |
| `STORAGE_OCI_PASSWORD` | `ociPassword` | empty | OCI registry password (**secret**) |
| `STORAGE_OCI_INSECURE` | `ociInsecure` | `false` | Plain HTTP to the registry (dev/test only) |
| - | `options` | none | YAML only; accepted by the schema but not read by any backend |

### Authentication and security (`auth`)

See [authentication](../security/authentication.md) and [RBAC](../security/rbac.md).

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `ALLOW_ANONYMOUS_READ` | `allowAnonymousRead` | `false` | Anonymous read of private repositories (repositories flagged public are always anonymously readable, never writable) |
| `API_KEY` | `server.apiKey` | empty | Deprecated static write key; authenticates as `system/legacy-api-key` and logs a warning on each use |
| `LEGACY_API_KEY_ENABLED` | `legacyApiKeyEnabled` | `false` | Accept `API_KEY` at all |
| `SESSION_DURATION_HOURS` | `sessionDurationHours` | `24` | Web session lifetime |
| `OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE` | `bootstrap.adminPasswordFile` | empty | File with the initial admin password, used only on an empty instance; nothing is printed. Preferred over the plain value (see [first login](installation.md#first-login)) |
| `OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD` | `bootstrap.adminPassword` | empty | The initial admin password itself (**secret**; compose convenience, prefer the `_FILE` form) |
| `OPENARTIFACT_BOOTSTRAP_FORCE_PASSWORD_CHANGE` | `bootstrap.forcePasswordChange` | `false` | Force a password change at the first login of a supplied password (a generated one always forces it) |
| `OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_FILE` | `bootstrap.adminTokenFile` | empty | File with an admin API token secret (`oa_pat_` + 32 to 128 characters), created as the token `bootstrap` on an empty instance |
| `OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_TTL` | `bootstrap.adminTokenTtl` | `0` | Lifetime of the bootstrap token; `0` = no expiry |
| `OPENARTIFACT_BOOTSTRAP_GENERATED_PASSWORD_TTL` | `bootstrap.generatedPasswordTtl` | `24h` | How long a generated (printed) admin password is accepted at login; `0` = never expires |
| `AUTH_HMAC_KEY` | `hmacKey` | generated once, persisted | Signs OIDC/SAML login state. Unset: a random key is generated once and stored in `system_settings` (shared by replicas); a configured value wins (**secret**) |
| `OPENARTIFACT_SAML_SP_CERT` | `samlSpCert` | empty | PEM certificate of the SAML service provider. Unset: a self-signed pair is generated once and persisted encrypted in `system_settings` (shared by replicas); regenerating it means re-registering the SP certificate at the IdP |
| `OPENARTIFACT_SAML_SP_KEY` | `samlSpKey` | empty | PEM private key of the SAML service provider (**secret**). Unset together with the certificate: see above. Stored unencrypted when `OPENARTIFACT_SECRET_KEY` is unset |

`API_KEY` lives under `server` in YAML, not `auth`.

### GPG signing (`gpg`)

Signs APT and RPM metadata and release bundle manifests; see [signing](../security/signing.md). Storing managed keys requires `OPENARTIFACT_SECRET_KEY`.

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `GPG_ENABLED` | `enabled` | `false` | Load the file key at `GPG_KEY_PATH`. Keys managed through the admin API sign regardless |
| `GPG_KEY_PATH` | `keyPath` | empty | Private key file (read-only fallback key) |
| `GPG_PASSPHRASE` | `passphrase` | empty | Key passphrase (**secret**) |
| `GPG_AUTO_GENERATE` | `autoGenerate` | `false` | Generate an RSA-4096 global signing key at startup when no key exists yet (needs `OPENARTIFACT_SECRET_KEY`; replica-safe) |

### Attestation signing (`attestation`)

Signs SBOM and VEX attestations (`?attest=true`); see [SBOM and VEX](../security/sbom.md#signed-sbom-and-vex-attestations).

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `ATTESTATION_KEY_PATH` | `keyPath` | empty | Unencrypted PKCS#8 or SEC1 PEM private key (ECDSA or Ed25519). Empty: an ECDSA P-256 key is generated on first use and stored encrypted in the database |

The Dependency-Track integration has no file or environment settings: it is configured in the admin UI or through the API ([Dependency-Track](../security/dependency-track.md)).

### Logging (`log`)

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `LOG_LEVEL` | `level` | `info` | `debug`, `info`, `warn`, `error` (unknown values fall back to `info`) |
| `LOG_FORMAT` | `format` | `json` | `json`; any other value gives console text |
| `LOG_OUTPUT` | `output` | `stdout` | `stdout`, `stderr` or a file path (appended) |

### Backup (`backup`)

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_BACKUP_DIR` | `backupDir` | `/var/lib/openartifact/backups` | Directory for backup files; see [backup](../operations/backup.md) |
| `OPENARTIFACT_BACKUP_LEASE_TTL` | `leaseTtl` | `5m` | How long a running backup's [lease](../operations/backup.md#consistency-without-locks) lasts without a heartbeat; the lease of a crashed backup stops holding blob deletions after this time. Must be positive |

### Blob deletion (`blobDeletion`)

Bytes of deleted blobs are removed later by a sweeper; see [deferred blob deletion](../operations/storage.md#deferred-blob-deletion).

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_BLOB_DELETION_GRACE` | `grace` | `24h` | Minimum time between deleting a blob and removing its bytes. `0` removes them as soon as no running backup needs them. Must not be negative |
| `OPENARTIFACT_BLOB_DELETION_SWEEP_INTERVAL` | `sweepInterval` | `1m` | Pause between sweeper passes; `0` turns the sweeper off on this replica |
| `OPENARTIFACT_BLOB_DELETION_SWEEP_BATCH` | `sweepBatch` | `200` | Deletions claimed per pass. Must be positive |

### Projects (`projects`)

See [Projects](../security/projects.md). An administrator can change `selfCreate` in the UI (Admin > Projects) or through `PUT /api/v1/admin/projects/settings`; once saved, the stored value takes precedence over this default and applies immediately.

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_PROJECTS_SELF_CREATE` | `selfCreate` | `false` | Any authenticated user may create a project and becomes its owner; policy fields stay at their defaults until a global admin changes them |

### AI analysis (`ai`)

Optional and off by default; see [AI analysis](../integrations/ai-analysis.md). Once an administrator has saved the `ai.enabled` setting through the UI/API, the stored settings decide whether analysis runs and the values below only fill fields left empty there. Saved changes apply immediately without a restart; changes to these environment variables need one.

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `AI_ENABLED` | `enabled` | `false` | Enable analysis |
| `AI_AUTO_ANALYZE` | `autoAnalyze` | `false` | Analyse every upload automatically (the manifest of each upload is sent to the provider) |
| `AI_PROVIDER` | `provider` | `openai` | `openai`, `azure`, `ollama` or `custom` (OpenAI-compatible endpoints); the admin API rejects other values |
| `AI_BASE_URL` | `baseUrl` | empty | Endpoint; defaults to `https://api.openai.com/v1` (openai) or `http://localhost:11434/v1` (ollama); required for azure and custom |
| `AI_API_KEY` | `apiKey` | empty | Provider key (**secret**) |
| `AI_MODEL` | `model` | `gpt-4o-mini` | Model name |
| `AI_MAX_TOKENS` | `maxTokens` | `1024` | Completion tokens per request |
| `AI_TIMEOUT_SECS` | `timeoutSecs` | `30` | HTTP timeout in seconds |
| `AI_SYSTEM_PROMPT` | `systemPrompt` | empty | Replace the built-in prompt |

### MCP (`mcp`)

Optional and off by default; see [MCP](../integrations/mcp.md).

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `MCP_ENABLED` | `enabled` | `false` | Mount the MCP endpoint |
| `MCP_TRANSPORT` | `transport` | `http` | Only `http` is valid (`stdio` is rejected) |
| `MCP_HTTP_PATH` | `httpPath` | `/mcp` | Mount path; requires `Authorization: Bearer <TOKEN>` |
| `MCP_ALLOWED_ORIGINS` | `allowedOrigins` | none | Browser origins accepted in `Origin` besides the `BASE_URL` origin; other origins get 403 |
| `MCP_MAX_BODY_BYTES` | `maxBodyBytes` | `1048576` | Max size of one MCP request body |
| `MCP_RATE_LIMIT_PER_MINUTE` | `rateLimitPerMinute` | `600` | MCP requests per authenticated principal per minute (`0` = unlimited) |
| `MCP_AI_TRIGGER_PER_MINUTE` | `aiTriggerPerMinute` | `10` | `trigger_ai_analysis` calls per principal per minute (`0` = unlimited) |

### Pre-set in the Docker Compose file

`docker/docker-compose.yml` sets `SERVER_HOST`, `SERVER_PORT`, `SERVER_SHUTDOWN_TIMEOUT`, `DB_*` (from `POSTGRES_*`, with `DB_SSLMODE=disable`), `STORAGE_BACKEND=filesystem`, `STORAGE_PATH`, `AUTH_HMAC_KEY`, `OPENARTIFACT_BACKUP_DIR`, `LOG_LEVEL` and `LOG_FORMAT`. Other variables must be added to its `environment:` block.
