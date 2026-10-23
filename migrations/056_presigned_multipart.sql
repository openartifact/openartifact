-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Multipart presigned uploads (objects above the 5 GiB single-PUT limit).
--
-- part_size is the planned size of every part but the last; 0 marks a plain
-- single-PUT upload. The part count follows from size_bytes and part_size.
-- multipart_id is the backend's handle of the open multipart upload (the S3
-- upload id; empty for backends that need none). Both are fixed at creation, so
-- an upload keeps its plan even if the server's defaults change.
ALTER TABLE presigned_uploads
    ADD COLUMN part_size bigint DEFAULT 0 NOT NULL,
    ADD COLUMN multipart_id text DEFAULT '' NOT NULL,
    ADD CONSTRAINT presigned_uploads_part_size_check CHECK (part_size >= 0);

COMMENT ON COLUMN presigned_uploads.part_size IS 'Planned size of each part of a multipart upload; 0 for a single-PUT upload.';
COMMENT ON COLUMN presigned_uploads.multipart_id IS 'Backend handle (S3 upload id) of the open multipart upload; empty when the backend needs none.';
