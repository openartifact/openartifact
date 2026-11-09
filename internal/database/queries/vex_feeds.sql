-- name: ListVEXFeeds :many
SELECT sqlc.embed(f),
       (SELECT count(*) FROM vex_feed_statements s WHERE s.feed_id = f.id)::int AS statement_count,
       (SELECT count(*) FROM vex_statements v WHERE v.feed_id = f.id)::int AS applied_count
FROM vex_feeds f
ORDER BY f.name;

-- name: GetVEXFeed :one
SELECT sqlc.embed(f),
       (SELECT count(*) FROM vex_feed_statements s WHERE s.feed_id = f.id)::int AS statement_count,
       (SELECT count(*) FROM vex_statements v WHERE v.feed_id = f.id)::int AS applied_count
FROM vex_feeds f
WHERE f.id = $1;

-- name: CreateVEXFeed :one
INSERT INTO vex_feeds (name, url, enabled, require_signature, pgp_keys, interval_hours)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateVEXFeed :one
-- The editable fields. A new URL is another provider, and new keys or a
-- relaxed signature requirement may accept documents rejected before: the
-- cursor starts over in all three cases.
UPDATE vex_feeds SET
    name = $2,
    url = $3,
    enabled = $4,
    require_signature = $5,
    pgp_keys = $6,
    interval_hours = $7,
    cursor_at = CASE WHEN url = $3 AND require_signature = $5 AND pgp_keys = $6 THEN cursor_at END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteVEXFeed :execrows
DELETE FROM vex_feeds WHERE id = $1;

-- name: DeleteVEXFeedDocument :exec
DELETE FROM vex_feed_statements WHERE feed_id = $1 AND document_url = $2;

-- name: InsertVEXFeedStatements :exec
-- The statements of one document; released_at '' is NULL.
INSERT INTO vex_feed_statements (feed_id, document_url, tracking_id, vuln_id, purl, cpe, status,
                                 justification, impact_statement, action_statement, released_at)
SELECT sqlc.arg(feed_id)::uuid, sqlc.arg(document_url)::text, u.tracking_id, u.vuln_id, u.purl, u.cpe, u.status,
       u.justification, u.impact_statement, u.action_statement, NULLIF(u.released_at, '')::timestamptz
FROM (SELECT unnest(sqlc.arg(tracking_ids)::text[]) AS tracking_id,
             unnest(sqlc.arg(vuln_ids)::text[]) AS vuln_id,
             unnest(sqlc.arg(purls)::text[]) AS purl,
             unnest(sqlc.arg(cpes)::text[]) AS cpe,
             unnest(sqlc.arg(statuses)::text[]) AS status,
             unnest(sqlc.arg(justifications)::text[]) AS justification,
             unnest(sqlc.arg(impact_statements)::text[]) AS impact_statement,
             unnest(sqlc.arg(action_statements)::text[]) AS action_statement,
             unnest(sqlc.arg(released_ats)::text[]) AS released_at) u;

-- name: ListVEXFeedStatements :many
SELECT document_url, tracking_id, vuln_id, purl, cpe, status, justification,
       impact_statement, action_statement, released_at
FROM vex_feed_statements
WHERE feed_id = $1
ORDER BY id;

-- name: RecordVEXFeedSync :execrows
UPDATE vex_feeds SET
    last_attempt_at = now(),
    last_error = sqlc.arg(last_error)::text,
    last_success_at = CASE WHEN sqlc.arg(success)::boolean THEN now() ELSE last_success_at END,
    cursor_at = CASE WHEN sqlc.arg(success)::boolean THEN COALESCE(sqlc.narg(cursor_at)::timestamptz, cursor_at) ELSE cursor_at END,
    documents_total = documents_total + CASE WHEN sqlc.arg(success)::boolean THEN sqlc.arg(documents)::int ELSE 0 END,
    rejected_total = rejected_total + CASE WHEN sqlc.arg(success)::boolean THEN sqlc.arg(rejected)::int ELSE 0 END
WHERE id = sqlc.arg(id)::uuid;
