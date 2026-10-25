-- name: CreatePackageDelta :exec
INSERT INTO package_deltas (id, repository_id, from_sha256, from_filename, to_sha256, to_filename, delta_sha256, delta_size, format)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (repository_id, from_sha256, to_sha256) DO NOTHING;

-- name: GetPackageDelta :one
SELECT id, repository_id, from_sha256, from_filename, to_sha256, to_filename, delta_sha256, delta_size, format, created_at
FROM package_deltas
WHERE repository_id = $1 AND from_sha256 = $2 AND to_sha256 = $3;
