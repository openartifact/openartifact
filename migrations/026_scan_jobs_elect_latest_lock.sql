-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Two concurrent inserts for the same (artifact, scan type) each elected the
-- latest job from a snapshot that did not see the other's uncommitted row, so
-- both could leave is_latest wrong. Serialize the election per pair with a
-- transaction-scoped advisory lock; the second waits for the first's commit and
-- then elects over a statement snapshot that sees it.
CREATE OR REPLACE FUNCTION public.scan_jobs_elect_latest() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    ref uuid := COALESCE(NEW.artifact_ref_id, OLD.artifact_ref_id);
    typ text := COALESCE(NEW.scan_type, OLD.scan_type);
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(ref::text || ':' || typ, 0));

    UPDATE public.scan_jobs j SET is_latest = (j.id = w.id)
    FROM (SELECT id FROM public.scan_jobs WHERE artifact_ref_id = ref AND scan_type = typ ORDER BY created_at DESC, id LIMIT 1) w
    WHERE j.artifact_ref_id = ref AND j.scan_type = typ AND j.is_latest IS DISTINCT FROM (j.id = w.id);

    RETURN NULL;
END
$$;
