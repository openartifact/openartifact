<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Webhooks

Webhooks send an HTTP callback to your own service when a repository or artifact event happens. Use them to trigger CI, notify a chat channel or feed an event bus.

Each delivery is signed with HMAC-SHA256, retried with backoff and recorded in a delivery log. A webhook is either global (every repository) or scoped to one repository. Management is admin only, in the UI under **Settings > Webhooks** (`/settings/webhooks`), with the `oa` CLI, or through the API.

## Create a webhook

With the CLI, a global webhook needs `--name`, `--url` and `--events`:

```sh
printf '%s' "<SECRET>" > webhook-secret.txt
oa webhook create --name ci --url https://ci.example.com/hook \
  --events artifact.uploaded,artifact.deleted --secret-file webhook-secret.txt
```

Flags of `oa webhook create`:

| Flag | Meaning |
|---|---|
| `--name` | required |
| `--url` | required |
| `--events` | required, comma-separated event names |
| `--secret-file` | file with the signing secret, or `-` for stdin. The environment variable `OA_WEBHOOK_SECRET` works too |
| `--active` | defaults to `true`; use `--active=false` to create it disabled |

The CLI has no `--secret` flag, because command-line arguments are visible in the process list.

For a single repository, use `oa webhook repo create REPO` with the same flags except `--active` (the webhook is always created enabled). `oa webhook repo list REPO` lists them.

Other subcommands:

```sh
oa webhook list
oa webhook show <ID>
oa webhook update <ID> --url https://ci.example.com/new --active=false
oa webhook test <ID>
oa webhook deliveries <ID>
oa webhook delete <ID>
```

`oa webhook update` changes only the flags you pass (`--name`, `--url`, `--events`, `--active`). Payload format, retries, timeout and TLS verification are set through the API or the UI.

The same with the API, including the optional fields:

```sh
curl -X POST HOST/api/v1/repositories/REPO/webhooks \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"name":"ci","url":"https://ci.example.com/hook","secret":"<SECRET>","events":["artifact.uploaded","artifact.deleted"]}'

# CloudEvents consumers (Knative, Argo Events, EventBridge pipes, ...)
curl -X POST HOST/api/v1/webhooks \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"name":"events","url":"https://broker.example.com/in","events":["artifact.uploaded"],"payload_format":"cloudevents"}'
```

## Test it and read the delivery log

Queue a test delivery, then read the log:

```sh
curl -X POST -H "Authorization: Bearer <TOKEN>" HOST/api/v1/webhooks/<ID>/test
curl -H "Authorization: Bearer <TOKEN>" "HOST/api/v1/webhooks/<ID>/deliveries?limit=20"
```

The test sends a `webhook.test` event, whatever the subscriptions and the enabled flag. It uses the webhook's payload format.

A log entry has these fields:

- `id`, `webhook_id`, `event_type` and `payload` (always the native payload).
- `status`: `pending`, `success` or `failed`.
- `response_status` and `response_body` (up to 64 KiB).
- `attempt_count`, `next_retry_at`, `delivered_at`, `created_at` and `updated_at`.

For a `pending` entry, `next_retry_at` is the next scheduled attempt. For a delivery just created it is the end of its lease.

The list takes `limit` from 1 to 500 (default 50).

### Delivery log retention

The server deletes finished entries (`success` or `failed`) older than 30 days, once an hour. Pending entries are never deleted. Deleting a webhook deletes its log.

Change the period with `OPENARTIFACT_WEBHOOK_DELIVERY_RETENTION_DAYS`, or `workers.webhookDeliveryRetentionDays` in the YAML configuration. `0` keeps the entries forever and a negative value is rejected at startup.

## Verify the signature

When a webhook has a secret, every delivery carries `X-Hub-Signature-256: sha256=<hex>`. It is the HMAC-SHA256 of the raw request body keyed with the secret, the same scheme GitHub webhooks use. The header is computed over the body actually sent, for every payload format.

Verify it before parsing the body, with a constant-time compare:

```python
import hashlib, hmac

def valid(body: bytes, header: str, secret: bytes) -> bool:
    expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header)
```

That header covers the body only, so a captured delivery stays valid forever. To reject replays, verify the newer pair instead:

- `X-OA-Timestamp`: the Unix time in seconds at which this attempt was sent. Every retry is signed again with a fresh timestamp.
- `X-OA-Signature-V2: sha256=<hex>`: the HMAC-SHA256, keyed with the secret, of `<timestamp>.<delivery id>.<body>`, where the delivery id is the `X-OA-Delivery` header.

Reject the delivery when the timestamp is more than 5 minutes away from your clock, and compare in constant time:

```python
import hashlib, hmac, time

def valid_v2(body: bytes, headers: dict, secret: bytes) -> bool:
    ts = headers["X-OA-Timestamp"]
    if abs(time.time() - int(ts)) > 300:
        return False
    signed = ts.encode() + b"." + headers["X-OA-Delivery"].encode() + b"." + body
    expected = "sha256=" + hmac.new(secret, signed, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, headers["X-OA-Signature-V2"])
```

Slack and Discord incoming webhooks ignore these headers. They are sent anyway.

## Events

| Event | Fired when |
|---|---|
| `artifact.uploaded` | an artifact is published to a repository |
| `artifact.deleted` | an artifact is deleted, softly or permanently |
| `artifact.restored` | an artifact is restored from the [trash](trash.md) |
| `artifact.purged` | a trashed artifact is removed for good |
| `repository.created` | a repository is created |
| `repository.deleted` | a repository is deleted, to the trash or permanently |
| `repository.restored` | a repository is restored from the trash |
| `repository.purged` | a trashed repository is removed for good |
| `bundle.promoted` | a [release bundle](../repositories/release-bundles.md) is promoted into the target repository of a pipeline stage |
| `scan.completed` | a vulnerability, license or secret scan stored its final result (vulnerability scans: `completed`, `partial` or `failed`) |
| `vulnerability.found` | a vulnerability scan that completed (or partly completed) has at least one finding |
| `quarantine.changed` | an artifact is quarantined or released, by a blocking license policy or an administrator's review |
| `bundle.check_updated` | a CI system reported a check against a [release bundle](../repositories/release-bundles.md#required-checks) |
| `webhook.test` | only by the test route; you cannot subscribe to it |

Notes on when events fire:

- `artifact.uploaded` fires for every format. Proxy cache fills do not fire it.
- `artifact.deleted` is not emitted for Go modules and Cargo.
- `artifact.purged` fires for a purge by an admin and by the retention purge.
- `bundle.promoted` goes to the webhooks of the target repository and to the global ones.
- `scan.completed`, `vulnerability.found` and `quarantine.changed` go to the webhooks of the scanned repository and to the global ones. A scan run through the API, the background worker or a license property change fires them exactly once per stored result. `vulnerability.found` is for a scan you requested; `vulnerability.new` is the OSV mirror finding a new advisory on an artifact scanned earlier.
- `quarantine.changed` fires only when the state changes: releasing an artifact that is not quarantined, or quarantining one that already is, fires nothing. A license scan that re-evaluates an artifact and leaves its quarantine as it was fires `scan.completed` alone.
- `bundle.check_updated` goes to the webhooks of the bundle's first source repository and to the global ones; a bundle without items reaches the global ones only. It fires for every report, including a repeat of the same status.
- Events are published after the operation succeeds. A failure to record an event never fails the upload or delete that caused it.

There is no `artifact.downloaded` event, because downloads are the highest-volume operation and each delivery costs a database insert and an HTTP request. Subscribing to it is rejected with 400. Use the `artifact.downloaded` action of the [audit log](monitoring.md) or the download metrics instead.

### Payloads

These are the native (`openartifact`) payloads. Other [formats](#payload-formats) re-render the same data.

Artifact events:

```json
{
  "event": "artifact.uploaded",
  "timestamp": "2026-01-02T15:04:05Z",
  "repository": "REPO",
  "format": "npm",
  "artifact": {
    "name": "left-pad",
    "path": "left-pad/-/left-pad-1.3.0.tgz",
    "version": "1.3.0",
    "sha256": "<hex>",
    "size": 4096
  }
}
```

`version`, `sha256` and `size` are omitted when the format or the operation does not provide them.

- Repository events carry `event`, `timestamp`, `repository`, `format` and `type` (`hosted`, `proxy` or `group`).
- `artifact.restored` and `artifact.purged` use the artifact payload. `repository.restored` and `repository.purged` use the repository payload. Only `event` differs. See [Trash](trash.md#audit-and-webhook-events).
- `bundle.promoted` carries the target repository's repository payload plus the fields below.
- The test event is `{"event":"webhook.test","message":"This is a test webhook delivery","timestamp":<unix seconds>}`.

Extra fields of `bundle.promoted`:

| Field | Meaning |
|---|---|
| `bundle` | `name` and `version` of the bundle |
| `from_stage` | the source stage, empty when the bundle had no stage |
| `to_stage` | the target stage |
| `items` | bundle items promoted |
| `copied` | artifacts newly written to the target |
| `existing` | artifacts the target already held with the same content |
| `replaced` | artifacts a forced promotion moved to the target's trash |

Scan and quarantine events carry the repository payload (`repository`, `format`, `type`) plus one block:

```json
{
  "event": "vulnerability.found",
  "timestamp": "2026-01-02T15:04:05Z",
  "repository": "npm-hosted", "format": "npm", "type": "hosted",
  "scan": {
    "id": "<scan id>", "kind": "vulnerability", "status": "completed",
    "artifact_id": "lodash@4.17.20", "package": "lodash", "version": "4.17.20",
    "counts": {"total": 3, "critical": 1, "high": 2, "medium": 0, "low": 0}
  }
}
```

`scan.completed` has the same shape. `kind` is `vulnerability`, `license` or `secret`; `status` is the stored scan status (`completed`, `partial`, `failed`; `clean` or `flagged` for secrets). `counts` holds `total` and the four severities for a vulnerability scan, `licenses` for a license scan and `findings` for a secret scan. `package` and `version` appear when the scan names them.

```json
{
  "event": "quarantine.changed",
  "timestamp": "2026-01-02T15:04:05Z",
  "repository": "npm-hosted", "format": "npm", "type": "hosted",
  "quarantine": {
    "state": "quarantined", "kind": "license", "artifact_id": "lodash@4.17.20",
    "reason": "blocking license policy", "actor": "alice"
  }
}
```

`state` is `quarantined` or `released`; `actor` is empty when the system acted.

`bundle.check_updated` carries the first source repository's repository payload (`type` is always `hosted`), the `bundle` (`name`, `version`) and the `check`:

```json
{
  "event": "bundle.check_updated",
  "timestamp": "2026-01-02T15:04:05Z",
  "repository": "build", "format": "raw", "type": "hosted",
  "bundle": {"name": "shop", "version": "2.4.0"},
  "check": {
    "name": "conforma", "status": "success", "summary": "all rules passed",
    "details_url": "https://ci.example.com/run/1", "reported_by": "tekton-ci",
    "expires_at": null
  }
}
```

## Payload formats

`payload_format` selects the body of every delivery of a webhook. The default is `openartifact`.

The delivery log always keeps the native payload. The format is applied when the request is sent, so a change also applies to pending retries.

| `payload_format` | Body | `Content-Type` |
|---|---|---|
| `openartifact` | the native payloads above | `application/json` |
| `cloudevents` | a [CloudEvents 1.0](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md) event in structured JSON mode | `application/cloudevents+json` |
| `slack` | a Slack [incoming webhook](https://api.slack.com/messaging/webhooks) message with Block Kit blocks | `application/json` |
| `teams` | a Teams Workflows ("When a Teams webhook request is received") message with an Adaptive Card 1.4 | `application/json` |
| `discord` | a Discord [webhook](https://discord.com/developers/docs/resources/webhook#execute-webhook) message with one embed | `application/json` |

The formats are fixed built-in templates. There is no user-supplied template language.

### CloudEvents

```json
{
  "specversion": "1.0",
  "id": "5b1f3c1e-0000-4000-8000-00000000d001",
  "source": "https://oa.example.com",
  "type": "io.openartifact.artifact.uploaded",
  "time": "2026-01-02T15:04:05Z",
  "subject": "npm-hosted/left-pad/-/left-pad-1.3.0.tgz",
  "datacontenttype": "application/json",
  "data": { "event": "artifact.uploaded", "timestamp": "2026-01-02T15:04:05Z", "repository": "npm-hosted", "format": "npm", "artifact": { "name": "left-pad", "path": "left-pad/-/left-pad-1.3.0.tgz" } }
}
```

- `id` is the delivery ID, identical on every retry. Use it to deduplicate.
- `source` is the server's public `BASE_URL` (default `http://localhost:8080`) without a trailing slash.
- `type` is `io.openartifact.` plus the event name, for example `io.openartifact.repository.created`.
- `time` is the event time in RFC 3339.
- `subject` is `<repository>/<path>` for artifact events and `<repository>` for repository events. It is absent for `webhook.test`.
- `data` is the native payload.

### Slack, Teams and Discord

Each message has a title (`Artifact uploaded`, `Repository deleted`, `Webhook test`, ...) and the event's facts: repository, format and type. Artifact events add name, version, path, SHA-256 and size.

Values are untrusted, because whoever uploads chooses the artifact name. They are escaped for the target service:

- **Slack**: `&`, `<` and `>` are entity-escaped, so `<!channel>`, `<@U123>` and `<url|text>` cannot form. Values are shown as inline code.
- **Discord**: values are inline code spans, and `allowed_mentions.parse` is empty, so nothing can ping `@everyone`, a role or a user.
- **Teams**: markdown characters are backslash-escaped, so values render literally, without links or emphasis.

Values longer than 1000 characters are truncated in all three.

## Retries

Each delivery is an HTTP `POST` of the formatted body with these headers:

| Header | Value |
|---|---|
| `Content-Type` | `application/json`, or `application/cloudevents+json` for the `cloudevents` format |
| `User-Agent` | `OpenArtifact-Webhook/1.0` |
| `X-Hub-Signature-256` | `sha256=<hex>`, only when the webhook has a secret |
| `X-OA-Timestamp` | Unix time in seconds of this attempt, only when the webhook has a secret |
| `X-OA-Signature-V2` | `sha256=<hex>` over `<timestamp>.<delivery id>.<body>`, only when the webhook has a secret |

A `2xx` response is a success. Any other outcome is a failed attempt: another status, a connection error, a timeout or a secret that cannot be decrypted. Redirects are never followed, so a `3xx` response is a failed attempt.

Retry behaviour:

- `max_retries` is the total number of attempts: 3 by default, 0 to 10. Values 0 and 1 both mean one attempt.
- After failed attempt `n`, the next one runs `30 * 2^(n-1)` seconds later, capped at 1 hour: 30 s, 1 min, 2 min, 4 min and so on.
- A retry can start up to 30 s after its scheduled time.
- A delivery ends as `success`, or as `failed` once its attempts are used up.
- `timeout_seconds` limits one attempt: default 30, clamped to 1 to 30.

Delivery is at least once. A delivery is stored before it is sent. If the server stops or its queue is full, the delivery stays `pending` and is sent later. If an instance crashes mid-delivery, another instance sends it again after 2 minutes. With several replicas, each pending delivery is claimed by one of them. Make receivers idempotent.

## Allowed target addresses

The URL must be `http` or `https`, and its host must resolve when the webhook is saved. The server rejects the URL if any resolved address is:

- unspecified, multicast or link-local, which includes the cloud metadata address `169.254.169.254` and `fd00:ec2::/32`. Always blocked.
- in a reserved IPv4 range, or an IPv6 form that embeds a blocked IPv4 address (NAT64, 6to4, Teredo). Always blocked.
- loopback, private (RFC 1918, `fc00::/7`) or carrier-grade NAT (`100.64.0.0/10`). Blocked unless the server runs with `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`.

Set that variable when the receiver is an internal CI or chat bridge. It also relaxes proxy repository upstreams, see [hosted, proxy, group](../repositories/hosted-proxy-group.md).

The check runs again at delivery time on the address actually dialed, so a name that later resolves to a blocked address cannot be reached. Environment proxy settings are ignored. `ssl_verify=false` only disables TLS certificate verification. The address checks still apply.

## API reference

| Route | Purpose |
|---|---|
| `GET /api/v1/webhooks` | list global webhooks, as `{"webhooks":[...]}` |
| `POST /api/v1/webhooks` | create a global webhook |
| `GET /api/v1/repositories/{name}/webhooks` | list a repository's webhooks |
| `POST /api/v1/repositories/{name}/webhooks` | create a repository webhook |
| `GET /api/v1/webhooks/{id}` | show one |
| `PUT /api/v1/webhooks/{id}` | update; omitted fields stay unchanged |
| `DELETE /api/v1/webhooks/{id}` | delete (`204`), including its delivery log |
| `POST /api/v1/webhooks/{id}/test` | queue a `webhook.test` delivery (`202`) |
| `GET /api/v1/webhooks/{id}/deliveries` | delivery log |

A repository webhook fires only for events in its repository. A global one fires for all repositories. Request bodies are limited to 64 KiB.

Fields:

| Field | On create | Notes |
|---|---|---|
| `name` | required | up to 255 characters |
| `url` | required | up to 2048 characters |
| `events` | required | 1 to 32 names from the events table, without `webhook.test`; duplicates are dropped |
| `secret` | optional | write-only, up to 512 characters |
| `enabled` | default `true` | |
| `content_type` | default `application/json` | no other value is accepted; the `cloudevents` format sends `application/cloudevents+json` regardless |
| `payload_format` | default `openartifact` | `openartifact`, `cloudevents`, `slack`, `teams` or `discord`; anything else is a 400 |
| `ssl_verify` | default `true` | |
| `max_retries` | default `3` | clamped to 0 to 10 |
| `timeout_seconds` | default `30` | clamped to 1 to 30 |

Responses carry `id`, `repository_id` (null for global), `name`, `url`, `enabled`, `events`, `content_type`, `payload_format`, `ssl_verify`, `max_retries`, `timeout_seconds`, `has_secret`, `created_at` and `updated_at`. The secret is never returned.

An empty `"secret": ""` in an update removes the signing secret.

## Secret storage

The secret is encrypted with AES-256-GCM using `OPENARTIFACT_SECRET_KEY` and decrypted only to sign a delivery. Without that key it is stored as plaintext and the server warns at startup.

If the key changes, signing fails and deliveries of secret-protected webhooks fail until you set the secret again. Details in [storage](storage.md#stored-secrets). [Backups](backup.md) drop webhook secrets, so set them again after a restore.
