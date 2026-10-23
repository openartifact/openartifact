<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Webhooks

HTTP callbacks for repository and artifact events, signed with HMAC-SHA256, retried with backoff and logged per delivery.

A webhook is either global (fires for events in every repository) or scoped to one repository. All management routes are admin only; they are also in the UI under Admin, Webhooks.

## Events

| Event | Fired when |
|---|---|
| `artifact.uploaded` | an artifact is published to a repository, in every format. Proxy cache fills do not fire it |
| `artifact.deleted` | an artifact is deleted, in every format except Go modules and Cargo, which do not emit it. It fires for both soft deletes (moved to the [trash](trash.md)) and permanent deletes |
| `artifact.restored` | an artifact is restored from the trash |
| `artifact.purged` | a trashed artifact is removed for good, by an admin or by the retention purge worker |
| `repository.created` | a repository is created |
| `repository.deleted` | a repository is deleted (moved to the trash, or permanently when its retention is 0) |
| `repository.restored` | a repository is restored from the trash |
| `repository.purged` | a trashed repository is removed for good |
| `webhook.test` | only by the test route; cannot be subscribed to |

Events are published after the operation succeeds. A failure to record an event never fails the upload or delete that caused it.

There is no `artifact.downloaded` event. A download is the highest-volume operation of an artifact manager (every CI job and every package manager resolve, every client retry and range request), and a delivery means one database insert plus one outbound HTTP request, so it is not offered; subscribing to it is rejected with 400. Use the [audit log](monitoring.md) (`artifact.downloaded` action) or the download metrics for that.

### Payloads

These are the native (`openartifact`) payloads. Other [formats](#payload-formats) wrap or re-render the same data.

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

`version`, `sha256` and `size` are omitted when the format or the operation does not provide them. Repository events carry `event`, `timestamp`, `repository`, `format` and `type` (`hosted`, `proxy` or `group`). `artifact.restored` and `artifact.purged` use the artifact payload and `repository.restored` and `repository.purged` the repository payload; only the `event` value differs. See [Trash](trash.md#audit-and-webhook-events). The test event is `{"event":"webhook.test","message":"This is a test webhook delivery","timestamp":<unix seconds>}`.

## Payload formats

`payload_format` selects the body of every delivery of a webhook (default `openartifact`). The stored delivery log always keeps the native payload; the format is applied when the request is sent, so changing it also applies to pending retries. The test delivery uses the webhook's format. The `X-Hub-Signature-256` header is computed over the body actually sent, for every format.

| `payload_format` | Body | `Content-Type` |
|---|---|---|
| `openartifact` | the native payloads above, unchanged | `application/json` |
| `cloudevents` | a [CloudEvents 1.0](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md) event in structured JSON mode | `application/cloudevents+json` |
| `slack` | a Slack [incoming webhook](https://api.slack.com/messaging/webhooks) message with Block Kit blocks | `application/json` |
| `teams` | a Teams Workflows ("When a Teams webhook request is received") message carrying an Adaptive Card 1.4 | `application/json` |
| `discord` | a Discord [webhook](https://discord.com/developers/docs/resources/webhook#execute-webhook) message with one embed | `application/json` |

The formats are fixed built-in templates; there is no user-supplied template language.

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

- `id` is the delivery ID, identical on every retry of that delivery: use it to deduplicate.
- `source` is the server's public `BASE_URL` (default `http://localhost:8080`), without a trailing slash; `type` is `io.openartifact.` plus the event name, e.g. `io.openartifact.repository.created` or `io.openartifact.webhook.test`.
- `time` is the event time as RFC 3339 (the test event's unix timestamp is converted).
- `subject` is `<repository>/<path>` for artifact events and `<repository>` for repository events; it is absent for `webhook.test`.
- `data` is the native payload.

The output is validated against the CloudEvents 1.0.2 JSON schema in the unit tests.

### Slack, Teams and Discord

Each shows a title (`Artifact uploaded`, `Repository deleted`, `Webhook test`, ...) and the event's facts: repository, format, type, and for artifact events the name, version, path, SHA-256 and size. Values are untrusted (an artifact name is chosen by whoever uploads), so they are escaped for the target service:

- **Slack**: `&`, `<` and `>` are entity-escaped (so `<!channel>`, `<@U123>` and `<url|text>` cannot form), and values are shown as inline code.
- **Discord**: values are inline code spans (markdown and mentions are literal), and `allowed_mentions.parse` is empty, so nothing can ping `@everyone`, a role or a user. A value is cut at 1000 characters (the embed limit is 1024).
- **Teams**: markdown characters in values are backslash-escaped, so they render literally with no links or emphasis. The Adaptive Card is validated against the official Adaptive Cards 1.5 schema in the unit tests.

Over-long values are truncated to 1000 characters in all three. Slack and Discord incoming webhooks do not verify the signature header; it is sent anyway and ignored. These services answer `2xx` on success and an error status otherwise, which the retry logic above handles as for any receiver.

## Request

Each delivery is an HTTP `POST` of the formatted body:

| Header | Value |
|---|---|
| `Content-Type` | `application/json`, or `application/cloudevents+json` for the `cloudevents` format |
| `User-Agent` | `OpenArtifact-Webhook/1.0` |
| `X-Hub-Signature-256` | `sha256=<hex>`, only when the webhook has a secret |

The signature is the HMAC-SHA256 of the raw request body keyed with the secret (the header name and format GitHub webhooks use). Verify it before parsing, with a constant-time compare:

```python
import hashlib, hmac

def valid(body: bytes, header: str, secret: bytes) -> bool:
    expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header)
```

A response with a `2xx` status is a success. Everything else (other status, connection error, timeout, a secret that cannot be decrypted) is a failed attempt. The response status and up to 64 KiB of the body are stored in the delivery log. Redirects are never followed (a replayed signed body must not reach a host the owner did not configure): a `3xx` response is a failed attempt.

## Retries

- `max_retries` is the total number of attempts: 3 by default, 0 to 10. Values 0 and 1 both mean a single attempt.
- After failed attempt `n`, the next one is scheduled `30 * 2^(n-1)` seconds later, capped at 1 hour: 30 s, 1 min, 2 min, 4 min and so on.
- Pending retries are picked up by a poller that runs every 30 s, so a retry can start up to 30 s after its scheduled time.
- A delivery ends as `success`, or `failed` once attempts are used up.
- Deliveries are written to the database before they are sent, handled by 8 workers from a queue of 1000, and leased for 2 minutes. If the queue is full or the server stops, the row stays `pending` and the poller sends it. With several replicas each pending delivery is claimed by exactly one of them.
- `timeout_seconds` is the per-attempt limit: default 30, clamped to 1 to 30.
- Delivery is at least once: a delivery claimed by an instance that crashes is sent again when its 2-minute lease expires, so receivers should be idempotent.

## Address validation (SSRF)

The URL must be `http` or `https` and its host must resolve (names are resolved when the webhook is saved). It is rejected if any resolved address is:

- unspecified, multicast or link-local, which includes the cloud metadata address `169.254.169.254` and `fd00:ec2::/32`: always blocked;
- reserved IPv4 ranges, and IPv6 forms that embed an IPv4 address (NAT64, 6to4, Teredo) pointing at a blocked IPv4 address: always blocked;
- loopback, private (RFC 1918, `fc00::/7`) or carrier-grade NAT (`100.64.0.0/10`): blocked unless the server runs with `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`. Set it when the receiver is an internal CI or chat bridge. The same switch relaxes proxy repository upstreams, see [hosted, proxy, group](../repositories/hosted-proxy-group.md).

The check runs again at delivery time on the address actually dialed, so a name that later resolves to a blocked address (DNS rebinding) cannot be reached. Environment proxy settings are ignored. `ssl_verify=false` only disables TLS certificate verification; the address checks still apply.

## Manage

| Route | Purpose |
|---|---|
| `GET /api/v1/webhooks` | list global webhooks, as `{"webhooks":[...]}` |
| `POST /api/v1/webhooks` | create a global webhook |
| `GET /api/v1/repositories/{name}/webhooks` | list a repository's webhooks |
| `POST /api/v1/repositories/{name}/webhooks` | create a repository webhook |
| `GET /api/v1/webhooks/{id}` | show one |
| `PUT /api/v1/webhooks/{id}` | update; omitted fields stay unchanged |
| `DELETE /api/v1/webhooks/{id}` | delete (`204`), including its delivery log |
| `POST /api/v1/webhooks/{id}/test` | queue a `webhook.test` delivery (`202`), regardless of subscriptions and the `enabled` flag |
| `GET /api/v1/webhooks/{id}/deliveries` | delivery log; `limit` 1 to 500, default 50. Finished (`success`/`failed`) entries older than `OPENARTIFACT_WEBHOOK_DELIVERY_RETENTION_DAYS` (default 30; `0` keeps them) are deleted hourly |

A repository webhook fires only for events in its repository; a global one fires for all repositories. Bodies are limited to 64 KiB.

Fields:

| Field | Create | Notes |
|---|---|---|
| `name` | required | up to 255 characters |
| `url` | required | up to 2048 characters, validated as above |
| `events` | required | 1 to 32 names from the table above (without `webhook.test`); duplicates are dropped |
| `secret` | optional | write-only, up to 512 characters |
| `enabled` | default `true` | |
| `content_type` | default `application/json` | no other value is accepted; the `cloudevents` format sends `application/cloudevents+json` regardless |
| `payload_format` | default `openartifact` | `openartifact`, `cloudevents`, `slack`, `teams` or `discord`; anything else is a 400 |
| `ssl_verify` | default `true` | |
| `max_retries` | default `3` | clamped to 0 to 10 |
| `timeout_seconds` | default `30` | clamped to 1 to 30 |

Responses carry `id`, `repository_id` (null for global), `name`, `url`, `enabled`, `events`, `content_type`, `payload_format`, `ssl_verify`, `max_retries`, `timeout_seconds`, `has_secret`, `created_at` and `updated_at`. The secret is never returned.

```sh
curl -X POST HOST/api/v1/repositories/REPO/webhooks \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"name":"ci","url":"https://ci.example.com/hook","secret":"<SECRET>","events":["artifact.uploaded","artifact.deleted"]}'

# CloudEvents consumers (Knative, Argo Events, EventBridge pipes, ...)
curl -X POST HOST/api/v1/webhooks \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"name":"events","url":"https://broker.example.com/in","events":["artifact.uploaded"],"payload_format":"cloudevents"}'

curl -X POST -H "Authorization: Bearer <TOKEN>" HOST/api/v1/webhooks/<ID>/test
curl -H "Authorization: Bearer <TOKEN>" "HOST/api/v1/webhooks/<ID>/deliveries?limit=20"
```

An empty `"secret": ""` in an update removes the signing secret.

## Delivery log

Each entry has `id`, `webhook_id`, `event_type`, `payload`, `status` (`pending`, `success` or `failed`), `response_status`, `response_body`, `attempt_count`, `next_retry_at`, `delivered_at`, `created_at` and `updated_at`. For a `pending` entry `next_retry_at` is the next scheduled attempt (or the lease end for a delivery just created). The log is kept until the webhook is deleted; there is no automatic purge.

## Secret storage

The secret is encrypted with AES-256-GCM before it is stored, using `OPENARTIFACT_SECRET_KEY`, and decrypted only to sign a delivery. Without that key it is stored as plaintext and the server warns at startup. If the key changes, signing fails and deliveries of secret-protected webhooks fail until the secret is set again. Details in [storage](storage.md#stored-secrets). [Backups](backup.md) drop webhook secrets, so set them again after a restore.

## Limits

- `oa webhook create` and `oa webhook update` send `active` and no `name` or `events`, and the server requires `name` and `events`, so create fails with 400. Use the routes above or the UI. `oa webhook list` reads an `active` field the server does not return (it is `enabled`).
