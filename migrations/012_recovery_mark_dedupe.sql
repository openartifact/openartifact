-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- One upload changes the same row several times in one transaction (a package
-- row inserted, then touched again by the version, file and tag writes that
-- follow), and every change called recovery_mark, whose upsert rewrote the
-- queue row each time: a new tuple, an index entry and a nextval per call.
-- The sidecar is built from the row as it is when the drain reads it, so a
-- second mark of a row this transaction already marked changes nothing.
--
-- A queue row whose xmin is the current transaction was written by it (insert
-- or upsert), is invisible to the drain and to every other writer until commit,
-- and keeps the seq it got. The check is a read of that tuple; only the first
-- mark of a row per transaction writes. A mark made inside a savepoint carries
-- the subtransaction's xid, so it never matches here: the mark is then written
-- again, which is only redundant.
SET LOCAL lock_timeout = '5s';

CREATE OR REPLACE FUNCTION public.recovery_mark(p_table text, p_key text) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM recovery_dirty
        WHERE table_name = p_table AND row_key = p_key AND xmin = pg_current_xact_id()::xid
    ) THEN
        RETURN;
    END IF;

    INSERT INTO recovery_dirty (table_name, row_key) VALUES (p_table, p_key)
    ON CONFLICT (table_name, row_key)
    DO UPDATE SET seq = nextval('recovery_dirty_seq'), marked_at = now(), retry_at = NULL;
END
$$;
