// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

// The SQL of the docker promoter. Every statement is a constant; request values
// are bind parameters. Source rows are addressed by id, destination rows by
// (repository_id, image, digest/name), the live unique keys of docker_manifests
// and docker_tags (migrations 018, 022).

// dockerLockSQL takes the shared per-repository docker lock that manifest
// pushes take (database.dockerRepoLockKey) and registry GC takes exclusively:
// GC can then neither delete a blob this promotion relies on in the destination
// nor a manifest/blob it copies from the source before the transaction ends.
const dockerLockSQL = `SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))`

// dockerRepoLockPrefix must stay equal to the prefix of database.dockerRepoLockKey.
const dockerRepoLockPrefix = "docker-repo:"

// dockerRootsSQL lists the live manifests of the source repository stored as
// the blob with the item's sha256 ($2), with the live tags pointing at them
// (jsonb array of {id,name}, by name).
const dockerRootsSQL = `
SELECT m.id::text, m.image, m.digest, b.sha256, m.size,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('id', t.id, 'name', t.name) ORDER BY t.name)
                   FROM docker_tags t
                  WHERE t.repository_id = m.repository_id AND t.image = m.image
                    AND t.manifest_digest = m.digest AND t.deleted_at IS NULL), '[]'::jsonb)
  FROM docker_manifests m
  JOIN blobs b ON b.id = m.blob_id
 WHERE m.repository_id = $1::uuid AND m.deleted_at IS NULL AND b.sha256 = $2
 ORDER BY m.created_at, m.id`

// dockerIndexedSQL reports whether the references of source manifest $1 are
// recorded (docker_manifest_refs kind 'indexed'); manifests pushed before the
// refs table existed or restored from a backup are indexed later by registry GC.
const dockerIndexedSQL = `
SELECT EXISTS (SELECT 1 FROM docker_manifest_refs WHERE manifest_id = $1::uuid AND kind = 'indexed')`

// dockerInsertManifestSQL copies source manifest $2 into repository $1. It
// returns the new row id, or no row when the destination already holds a live
// manifest with that image and digest (digest = content, so that is the same
// manifest). subject_digest (referrers) is copied with the row.
const dockerInsertManifestSQL = `
INSERT INTO docker_manifests (repository_id, image, digest, media_type, size, subject_digest, blob_id)
SELECT $1::uuid, s.image, s.digest, s.media_type, s.size, s.subject_digest, s.blob_id
  FROM docker_manifests s
 WHERE s.id = $2::uuid
ON CONFLICT (repository_id, image, digest) WHERE deleted_at IS NULL DO NOTHING
RETURNING id::text`

// dockerInsertRefsSQL re-points the references of source manifest $3 (blobs,
// child manifests, the indexed marker) at the new destination manifest $1 of
// repository $2.
const dockerInsertRefsSQL = `
INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest)
SELECT $1::uuid, $2::uuid, r.image, r.kind, r.digest
  FROM docker_manifest_refs r
 WHERE r.manifest_id = $3::uuid`

// dockerCopyBlobsSQL gives repository $2 a docker_blobs row (same blobs row,
// verified size) for every config/layer blob source manifest $1 references and
// the source repository holds. A digest the destination already holds is left
// alone. It returns, for every blob the source holds, its row id, digest, size
// and whether this statement created the destination row. References the source
// does not hold (foreign / non-distributable layers, which are never pushed to
// a registry) yield no row.
const dockerCopyBlobsSQL = `
WITH src AS (
    SELECT sb.id, sb.digest, sb.blob_id, COALESCE(sb.size_bytes, b.size_bytes)::bigint AS size
      FROM docker_manifest_refs r
      JOIN docker_blobs sb ON sb.repository_id = r.repository_id AND sb.digest = r.digest
      JOIN blobs b ON b.id = sb.blob_id
     WHERE r.manifest_id = $1::uuid AND r.kind = 'blob'
), ins AS (
    INSERT INTO docker_blobs (repository_id, digest, blob_id, size_bytes)
    SELECT $2::uuid, src.digest, src.blob_id, src.size FROM src
    ON CONFLICT (repository_id, digest) DO NOTHING
    RETURNING digest
)
SELECT src.id::text, src.digest, src.size, (ins.digest IS NOT NULL)
  FROM src
  LEFT JOIN ins ON ins.digest = src.digest
 ORDER BY src.digest`

// dockerLinkBlobsSQL marks the destination blobs source manifest $1 references
// as just used (registry GC keeps them for its grace period) and links them to
// image $3 of repository $2, exactly as a manifest push does.
const dockerLinkBlobsSQL = `
WITH touched AS (
    UPDATE docker_blobs d SET last_used_at = now()
      FROM docker_manifest_refs r
     WHERE r.manifest_id = $1::uuid AND r.kind = 'blob'
       AND d.repository_id = $2::uuid AND d.digest = r.digest
    RETURNING d.digest
)
INSERT INTO docker_blob_images (repository_id, image, digest)
SELECT $2::uuid, $3::text, touched.digest FROM touched
ON CONFLICT DO NOTHING`

// dockerChildrenSQL lists the live child manifests (index / manifest list
// entries) of source manifest $1, resolved inside the same image.
const dockerChildrenSQL = `
SELECT cm.id::text, cm.image, cm.digest, cb.sha256, cm.size
  FROM docker_manifest_refs r
  JOIN docker_manifests cm ON cm.repository_id = r.repository_id AND cm.image = r.image
                          AND cm.digest = r.digest AND cm.deleted_at IS NULL
  JOIN blobs cb ON cb.id = cm.blob_id
 WHERE r.manifest_id = $1::uuid AND r.kind = 'manifest'
 ORDER BY r.digest`

// dockerInsertTagSQL copies live source tag $2 into repository $1. It returns
// the new row id, or no row when the destination already holds a live tag of
// that name for the image.
const dockerInsertTagSQL = `
INSERT INTO docker_tags (repository_id, image, name, manifest_digest)
SELECT $1::uuid, s.image, s.name, s.manifest_digest
  FROM docker_tags s
 WHERE s.id = $2::uuid AND s.deleted_at IS NULL
ON CONFLICT (repository_id, image, name) WHERE deleted_at IS NULL DO NOTHING
RETURNING id::text`

// dockerTagDigestSQL returns the id and manifest digest the live destination
// tag ($3) of image $2 in repository $1 points at, with the sha256 and size of
// that manifest's content (” and 0 when the manifest is not live there).
const dockerTagDigestSQL = `
SELECT t.id::text, t.manifest_digest, COALESCE(b.sha256, ''), COALESCE(m.size, 0)::bigint
  FROM docker_tags t
  LEFT JOIN docker_manifests m ON m.repository_id = t.repository_id AND m.image = t.image
                              AND m.digest = t.manifest_digest AND m.deleted_at IS NULL
  LEFT JOIN blobs b ON b.id = m.blob_id
 WHERE t.repository_id = $1::uuid AND t.image = $2 AND t.name = $3 AND t.deleted_at IS NULL`
