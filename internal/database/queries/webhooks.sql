-- name: CreateWebhook :one
INSERT INTO webhooks (repository_id, name, url, secret_enc, enabled, events, content_type, payload_format, ssl_verify, max_retries, timeout_seconds)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, created_at, updated_at;

-- name: GetWebhook :one
SELECT id, COALESCE(repository_id::text, '') AS repository_id, name, url, COALESCE(secret_enc, '') AS secret_enc, enabled, COALESCE(events, '{}') AS events, content_type, payload_format, ssl_verify, max_retries, timeout_seconds, created_at, updated_at
FROM webhooks WHERE id = $1;

-- name: UpdateWebhook :exec
UPDATE webhooks
SET name = $2, url = $3, secret_enc = $4, enabled = $5, events = $6, content_type = $7, payload_format = $8, ssl_verify = $9, max_retries = $10, timeout_seconds = $11, updated_at = now()
WHERE id = $1;

-- name: DeleteWebhook :exec
DELETE FROM webhooks WHERE id = $1;

-- name: ListGlobalWebhooks :many
SELECT id, COALESCE(repository_id::text, '') AS repository_id, name, url, COALESCE(secret_enc, '') AS secret_enc, enabled, COALESCE(events, '{}') AS events, content_type, payload_format, ssl_verify, max_retries, timeout_seconds, created_at, updated_at
FROM webhooks WHERE repository_id IS NULL ORDER BY created_at DESC;

-- name: ListWebhooksByRepo :many
SELECT id, COALESCE(repository_id::text, '') AS repository_id, name, url, COALESCE(secret_enc, '') AS secret_enc, enabled, COALESCE(events, '{}') AS events, content_type, payload_format, ssl_verify, max_retries, timeout_seconds, created_at, updated_at
FROM webhooks WHERE repository_id = $1 ORDER BY created_at DESC;

-- name: ListWebhooksForEvent :many
-- event_type must stay an explicit ::text scalar: with a bare `$2 = ANY(events)`
-- sqlc infers the array column's type (generating a []string param) while
-- Postgres types the placeholder as text, so pgx fails to encode the argument.
SELECT id, COALESCE(repository_id::text, '') AS repository_id, name, url, COALESCE(secret_enc, '') AS secret_enc, enabled, COALESCE(events, '{}') AS events, content_type, payload_format, ssl_verify, max_retries, timeout_seconds, created_at, updated_at
FROM webhooks
WHERE (repository_id = sqlc.narg(repository_id) OR repository_id IS NULL) AND enabled = true AND sqlc.arg(event_type)::text = ANY(events)
ORDER BY created_at DESC;

-- name: CreateWebhookDelivery :one
-- next_retry_at doubles as the claim lease: a delivery created by Dispatch is
-- inserted already leased to the creating instance so a concurrent poller on
-- another instance cannot pick it up.
INSERT INTO webhook_deliveries (webhook_id, event_type, payload, status, attempt_count, next_retry_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at, updated_at;

-- name: UpdateWebhookDelivery :exec
UPDATE webhook_deliveries
SET status = $2, response_status = $3, response_body = $4, attempt_count = $5, next_retry_at = $6, delivered_at = $7, payload = $8, updated_at = now()
WHERE id = $1;

-- name: ListWebhookDeliveries :many
SELECT id, webhook_id, event_type, payload, status, response_status, response_body, attempt_count, next_retry_at, delivered_at, created_at, updated_at
FROM webhook_deliveries WHERE webhook_id = $1 ORDER BY created_at DESC LIMIT $2;

-- name: ClaimPendingWebhookDeliveries :many
-- Atomically claims due deliveries by pushing next_retry_at to the end of the
-- lease. FOR UPDATE SKIP LOCKED lets several server instances poll the same
-- table without ever receiving the same row; a crashed instance's claims become
-- due again once the lease expires.
UPDATE webhook_deliveries
SET next_retry_at = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8), updated_at = now()
WHERE id IN (
    SELECT wd.id FROM webhook_deliveries wd
    WHERE wd.status = 'pending' AND (wd.next_retry_at IS NULL OR wd.next_retry_at <= now())
    ORDER BY wd.created_at ASC
    LIMIT sqlc.arg(batch_size)::int
    FOR UPDATE SKIP LOCKED
)
RETURNING id, webhook_id, event_type, payload, status, response_status, response_body, attempt_count, next_retry_at, delivered_at, created_at, updated_at;
