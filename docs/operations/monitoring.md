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
