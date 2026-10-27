-- name: CreatePacmanPackage :one
INSERT INTO pacman_packages (
    repository_id, name, base, version, arch, repo_arch, filename, blob_id, md5,
    description, url, packager, build_date, installed_size,
    licenses, package_groups, replaces, conflicts, provides, depends, optdepends, makedepends, checkdepends,
    file_list, signature, signature_source, signature_key, created_at
)
VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9,
    $10, $11, $12, $13, $14,
    $15, $16, $17, $18, $19, $20, $21, $22, $23,
    sqlc.narg(file_list)::text, $24, $25, $26, COALESCE(sqlc.narg(created_at)::timestamptz, NOW())
)
RETURNING id, created_at, updated_at;

-- name: GetPacmanPackageByFilename :one
SELECT pp.id, pp.repository_id, pp.name, pp.base, pp.version, pp.arch, pp.repo_arch, pp.filename, b.blob_key, b.size_bytes, b.sha256, pp.md5,
       pp.description, pp.url, pp.packager, pp.build_date, pp.installed_size,
       pp.licenses, pp.package_groups, pp.replaces, pp.conflicts, pp.provides, pp.depends, pp.optdepends, pp.makedepends, pp.checkdepends,
       pp.signature, pp.signature_source, pp.signature_key, pp.created_at, pp.updated_at
FROM pacman_packages pp
JOIN blobs b ON b.id = pp.blob_id
WHERE pp.repository_id = $1 AND pp.filename = $2 AND pp.deleted_at IS NULL;

-- name: ListPacmanPackages :many
-- Every live package of the repository, without its file list.
SELECT pp.id, pp.repository_id, pp.name, pp.base, pp.version, pp.arch, pp.repo_arch, pp.filename, b.blob_key, b.size_bytes, b.sha256, pp.md5,
       pp.description, pp.url, pp.packager, pp.build_date, pp.installed_size,
       pp.licenses, pp.package_groups, pp.replaces, pp.conflicts, pp.provides, pp.depends, pp.optdepends, pp.makedepends, pp.checkdepends,
       pp.signature, pp.signature_source, pp.signature_key, pp.created_at, pp.updated_at
FROM pacman_packages pp
JOIN blobs b ON b.id = pp.blob_id
WHERE pp.repository_id = $1 AND pp.deleted_at IS NULL
ORDER BY pp.name, pp.created_at, pp.id;

-- name: ListPacmanFileLists :many
-- The file lists of the live packages that have one.
SELECT pp.id, pp.file_list
FROM pacman_packages pp
WHERE pp.repository_id = $1 AND pp.deleted_at IS NULL AND pp.file_list IS NOT NULL;

-- name: ListPacmanPackagesWithoutFileList :many
-- Live packages whose file list has not been read from the blob yet.
SELECT pp.id, b.blob_key, pp.filename
FROM pacman_packages pp
JOIN blobs b ON b.id = pp.blob_id
WHERE pp.repository_id = $1 AND pp.deleted_at IS NULL AND pp.file_list IS NULL
ORDER BY pp.created_at, pp.id;

-- name: SetPacmanFileList :exec
UPDATE pacman_packages SET file_list = $3
WHERE id = $1 AND repository_id = $2 AND file_list IS NULL;

-- name: SetPacmanSignature :exec
UPDATE pacman_packages SET signature = $3, signature_source = $4, signature_key = $5
WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;

-- name: DeletePacmanPackage :exec
DELETE FROM pacman_packages WHERE id = $1 AND repository_id = $2 AND deleted_at IS NULL;
