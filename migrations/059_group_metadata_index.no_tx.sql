-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Foreign-key index for the cascade that walks group_package_owners when a
-- member repository is deleted. Re-runnable: an interrupted CREATE INDEX
-- CONCURRENTLY leaves an INVALID index that IF NOT EXISTS would skip for good,
-- so it is dropped first when invalid.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
        WHERE c.relname = 'idx_group_package_owners_member' AND NOT i.indisvalid
    ) THEN
        DROP INDEX idx_group_package_owners_member;
    END IF;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_group_package_owners_member ON group_package_owners (member_repo_id);
