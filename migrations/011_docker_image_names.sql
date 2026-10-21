-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- OCI distribution names may contain "/" ("reg/org/team/app"). The first
-- segment names the OpenArtifact repository; the rest is the image inside it.
-- Tags and manifests are scoped per image so "org/a:1" and "org/b:1" coexist
-- in one repository. The empty image is the repository's default image, which
-- is where every row written before this migration lives.
ALTER TABLE docker_manifests ADD COLUMN image text DEFAULT '' NOT NULL;
ALTER TABLE docker_tags ADD COLUMN image text DEFAULT '' NOT NULL;

ALTER TABLE docker_manifests DROP CONSTRAINT docker_manifests_repository_id_digest_key;
ALTER TABLE docker_manifests
    ADD CONSTRAINT docker_manifests_repository_id_image_digest_key UNIQUE (repository_id, image, digest);

ALTER TABLE docker_tags DROP CONSTRAINT docker_tags_repository_id_name_key;
ALTER TABLE docker_tags
    ADD CONSTRAINT docker_tags_repository_id_image_name_key UNIQUE (repository_id, image, name);

COMMENT ON COLUMN docker_manifests.image IS 'Image name inside the repository (distribution name minus its first segment); empty for the default image.';
COMMENT ON COLUMN docker_tags.image IS 'Image name inside the repository (distribution name minus its first segment); empty for the default image.';
