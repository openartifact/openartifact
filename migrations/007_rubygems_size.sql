-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Per-gem size so quota can be released with the real artifact size on removal.
ALTER TABLE rubygems_gems ADD COLUMN IF NOT EXISTS size bigint NOT NULL DEFAULT 0;

-- Backfill from the blob for gems pushed before the column existed (blobs rows
-- written by older versions may carry size 0; those stay 0).
UPDATE rubygems_gems rg SET size = b.size_bytes
FROM blobs b
WHERE b.id = rg.blob_id AND rg.size = 0 AND b.size_bytes > 0;
