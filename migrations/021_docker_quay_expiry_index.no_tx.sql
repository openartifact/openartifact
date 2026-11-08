-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- The expiry pass looks up the manifests of one repository past their deadline;
-- few manifests carry one, so the index is partial. The build is CONCURRENTLY;
-- an interrupted earlier build leaves an invalid index that IF NOT EXISTS would
-- accept, so it is dropped first.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
               WHERE c.relname = 'idx_docker_manifests_expires' AND NOT i.indisvalid) THEN
        DROP INDEX public.idx_docker_manifests_expires;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_docker_manifests_expires
    ON public.docker_manifests (repository_id, expires_at)
    WHERE expires_at IS NOT NULL AND deleted_at IS NULL;
