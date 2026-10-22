-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Backfill docker_blob_images from the blobs existing manifests reference.
-- Manifests not yet indexed in docker_manifest_refs (they predate that table or
-- were restored from a backup) have no recorded references, so every image
-- holding one keeps access to every blob its repository already had: no
-- existing pull breaks, the images are narrowed when GC indexes the manifests.
-- Idempotent, so a migration that stopped half way continues at the next start.
INSERT INTO docker_blob_images (repository_id, image, digest)
SELECT DISTINCT r.repository_id, r.image, r.digest
FROM docker_manifest_refs r
JOIN docker_blobs b ON b.repository_id = r.repository_id AND b.digest = r.digest
WHERE r.kind = 'blob'
ON CONFLICT DO NOTHING;

INSERT INTO docker_blob_images (repository_id, image, digest)
SELECT b.repository_id, m.image, b.digest
FROM (
    SELECT DISTINCT dm.repository_id, dm.image
    FROM docker_manifests dm
    WHERE NOT EXISTS (
        SELECT 1 FROM docker_manifest_refs x WHERE x.manifest_id = dm.id AND x.kind = 'indexed'
    )
) m
JOIN docker_blobs b ON b.repository_id = m.repository_id
ON CONFLICT DO NOTHING;
