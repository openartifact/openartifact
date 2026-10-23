-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Adds the user.deleted action (audit.ActionUserDeleted) to chk_audit_action.
-- The constraint definition is read back and extended in place instead of
-- re-listing every action, so the migration composes with other migrations
-- that widen the same list. NOT VALID here; existing rows are checked in
-- 080_audit_user_deleted_validate.no_tx.sql.

SET LOCAL lock_timeout = '10s';

DO $$
DECLARE
    def text;
BEGIN
    SELECT pg_get_constraintdef(oid) INTO def
    FROM pg_constraint
    WHERE conname = 'chk_audit_action' AND conrelid = 'audit_logs'::regclass;

    IF def IS NULL OR position('user.deleted' IN def) > 0 THEN
        RETURN;
    END IF;

    def := replace(def, '''user.disabled''::character varying',
        '''user.disabled''::character varying, ''user.deleted''::character varying');

    IF position('user.deleted' IN def) = 0 THEN
        RAISE EXCEPTION 'chk_audit_action: could not extend constraint definition: %', def;
    END IF;

    ALTER TABLE audit_logs DROP CONSTRAINT chk_audit_action;
    EXECUTE 'ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_action ' || def || ' NOT VALID';
END
$$;
