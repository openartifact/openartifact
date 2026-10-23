<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Monitoring and logging

Health and readiness endpoints, Prometheus metrics, OpenTelemetry tracing, the audit log and server logs.

The REST API, OpenAPI spec and Swagger UI are covered in [API](../integrations/api.md).

## Health and readiness

`/health`, `/ready` and `/metrics` are on the public router: no login, no RBAC. They still pass through the server-wide middleware, so `OPENARTIFACT_IP_ALLOWLIST` and `OPENARTIFACT_RATE_LIMIT_RPS` apply to probes and scrapers too.

| Endpoint | Success | Failure |
|---|---|---|
| `GET /health` | always `200`, JSON | never fails; a broken dependency shows as `"status":"degraded"` |
| `GET /ready` | `200 {"status":"ready"}` | `503 {"status":"not ready","reason":"database unavailable"}`, `"storage unavailable"` or `"storage tiers not loaded"` |

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

What is checked: the database is pinged. Storage is checked (bucket access) only for the S3-family backends `s3`, `garage`, `seaweedfs` and `gcs`. Filesystem, Azure and OCI always report `ok`. See [storage](storage.md).

The compose healthcheck is `wget --spider http://localhost:8080/health`.

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
| `openartifact_uploads_total` | counter | `format`, `result` | hosted upload requests: `format` is one of `deb`, `rpm`, `npm`, `maven`, `helm`, `rubygems`, `docker`, `pypi`, `cargo`, `go`, `nuget`, `conda`, `conan`, `ivy`, `sbt`, `raw`; `result` is `success` (HTTP status below 400) or `error` (400 and above). See [what counts as an upload](#what-counts-as-an-upload) |
| `openartifact_blob_bytes_total` | counter | `operation` | bytes written (`put`) and read (`get`) through the blob store |
| `openartifact_blob_operation_duration_seconds` | histogram | `operation`, `result` | latency of the object store as the server sees it, by `operation` (`put`, `get`, `exists`, `exists_by_sha256`, `delete`, `size`, `metadata`) and `result` (`ok`, `error`). This is the floor under every upload: on a store that syncs to a replicated volume it is tens of milliseconds, which is why every write route has the same p50. `put` includes reading the client's body, so a slow client inflates it; `get` is the time to open the object, not to stream it |
| `openartifact_blob_downloads_total` | counter | `mode`, `reason` | blob `GET`s by delivery: `mode=redirect` (`reason=presigned`) or `mode=stream` (`disabled`, `unsupported`, `below_min_size`, `sign_failed`, `repo_lookup_failed`, `missing_object`); see [presigned URLs](storage.md#presigned-urls) |
| `openartifact_metadata_regenerations_total` | counter | `format` | `rpm`, `maven`, `npm` |
| `openartifact_metadata_regeneration_duration_seconds` | histogram | `format` | same buckets as the request histogram |
| `openartifact_audit_dropped_total` | counter | | audit events lost to backpressure or after shutdown |
| `openartifact_trash_purged_total` | counter | `kind` | trashed items (and repositories, `kind="repository"`) permanently removed by the retention purge; `kind` is the trash kind, e.g. `maven_artifact`, `docker_manifest` |
| `openartifact_storage_tier_blobs`, `openartifact_storage_tier_bytes` | gauge | `tier` | blobs and bytes placed on each [storage tier](storage-tiering.md#metrics), refreshed after every mover pass |
| `openartifact_storage_tier_reads_total` | counter | `tier` | blob reads served by each tier |
| `openartifact_storage_tier_moves_total` | counter | `from`, `to`, `result` | tier moves; `result` is `moved`, `skipped` or `failed` |
| `openartifact_storage_tier_moved_bytes_total` | counter | `from`, `to` | bytes moved between tiers |
| `openartifact_storage_tier_moves_pending` | gauge | | interrupted or failing moves awaiting recovery; should return to 0 |
| `openartifact_blob_deletions_pending` | gauge | | blob copies queued for [deferred deletion](storage.md#deferred-blob-deletion), refreshed after every sweeper pass; grows during the grace period and while a backup runs, should fall back afterwards |
| `openartifact_scanner_install_attempts_total` | counter | `scanner`, `result` | runtime installs of Trivy / Grype (`scanners.<name>.install: auto`): `installed`, `reused` (a verified copy was already there), `unsupported_platform`, `download_failed`, `checksum_mismatch`, `extract_failed`, `noexec`, `insecure_location`, `failed` |
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
- `helm`, `rubygems`, `pypi`, `cargo`, `nuget`, `conda`, `conan`, `ivy`, `sbt` and `raw` (direct `PUT`) are counted by the HTTP middleware from the response status of the upload route (`POST /charts`, `PUT /api/v1/crates/new`, `POST /legacy/`, ...).
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

## OpenTelemetry tracing

OpenArtifact can export distributed traces over OTLP to any collector (OpenTelemetry Collector, Jaeger, Tempo, Honeycomb, ...). It is **off by default**: while disabled no provider is installed, no span is created and no `traceparent` header is sent.

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
    metrics: false
```

| Key | Environment | Default | Notes |
|---|---|---|---|
| `enabled` | `OPENARTIFACT_OTLP_ENABLED` | `false` | the only switch; `OTEL_SDK_DISABLED=true` forces it off |
| `endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | `host:port` or `http(s)://host:port[/prefix]`; empty uses the SDK default (`localhost:4317` for gRPC, `localhost:4318` for HTTP). For `http` the `/v1/traces` (`/v1/metrics`) suffix is appended to the prefix |
| `protocol` | `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | `grpc`, or `http` (alias `http/protobuf`) |
| `headers` | `OTEL_EXPORTER_OTLP_HEADERS` | none | `k=v,k2=v2`, values percent-decoded; merged over the YAML map. Treated as secrets and never logged |
| `insecure` | `OTEL_EXPORTER_OTLP_INSECURE` | `false` | no TLS to the collector; an `http://` endpoint implies it |
| `serviceName` | `OTEL_SERVICE_NAME` | `openartifact` | `service.name` resource attribute |
| `sampleRatio` | `OTEL_TRACES_SAMPLER_ARG` | `1` | fraction of **new** traces recorded (0 to 1). Parent based: a request arriving with a sampled `traceparent` is always recorded, an unsampled one never |
| `metrics` | `OTEL_METRICS_EXPORTER=otlp` (`none` turns it off) | `false` | also export metrics over OTLP, see below |

Environment variables win over the YAML file. Setting only an `OTEL_EXPORTER_OTLP_*` variable does not enable tracing: set `enabled` too. The SDK also reads the rest of the standard environment where the table says nothing: `OTEL_RESOURCE_ATTRIBUTES` adds resource attributes, `OTEL_METRIC_EXPORT_INTERVAL` sets the metric interval, and the signal-specific `OTEL_EXPORTER_OTLP_TRACES_*` variables apply while `endpoint` is empty. `service.version` and, when `OPENARTIFACT_INSTANCE_ID` is set, `service.instance.id` are filled in. Export failures are logged as a warning at most once a minute.

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

The fields are absent while tracing is disabled.

### Metrics over OTLP

Prometheus stays the primary interface and `/metrics` is unchanged. With `metrics: true` (or `OTEL_METRICS_EXPORTER=otlp`) the same registry (`openartifact_*`, `go_*`, `process_*`) is also pushed to the collector through the OpenTelemetry Prometheus bridge, with the same endpoint, protocol and headers: no second set of instruments exists, so the two views cannot disagree.

### Overhead and binary size

The OpenTelemetry SDK and the OTLP exporters are always compiled in, enabled or not (about 14 MB: the stripped server binary grows from 32.3 to 45.9 MB, and `go list -m all` from 159 to 217 modules, most of them OpenTelemetry, gRPC and protobuf). With tracing disabled the runtime cost is one atomic load per outbound request and database statement; the HTTP middleware is not installed at all. Lower `sampleRatio` on busy instances: every proxied download is a request.

### Trying it locally

With the bundled compose file, add the variables to the `openartifact` service's `environment:` (the file passes only the variables it lists), for example `OPENARTIFACT_OTLP_ENABLED: "true"` and `OTEL_EXPORTER_OTLP_ENDPOINT: http://jaeger:4317` next to a Jaeger service on the same network.

```sh
docker run --rm -d --name jaeger -p 16686:16686 -p 4317:4317 -p 4318:4318 docker.io/jaegertracing/jaeger:latest
OPENARTIFACT_OTLP_ENABLED=true OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 ./bin/openartifact
# browse http://localhost:16686, service "openartifact"
```

## Audit log

The server records who changed what. Events are stored in the `audit_logs` table and shown under Admin, Audit in the UI.

Members of a project read the slice that concerns them (the project's own events and those of its repositories) without admin rights: see [project activity](../security/projects.md#project-activity).

Recorded:

- Every authenticated `POST`, `PUT` and `DELETE` request (reads are not recorded), with actor (username, or `anonymous`), client IP and a `details` object with `method`, `path` (the route pattern such as `/api/v1/repositories/{name}`, not the concrete URL), `status`, and `error: true` for status 400 and above.
- Named actions: `repository.created`, `repository.deleted`, `repository.restored`, `repository.purged`, `artifact.uploaded` (deb upload through `POST /api/v1/repositories/{name}/debs` only), `artifact.deleted` (details `moved to trash` or `deleted`), `artifact.restored`, `artifact.purged` (see [trash](trash.md#audit-and-webhook-events)), `bundle.promoted` (a release bundle promotion; resource is the bundle, repository the promotion target, details the stage and the counts), `auth.password_changed`, `auth.token_created`, `auth.token_revoked`. A rejected `auth.*` request is recorded as `http.request` instead. Admin changes also write an explicit event naming the target (`resource_type`, `resource_id`, and the changed fields, never secrets): `user.created`, `user.updated` (changed fields, e.g. `password_reset`, `disabled=true`), `user.disabled`, `group.created`, `group.updated`, `group.deleted`, `group.member_added`, `group.member_removed`, `group.members_set`, `permission_target.created`, `permission_target.updated`, `permission_target.deleted`, `permission_target.assigned`, `permission_target.unassigned`, `sso_provider.created`, `sso_provider.updated`, `sso_provider.deleted` and `token.created_for_user` (a token minted for another user). Denied (403) requests are not audited. Every other write, including format uploads such as `PUT /repos/...`, is `http.request`.
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

The response is `{"data":[...],"page":1,"per_page":25,"total":N}`, newest first. Each entry has `id`, `timestamp`, `action`, `actor`, `target`, `details`, `ip`, `repository_id`, `resource_type` and `resource_id`.

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

Members of a project export the project's own activity with `GET /api/v1/projects/{project}/activity/export` (same `format`, `since`, `until`, `actor` and `action` parameters, file name `openartifact-<project>-activity-<timestamp>.<ext>`). It applies the same visibility rules as the [project activity](../security/projects.md#project-activity) feed: `project.token_*` events only for maintainers and above and no client addresses. Its fields are `id`, `timestamp`, `action`, `actor`, `actor_type`, `message`, `resource_type`, `resource_id` and `repository_id`. The Admin, Audit page and the project's Activity section have **Export JSONL** and **Export CSV** buttons for the same downloads (the admin button applies the filters typed on the page).

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
- There is no retention or purge: `audit_logs` grows until you delete rows yourself (for example with a scheduled SQL `DELETE ... WHERE created_at < now() - interval '1 year'`).
- Audit logs are not part of [backups](backup.md).

## Logging

Logs are structured, written by zap.

| Environment variable | YAML key (`log:`) | Values | Default |
|---|---|---|---|
| `LOG_LEVEL` | `level` | `debug`, `info`, `warn`, `error`; anything else is `info` | `info` |
| `LOG_FORMAT` | `format` | `json`; any other value gives human-readable console output (the example config says `text`) | `json` |
| `LOG_OUTPUT` | `output` | `stdout`, `stderr`, or a file path (created, appended, mode `0644`, no rotation) | `stdout` |

Every request is logged at `info` as `HTTP request` with `request_id`, `method`, `path` (without the query string, which may carry credentials), `status`, `duration_ms` and `remote_addr`. Each response carries `X-Request-ID` (generated per request, an incoming value is replaced) and `X-Trace-Id` (taken from the request if present, otherwise generated), so a client report can be matched to a log line. Panics are recovered, logged at `error` with the request ID, and answered with `500`.

Other lines worth alerting on: startup warnings about `OPENARTIFACT_SECRET_KEY`, `/metrics is disabled`, `pprof enabled`, webhook delivery failures, and `restore completed with failures` ([backup](backup.md)).

Keep `LOG_OUTPUT=stdout` in containers and let the runtime collect the stream. A file output is opened once and never rotated by the server.
