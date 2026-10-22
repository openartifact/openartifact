-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Checks the existing blobs rows against the RESTRICT foreign key added NOT
-- VALID by 033_blobs_backend_restrict.sql, without blocking writes (SHARE
-- UPDATE EXCLUSIVE). A no-op once validated.
ALTER TABLE blobs VALIDATE CONSTRAINT blobs_storage_backend_id_fkey;
