-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A download only increments package_files.download_count (and bumps
-- updated_at). The journal's row image excludes download_count, so the sidecar
-- is the same either way; the generic trigger still upserted recovery_dirty and
-- called nextval for every download. This function skips an UPDATE whose only
-- differences are download_count and updated_at, and is otherwise recovery_trg_id.
SET LOCAL lock_timeout = '5s';

CREATE OR REPLACE FUNCTION public.recovery_trg_package_files() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.download_count IS DISTINCT FROM NEW.download_count
       AND to_jsonb(OLD) - 'download_count' - 'updated_at' = to_jsonb(NEW) - 'download_count' - 'updated_at' THEN
        RETURN NULL;
    END IF;

    IF NOT (SELECT enabled FROM recovery_state) THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        PERFORM recovery_mark(TG_TABLE_NAME, OLD.id::text);
    ELSE
        PERFORM recovery_mark(TG_TABLE_NAME, NEW.id::text);

        IF TG_OP = 'UPDATE' AND OLD.id IS DISTINCT FROM NEW.id THEN
            PERFORM recovery_mark(TG_TABLE_NAME, OLD.id::text);
        END IF;
    END IF;

    RETURN NULL;
END
$$;

CREATE OR REPLACE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.package_files
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_package_files();
