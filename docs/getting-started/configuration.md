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
- With no `AUTH_HMAC_KEY` the server generates a random key at each start and warns on stderr.

## Environment-only variables

These are read directly from the environment, have no YAML key and are not in `internal/config`:

| Variable | Meaning |
|---|---|
| `OPENARTIFACT_SECRET_KEY` | Secret-encryption key (any string of at least 16 bytes; hashed to an AES-256-GCM key). Unset: stored credentials are kept in plaintext and a warning is logged. A shorter key aborts startup. Changing or losing it makes existing `enc:v1:` ciphertexts unreadable. Protects webhook secrets, upstream passwords, SSO client secrets, AI keys and storage credentials stored in the database |
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
| `DB_MAX_OPEN_CONNS` | `maxOpenConns` | `25` | Connection pool size |
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
| `OPENARTIFACT_DOCKER_UPLOAD_TTL` | `workers.dockerUploadTtl` | `24h` | Age after which an unfinished Docker blob upload session and its staged file are removed; `0` disables the sweep. Must not be negative |
| `OPENARTIFACT_DOCKER_UPLOAD_DIR` | `workers.dockerUploadDir` | `/tmp/openartifact/docker-uploads` | Directory where in-progress Docker blob uploads are staged; use a persistent volume that all replicas handling one upload share |
| `OPENARTIFACT_WEBHOOK_DELIVERY_RETENTION_DAYS` | `workers.webhookDeliveryRetentionDays` | `30` | Days finished (success or failed) webhook delivery records are kept; the hourly purge deletes older ones. `0` keeps them forever. Must not be negative |

### Trash (`trash`)

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_TRASH_RETENTION` | `trash.retentionDays` | `7` | Days a deleted artifact, version or repository stays restorable before the purge worker removes it; repositories can override it (`trash_retention_days`); `0` disables the trash (deletes are immediate and permanent). Must not be negative. See [Trash](../operations/trash.md) |
| `OPENARTIFACT_TRASH_PURGE_INTERVAL` | `trash.purgeInterval` | `15m` | Pause between purge passes; `0` disables the schedule (admins can still purge by hand) |

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
| `AUTH_HMAC_KEY` | `hmacKey` | random per start | Signs OIDC/SAML login state; set for restart- and replica-safe SSO (**secret**) |
| `OPENARTIFACT_SAML_SP_CERT` | `samlSpCert` | empty | PEM certificate of the SAML service provider |
| `OPENARTIFACT_SAML_SP_KEY` | `samlSpKey` | empty | PEM private key of the SAML service provider (**secret**). Without the pair an ephemeral keypair is generated per process and the IdP registration breaks on restart |

`API_KEY` lives under `server` in YAML, not `auth`.

### GPG signing (`gpg`)

Signs APT and RPM metadata; see [signing](../security/signing.md).

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `GPG_ENABLED` | `enabled` | `false` | Enable signing (needs `GPG_KEY_PATH`) |
| `GPG_KEY_PATH` | `keyPath` | empty | Private key file |
| `GPG_PASSPHRASE` | `passphrase` | empty | Key passphrase (**secret**) |
| `GPG_AUTO_GENERATE` | `autoGenerate` | `false` | Parsed but not read by any code at present: it has no effect |

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

### Pre-set in the Docker Compose file

`docker/docker-compose.yml` sets `SERVER_HOST`, `SERVER_PORT`, `SERVER_SHUTDOWN_TIMEOUT`, `DB_*` (from `POSTGRES_*`, with `DB_SSLMODE=disable`), `STORAGE_BACKEND=filesystem`, `STORAGE_PATH`, `AUTH_HMAC_KEY`, `OPENARTIFACT_BACKUP_DIR`, `LOG_LEVEL` and `LOG_FORMAT`. Other variables must be added to its `environment:` block.
