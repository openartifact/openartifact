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
| `openartifact_uploads_total` | counter | `format`, `result` | hosted upload requests: `format` is one of `deb`, `rpm`, `npm`, `maven`, `helm`, `rubygems`, `docker`, `pypi`, `cargo`, `go`, `nuget`, `conda`, `conan`, `apk`, `terraform`, `ivy`, `sbt`, `raw`; `result` is `success` (HTTP status below 400) or `error` (400 and above). See [what counts as an upload](#what-counts-as-an-upload) |
| `openartifact_blob_bytes_total` | counter | `operation` | bytes written (`put`) and read (`get`) through the blob store |
| `openartifact_blob_operation_duration_seconds` | histogram | `operation`, `result` | latency of the object store as the server sees it, by `operation` (`put`, `get`, `exists`, `exists_by_sha256`, `delete`, `size`, `metadata`) and `result` (`ok`, `error`). This is the floor under every upload: on a store that syncs to a replicated volume it is tens of milliseconds, which is why every write route has the same p50. `put` includes reading the client's body, so a slow client inflates it; `get` is the time to open the object, not to stream it |
| `openartifact_blob_downloads_total` | counter | `mode`, `reason` | blob `GET`s by delivery: `mode=redirect` (`reason=presigned`) or `mode=stream` (`disabled`, `unsupported`, `below_min_size`, `sign_failed`, `repo_lookup_failed`, `missing_object`); see [presigned URLs](storage.md#presigned-urls) |
| `openartifact_metadata_regenerations_total` | counter | `format` | `rpm`, `maven`, `npm` |
| `openartifact_metadata_regeneration_duration_seconds` | histogram | `format` | same buckets as the request histogram |
| `openartifact_audit_dropped_total` | counter | | audit events lost to backpressure or after shutdown: never written to `audit_logs` nor [streamed](#streaming-over-otlp) |
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

Other templates worth knowing: `/repos/{name}/*` (a publish to an npm or Go repository, `PUT`), `/repos/{name}/{pkg}` (npm packument), `/repos/{name}/{platform}` (conda), `/*` (the web UI).

#### What counts as an upload

`openartifact_uploads_total` counts one per hosted upload request that reaches an upload handler, whatever the client protocol, so every format has the same series:

- `deb`, `rpm` (API upload), `npm` (publish), `maven` (`PUT`), `raw` (presigned upload completion), and `go` (GOPROXY upload) are counted by their handlers.
- `helm`, `rubygems`, `pypi`, `cargo`, `nuget`, `conda`, `conan`, `apk`, `terraform`, `ivy`, `sbt` and `raw` (direct `PUT`) are counted by the HTTP middleware from the response status of the upload route (`POST /charts`, `PUT /api/v1/crates/new`, `POST /legacy/`, `PUT /_api/modules/...`, ...).
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
| `audit` | `OPENARTIFACT_OTLP_AUDIT` | `false` | every audit event as an OTLP log event (scope `openartifact.audit`), independent of `traces`; see [streaming the audit log](#streaming-over-otlp) |

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

With `logs` or `audit` on, one OTLP log provider is started on the same endpoint, protocol and headers (`/v1/logs` for `http`). `logs` is for the application log records, `audit` for the audit events under the instrumentation scope `openartifact.audit` (see [streaming the audit log](#streaming-over-otlp)); either works with `traces` off. The `audit_logs` table stays the source of truth for the audit trail.

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

Writing a record never waits for the collector. Records are queued for export (`OTEL_BLRP_MAX_QUEUE_SIZE`, 2048 by default); while the queue is full, because the collector is slow or unreachable, new records are dropped and `openartifact_telemetry_log_records_dropped_total` counts them by instrumentation scope. It should stay 0. The records of an export that finally fails (the exporter retries for up to a minute) are lost as well and are not counted: the failure only shows as the `OpenTelemetry export error` warning.

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

[`docker/otel-collector.yaml`](../../docker/otel-collector.yaml) is a baseline configuration: OTLP in on `localhost` (gRPC `4317`, HTTP `4318`; `OTELCOL_LISTEN_HOST` changes the bind address), the `memory_limiter` and `batch` processors, and OTLP/HTTP out to `OTELCOL_EXPORT_ENDPOINT` with the `Authorization` header taken from `OTELCOL_EXPORT_AUTHORIZATION`. It has a pipeline for traces, metrics and logs.

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

## Audit log

The server records who changed what. Events are stored in the `audit_logs` table and shown under **Settings > Audit log** (`/settings/audit`) in the UI.

Members of a project read the slice that concerns them (the project's own events and those of its repositories) without admin rights: see [project activity](../security/projects.md#project-activity).

Recorded:

- Every authenticated `POST`, `PUT`, `PATCH` and `DELETE` request (reads are not recorded), with actor (username, or `anonymous`), client IP and a `details` object with `method`, `path` (the route pattern such as `/api/v1/repositories/{name}`, not the concrete URL), `status`, and `error: true` for status 400 and above.
- Named actions: `repository.created`, `repository.deleted`, `repository.restored`, `repository.purged`, `artifact.uploaded` (deb upload through `POST /api/v1/repositories/{name}/debs` only), `artifact.deleted` (details `moved to trash` or `deleted`), `artifact.restored`, `artifact.purged` (see [trash](trash.md#audit-and-webhook-events)), `bundle.promoted` (a release bundle promotion; resource is the bundle, repository the promotion target, details the stage and the counts), `auth.password_changed`, `auth.token_created`, `auth.token_revoked`. A rejected `auth.*` request is recorded as `http.request` instead. Admin changes also write an explicit event naming the target (`resource_type`, `resource_id`, and the changed fields, never secrets): `user.created`, `user.updated` (changed fields, e.g. `password_reset`, `disabled=true`), `user.disabled`, `group.created`, `group.updated`, `group.deleted`, `group.member_added`, `group.member_removed`, `group.members_set`, `permission_target.created`, `permission_target.updated`, `permission_target.deleted`, `permission_target.assigned`, `permission_target.unassigned`, `sso_provider.created`, `sso_provider.updated`, `sso_provider.deleted` and `token.created_for_user` (a token minted for another user). Denied (403) requests are not audited. Every other write, including format uploads such as `PUT /repos/...`, is `http.request`.
- Scan events written by the [local OSV mirror](../security/scanning.md#local-osv-mirror): `scan.vuln_detected` (advisories that newly affect a stored package version; resource is the package, details the advisory ids) and `scan.vuln_resolved` (advisories withdrawn or no longer affecting it). Manual scans record `scan.vuln_detected` too. The mirror's first import of an ecosystem writes neither.
- Login traffic: `auth.login`, `auth.login_failed` (with the attempted username, never the password; the [OTLP stream](#streaming-over-otlp) leaves the username out), `auth.logout` and `auth.sso_login`. Requests answered with `429` are not recorded.

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
| `details` | the event details: a JSON object in JSONL, its JSON text in one CSV cell. Keys, each present only when set: `message`, `actor`, `package`, `version`, `subject`, `outcome` (`success` or `failure`), `username` (login name of the authenticated user), `token_id` (API token the request used), `request_id` (its `X-Request-ID`) and `status` (HTTP status, a number) |

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
- The entry id and `timestamp` are assigned when the event is recorded, before it is queued, so the [OTLP stream](#streaming-over-otlp) carries the same values.
- Entries are kept forever unless you set `audit.retentionDays` (`OPENARTIFACT_AUDIT_RETENTION_DAYS`): a worker then deletes older ones in paced batches. Nothing is exported first, so run `oa audit export` beforehand; see [audit log retention](audit-retention.md).
- Audit logs are not part of [backups](backup.md).

### Streaming over OTLP

With [`telemetry.otlp.enabled`](#opentelemetry) and `telemetry.otlp.audit` (`OPENARTIFACT_OTLP_AUDIT=true`) on, every audit event is also sent as an [OpenTelemetry log event](https://opentelemetry.io/docs/specs/otel/logs/data-model/#field-eventname) to the configured collector, continuously, instead of being pulled with `oa audit export`. It works with traces, metrics and application logs off. Syslog/CEF, Splunk HEC, Elasticsearch and Loki are then routing in the [Collector](#local-collector), not code in OpenArtifact.

Each event is one log record:

| Field | Value |
|---|---|
| instrumentation scope | `openartifact.audit` |
| event name | `openartifact.audit` for every action (event names carry no dynamic value; the action is an attribute) |
| timestamp | when the event was recorded: the entry's `timestamp` in `audit_logs` and the export, to the microsecond |
| trace id, span id | the request's span when traces are on, so an event links to its trace; empty otherwise |
| severity | `INFO` (`9`), `WARN` (`13`) when the outcome is `failure` |
| body | the details `message`, or the action when there is none |

Attributes, each present only when the event has a value. They are named after the [export](#export) fields and the `details` keys, so the stream and `oa audit export` describe an entry the same way. The set is a contract: attributes may be added, never renamed or removed.

| Attribute | Type | Meaning |
|---|---|---|
| `openartifact.audit.id` | string | entry UUID, the export `id`: the dedup key between the stream and the export |
| `openartifact.audit.action` | string | action name, for example `auth.login` |
| `openartifact.audit.outcome` | string | `success` or `failure` (an HTTP status of 400 and above, a failed login) |
| `openartifact.audit.actor` | string | username or name of the acting principal; `anonymous` for unauthenticated writes |
| `openartifact.audit.actor_type` | string | `user`, `system`, `token` or `sso` |
| `openartifact.audit.actor_id` | string | UUID of the acting user |
| `openartifact.audit.username` | string | login name of the authenticated user |
| `openartifact.audit.token_id` | string | UUID of the API token the request authenticated with |
| `openartifact.audit.resource_type`, `openartifact.audit.resource_id` | string | the object the event names |
| `openartifact.audit.repository_id`, `openartifact.audit.project_id` | string | UUIDs of the repository or project concerned |
| `openartifact.audit.package`, `openartifact.audit.version`, `openartifact.audit.subject` | string | package version the event concerns and the display name of what it acted on |
| `openartifact.audit.ip` | string | client address |
| `openartifact.audit.request_id` | string | `X-Request-ID` of the request, also in the `HTTP request` log line |
| `openartifact.audit.status` | int | HTTP status of the request, for events recorded by the HTTP middleware |

`auth.login_failed` events carry neither `actor` nor `username`: the attempted username is whatever the client typed, often a password. Correlate failed logins by `ip`; the `audit_logs` entry still names the attempted user for admins. A hash would not help: an unkeyed hash of a typed password is reversed by a dictionary.

Delivery is best effort, at most once per event. The event is sent after the database insert, whatever its result, so an event the database refused is still streamed. Nothing ever waits for the collector: an event that does not fit the export queue (collector slow or down) is dropped and counted in `openartifact_telemetry_log_records_dropped_total{scope="openartifact.audit"}`; the events of an export that finally fails after a minute of retries are lost without being counted. Events lost before the writer (`openartifact_audit_dropped_total`) are neither stored nor streamed. Fill a gap from the source of truth with `oa audit export --since <start of the gap>` and drop the duplicates on `id` (`openartifact.audit.id` in the stream).

Alert on:

```promql
increase(openartifact_telemetry_log_records_dropped_total{scope="openartifact.audit"}[15m]) > 0
```

#### Volume

Every authenticated `POST`, `PUT`, `PATCH` and `DELETE` that has no named action is an `http.request` event, which includes every file of every package upload (a Maven deploy is one per file). The stream sends them because the database stores them; drop them in the Collector if the SIEM only needs the named actions. The rate is bounded by the audit writer, which streams at the speed it inserts.

#### Collector routing

This Collector configuration sends audit events to Splunk (HTTP Event Collector) and to Loki (native OTLP ingestion, Loki 3), without the `http.request` events, and every other log record to an OTLP backend. The `routing` connector selects on the instrumentation scope:

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: localhost:4317

processors:
  batch: {}
  # Optional: drop the generic http.request events (one per write request).
  filter/audit-http-request:
    error_mode: ignore
    logs:
      log_record:
        - attributes["openartifact.audit.action"] == "http.request"

connectors:
  # Audit events go to logs/audit, every other log record to logs/app.
  routing/audit:
    default_pipelines: [logs/app]
    table:
      - context: log
        condition: instrumentation_scope.name == "openartifact.audit"
        pipelines: [logs/audit]

exporters:
  splunk_hec/audit:
    endpoint: https://splunk.example.com:8088/services/collector
    token: ${env:SPLUNK_HEC_TOKEN}
    source: openartifact
    sourcetype: openartifact:audit
    index: audit
  otlphttp/loki:
    endpoint: http://loki.example.com:3100/otlp
  otlphttp/app:
    endpoint: https://otlp.example.com

service:
  pipelines:
    logs:
      receivers: [otlp]
      exporters: [routing/audit]
    logs/audit:
      receivers: [routing/audit]
      processors: [filter/audit-http-request, batch]
      exporters: [splunk_hec/audit, otlphttp/loki]
    logs/app:
      receivers: [routing/audit]
      processors: [batch]
      exporters: [otlphttp/app]
```

Check it with `otelcol-contrib validate --config <file>`. Splunk indexes the attributes as fields and takes the event time from the record. Loki stores them as structured metadata (`openartifact_audit_action` and so on) and labels the stream with `service_name`, so `{service_name="openartifact"} | openartifact_audit_action="auth.login_failed"` finds failed logins. Syslog/CEF works the same way with the Collector's `syslog` exporter. The baseline [`docker/otel-collector.yaml`](../../docker/otel-collector.yaml) has no routing: audit events go to `OTELCOL_EXPORT_ENDPOINT` with the other logs.

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
