-- name: CreateAnsibleCollection :one
INSERT INTO ansible_collections (
    repository_id, namespace, name, version, filename, blob_id, dependencies, metadata, requires_ansible, created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE(sqlc.narg(created_at)::timestamptz, NOW()))
RETURNING id, created_at, updated_at;

-- name: GetAnsibleCollection :one
SELECT ac.id, ac.repository_id, ac.namespace, ac.name, ac.version, ac.filename, b.blob_key, b.size_bytes, b.sha256,
       ac.dependencies, ac.metadata, ac.requires_ansible, ac.created_at, ac.updated_at
FROM ansible_collections ac
JOIN blobs b ON b.id = ac.blob_id
WHERE ac.repository_id = $1 AND ac.namespace = $2 AND ac.name = $3 AND ac.version = $4 AND ac.deleted_at IS NULL;

-- name: GetAnsibleCollectionByID :one
SELECT ac.id, ac.repository_id, ac.namespace, ac.name, ac.version, ac.filename, b.blob_key, b.size_bytes, b.sha256,
       ac.dependencies, ac.metadata, ac.requires_ansible, ac.created_at, ac.updated_at
FROM ansible_collections ac
JOIN blobs b ON b.id = ac.blob_id
WHERE ac.repository_id = $1 AND ac.id = $2 AND ac.deleted_at IS NULL;

-- name: ListAnsibleCollectionVersions :many
-- Every live version of one collection; the caller orders them by version.
SELECT ac.id, ac.repository_id, ac.namespace, ac.name, ac.version, ac.filename, b.blob_key, b.size_bytes, b.sha256,
       ac.dependencies, ac.metadata, ac.requires_ansible, ac.created_at, ac.updated_at
FROM ansible_collections ac
JOIN blobs b ON b.id = ac.blob_id
WHERE ac.repository_id = $1 AND ac.namespace = $2 AND ac.name = $3 AND ac.deleted_at IS NULL
ORDER BY ac.created_at, ac.id;

-- name: ListAnsibleCollections :many
-- Every live version of every collection of the repository (backup export).
SELECT ac.id, ac.repository_id, ac.namespace, ac.name, ac.version, ac.filename, b.blob_key, b.size_bytes, b.sha256,
       ac.dependencies, ac.metadata, ac.requires_ansible, ac.created_at, ac.updated_at
FROM ansible_collections ac
JOIN blobs b ON b.id = ac.blob_id
WHERE ac.repository_id = $1 AND ac.deleted_at IS NULL
ORDER BY ac.namespace, ac.name, ac.created_at, ac.id;

-- name: ListAnsibleCollectionNames :many
-- One row per collection of the repository, a page of them in name order.
SELECT ac.namespace, ac.name, MIN(ac.created_at)::timestamptz AS created_at, MAX(ac.updated_at)::timestamptz AS updated_at
FROM ansible_collections ac
WHERE ac.repository_id = $1 AND ac.deleted_at IS NULL
GROUP BY ac.namespace, ac.name
ORDER BY ac.namespace, ac.name
LIMIT $2 OFFSET $3;

-- name: CountAnsibleCollectionNames :one
SELECT COUNT(*) FROM (
    SELECT 1 FROM ansible_collections ac
    WHERE ac.repository_id = $1 AND ac.deleted_at IS NULL
    GROUP BY ac.namespace, ac.name
) c;

-- name: DeleteAnsibleCollection :exec
DELETE FROM ansible_collections WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;
