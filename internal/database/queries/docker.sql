-- name: CreateDockerBlob :one
-- Re-pushing a blob the repository already holds is a no-op, not an error.
-- inserted tells a new row (which the caller has reserved quota for) from the
-- refresh of an existing one: xmax is 0 only on a row this statement inserted.
INSERT INTO docker_blobs (repository_id, digest, blob_id, size_bytes)
VALUES ($1, $2, $3, $4)
ON CONFLICT (repository_id, digest) DO UPDATE SET blob_id = EXCLUDED.blob_id, last_used_at = now(),
    size_bytes = COALESCE(docker_blobs.size_bytes, EXCLUDED.size_bytes)
RETURNING id, created_at, (xmax = 0) AS inserted;

-- name: GetDockerBlob :one
SELECT dbb.id, dbb.repository_id, dbb.digest, b.media_type, COALESCE(dbb.size_bytes, b.size_bytes)::bigint AS size, b.sha256 AS blob_sha256, dbb.created_at
FROM docker_blobs dbb
JOIN blobs b ON b.id = dbb.blob_id
WHERE dbb.repository_id = $1 AND dbb.digest = $2;

-- name: DeleteDockerBlob :execrows
DELETE FROM docker_blobs WHERE repository_id = $1 AND digest = $2;

-- name: ListDockerBlobs :many
SELECT dbb.id, dbb.repository_id, dbb.digest, b.media_type, COALESCE(dbb.size_bytes, b.size_bytes)::bigint AS size, b.sha256 AS blob_sha256, dbb.created_at
FROM docker_blobs dbb
JOIN blobs b ON b.id = dbb.blob_id
WHERE dbb.repository_id = $1
ORDER BY dbb.created_at DESC;

-- name: CreateDockerUpload :one
INSERT INTO docker_uploads (repository_id, upload_uuid, "offset")
VALUES ($1, $2, $3)
RETURNING id, created_at, updated_at;

-- name: GetDockerUpload :one
SELECT id, repository_id, upload_uuid, "offset", created_at, updated_at
FROM docker_uploads
WHERE repository_id = $1 AND upload_uuid = $2;

-- name: UpdateDockerUploadOffset :execrows
-- Compare-and-set: advances the offset only while it still equals the value the
-- caller based its write on, so two requests on one session cannot both claim
-- the same byte range.
UPDATE docker_uploads SET "offset" = sqlc.arg(new_offset), updated_at = now()
WHERE repository_id = sqlc.arg(repository_id) AND upload_uuid = sqlc.arg(upload_uuid) AND "offset" = sqlc.arg(old_offset);

-- name: DeleteDockerUpload :exec
DELETE FROM docker_uploads WHERE repository_id = $1 AND upload_uuid = $2;

-- name: DeleteStaleDockerUploads :many
-- Drops upload sessions that saw no activity since the cutoff and returns their
-- UUIDs so the caller can remove the staged files.
DELETE FROM docker_uploads WHERE updated_at < $1 RETURNING upload_uuid;

-- name: CreateDockerManifest :one
-- Re-pushing a manifest already stored for the image is a no-op refresh, not
-- an error (OCI distribution: manifest PUT is idempotent). A copy of the same
-- digest sitting in the trash does not conflict: the push creates a new live row.
INSERT INTO docker_manifests (repository_id, image, digest, media_type, blob_id, size, subject_digest)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
ON CONFLICT (repository_id, image, digest) WHERE deleted_at IS NULL DO UPDATE
    SET media_type = EXCLUDED.media_type, blob_id = EXCLUDED.blob_id, size = EXCLUDED.size,
        subject_digest = EXCLUDED.subject_digest, updated_at = now()
RETURNING id, created_at, updated_at, (xmax = 0) AS inserted;

-- name: GetDockerManifest :one
SELECT id, repository_id, image, digest, media_type, blob_id, size,
       COALESCE(subject_digest, '') AS subject_digest,
       created_at, updated_at
FROM docker_manifests
WHERE repository_id = $1 AND image = $2 AND digest = $3 AND deleted_at IS NULL;

-- name: DeleteDockerManifest :execrows
-- Permanent delete of a live manifest (retention 0 path); trashed rows are left to purge.
DELETE FROM docker_manifests WHERE repository_id = $1 AND image = $2 AND digest = $3 AND deleted_at IS NULL;

-- name: UpsertDockerTag :one
INSERT INTO docker_tags (repository_id, image, name, manifest_digest)
VALUES ($1, $2, $3, $4)
ON CONFLICT (repository_id, image, name) WHERE deleted_at IS NULL DO UPDATE SET manifest_digest = EXCLUDED.manifest_digest, updated_at = now()
RETURNING id, created_at, updated_at;

-- name: GetDockerTag :one
SELECT t.id, t.repository_id, t.name, t.manifest_digest, t.created_at, t.updated_at, t.image, t.deleted_at, t.deleted_by
FROM docker_tags t
WHERE t.repository_id = $1 AND t.image = $2 AND t.name = $3
  AND t.deleted_at IS NULL
  AND EXISTS (
      SELECT 1 FROM docker_manifests m
      WHERE m.repository_id = t.repository_id AND m.image = t.image
        AND m.digest = t.manifest_digest AND m.deleted_at IS NULL
  );

-- name: DeleteDockerTag :exec
DELETE FROM docker_tags WHERE repository_id = $1 AND image = $2 AND name = $3 AND deleted_at IS NULL;

-- name: DeleteDockerTagsByDigest :exec
DELETE FROM docker_tags WHERE repository_id = $1 AND image = $2 AND manifest_digest = $3 AND deleted_at IS NULL;

-- name: ListDockerTags :many
SELECT t.id, t.repository_id, t.name, t.manifest_digest, t.created_at, t.updated_at, t.image, t.deleted_at, t.deleted_by
FROM docker_tags t
WHERE t.repository_id = $1 AND t.image = $2
  AND t.deleted_at IS NULL
  AND EXISTS (
      SELECT 1 FROM docker_manifests m
      WHERE m.repository_id = t.repository_id AND m.image = t.image
        AND m.digest = t.manifest_digest AND m.deleted_at IS NULL
  )
ORDER BY t.name ASC
LIMIT $3;

-- name: ListDockerTagsAfter :many
SELECT t.id, t.repository_id, t.name, t.manifest_digest, t.created_at, t.updated_at, t.image, t.deleted_at, t.deleted_by
FROM docker_tags t
WHERE t.repository_id = $1 AND t.image = $2 AND t.name > $3
  AND t.deleted_at IS NULL
  AND EXISTS (
      SELECT 1 FROM docker_manifests m
      WHERE m.repository_id = t.repository_id AND m.image = t.image
        AND m.digest = t.manifest_digest AND m.deleted_at IS NULL
  )
ORDER BY t.name ASC
LIMIT $4;

-- name: ListDockerTagsByRepo :many
SELECT t.id, t.repository_id, t.name, t.manifest_digest, t.created_at, t.updated_at, t.image, t.deleted_at, t.deleted_by
FROM docker_tags t
WHERE t.repository_id = $1
  AND t.deleted_at IS NULL
  AND EXISTS (
      SELECT 1 FROM docker_manifests m
      WHERE m.repository_id = t.repository_id AND m.image = t.image
        AND m.digest = t.manifest_digest AND m.deleted_at IS NULL
  )
ORDER BY t.image ASC, t.name ASC
LIMIT $2;

-- name: ListDockerManifestsByRepo :many
SELECT dm.id, dm.repository_id, dm.image, dm.digest, dm.media_type, b.sha256 AS blob_sha256, dm.size,
       COALESCE(dm.subject_digest, '') AS subject_digest,
       dm.created_at, dm.updated_at
FROM docker_manifests dm
JOIN blobs b ON b.id = dm.blob_id
WHERE dm.repository_id = $1 AND dm.deleted_at IS NULL
ORDER BY dm.created_at;

-- name: TouchDockerBlob :one
-- Marks a blob as just relied on by a client (HEAD probe, re-push, mount) so
-- registry GC leaves it alone for its grace period. No row means the blob has
-- been removed and the client must upload it again.
UPDATE docker_blobs SET last_used_at = now()
WHERE repository_id = $1 AND digest = $2
RETURNING id;

-- name: LinkDockerBlobImages :exec
-- Records that the image may serve and reference the blobs (see
-- docker_blob_images). Digests the repository does not hold are skipped.
INSERT INTO docker_blob_images (repository_id, image, digest)
SELECT b.repository_id, sqlc.arg(image)::text, b.digest
FROM docker_blobs b
WHERE b.repository_id = sqlc.arg(repository_id)::uuid AND b.digest = ANY(sqlc.arg(digests)::text[])
ON CONFLICT DO NOTHING;

-- name: DockerBlobVisibleToImage :one
-- Whether the image may serve and reference the blob: it is linked to the
-- image, or it predates docker_blob_images and has no link to any image.
SELECT EXISTS (
    SELECT 1 FROM docker_blob_images i
    WHERE i.repository_id = sqlc.arg(repository_id)::uuid AND i.image = sqlc.arg(image)::text AND i.digest = sqlc.arg(digest)::text
) OR NOT EXISTS (
    SELECT 1 FROM docker_blob_images i
    WHERE i.repository_id = sqlc.arg(repository_id)::uuid AND i.digest = sqlc.arg(digest)::text
) AS visible;

-- name: CountDockerBlobsVisibleToImage :one
-- How many of the digests the repository holds and the image may reference
-- (see DockerBlobVisibleToImage); also marks them as just used, so registry GC
-- keeps them for its grace period.
WITH visible AS (
    UPDATE docker_blobs b SET last_used_at = now()
    WHERE b.repository_id = sqlc.arg(repository_id)::uuid AND b.digest = ANY(sqlc.arg(digests)::text[])
      AND (
          EXISTS (
              SELECT 1 FROM docker_blob_images i
              WHERE i.repository_id = b.repository_id AND i.image = sqlc.arg(image)::text AND i.digest = b.digest
          )
          OR NOT EXISTS (
              SELECT 1 FROM docker_blob_images i
              WHERE i.repository_id = b.repository_id AND i.digest = b.digest
          )
      )
    RETURNING b.id
)
SELECT count(*)::bigint AS n FROM visible;

-- name: InsertDockerManifestRefs :exec
-- Records the references of one kind held by a manifest body (see
-- docker_manifest_refs). Idempotent: a re-pushed manifest has the same refs.
INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest)
SELECT sqlc.arg(manifest_id)::uuid, sqlc.arg(repository_id)::uuid, sqlc.arg(image)::text, sqlc.arg(kind)::text, d
FROM unnest(sqlc.arg(digests)::text[]) AS d
ON CONFLICT (manifest_id, kind, digest) DO NOTHING;

-- name: ListUnindexedDockerManifests :many
-- Manifests whose references have not been recorded yet (pushed before
-- docker_manifest_refs existed, or restored from a backup). Trashed manifests are
-- included on purpose: their refs must be recorded so they keep their layers alive.
SELECT dm.id, dm.image, dm.digest, b.sha256 AS blob_sha256
FROM docker_manifests dm
JOIN blobs b ON b.id = dm.blob_id
WHERE dm.repository_id = $1
  AND NOT EXISTS (SELECT 1 FROM docker_manifest_refs r WHERE r.manifest_id = dm.id AND r.kind = 'indexed')
ORDER BY dm.created_at
LIMIT $2;

-- name: ListDockerGCManifests :many
-- Every live manifest of the repository (trashed ones are never GC candidates;
-- purge owns them) with what GC needs to decide its fate:
-- tagged (some tag of the same image points at it, a trashed tag included: the
-- manifest must survive until the tag is purged so a restore never dangles), young (pushed or re-pushed
-- within the grace period) and the child manifests it references (an index).
SELECT dm.id, dm.image, dm.digest,
       COALESCE(dm.subject_digest, '')::text AS subject_digest,
       dm.size,
       b.sha256 AS blob_sha256,
       EXISTS (
           SELECT 1 FROM docker_tags t
           WHERE t.repository_id = dm.repository_id AND t.image = dm.image AND t.manifest_digest = dm.digest
       ) AS tagged,
       (dm.updated_at > now() - make_interval(secs => sqlc.arg(grace_seconds)::float8))::boolean AS young,
       COALESCE((
           SELECT string_agg(r.digest, ',') FROM docker_manifest_refs r
           WHERE r.manifest_id = dm.id AND r.kind = 'manifest'
       ), '')::text AS children
FROM docker_manifests dm
JOIN blobs b ON b.id = dm.blob_id
WHERE dm.repository_id = sqlc.arg(repository_id)::uuid
  AND dm.deleted_at IS NULL
ORDER BY dm.created_at;

-- name: DeleteDockerManifestsForGC :many
-- Deletes the given untagged live manifests (never a trashed one), re-checking every condition against the
-- current state: still older than the grace period, still untagged, and not
-- adjacent (index child, index parent, subject or referrer) to a manifest that
-- is not being deleted. Must run in a transaction holding the repository's
-- exclusive docker lock so a concurrent push cannot register new references
-- between the check and the delete.
WITH deleted AS (
DELETE FROM docker_manifests dm
WHERE dm.repository_id = sqlc.arg(repository_id)::uuid
  AND dm.id = ANY(sqlc.arg(ids)::uuid[])
  AND dm.deleted_at IS NULL
  AND dm.updated_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::float8)
  AND NOT EXISTS (
      SELECT 1 FROM docker_tags t
      WHERE t.repository_id = dm.repository_id AND t.image = dm.image AND t.manifest_digest = dm.digest
  )
  AND NOT EXISTS (
      SELECT 1 FROM docker_manifest_refs r
      WHERE r.repository_id = dm.repository_id AND r.kind = 'manifest'
        AND r.image = dm.image AND r.digest = dm.digest
        AND r.manifest_id <> ALL(sqlc.arg(ids)::uuid[])
  )
  AND NOT EXISTS (
      SELECT 1 FROM docker_manifests rf
      WHERE rf.repository_id = dm.repository_id AND rf.image = dm.image
        AND rf.subject_digest = dm.digest AND rf.id <> ALL(sqlc.arg(ids)::uuid[])
  )
  AND NOT EXISTS (
      SELECT 1 FROM docker_manifests s
      WHERE s.repository_id = dm.repository_id AND s.image = dm.image
        AND s.digest = dm.subject_digest AND s.id <> ALL(sqlc.arg(ids)::uuid[])
  )
RETURNING dm.id, dm.image, dm.digest, dm.size, dm.blob_id
)
SELECT d.id, d.image, d.digest, d.size, b.sha256 AS blob_sha256
FROM deleted d
JOIN blobs b ON b.id = d.blob_id;

-- name: ListDockerGCBlobs :many
-- Blob records of the repository that no manifest references any more (ignoring
-- the manifests in exclude_manifest_ids, which the caller is about to delete)
-- and that no client has used within the grace period. Returns nothing while
-- any manifest still has unrecorded references.
SELECT db.digest, b.sha256 AS blob_sha256, b.size_bytes AS size
FROM docker_blobs db
JOIN blobs b ON b.id = db.blob_id
WHERE db.repository_id = sqlc.arg(repository_id)::uuid
  AND db.last_used_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::float8)
  AND NOT EXISTS (
      SELECT 1 FROM docker_manifest_refs r
      WHERE r.repository_id = db.repository_id AND r.kind = 'blob' AND r.digest = db.digest
        AND r.manifest_id <> ALL(sqlc.arg(exclude_manifest_ids)::uuid[])
  )
  AND NOT EXISTS (
      SELECT 1 FROM docker_manifests dm
      WHERE dm.repository_id = db.repository_id
        AND dm.id <> ALL(sqlc.arg(exclude_manifest_ids)::uuid[])
        AND NOT EXISTS (SELECT 1 FROM docker_manifest_refs x WHERE x.manifest_id = dm.id AND x.kind = 'indexed')
  )
ORDER BY db.last_used_at, db.digest
LIMIT sqlc.arg(row_limit);

-- name: DeleteDockerBlobsForGC :many
-- Deletes the given blob records, re-checking against the current state that no
-- manifest references them and that they are still unused for the grace period.
-- Must run in a transaction holding the repository's exclusive docker lock.
WITH deleted AS (
DELETE FROM docker_blobs db
WHERE db.repository_id = sqlc.arg(repository_id)::uuid
  AND db.digest = ANY(sqlc.arg(digests)::text[])
  AND db.last_used_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::float8)
  AND NOT EXISTS (
      SELECT 1 FROM docker_manifest_refs r
      WHERE r.repository_id = db.repository_id AND r.kind = 'blob' AND r.digest = db.digest
  )
  AND NOT EXISTS (
      SELECT 1 FROM docker_manifests dm
      WHERE dm.repository_id = db.repository_id
        AND NOT EXISTS (SELECT 1 FROM docker_manifest_refs x WHERE x.manifest_id = dm.id AND x.kind = 'indexed')
  )
RETURNING db.digest, db.blob_id
)
SELECT d.digest, b.sha256 AS blob_sha256, b.size_bytes AS size
FROM deleted d
JOIN blobs b ON b.id = d.blob_id;

-- name: BlobRowExists :one
SELECT EXISTS (SELECT 1 FROM blobs WHERE sha256 = $1);

-- name: DockerBlobHeldOnlyByTrash :one
-- Whether the blob is referenced by manifests of the repository and every one
-- of them is in the trash: it is then not served by digest until a restore.
-- The CASE keeps the cheap answers first: a blob a live manifest references
-- (the common case, a shared layer) ends at the first live row, and a blob no
-- manifest references (an upload not yet claimed) at the first lookup. The last
-- branch guards manifests that predate docker_manifest_refs and have not been
-- indexed yet: their references are unknown, so while a live one exists the
-- repository's blobs are never hidden.
SELECT (CASE
    WHEN EXISTS (
        SELECT 1 FROM docker_manifest_refs r
        JOIN docker_manifests dm ON dm.id = r.manifest_id
        WHERE r.repository_id = sqlc.arg(repository_id)::uuid AND r.kind = 'blob'
          AND r.digest = sqlc.arg(digest)::text AND dm.deleted_at IS NULL
    ) THEN false
    WHEN NOT EXISTS (
        SELECT 1 FROM docker_manifest_refs r
        JOIN docker_manifests dm ON dm.id = r.manifest_id
        WHERE r.repository_id = sqlc.arg(repository_id)::uuid AND r.kind = 'blob'
          AND r.digest = sqlc.arg(digest)::text AND dm.deleted_at IS NOT NULL
    ) THEN false
    ELSE NOT EXISTS (
        SELECT 1 FROM docker_manifests dm
        WHERE dm.repository_id = sqlc.arg(repository_id)::uuid AND dm.deleted_at IS NULL
          AND NOT EXISTS (
              SELECT 1 FROM docker_manifest_refs r
              WHERE r.manifest_id = dm.id AND r.kind = 'indexed'
          )
    )
END)::boolean AS trashed;

-- name: ListDockerCatalog :many
-- One page of the registry catalog: every image of the docker repositories,
-- ordered by distribution name ("<repository>/<image>", just "<repository>"
-- for the default image) after $1, up to $2 names. Hosted repositories list
-- their images with a live manifest (a loose index scan over the live-digest
-- index, one probe per image), proxy repositories the images with a cached
-- manifest, groups the images of their members under the group's own name.
-- sources lists, per name, the repositories that hold the image (the repository
-- itself unless it is a group): the caller authorizes each of them. The
-- parallel arrays are ordered by source id.
WITH RECURSIVE hosted(repository_id, image) AS (
    SELECT r.id, (SELECT min(m.image) FROM docker_manifests m
                  WHERE m.repository_id = r.id AND m.deleted_at IS NULL)
    FROM repositories r
    WHERE r.format = 'docker' AND r.repo_type = 'hosted' AND r.deleted_at IS NULL AND NOT r.blocked
  UNION ALL
    SELECT h.repository_id, (SELECT min(m.image) FROM docker_manifests m
                             WHERE m.repository_id = h.repository_id AND m.deleted_at IS NULL
                               AND m.image > h.image)
    FROM hosted h
    WHERE h.image IS NOT NULL
),
proxied AS (
    SELECT DISTINCT e.repository_id,
           substring(e.cache_key FROM '^(.+)/manifests/[^/]+$') AS image
    FROM proxy_cache_entries e
    JOIN repositories r ON r.id = e.repository_id
    WHERE r.format = 'docker' AND r.repo_type = 'proxy' AND r.deleted_at IS NULL AND NOT r.blocked
      AND e.status_code = 200 AND e.blob_key IS NOT NULL
      AND e.cache_key LIKE '%/manifests/%'
),
held AS (
    SELECT repository_id, image FROM hosted WHERE image IS NOT NULL
    UNION
    SELECT repository_id, image FROM proxied WHERE image IS NOT NULL
),
listed AS (
    SELECT r.id AS repository_id, r.name AS repository_name, r.project_id AS repository_project_id,
           h.image, r.id AS source_id, r.name AS source_name, r.project_id AS source_project_id
    FROM held h
    JOIN repositories r ON r.id = h.repository_id
  UNION ALL
    SELECT g.id, g.name, g.project_id, h.image, mr.id, mr.name, mr.project_id
    FROM repositories g
    JOIN repository_group_members m ON m.group_repo_id = g.id
    JOIN repositories mr ON mr.id = m.member_repo_id AND mr.deleted_at IS NULL AND NOT mr.blocked
    JOIN held h ON h.repository_id = mr.id AND h.image <> ''
    WHERE g.format = 'docker' AND g.repo_type = 'group' AND g.deleted_at IS NULL
),
named AS (
    SELECT l.*,
           (CASE WHEN l.image = '' THEN l.repository_name ELSE l.repository_name || '/' || l.image END) COLLATE "C" AS full_name
    FROM listed l
)
SELECT n.full_name::text AS full_name,
       n.repository_id::uuid AS repository_id,
       n.repository_name::text AS repository_name,
       COALESCE(n.repository_project_id::text, '')::text AS repository_project_id,
       n.image::text AS image,
       array_agg(n.source_id::text ORDER BY n.source_id)::text[] AS source_ids,
       array_agg(n.source_name::text ORDER BY n.source_id)::text[] AS source_names,
       array_agg(COALESCE(n.source_project_id::text, '') ORDER BY n.source_id)::text[] AS source_project_ids
FROM named n
WHERE n.full_name > sqlc.arg(after)::text COLLATE "C"
GROUP BY n.full_name, n.repository_id, n.repository_name, n.repository_project_id, n.image
ORDER BY n.full_name
LIMIT sqlc.arg(page_limit)::int;
