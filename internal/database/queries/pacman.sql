-- Pacman packages on the generic tables (see pkg_pacman.go for the mapping):
-- the package file is package_files, its version package_versions (the index
-- stanza fields in metadata), the file list and signature pacman_files. Reads
-- hide trashed files.

-- name: InsertPacmanFile :exec
INSERT INTO pacman_files (id, file_list, signature, signature_source, signature_key)
VALUES ($1, sqlc.narg(file_list)::text, $2, $3, $4);

-- Download by file name: idx_package_files_filename.
-- name: GetPacmanPackageByFilename :one
SELECT f.id, f.repository_id, p.name, v.version, f.arch, f.filename,
       b.blob_key, b.size_bytes, b.sha256,
       COALESCE(f.attrs ->> 'md5', '')::text AS md5,
       COALESCE((f.attrs ->> 'build_time')::bigint, 0)::bigint AS build_date,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       v.metadata,
       s.signature, s.signature_source, s.signature_key, f.created_at, f.updated_at
FROM package_files f
JOIN pacman_files s ON s.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.filename = $2 AND f.deleted_at IS NULL;

-- name: ListPacmanPackages :many
-- Every live package of the repository, without its file list.
SELECT f.id, f.repository_id, p.name, v.version, f.arch, f.filename,
       b.blob_key, b.size_bytes, b.sha256,
       COALESCE(f.attrs ->> 'md5', '')::text AS md5,
       COALESCE((f.attrs ->> 'build_time')::bigint, 0)::bigint AS build_date,
       COALESCE((f.attrs ->> 'installed_size')::bigint, 0)::bigint AS installed_size,
       v.metadata,
       s.signature, s.signature_source, s.signature_key, f.created_at, f.updated_at
FROM package_files f
JOIN pacman_files s ON s.id = f.id
JOIN package_versions v ON v.id = f.version_id
JOIN packages p ON p.id = v.package_id
JOIN blobs b ON b.id = f.blob_id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY p.name, f.created_at, f.id;

-- name: ListPacmanFileLists :many
-- The file lists of the live packages that have one.
SELECT s.id, s.file_list
FROM pacman_files s
JOIN package_files f ON f.id = s.id
WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND s.file_list IS NOT NULL;

-- name: ListPacmanPackagesWithoutFileList :many
-- Live packages whose file list has not been read from the blob yet
-- (idx_pacman_files_no_list holds only those rows).
SELECT s.id, b.blob_key, f.filename
FROM pacman_files s
JOIN package_files f ON f.id = s.id
JOIN blobs b ON b.id = f.blob_id
WHERE s.file_list IS NULL AND f.repository_id = $1 AND f.deleted_at IS NULL
ORDER BY f.created_at, f.id;

-- name: SetPacmanFileList :exec
UPDATE pacman_files s SET file_list = $3
FROM package_files f
WHERE s.id = $1 AND f.id = s.id AND f.repository_id = $2 AND s.file_list IS NULL;

-- Touches the narrow side row only: not the listing row nor its updated_at.
-- name: SetPacmanSignature :exec
UPDATE pacman_files s SET signature = $3, signature_source = $4, signature_key = $5
FROM package_files f
WHERE s.id = $1 AND f.id = s.id AND f.repository_id = $2 AND f.deleted_at IS NULL;
