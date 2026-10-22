<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Monitoring and logging

Health and readiness endpoints, Prometheus metrics, the audit log and server logs.

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

## Audit log

The server records who changed what. Events are stored in the `audit_logs` table and shown under Admin, Audit in the UI.

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
