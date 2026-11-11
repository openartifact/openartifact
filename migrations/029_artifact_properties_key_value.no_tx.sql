-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- prop:KEY=VALUE on GET /api/v1/search looks properties up by key and value across
-- repositories. idx_artifact_properties_repo_key leads with repository_id, so it
-- can only narrow by key; this index answers key and value directly, and the
-- included artifact_ref_id makes the lookup index-only. The build is CONCURRENTLY;
-- an interrupted earlier build leaves an invalid index that IF NOT EXISTS would
-- accept, so it is dropped first.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
               WHERE c.relname = 'idx_artifact_properties_key_value' AND NOT i.indisvalid) THEN
        DROP INDEX public.idx_artifact_properties_key_value;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_artifact_properties_key_value
    ON public.artifact_properties (key, value) INCLUDE (artifact_ref_id);
