-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Presigned URLs for blob downloads and uploads.
--
-- repositories.presign_downloads overrides the global storage.presignDownloads
-- setting per repository: NULL inherits it, true redirects blob downloads to
-- presigned object-store URLs, false always streams through the server.
ALTER TABLE repositories ADD COLUMN presign_downloads boolean;

COMMENT ON COLUMN repositories.presign_downloads IS 'Per-repository override of storage.presignDownloads: NULL inherits the global setting, true/false force redirect/streaming.';

-- Presigned uploads in flight: the client PUTs the object straight to a staging
-- key in the object store, then calls the complete endpoint, which verifies
-- size and sha256, promotes the object to its content-addressed key and records
-- the artifact. Rows (and their staging objects) that are never completed are
-- removed by the cleanup worker once expires_at has passed; expires_at is the
-- complete deadline, i.e. the upload URL expiry plus a grace period.
CREATE TABLE presigned_uploads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    path text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    content_type text DEFAULT '' NOT NULL,
    created_by text DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT presigned_uploads_pkey PRIMARY KEY (id),
    CONSTRAINT presigned_uploads_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
    CONSTRAINT presigned_uploads_size_check CHECK (size_bytes >= 0),
    CONSTRAINT presigned_uploads_sha256_check CHECK (sha256 ~ '^[0-9a-f]{64}$')
);

CREATE INDEX idx_presigned_uploads_expires ON presigned_uploads (expires_at);

COMMENT ON TABLE presigned_uploads IS 'Presigned uploads awaiting their complete call; the object lives at the staging key staging/<id> in the blob store.';
