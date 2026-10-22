-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A storage backend that still holds blobs must not be deleted: ON DELETE SET
-- NULL silently re-pointed those blobs at the primary store, where their bytes
-- are not. Replace the FK with RESTRICT. Added NOT VALID here (no table scan
-- under lock); validated in 033_blobs_backend_restrict_validate.no_tx.sql.

SET LOCAL lock_timeout = '10s';

ALTER TABLE blobs DROP CONSTRAINT blobs_storage_backend_id_fkey;
ALTER TABLE blobs ADD CONSTRAINT blobs_storage_backend_id_fkey
    FOREIGN KEY (storage_backend_id) REFERENCES storage_backends(id) ON DELETE RESTRICT NOT VALID;
