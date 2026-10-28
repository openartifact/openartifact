-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Local mirror of the OSV vulnerability database (https://osv.dev, data under
-- CC-BY 4.0), imported per ecosystem by the OSV mirror worker and matched
-- against the stored packages. See docs/security/scanning.md#local-osv-mirror.
--
-- osv_advisories      one row per OSV record (id, summary, severity, withdrawn).
-- osv_advisory_aliases every id the record is known by (CVE, GHSA, ...), plus a
--                      row with the record's own id, so a lookup by any id is
--                      a single join on alias.
-- osv_affected         one row per affected package of a record: the ranges and
--                      the explicit versions, exactly as OSV publishes them.
-- osv_mirror_state     one row per imported ecosystem: the high-water mark of
--                      `modified`, the validator of the last download and the
--                      outcome of the last import.
--
-- Only new tables, and the audit action scan.vuln_resolved (the constraint is
-- read back and extended in place so this composes with migrations that widen
-- the same list). Existing rows are checked in 094_osv_mirror_validate.no_tx.sql.

SET LOCAL lock_timeout = '10s';

CREATE TABLE osv_advisories (
    id text NOT NULL,
    modified timestamptz NOT NULL,
    published timestamptz,
    withdrawn timestamptz,
    summary text DEFAULT '' NOT NULL,
    -- The record's severity array as published ([{"type":"CVSS_V3","score":"..."}]).
    severity jsonb DEFAULT '[]' NOT NULL,
    -- database_specific.severity (CRITICAL, HIGH, MODERATE, ...), "" when absent.
    db_severity text DEFAULT '' NOT NULL,
    cwe_ids text[] DEFAULT '{}' NOT NULL,
    refs text[] DEFAULT '{}' NOT NULL,
    imported_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT osv_advisories_pkey PRIMARY KEY (id)
);

CREATE TABLE osv_advisory_aliases (
    advisory_id text NOT NULL,
    alias text NOT NULL,
    CONSTRAINT osv_advisory_aliases_pkey PRIMARY KEY (advisory_id, alias),
    CONSTRAINT osv_advisory_aliases_advisory_fkey FOREIGN KEY (advisory_id) REFERENCES osv_advisories(id) ON DELETE CASCADE
);

CREATE INDEX idx_osv_advisory_aliases_alias ON osv_advisory_aliases (alias);

CREATE TABLE osv_affected (
    id bigint GENERATED ALWAYS AS IDENTITY NOT NULL,
    advisory_id text NOT NULL,
    -- The OSV ecosystem name ("npm", "PyPI", "crates.io", ...).
    ecosystem text NOT NULL,
    name text NOT NULL,
    -- The name normalized for matching (PEP 503 for PyPI, lower case for the
    -- case-insensitive ecosystems); see internal/osv NormalizeName.
    name_norm text NOT NULL,
    ranges jsonb DEFAULT '[]' NOT NULL,
    versions text[] DEFAULT '{}' NOT NULL,
    CONSTRAINT osv_affected_pkey PRIMARY KEY (id),
    CONSTRAINT osv_affected_advisory_fkey FOREIGN KEY (advisory_id) REFERENCES osv_advisories(id) ON DELETE CASCADE
);

CREATE INDEX idx_osv_affected_package ON osv_affected (ecosystem, name_norm);

CREATE INDEX idx_osv_affected_advisory ON osv_affected (advisory_id);

CREATE TABLE osv_mirror_state (
    ecosystem text NOT NULL,
    -- Largest `modified` among the imported records: the next import only
    -- processes records modified at or after it.
    high_water timestamptz,
    -- ETag / Last-Modified of the last archive download, for conditional requests.
    etag text DEFAULT '' NOT NULL,
    archive_modified text DEFAULT '' NOT NULL,
    source_url text DEFAULT '' NOT NULL,
    advisories integer DEFAULT 0 NOT NULL,
    last_checked_at timestamptz,
    last_imported_at timestamptz,
    last_import_seconds double precision DEFAULT 0 NOT NULL,
    last_rematch_seconds double precision DEFAULT 0 NOT NULL,
    last_error text DEFAULT '' NOT NULL,
    -- Set in the transaction that applies advisory changes and cleared once the
    -- re-match of the stored packages is done: a crash in between makes the
    -- next run re-match every stored package of the ecosystem.
    rematch_pending boolean DEFAULT false NOT NULL,
    -- When the last completed re-match started: the next one also matches the
    -- package versions stored since, which no advisory change names.
    rematched_at timestamptz,
    CONSTRAINT osv_mirror_state_pkey PRIMARY KEY (ecosystem)
);

DO $$
DECLARE
    def text;
BEGIN
    SELECT pg_get_constraintdef(oid) INTO def
    FROM pg_constraint
    WHERE conname = 'chk_audit_action' AND conrelid = 'audit_logs'::regclass;

    IF def IS NULL OR position('scan.vuln_resolved' IN def) > 0 THEN
        RETURN;
    END IF;

    def := replace(def, '''scan.vuln_detected''::character varying',
        '''scan.vuln_detected''::character varying, ''scan.vuln_resolved''::character varying');

    IF position('scan.vuln_resolved' IN def) = 0 THEN
        RAISE EXCEPTION 'chk_audit_action: could not extend constraint definition: %', def;
    END IF;

    ALTER TABLE audit_logs DROP CONSTRAINT chk_audit_action;
    EXECUTE 'ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_action ' || def || ' NOT VALID';
END
$$;
