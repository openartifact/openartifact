-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- GET /api/v1/search?q=TERM matches the AI analysis tags of an artifact
-- case-insensitively and exactly (internal/search, aiTaggedArtifacts).
-- idx_ai_results_tags is a plain containment index over the tags as the model
-- wrote them, so it can only answer a case-sensitive match; this one indexes the
-- lower-cased array, and the search probes it with the same expression. The build
-- is CONCURRENTLY; an interrupted earlier build leaves an invalid index that
-- IF NOT EXISTS would accept, so it is dropped first.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
               WHERE c.relname = 'idx_ai_results_tags_lower' AND NOT i.indisvalid) THEN
        DROP INDEX public.idx_ai_results_tags_lower;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ai_results_tags_lower
    ON public.ai_results USING gin ((lower(tags::text)::jsonb));
