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
| `GET /ready` | `200 {"status":"ready"}` | `503 {"status":"not ready","reason":"database unavailable"}` or `"storage unavailable"` |

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
| `openartifact_http_requests_total` | counter | `method`, `path`, `status` | `path` is the route pattern (trailing `/*` removed), or `unknown`, so cardinality stays bounded |
| `openartifact_http_request_duration_seconds` | histogram | `method`, `path` | buckets 0.005 to 10 s |
| `openartifact_uploads_total` | counter | `format`, `result` | `result` is `success` or `error`; only the API upload handlers for `deb`, `npm`, `rpm` and `maven` count here |
| `openartifact_blob_bytes_total` | counter | `operation` | bytes written (`put`) and read (`get`) through the blob store |
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
| `openartifact_blob_deletions_swept_total` | counter | `result` | sweeper decisions: `deleted` (bytes removed) or `kept` (the blob was referenced or placed there again, entry dropped) |

Useful queries:

```promql
# error ratio per route
sum by (path) (rate(openartifact_http_requests_total{status=~"5.."}[5m]))
  / sum by (path) (rate(openartifact_http_requests_total[5m]))

# p95 latency per route
histogram_quantile(0.95, sum by (le, path) (rate(openartifact_http_request_duration_seconds_bucket[5m])))

# audit events being dropped (should stay 0)
increase(openartifact_audit_dropped_total[1h]) > 0
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

Server spans are marked as errors for a `5xx` status (a panic is recorded as `500`), client spans for transport errors and `4xx`/`5xx` responses.

Attributes: `http.request.method`, `url.scheme`, `network.protocol.version`, `http.route`, `http.response.status_code` on server spans; `http.request.method`, `url.scheme`, `server.address`, `server.port`, `http.response.status_code`, `error.type` on client spans; `db.system.name`, `db.operation.name`, `db.query.summary`, `db.response.status_code` (SQLSTATE) on database spans. **Nothing else is recorded**: no path or query string of the request (tokens travel in them), no `Authorization` or any other header, no client IP, no user name, no SQL text or arguments, no error messages (they quote URLs and values; only the Go type or SQLSTATE is kept).

### Propagation

The W3C `traceparent`, `tracestate` and `baggage` headers of an incoming request are honored, so OpenArtifact joins the caller's trace (a CI job, a reverse proxy that traces). The same headers are added to **every outbound request** made under that context, including those to external upstreams, webhook endpoints and AI providers: they identify the trace, but `baggage` carries whatever the caller put in it. Strip `baggage` at your ingress if clients may send values that must not reach third parties. With tracing enabled the `X-Trace-Id` response header carries the W3C trace id (it is otherwise a random id, or the one the client sent).

### Log correlation

With tracing enabled every `HTTP request` log line carries `trace_id` and `span_id`, the same ids as the exported spans and `X-Trace-Id`:

```json
{"message":"HTTP request","method":"GET","path":"/repos/npm/left-pad","status":200,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
```

The fields are absent while tracing is disabled.

### Metrics over OTLP

Prometheus stays the primary interface and `/metrics` is unchanged. With `metrics: true` (or `OTEL_METRICS_EXPORTER=otlp`) the same registry (`openartifact_*`, `go_*`, `process_*`) is also pushed to the collector through the OpenTelemetry Prometheus bridge, with the same endpoint, protocol and headers: no second set of instruments exists, so the two views cannot disagree.

### Overhead and binary size

Enabling the OTLP exporters adds the OpenTelemetry SDK and the OTLP protobuf definitions to the server binary (about 14 MB: the stripped server binary grows from 32.3 to 45.9 MB, and `go list -m all` from 159 to 217 modules, most of them OpenTelemetry, gRPC and protobuf). With tracing disabled the runtime cost is one atomic load per outbound request and database statement; the HTTP middleware is not installed at all. Lower `sampleRatio` on busy instances: every proxied download is a request.

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
- Named actions: `repository.created`, `repository.deleted`, `repository.restored`, `repository.purged`, `artifact.uploaded` (deb upload through `POST /api/v1/repositories/{name}/debs` only), `artifact.deleted` (details `moved to trash` or `deleted`), `artifact.restored`, `artifact.purged` (see [trash](trash.md#audit-and-webhook-events)), `auth.password_changed`, `auth.token_created`, `auth.token_revoked`. A rejected `auth.*` request is recorded as `http.request` instead. Admin changes also write an explicit event naming the target (`resource_type`, `resource_id`, and the changed fields, never secrets): `user.created`, `user.updated` (changed fields, e.g. `password_reset`, `disabled=true`), `user.disabled`, `group.created`, `group.updated`, `group.deleted`, `group.member_added`, `group.member_removed`, `group.members_set`, `permission_target.created`, `permission_target.updated`, `permission_target.deleted`, `permission_target.assigned`, `permission_target.unassigned`, `sso_provider.created`, `sso_provider.updated`, `sso_provider.deleted` and `token.created_for_user` (a token minted for another user). Denied (403) requests are not audited. Every other write, including format uploads such as `PUT /repos/...`, is `http.request`.
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

Behaviour to know:

- Writes go through a buffered queue (1000 events). When it is full a request waits up to 2 s, then the event is dropped and `openartifact_audit_dropped_total` increases. The queue is flushed on graceful shutdown (5 s).
- There is no retention or purge: `audit_logs` grows until you delete rows yourself (for example with a scheduled SQL `DELETE ... WHERE created_at < now() - interval '1 year'`).
- Audit logs are not part of [backups](backup.md).
- The `oa audit list` command does not work against this endpoint (it sends `limit` and `user` and reads `entries`); use the route above.

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
