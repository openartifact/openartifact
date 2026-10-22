-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Image-level blob visibility for the registry.
--
-- docker_blobs is a per-repository index, but RBAC path_pattern scopes docker
-- permissions per image inside a repository. docker_blob_images records which
-- images a blob was pushed to, mounted into or referenced by, so blob
-- GET/HEAD, mount and manifest PUT can refuse a digest that only another image
-- of the repository holds. Rows go with their docker_blobs row.
--
-- docker_blobs.size_bytes is the size verified when the blob was stored. The
-- shared blobs row keeps the first writer's size (0 for some formats), so the
-- registry serves Content-Length from here; NULL on rows that predate this
-- migration (the handler then asks the blob store).
--
-- The backfill of docker_blob_images from existing manifests is in the
-- following 022_docker_blob_images_backfill.no_tx.sql.
ALTER TABLE docker_blobs ADD COLUMN size_bytes bigint;

COMMENT ON COLUMN docker_blobs.size_bytes IS 'Size in bytes verified at upload; NULL for rows created before it was recorded.';

CREATE TABLE docker_blob_images (
    repository_id uuid NOT NULL,
    image text DEFAULT '' NOT NULL,
    digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT docker_blob_images_pkey PRIMARY KEY (repository_id, image, digest),
    CONSTRAINT docker_blob_images_blob_fkey FOREIGN KEY (repository_id, digest)
        REFERENCES docker_blobs (repository_id, digest) ON DELETE CASCADE
);

COMMENT ON TABLE docker_blob_images IS 'Images of a repository that may serve and reference a blob (pushed, mounted or referenced by one of their manifests). A docker_blobs row with no entry here predates this table and stays visible to every image of its repository.';
