<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Monitoring and logging

Health and readiness endpoints, Prometheus metrics, OpenTelemetry (traces, metrics and log records over OTLP), the audit log and server logs.

The REST API, OpenAPI spec and interactive API reference are covered in [API](../integrations/api.md).

## Health and readiness

`/health`, `/ready` and `/metrics` are on the public router: no login, no RBAC. They still pass through the server-wide middleware, so `OPENARTIFACT_IP_ALLOWLIST` and `OPENARTIFACT_RATE_LIMIT_RPS` apply to probes and scrapers too.

| Endpoint | Success | Failure |
|---|---|---|
| `GET /health` | always `200`, JSON | never fails; a broken dependency shows as `"status":"degraded"` |
| `GET /ready` | `200 {"status":"ready"}` | `503 {"status":"not ready","reason":"database unavailable"}`, `"storage unavailable"`, `"storage tiers not loaded"` or `"shutting down"` (after a shutdown signal, see [graceful shutdown](#graceful-shutdown-and-rolling-updates)) |

`/health` body:

```json
{
  "status": "healthy",
  "version": "<build version>",
  "instance_id": "<OPENARTIFACT_INSTANCE_ID, omitted when empty>",
  "components": {
    "database": {"status": "ok"},
    "storage": {"status": "ok"}
  }
}
```

A failing component has `"status":"error"` and `"message":"unavailable"` (the cause is in the server log); the overall status is then `degraded`. Use `/ready` for load balancer and Kubernetes readiness checks, since `/health` returns 200 even when degraded.

What is checked: the database is pinged. Storage is probed through the same store stack that serves requests, for every backend: bucket access for `s3`, `garage`, `seaweedfs` and `gcs`, container access for `azure`, `GET /v2/` for `oci`, and for `filesystem` that the blob directory exists and a temporary file can be written and removed in it. The probe is cached for 10 seconds and bounded to 3 seconds, so polling `/health` does not turn into backend requests, and a failure shows up within that window. `/ready` checks the S3-family stores only, on every call. See [storage](storage.md#health).

The compose healthcheck is `wget --spider http://localhost:8080/health`.

### Graceful shutdown and rolling updates

On `SIGTERM` or `SIGINT` the server shuts down in this order:

1. `/ready` starts answering `503 {"status":"not ready","reason":"shutting down"}`; `/health` (liveness) keeps answering normally. HTTP keep-alives are switched off, so clients reconnect to another replica. The server keeps accepting and serving requests for `server.shutdownDelay` seconds (`SERVER_SHUTDOWN_DELAY`). A second signal skips the rest of the delay.
2. The listener stops and in-flight requests get up to `server.shutdownTimeout` seconds (`SERVER_SHUTDOWN_TIMEOUT`) to finish.
3. Background tasks drain (15 s at most), the workers stop and the audit log is flushed (5 s).

The delay exists because a Kubernetes Service removes a terminating pod from its endpoints a few seconds after the `SIGTERM`, so without it a few requests are routed to a pod that no longer accepts connections. The image has no shell, so a `preStop` `sleep` hook is not available; `shutdownDelay` replaces it. Set it to more than the readiness probe needs to fail plus the endpoint propagation time (10 is a good value), and make `terminationGracePeriodSeconds` at least `shutdownDelay + shutdownTimeout` plus about 20 s for the drain. The default is `0` (no delay, no change in behaviour) because outside Kubernetes nothing needs to be waited for; the Helm chart sets it for you.

## Metrics

`GET /metrics` serves the Prometheus text format from the default registry, so Go runtime (`go_*`) and process (`process_*`) metrics are included next to the `openartifact_*` families.

Access is controlled by two environment variables (environment only, not YAML). Metrics fail closed:

| `METRICS_AUTH_TOKEN` | `METRICS_ALLOW_ANONYMOUS` | Result |
|---|---|---|
| set | any | requires `Authorization: Bearer <METRICS_AUTH_TOKEN>`, otherwise `401` |
| unset | exactly `true` | open, no authentication |
| unset | anything else | `401` for everyone; a warning is logged at startup |

`METRICS_AUTH_TOKEN` is a static shared secret and is unrelated to API tokens. Only the literal `true` enables anonymous access.

```yaml
scrape_configs:
  - job_name: openartifact
    metrics_path: /metrics
    authorization:
      credentials: <METRICS_AUTH_TOKEN>
    static_configs:
      - targets: ["HOST:8080"]
```

### Metric families

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `openartifact_http_requests_total` | counter | `method`, `path`, `status` | `path` is the route template that served the request (see [the `path` label](#the-path-label)), or `unmatched` for requests that match no route; the raw URL is never used, so cardinality is bounded by the route table |
| `openartifact_http_request_duration_seconds` | histogram | `method`, `path` | buckets 0.005 to 10 s; same `path` values |
| `openartifact_uploads_total` | counter | `format`, `result` | hosted upload requests: `format` is one of `apk`, `cargo`, `conan`, `conda`, `deb`, `docker`, `go`, `helm`, `ivy`, `maven`, `npm`, `nuget`, `pypi`, `raw`, `rpm`, `rubygems`, `sbt`, `terraform`; `result` is `success` (HTTP status below 400) or `error` (400 and above). See [what counts as an upload](#what-counts-as-an-upload) |
| `openartifact_blob_bytes_total` | counter | `operation` | bytes written (`put`) and read (`get`) through the blob store |
| `openartifact_blob_operation_duration_seconds` | histogram | `operation`, `result` | latency of the object store as the server sees it, by `operation` (`put`, `get`, `exists`, `exists_by_sha256`, `delete`, `size`, `metadata`) and `result` (`ok`, `error`). This is the floor under every upload: on a store that syncs to a replicated volume it is tens of milliseconds, which is why every write route has the same p50. `put` includes reading the client's body, so a slow client inflates it; `get` is the time to open the object, not to stream it |
| `openartifact_blob_downloads_total` | counter | `mode`, `reason` | blob `GET`s by delivery: `mode=redirect` (`reason=presigned`) or `mode=stream` (`disabled`, `unsupported`, `below_min_size`, `sign_failed`, `repo_lookup_failed`, `missing_object`); see [presigned URLs](storage.md#presigned-urls) |
| `openartifact_metadata_regenerations_total` | counter | `format` | `maven`, `npm`, `rpm` |
| `openartifact_metadata_regeneration_duration_seconds` | histogram | `format` | same buckets as the request histogram |
| `openartifact_audit_dropped_total` | counter | | audit events lost to backpressure or after shutdown |
| `openartifact_osv_mirror_last_import_timestamp_seconds` | gauge | `ecosystem` | Unix time of the last successful [OSV mirror](../security/scanning.md#local-osv-mirror) import of an ecosystem (a check that found the export unchanged counts) |
| `openartifact_osv_mirror_advisories` | gauge | `ecosystem` | live (not withdrawn) advisories in the local OSV mirror that affect an ecosystem |
| `openartifact_osv_mirror_imports_total` | counter | `ecosystem`, `result` | OSV mirror imports; `result` is `imported`, `unchanged` or `error` |
| `openartifact_osv_mirror_rematch_duration_seconds` | histogram | | duration of the re-match of the stored packages against the changed advisories of one ecosystem; buckets 1 s to 1 h |
| `openartifact_osv_mirror_findings_total` | counter | `change` | findings the OSV re-match changed; `change` is `new`, `updated` or `resolved` |
| `openartifact_audit_retention_deleted_total` | counter | | audit entries deleted for being older than [`audit.retentionDays`](audit-retention.md) (stays 0 while retention is off) |
| `openartifact_audit_retention_last_run_timestamp_seconds` | gauge | | Unix time the last audit retention pass finished without error on this replica; 0 until one has |
| `openartifact_trash_purged_total` | counter | `kind` | trashed items (and repositories, `kind="repository"`) permanently removed by the retention purge; `kind` is the trash kind, e.g. `maven_artifact`, `docker_manifest` |
| `openartifact_storage_tier_blobs`, `openartifact_storage_tier_bytes` | gauge | `tier` | blobs and bytes placed on each [storage tier](storage-tiering.md#metrics), refreshed after every mover pass |
| `openartifact_storage_tier_reads_total` | counter | `tier` | blob reads served by each tier |
| `openartifact_storage_tier_moves_total` | counter | `from`, `to`, `result` | tier moves; `result` is `moved`, `skipped` or `failed` |
| `openartifact_storage_tier_moved_bytes_total` | counter | `from`, `to` | bytes moved between tiers |
| `openartifact_storage_tier_moves_pending` | gauge | | interrupted or failing moves awaiting recovery; should return to 0 |
| `openartifact_blob_deletions_pending` | gauge | | blob copies queued for [deferred deletion](storage.md#deferred-blob-deletion), refreshed after every sweeper pass; grows during the grace period and while a backup runs, should fall back afterwards |
| `openartifact_scanner_install_attempts_total` | counter | `scanner`, `result` | runtime installs of Trivy / Grype (`scanners.<name>.install: auto`): `installed`, `reused` (a verified copy was already there), `unsupported_platform`, `download_failed`, `checksum_mismatch`, `extract_failed`, `noexec`, `insecure_location`, `failed` |
| `openartifact_exploit_import_attempts_total` | counter | `feed`, `result` | imports of the [KEV and EPSS feeds](../security/exploit-signals.md#metrics): `feed` is `kev` or `epss`, `result` is `imported`, `unchanged` or `failed` |
| `openartifact_exploit_feed_last_success_timestamp_seconds` | gauge | `feed` | Unix time of the last successful import (or unchanged check) of each feed; alert when it gets old |
| `openartifact_scanner_db_update_attempts_total` | counter | `scanner`, `result` | background vulnerability database refreshes of Trivy / Grype: `success` or `failure` (a refresh cut short by shutdown is not counted) |
| `openartifact_scanner_db_last_success_timestamp_seconds` | gauge | `scanner` | Unix time of the last successful refresh; after a restart it starts from the age of the database on disk, and is absent for a scanner that has no database |
| `openartifact_db_pool_connections` | gauge | `pool`, `state` | established database connections: `state` is `in_use` (checked out) or `idle`; see [database pools](#database-connection-pools) |
| `openartifact_db_pool_open_connections` | gauge | `pool` | connections that exist, including (pgx) those being opened |
| `openartifact_db_pool_max_connections` | gauge | `pool` | configured limit (`database.maxOpenConns`) |
| `openartifact_db_pool_acquire_waits_total` | counter | `pool` | acquisitions that found every connection busy and had to wait |
| `openartifact_db_pool_acquire_wait_seconds_total` | counter | `pool` | cumulative time those acquisitions waited |
| `openartifact_db_pool_connections_closed_total` | counter | `pool`, `reason` | connections the pool closed: `max_lifetime` or `max_idle` (connection churn) |
| `openartifact_db_pool_acquires_total`, `openartifact_db_pool_acquire_seconds_total`, `openartifact_db_pool_acquire_canceled_total`, `openartifact_db_pool_connections_created_total` | counter | `pool` | `pool="pgx"` only (database/sql keeps no such counters): successful acquisitions, time spent acquiring, acquisitions abandoned by a cancelled caller, connections opened |
| `openartifact_blob_deletions_swept_total` | counter | `result` | sweeper decisions: `deleted` (bytes removed) or `kept` (the blob was referenced or placed there again, entry dropped) |
| `openartifact_telemetry_log_records_dropped_total` | counter | `scope` | log records not exported over OTLP because the export queue was full; `scope` is the instrumentation scope (`openartifact.audit` for audit events). See [log records](#log-records-over-otlp) |

#### The `path` label

`path` is the chi route template of the handler that served the request, never the raw URL: repository, package and image names, versions, tags and digests cannot reach a label. Templates keep their parameter names (`/repos/{name}/{pkg}/{version}`) and catch-alls keep their `/*` (`/repos/raw/{name}/*`, `/repos/maven/{name}/*`), so each catch-all is a single series however many files it serves. A request that matches no route is `unmatched`.

The OCI registry is served by one catch-all route, `/v2/*`; it is split by the endpoint the path addresses, using the distribution-spec templates:

| `path` | Request |
|---|---|
| `/v2/{name}/manifests/{reference}` | manifest get, head, put, delete |
| `/v2/{name}/blobs/{digest}` | blob get, head, delete |
| `/v2/{name}/blobs/uploads` | start a blob upload (also the monolithic `POST ?digest=`) |
| `/v2/{name}/blobs/uploads/{uuid}` | upload status, chunk, completion, cancel |
| `/v2/{name}/tags/list` | tag listing |
| `/v2/{name}/referrers/{digest}` | referrers |
| `/v2/*` | any other path under `/v2/` |
| `/v2`, `/v2/_catalog` | version check, catalog |

Other templates worth knowing: `/repos/{name}/*` (a publish to a Go or npm repository, `PUT`), `/repos/{name}/{pkg}` (npm packument), `/repos/{name}/{platform}` (conda), `/*` (the web UI).

#### What counts as an upload

`openartifact_uploads_total` counts one per hosted upload request that reaches an upload handler, whatever the client protocol, so every format has the same series:

- `deb`, `go` (GOPROXY upload), `maven` (`PUT`), `npm` (publish), `raw` (presigned upload completion), and `rpm` (API upload) are counted by their handlers.
- `apk`, `cargo`, `conan`, `conda`, `helm`, `ivy`, `nuget`, `pypi`, `raw` (direct `PUT`), `rubygems`, `sbt` and `terraform` are counted by the HTTP middleware from the response status of the upload route (`PUT /api/v1/crates/new`, `POST /charts`, `POST /legacy/`, `PUT /_api/modules/...`, ...).
- `docker` counts the requests that finish pushing an object: a manifest `PUT`, the `PUT` that completes a blob upload, and a monolithic `POST ?digest=`. Starting an upload and chunk `PATCH`es are not counted, and a push of several layers counts one per layer plus one for the manifest.
- A package made of several files counts one per file request (a Maven deploy issues a `PUT` for the POM, the jar and each checksum; Conan and Ivy/sbt likewise).
- A request refused by authentication or RBAC (`401`, `403`) never reaches the route and is not counted. Anything the upload route itself answers with `4xx` or `5xx` (unknown repository, validation, quota, immutable release, storage failure) is `result="error"`.

For the presigned raw upload (`POST /api/v1/repositories/{name}/uploads/{id}/complete`), a completion that fails (size or sha256 mismatch, expired upload, quota, storage error) is `error`; a retried completion of an already completed upload and an abort are not counted.

#### Database connection pools

The server keeps two connection pools to PostgreSQL, both capped by `database.maxOpenConns`, and the `pool` label tells them apart: `pgx` is the pgx pool the sqlc queries run on, `sql` is the `database/sql` pool (per-request authentication lookups, migrations, workers). They are read when Prometheus scrapes, not sampled in the background. Each pool opens connections independently, so up to twice `maxOpenConns` can be open at once: size `max_connections` of PostgreSQL accordingly.

Saturation shows as `in_use` close to `max_connections` together with a growing `acquire_waits_total` and `acquire_wait_seconds_total`; a high rate of `connections_closed_total` means the pool is opening and closing connections instead of reusing them.

Useful queries:

```promql
# error ratio per route
sum by (path) (rate(openartifact_http_requests_total{status=~"5.."}[5m]))
  / sum by (path) (rate(openartifact_http_requests_total[5m]))

# p95 latency per route
histogram_quantile(0.95, sum by (le, path) (rate(openartifact_http_request_duration_seconds_bucket[5m])))

# p95 latency of OCI manifest and blob traffic
histogram_quantile(0.95, sum by (le, path) (rate(openartifact_http_request_duration_seconds_bucket{path=~"/v2/.*"}[5m])))

# upload rate and failure ratio per format
sum by (format) (rate(openartifact_uploads_total[5m]))
sum by (format) (rate(openartifact_uploads_total{result="error"}[5m]))
  / sum by (format) (rate(openartifact_uploads_total[5m]))

# database pool utilisation (1 = every connection checked out)
openartifact_db_pool_connections{state="in_use"} / openartifact_db_pool_max_connections

# average time an acquisition waited, for acquisitions that had to wait
rate(openartifact_db_pool_acquire_wait_seconds_total[5m]) / rate(openartifact_db_pool_acquire_waits_total[5m])

# requests that waited for a database connection, per second
sum by (pool) (rate(openartifact_db_pool_acquire_waits_total[5m]))

# audit events being dropped (should stay 0)
increase(openartifact_audit_dropped_total[1h]) > 0

# a scanner database older than a day (the updater refreshes every 6 h by default)
time() - openartifact_scanner_db_last_success_timestamp_seconds > 86400

# database refreshes failing (the updater retries with backoff, so the old database keeps serving)
increase(openartifact_scanner_db_update_attempts_total{result="failure"}[6h]) > 0
```

For allocation and CPU profiling see [performance](performance.md).

## OpenTelemetry

OpenArtifact can export distributed traces, metrics and log records over OTLP to any collector (OpenTelemetry Collector, Jaeger, Tempo, Honeycomb, ...). It is **off by default**: while disabled no provider is installed, no span is created and no `traceparent` header is sent.

`enabled` is the master switch and the block owns the endpoint, protocol, headers and TLS settings shared by every signal. Each signal has its own switch under it: `traces` (on by default, so `enabled: true` alone means tracing), `metrics`, `logs` and `audit`. Every provider is created only for a signal that is on.

```yaml
telemetry:
  otlp:
    enabled: true
    endpoint: "otel-collector:4317"   # host:port or http(s) URL
    protocol: grpc                    # grpc | http
    headers: {}                       # sent with every export, e.g. authorization
    insecure: false                   # plaintext to the collector
    serviceName: openartifact
    sampleRatio: 1.0
    traces: true
    metrics: false
    logs: false
    audit: false
```

| Key | Environment | Default | Notes |
|---|---|---|---|
| `enabled` | `OPENARTIFACT_OTLP_ENABLED` | `false` | master switch: nothing is exported without it; `OTEL_SDK_DISABLED=true` forces it off. With every signal below off nothing is installed and the server logs one warning at startup |
| `endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | `host:port` or `http(s)://host:port[/prefix]`; empty uses the SDK default (`localhost:4317` for gRPC, `localhost:4318` for HTTP). For `http` the `/v1/traces` (`/v1/metrics`, `/v1/logs`) suffix is appended to the prefix |
| `protocol` | `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | `grpc`, or `http` (alias `http/protobuf`) |
| `headers` | `OTEL_EXPORTER_OTLP_HEADERS` | none | `k=v,k2=v2`, values percent-decoded; merged over the YAML map. Treated as secrets and never logged |
| `insecure` | `OTEL_EXPORTER_OTLP_INSECURE` | `false` | no TLS to the collector; an `http://` endpoint implies it |
| `serviceName` | `OTEL_SERVICE_NAME` | `openartifact` | `service.name` resource attribute |
| `sampleRatio` | `OTEL_TRACES_SAMPLER_ARG` | `1` | fraction of **new** traces recorded (0 to 1). Parent based: a request arriving with a sampled `traceparent` is always recorded, an unsampled one never |
| `traces` | `OTEL_TRACES_EXPORTER=otlp` (`none` turns it off) | `true` | distributed traces; the HTTP server middleware, the client and database spans and the log correlation fields exist only with it |
| `metrics` | `OTEL_METRICS_EXPORTER=otlp` (`none` turns it off) | `false` | also export metrics over OTLP, see below |
| `logs` | `OTEL_LOGS_EXPORTER=otlp` (`none` turns it off) | `false` | application log records over OTLP as well, the log output is unchanged; see [log records](#log-records-over-otlp) |
| `audit` | `OPENARTIFACT_OTLP_AUDIT` | `false` | audit events over OTLP (scope `openartifact.audit`), independent of `traces`; reserved, no events yet (#193), see [log records](#log-records-over-otlp) |

Environment variables win over the YAML file. An `OTEL_*_EXPORTER` variable is a comma-separated list: one containing `otlp` turns the signal on, otherwise `none` turns it off, and any other value (`zipkin`) is ignored. Setting only an `OTEL_EXPORTER_OTLP_*` variable does not enable telemetry: set `enabled` too. The SDK also reads the rest of the standard environment where the table says nothing: `OTEL_RESOURCE_ATTRIBUTES` adds resource attributes, `OTEL_METRIC_EXPORT_INTERVAL` sets the metric interval, and the signal-specific `OTEL_EXPORTER_OTLP_TRACES_*`, `OTEL_EXPORTER_OTLP_METRICS_*` and `OTEL_EXPORTER_OTLP_LOGS_*` variables apply while `endpoint` is empty. `service.version` and, when `OPENARTIFACT_INSTANCE_ID` is set, `service.instance.id` are filled in. Export failures are logged as a warning at most once a minute.

### What is traced

| Span | Kind | Name | Notes |
|---|---|---|---|
| HTTP request | server | `GET /repos/{name}`, `POST /api/v1/repositories/{name}` | the chi route template, never the raw path, so the number of span names is bounded by the route table; a request that matches no route is named after its method only (`GET`), a non-standard method is `_OTHER`. `/health`, `/ready` and `/metrics` are not traced |
| Outbound HTTP | client | `HTTP GET` | proxy upstreams (including the Go sumdb), webhook deliveries, the OSV vulnerability scanner, AI providers, OIDC and SAML metadata. The span lasts until the response body is read, so it covers the transfer of large downloads |
| Database | client | sqlc statement name (`GetUserByName`), or the verb (`SELECT`) for hand-written SQL, `batch` | only under a recorded request span, so background workers do not create a trace per query |
| Blob store | internal | `storage.put`, `storage.get`, `storage.exists`, `storage.exists_by_sha256`, `storage.delete`, `storage.size`, `storage.metadata`; on the S3 backend a `storage.put` is split into `storage.s3.spool` (the body is hashed to a temporary file), `storage.s3.head` and `storage.s3.upload` | only under a recorded request span; makes the object store latency visible as its own step instead of a gap between database spans. Attribute `blob.operation` only, never a blob id |

Server spans are marked as errors for a `5xx` status (a panic is recorded as `500`), client spans for transport errors and `4xx`/`5xx` responses.

Attributes: `http.request.method`, `url.scheme`, `network.protocol.version`, `http.route`, `http.response.status_code` on server spans; `http.request.method`, `url.scheme`, `server.address`, `server.port`, `http.response.status_code`, `error.type` on client spans; `db.system.name`, `db.operation.name`, `db.query.summary`, `db.response.status_code` (SQLSTATE) on database spans; `blob.operation` and `error.type` on blob store spans. **Nothing else is recorded**: no path or query string of the request (tokens travel in them), no `Authorization` or any other header, no client IP, no user name, no SQL text or arguments, no error messages (they quote URLs and values; only the Go type or SQLSTATE is kept).

### Propagation

The W3C `traceparent`, `tracestate` and `baggage` headers of an incoming request are honored, so OpenArtifact joins the caller's trace (a CI job, a reverse proxy that traces). Only `traceparent` and `tracestate` are added to **outbound requests** made under that context (proxy upstreams, webhook endpoints, scanners, AI providers, OIDC and SAML): they identify the trace and nothing else. `baggage` comes from untrusted clients and is never forwarded; it is only available in-process. With tracing enabled the `X-Trace-Id` response header carries the W3C trace id (it is otherwise a random id, or the one the client sent).

### Log correlation

With tracing enabled every `HTTP request` log line carries `trace_id` and `span_id`, the same ids as the exported spans and `X-Trace-Id`:

```json
{"message":"HTTP request","method":"GET","path":"/repos/npm/left-pad","status":200,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
```

The fields are absent while tracing is disabled. Exported over OTLP (`logs: true`), the same line is a log record whose own trace and span ids are those of the request span, so the backend links it to the trace without parsing fields.

Only the `HTTP request` line is written with the request context today. The other lines logged while serving a request (handler, upload, proxy and group errors) have no context: on stdout they have no `trace_id`, and over OTLP their record has no trace or span id.

### Metrics over OTLP

Prometheus stays the primary interface and `/metrics` is unchanged. With `metrics: true` (or `OTEL_METRICS_EXPORTER=otlp`) the same registry (`openartifact_*`, `go_*`, `process_*`) is also pushed to the collector through the OpenTelemetry Prometheus bridge, with the same endpoint, protocol and headers: no second set of instruments exists, so the two views cannot disagree.

### Log records over OTLP

With `logs` or `audit` on, one OTLP log provider is started on the same endpoint, protocol and headers (`/v1/logs` for `http`). `logs` is for the application log records, `audit` for the audit events under the instrumentation scope `openartifact.audit`; either works with `traces` off. The `audit_logs` table stays the source of truth for the audit trail. Audit events are not written to the provider yet (#193).

With `logs` on, every line the server logs once telemetry is set up (at or above `LOG_LEVEL`) is also exported as a record under the instrumentation scope `github.com/openartifact/openartifact`; the startup banner and the unencrypted-secrets warning come earlier and stay on the log output only. The configured [log output](#logging) is unchanged: same lines, same fields. Each record has:

| Record field | Value |
|---|---|
| body | the message (`HTTP request`) |
| severity | number from the level (`debug` 5 DEBUG, `info` 9 INFO, `warn` 13 WARN, `error` 17 ERROR), text the level itself |
| attributes | the line's fields (`method`, `path`, `status`, ...), `code.file.path`, `code.line.number` and `code.function.name`; an `error` field becomes `exception.type` and `exception.message`, the same text as on the log line |
| trace and span ids | those of the request span, for the lines written with the request context (see [log correlation](#log-correlation)) |
| resource | the same as traces and metrics: `service.name`, `service.version`, `service.instance.id` and `OTEL_RESOURCE_ATTRIBUTES` |

The attributes are the fields of the line as written, not filtered, unlike the [spans](#what-is-traced): the `HTTP request` line carries `path` (without the query string), `remote_addr` and `request_id`, and `exception.message` is the full error text, which can quote upstream or database detail. A Terraform provider download carries its 15-minute download ticket in the `path`. OpenTelemetry leaves scrubbing to the operator ([handling sensitive data](https://opentelemetry.io/docs/security/handling-sensitive-data/)): when the collector forwards to a backend you do not run, drop or rewrite these attributes there with its `redaction`, `attributes` or `transform` processor, or leave `logs` off.

Exporter errors are written to the log output only, never exported, so a collector outage does not feed its own errors into the queue.

```yaml
telemetry:
  otlp:
    enabled: true
    endpoint: "otel-collector:4317"
    traces: true
    logs: true
```

Writing a record never waits for the collector. Records are queued for export (`OTEL_BLRP_MAX_QUEUE_SIZE`, 2048 by default); while the queue is full, because the collector is slow or unreachable, new records are dropped and `openartifact_telemetry_log_records_dropped_total` counts them by instrumentation scope. It should stay 0.

### Overhead and binary size

The OpenTelemetry SDK and the OTLP exporters are always compiled in, enabled or not (about 5 MB of the 44 MB stripped `linux/amd64` server binary: OpenTelemetry 1.7 MB, gRPC 1.1 MB, protobuf 1.6 MB and the `telemetry` package 0.3 MB, measured with go-size-analyzer; `go list -m all` grew from 159 to 221 modules). The release builds use the `grpcnotrace` build tag, which drops gRPC's `net/trace` debug pages (`EnableTracing` is never set, so nothing changes at runtime); without it the same dependencies cost about 14 MB, because `net/trace` pulls in `html/template` and the linker then keeps every exported method of every reachable type. With tracing disabled the runtime cost is one atomic load per outbound request and database statement; the HTTP middleware is not installed at all. Lower `sampleRatio` on busy instances: every proxied download is a request.

### Trying it locally

With the bundled compose file, add the variables to the `openartifact` service's `environment:` (the file passes only the variables it lists), for example `OPENARTIFACT_OTLP_ENABLED: "true"` and `OTEL_EXPORTER_OTLP_ENDPOINT: http://jaeger:4317` next to a Jaeger service on the same network.

```sh
docker run --rm -d --name jaeger -p 16686:16686 -p 4317:4317 -p 4318:4318 docker.io/jaegertracing/jaeger:latest
OPENARTIFACT_OTLP_ENABLED=true OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 ./bin/openartifact
# browse http://localhost:16686, service "openartifact"
```

### Local Collector

The server only speaks OTLP. Batching, retries, queueing, redaction and routing to one or more backends belong in an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) (the `otelcol-contrib` distribution) running next to it, so only the Collector needs egress.

[`docker/otel-collector.yaml`](../../docker/otel-collector.yaml) is a baseline configuration: OTLP in on `localhost` (gRPC `4317`, HTTP `4318`; `OTELCOL_LISTEN_HOST` changes the bind address), the `memory_limiter`, `redaction/frontend` (see [Frontend telemetry](#frontend-telemetry-experimental)) and `batch` processors, and OTLP/HTTP out to `OTELCOL_EXPORT_ENDPOINT` with the `Authorization` header taken from `OTELCOL_EXPORT_AUTHORIZATION`. It has a pipeline for traces, metrics and logs.

The compose file runs it as the opt-in `otel-collector` profile: a separate service on the compose network, listening on `0.0.0.0` there with no published ports, so the server's default endpoint `http://otel-collector:4317` reaches it and either container can restart on its own. In `docker/.env`:

```sh
OPENARTIFACT_OTLP_ENABLED=true
OTELCOL_EXPORT_ENDPOINT=https://otlp.example.com
OTELCOL_EXPORT_AUTHORIZATION=Bearer <token>
```

```sh
docker compose -f docker/docker-compose.yml --env-file docker/.env --profile otel-collector up -d
```

`OTELCOL_EXPORT_ENDPOINT` is required: without it the Collector exits with `exporters::otlp_http: at least one endpoint must be specified` and compose keeps restarting it (`docker compose logs otel-collector`). An empty `OTELCOL_EXPORT_AUTHORIZATION` is still sent as an empty `Authorization` header; for a backend without authentication, remove the `headers` block from the configuration.

The per-signal variables (`OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER`, `OPENARTIFACT_OTLP_AUDIT`) and `OTEL_EXPORTER_OTLP_ENDPOINT` are passed through from `docker/.env` as well; the per-signal ones and `OPENARTIFACT_OTLP_ENABLED` only when set there, so they never override a mounted config file. Outside compose, run the same configuration with `otelcol-contrib --config docker/otel-collector.yaml` on the host or as a sidecar.

### Frontend telemetry (experimental)

The web UI can send its own errors and web vitals, and a client span for each API call, to the same Collector as the server. It is **experimental**: off by default, no compatibility promise, and a bump of the pinned OpenTelemetry browser packages may change the data model (event names, attributes) in any release. Ship no dashboards or alerts on this data; it is meant for ad-hoc research.

```yaml
telemetry:
  otlp:
    enabled: true
  frontend:
    enabled: true
    endpoint: "http://localhost:4318"   # OTLP/HTTP base URL of the Collector
```

| Key | Environment | Default | Notes |
|---|---|---|---|
| `frontend.enabled` | `OPENARTIFACT_TELEMETRY_FRONTEND_ENABLED` | `false` | the single toggle; needs `otlp.enabled` (without it the server logs a warning and the feature stays off) |
| `frontend.endpoint` | `OPENARTIFACT_TELEMETRY_FRONTEND_ENDPOINT` | empty | OTLP/HTTP base URL the relay forwards to; `/v1/traces` and `/v1/logs` are appended. Empty uses `otlp.endpoint`, which then has to be OTLP/HTTP (`protocol: http`); with `protocol: grpc` it is required. The compose file defaults it to `http://otel-collector:4318`; next to a Collector sidecar use `http://localhost:4318` |

The browser never talks to the Collector. It posts OTLP/HTTP protobuf to a same-origin relay, `POST /api/v1/telemetry/traces` and `POST /api/v1/telemetry/logs`, which forwards the body unchanged with the `otlp.headers`, so the Collector stays internal, there is no CORS, and the page's CSP stays as it is. The relay:

- accepts signed-in browser sessions only, with the `X-CSRF-Token` header like every other UI call: anonymous requests get `401`, API tokens (deploy tokens included) `403`;
- takes `Content-Type: application/x-protobuf`, uncompressed, at most 1 MiB per request (`415`, `413`);
- answers `404` while the feature is off and for any other signal (`/api/v1/telemetry/metrics`);
- answers `502` when the Collector is unreachable or fails (the browser exporter retries later), `400` when the Collector rejects the payload; a forward times out after 10 seconds and affects no other request;
- answers `503` with `Retry-After` while 32 forwards are already in flight, so a hung Collector cannot pile up request bodies in memory (the browser exporter retries);
- is covered by `server.rateLimitRps` like every other route, is not written to the audit log, and counts the requests it handles in `openartifact_frontend_telemetry_requests_total{signal, result}` with `result` `relayed`, `rejected` or `failed` (the `401` and a missing CSRF token are refused before it and not counted).

`GET /api/v1/telemetry/frontend` tells a signed-in UI whether to load the SDK: `{"enabled": true, "sample_ratio": ...}`, or `404` while off. `sample_ratio` is `otlp.sampleRatio`: the UI samples its traces with it, so its `traceparent` cannot make the server record more than it would on its own.

The relay does not parse or filter the payload: redaction belongs in the Collector. [`docker/otel-collector.yaml`](../../docker/otel-collector.yaml) already has a `redaction` processor on its traces and logs pipelines, so the compose profile does not export raw stack traces; it masks URLs the browser could record and query strings in error messages and stack traces. With another Collector, add the same processor:

```yaml
processors:
  redaction/frontend:
    allow_all_keys: true
    blocked_key_patterns:
      - '^url\.(full|path|query)$'
      - '^browser\.document\.url\.'
    blocked_values:
      - '[?&][^=&\s]+=[^&\s)]*'
    summary: silent

service:
  pipelines:
    traces:
      processors: [memory_limiter, redaction/frontend, batch]
    logs:
      processors: [memory_limiter, redaction/frontend, batch]
```

The UI sets `service.name=openartifact-web`: filter on it to separate the UI from the server. The relay forwards payloads unchanged and accepts them from every signed-in account, so a user can send records with any resource attributes, including the server's own `service.name`: treat this data as untrusted.

#### What the UI sends

After sign-in the UI asks `GET /api/v1/telemetry/frontend`. Only a `200` makes it load the OpenTelemetry wrapper, a separate `otel-*` chunk that the service worker does not precache; while the feature is off the default bundle holds no OpenTelemetry code and nothing else is requested. The pinned packages are `@opentelemetry/api`, `sdk-trace`, `sdk-logs`, `resources`, `exporter-trace-otlp-proto`, `exporter-logs-otlp-proto` and `browser-instrumentation` (its `errors` and `web-vitals` instrumentations only).

| Data | Signal | Contents |
|---|---|---|
| Errors | log event `exception` | unhandled errors and promise rejections, and errors caught by Vue's error handler: `exception.type`, `exception.message`, `exception.stacktrace`. A script from another origin only reports `Script error.` |
| Web vitals | log event `browser.web_vital` | CLS, INP, LCP, FCP and TTFB: `browser.web_vital.name`, `.value`, `.delta`, `.rating`, `.id`, `.navigation_type` |
| API calls | client span `HTTP GET` (`POST`, ...) | one per call through the UI's API client, with `http.request.method`, `http.response.status_code` and `error.type`, never the URL; its `traceparent` makes the server span of the call its child. Sampled with `sample_ratio` |

Every record has `service.name=openartifact-web` and `service.version` of the web build. Nothing else is collected: no console output, clicks, document load, navigation or resource timing, no fetch/XHR instrumentation, and no session id; nothing is kept in cookies, local storage or globals. Records are batched (64 per request) and sent every few seconds and when the page is hidden.

## Audit log

The server records who changed what. Events are stored in the `audit_logs` table and shown under **Settings > Audit log** (`/settings/audit`) in the UI.

Members of a project read the slice that concerns them (the project's own events and those of its repositories) without admin rights: see [project activity](../security/projects.md#project-activity).

Recorded:

- Every authenticated `POST`, `PUT` and `DELETE` request (reads are not recorded), with actor (username, or `anonymous`), client IP and a `details` object with `method`, `path` (the route pattern such as `/api/v1/repositories/{name}`, not the concrete URL), `status`, and `error: true` for status 400 and above.
- Named actions: `repository.created`, `repository.deleted`, `repository.restored`, `repository.purged`, `artifact.uploaded` (deb upload through `POST /api/v1/repositories/{name}/debs` only), `artifact.deleted` (details `moved to trash` or `deleted`), `artifact.restored`, `artifact.purged` (see [trash](trash.md#audit-and-webhook-events)), `bundle.promoted` (a release bundle promotion; resource is the bundle, repository the promotion target, details the stage and the counts), `auth.password_changed`, `auth.token_created`, `auth.token_revoked`. A rejected `auth.*` request is recorded as `http.request` instead. Admin changes also write an explicit event naming the target (`resource_type`, `resource_id`, and the changed fields, never secrets): `user.created`, `user.updated` (changed fields, e.g. `password_reset`, `disabled=true`), `user.disabled`, `group.created`, `group.updated`, `group.deleted`, `group.member_added`, `group.member_removed`, `group.members_set`, `permission_target.created`, `permission_target.updated`, `permission_target.deleted`, `permission_target.assigned`, `permission_target.unassigned`, `sso_provider.created`, `sso_provider.updated`, `sso_provider.deleted` and `token.created_for_user` (a token minted for another user). Denied (403) requests are not audited. Every other write, including format uploads such as `PUT /repos/...`, is `http.request`.
- Scan events written by the [local OSV mirror](../security/scanning.md#local-osv-mirror): `scan.vuln_detected` (advisories that newly affect a stored package version; resource is the package, details the advisory ids) and `scan.vuln_resolved` (advisories withdrawn or no longer affecting it). Manual scans record `scan.vuln_detected` too. The mirror's first import of an ecosystem writes neither.
- Login traffic: `auth.login`, `auth.login_failed` (with the attempted username, never the password), `auth.logout` and `auth.sso_login`. Requests answered with `429` are not recorded.

Query it (admin only):

```sh
curl -H "Authorization: Bearer <TOKEN>" \
  "HOST/api/v1/admin/audit?page=1&per_page=50&action=auth.login&actor=alice"
```

| Parameter | Default | Meaning |
|---|---|---|
| `page` | 1 | 1 to 10000 |
| `per_page` | 25 | 1 to 100 |
| `actor` | | case-insensitive substring of the actor |
| `action` | | case-insensitive substring of the action |

The response is `{"data":[...],"page":1,"per_page":25,"total":N}`, newest first. Each entry has `id`, `timestamp`, `action`, `actor`, `actor_name`, `subject`, `target`, `details`, `ip`, `repository_id`, `resource_type` and `resource_id`. `actor_name` is the name recorded with the event, else the user's current name, else `system` for background work. `subject` is what the event acted on, `{"kind","name","version?","repository?","project?"}` (kinds such as `repository`, `package`, `project`, `user`, `group`, `token`, `bundle`, `setting`), resolved from ids with one lookup per page; it is `null` for events that name nothing (logins, generic request rows). A subject that was deleted keeps the name its event recorded (trash events do), else `name` is `null`. The export keeps its field set.

### Export

`GET /api/v1/admin/audit/export` (admin only) downloads the log, not one page of it, as JSONL or CSV:

```sh
curl -fOJ -H "Authorization: Bearer <TOKEN>" \
  "HOST/api/v1/admin/audit/export?format=csv&since=2026-01-01&until=2026-02-01"

oa audit export --format jsonl --since 24h -o audit.jsonl   # or stdout without -o
```

| Parameter | Default | Meaning |
|---|---|---|
| `format` | `jsonl` | `jsonl` (one JSON object per line) or `csv` (header row, then one record per entry) |
| `since` | | entries at or after this instant: an RFC 3339 timestamp or a `YYYY-MM-DD` date (00:00 UTC) |
| `until` | | entries strictly before this instant (same forms); must be after `since` |
| `actor`, `action` | | the same substring filters as the listing |

`oa audit export` takes the same filters as `--format`, `--since`, `--until`, `--actor` and `--action`, plus `-o FILE` (written atomically, so an interrupted transfer leaves no partial file; without `-o` the export goes to stdout). `--since` and `--until` also accept an age such as `36h` or `7d`, resolved against the current time.

The response is a download named `openartifact-audit-<UTC timestamp>.<jsonl|csv>` (`Content-Disposition`), newest entry first. It is streamed: entries are read from the database in keyset pages of 1000 and flushed after every page, no row is counted, and memory stays at one page however large the log is. The body is chunked, so there is no `Content-Length`. If the server fails after the first bytes went out, or the connection drops, the response ends abnormally instead of finishing like a complete file; treat a transfer error as a partial export and run it again. Entries recorded while an export runs are newer than its starting point and may or may not be included.

Fields, in this order. The set is stable: fields may be appended, never renamed or removed.

| Field | Meaning |
|---|---|
| `id` | entry UUID; unique, usable as a dedup key |
| `timestamp` | RFC 3339 in UTC with microseconds, for example `2026-03-01T12:00:00.123456Z` |
| `action` | action name, for example `auth.login` |
| `actor` | username or token name (empty when the event has none) |
| `actor_type` | `user`, `system`, `token` or `sso` |
| `actor_id` | UUID of the acting user, if any |
| `resource_type`, `resource_id` | the object the event names, if any |
| `repository_id`, `project_id` | UUIDs of the repository or project concerned, if any |
| `ip` | client address |
| `details` | the event details: a JSON object in JSONL, its JSON text in one CSV cell |

Unset values are `null` in JSONL and an empty cell in CSV. CSV is RFC 4180 (quoted cells, doubled quotes, newlines allowed inside quotes). To keep spreadsheets from running attacker-controlled text, a CSV cell that starts with `=`, `+`, `-`, `@`, a tab or a carriage return is written with a leading `'`; read JSONL when you need the exact value.

Members of a project export the project's own activity with `GET /api/v1/projects/{project}/activity/export` (same `format`, `since`, `until`, `actor` and `action` parameters, file name `openartifact-<project>-activity-<timestamp>.<ext>`). It applies the same visibility rules as the [project activity](../security/projects.md#project-activity) feed: `project.token_*` events only for maintainers and above and no client addresses. Its fields are `id`, `timestamp`, `action`, `actor`, `actor_type`, `message`, `resource_type`, `resource_id` and `repository_id`. The **Settings > Audit log** section and the project's Activity tab have **Export JSONL** and **Export CSV** buttons for the same downloads (the admin button applies the filters typed on the page).

#### Shipping the log to a SIEM

Every entry has a unique `id` and an RFC 3339 `timestamp`, and `since` is inclusive, so a periodic job can ingest the last window and let the target drop the overlap. The examples assume `jq` and a token with admin rights in `OA_TOKEN`.

Splunk (HTTP Event Collector, event time taken from `timestamp`):

```sh
oa audit export --since 1h \
  | jq -c '{time: (.timestamp | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601), sourcetype: "openartifact:audit", event: .}' \
  | curl -fsS -H "Authorization: Splunk $HEC_TOKEN" --data-binary @- https://splunk.example.com:8088/services/collector/event
```

Elasticsearch (bulk API; using `id` as the document id makes re-ingesting an overlapping window idempotent):

```sh
oa audit export --since 1h \
  | jq -c '{index: {_index: "openartifact-audit", _id: .id}}, (. + {"@timestamp": .timestamp})' \
  | curl -fsS -H "Content-Type: application/x-ndjson" -u "$ES_USER:$ES_PASS" --data-binary @- https://es.example.com:9200/_bulk
```

For a large backlog split the stream first (`split -l 10000` over the export, one `_bulk` call per part).

Loki (push API; entries are sent oldest first, each as a log line with its own time; Loki's `reject_old_samples` limit, 7 days by default, refuses older entries):

```sh
oa audit export --since 1h \
  | jq -cs 'reverse | {streams: [{stream: {job: "openartifact-audit"}, values: map([(.timestamp | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601 | tostring) + "000000000", tostring])}]}' \
  | curl -fsS -H "Content-Type: application/json" --data-binary @- http://loki.example.com:3100/loki/api/v1/push
```

Behaviour to know:

- Writes go through a buffered queue (1000 events). When it is full a request waits up to 2 s, then the event is dropped and `openartifact_audit_dropped_total` increases. The queue is flushed on graceful shutdown (5 s).
- Entries are kept forever unless you set `audit.retentionDays` (`OPENARTIFACT_AUDIT_RETENTION_DAYS`): a worker then deletes older ones in paced batches. Nothing is exported first, so run `oa audit export` beforehand; see [audit log retention](audit-retention.md).
- Audit logs are not part of [backups](backup.md).

## Logging

Logs are structured, written by zap. With [`telemetry.otlp.logs`](#log-records-over-otlp) they are also exported over OTLP; the output below does not change.

| Environment variable | YAML key (`log:`) | Values | Default |
|---|---|---|---|
| `LOG_LEVEL` | `level` | `debug`, `info`, `warn`, `error`; anything else is `info` | `info` |
| `LOG_FORMAT` | `format` | `json`; any other value gives human-readable console output (the example config says `text`) | `json` |
| `LOG_OUTPUT` | `output` | `stdout`, `stderr`, or a file path (created, appended, mode `0644`, no rotation) | `stdout` |

Every request is logged at `info` as `HTTP request` with `request_id`, `method`, `path` (without the query string, which may carry credentials), `status`, `duration_ms` and `remote_addr`. Each response carries `X-Request-ID` (generated per request, an incoming value is replaced) and `X-Trace-Id` (taken from the request if present, otherwise generated), so a client report can be matched to a log line. Panics are recovered, logged at `error` with the request ID, and answered with `500`.

Other lines worth alerting on: startup warnings about `OPENARTIFACT_SECRET_KEY`, `/metrics is disabled`, `pprof enabled`, webhook delivery failures, and `restore completed with failures` ([backup](backup.md)).

Keep `LOG_OUTPUT=stdout` in containers and let the runtime collect the stream. A file output is opened once and never rotated by the server.
