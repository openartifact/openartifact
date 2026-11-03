-- The repo_stats rollup of the repository list: the marks writers leave in
-- repo_stats_dirty (see migration 011), and the rows the repo stats worker
-- stores. The figures themselves come from RepoSecurityCounts
-- (catalog_security.sql) and the catalog package queries (catalog_sql.go).

-- name: ClaimRepoStatsMarks :many
-- Deletes the marks of up to batch repositories, the longest waiting first, and
-- returns those repositories. Run it in the transaction that stores their
-- recomputed figures, so a failed pass leaves the marks in place.
WITH picked AS (
    SELECT d.repository_id
    FROM repo_stats_dirty d
    GROUP BY d.repository_id
    ORDER BY min(d.marked_at)
    LIMIT sqlc.arg(batch)::int
), claimed AS (
    DELETE FROM repo_stats_dirty d
    USING picked p
    WHERE d.repository_id = p.repository_id
    RETURNING d.repository_id
)
SELECT DISTINCT c.repository_id::uuid AS repository_id FROM claimed c;

-- name: ListDriftedRepoStats :many
-- Repositories whose figures were never computed or were last computed more than
-- max_age_seconds ago, the oldest first: the repair of drift no mark announced (rows
-- written with the triggers disabled, a restore from a database backup).
SELECT r.id::uuid AS repository_id
FROM repositories r
LEFT JOIN repo_stats s ON s.repository_id = r.id
WHERE r.deleted_at IS NULL
  AND (s.repository_id IS NULL OR s.refreshed_at < now() - make_interval(secs => sqlc.arg(max_age_seconds)::float8))
ORDER BY s.refreshed_at NULLS FIRST, r.id
LIMIT sqlc.arg(batch)::int;

-- name: UpsertRepoStats :exec
-- Stores the recomputed figures of one repository; nothing for a repository
-- deleted in the meantime.
INSERT INTO repo_stats (repository_id, package_count, last_published_at,
                        vuln_critical, vuln_high, vuln_medium, vuln_low, secrets, license_violations, quarantined,
                        scanned_at, refreshed_at)
SELECT r.id, sqlc.arg(package_count)::bigint, sqlc.narg(last_published_at)::timestamptz,
       sqlc.arg(vuln_critical)::bigint, sqlc.arg(vuln_high)::bigint, sqlc.arg(vuln_medium)::bigint, sqlc.arg(vuln_low)::bigint,
       sqlc.arg(secrets)::bigint, sqlc.arg(license_violations)::bigint, sqlc.arg(quarantined)::bigint,
       sqlc.narg(scanned_at)::timestamptz, now()
FROM repositories r
WHERE r.id = sqlc.arg(repository_id)::uuid
ON CONFLICT (repository_id) DO UPDATE SET
    package_count = EXCLUDED.package_count,
    last_published_at = EXCLUDED.last_published_at,
    vuln_critical = EXCLUDED.vuln_critical,
    vuln_high = EXCLUDED.vuln_high,
    vuln_medium = EXCLUDED.vuln_medium,
    vuln_low = EXCLUDED.vuln_low,
    secrets = EXCLUDED.secrets,
    license_violations = EXCLUDED.license_violations,
    quarantined = EXCLUDED.quarantined,
    scanned_at = EXCLUDED.scanned_at,
    refreshed_at = EXCLUDED.refreshed_at;
