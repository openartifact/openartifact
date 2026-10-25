-- name: GetRPMRepo :one
SELECT id, repository_id, base_arch, metadata_stale,
       created_at, updated_at
FROM rpm_repos
WHERE repository_id = $1;

-- name: CreateRPMRepo :one
INSERT INTO rpm_repos (repository_id, base_arch)
VALUES ($1, $2)
RETURNING id, created_at, updated_at;

-- name: MarkRepoStale :exec
UPDATE rpm_repos SET metadata_stale = true WHERE repository_id = $1;

-- name: GetRepoMetadataByPath :one
SELECT id, repository_id, path, blob_sha256, blob_size, last_generated
FROM repository_metadata
WHERE repository_id = $1 AND path = $2;

-- name: GetRepoMetadataByRepo :many
SELECT id, repository_id, path, blob_sha256, blob_size, last_generated
FROM repository_metadata
WHERE repository_id = $1;

-- name: UpsertRepoMetadata :one
INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size)
VALUES ($1, $2, $3, $4)
ON CONFLICT (repository_id, path) DO UPDATE
    SET blob_sha256 = $3, blob_size = $4, last_generated = now()
RETURNING id, last_generated;
