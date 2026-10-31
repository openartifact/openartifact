-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A repository's activity (GET /api/v1/repositories/{name}/activity) reads its
-- audit entries newest first by keyset: WHERE repository_id = $1 ORDER BY
-- created_at DESC, id LIMIT n. With uploads audited per artifact a busy
-- repository holds millions of entries, and the single-column index would sort
-- them all for every page; this one returns each page straight from the index.
-- Its leading column also serves the ON DELETE SET NULL foreign key, so it
-- replaces idx_audit_logs_repository_id.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_logs_repository_created
    ON public.audit_logs (repository_id, created_at DESC, id) WHERE repository_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_audit_logs_repository_id;
