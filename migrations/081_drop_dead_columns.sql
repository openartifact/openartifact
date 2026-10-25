-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Drops columns nothing reads or writes:
--   * search_tsv (+ GIN index) on deb_packages, rpm_packages, maven_artifacts
--     and npm_versions: a stored to_tsvector() recomputed on every insert and
--     update; package search uses the trigram indexes.
--   * blobs.ref_count (+ CHECK, partial index): always 0; blob garbage
--     collection uses blob_deletions.
--   * rpm_repos.signing_key_id / signing_key_fingerprint (+ index): repodata
--     signing goes through gpg_keys.
--   * release_bundles.source_query and scan_jobs.started_at.
-- The dependent indexes and the CHECK go with their columns. Each ALTER takes
-- a brief ACCESS EXCLUSIVE lock and is metadata-only except the stored
-- generated columns, whose drop does not rewrite the table either. The
-- redundant plain indexes are dropped in 082_drop_redundant_indexes.no_tx.sql.

SET LOCAL lock_timeout = '10s';

ALTER TABLE deb_packages DROP COLUMN IF EXISTS search_tsv;
ALTER TABLE rpm_packages DROP COLUMN IF EXISTS search_tsv;
ALTER TABLE maven_artifacts DROP COLUMN IF EXISTS search_tsv;
ALTER TABLE npm_versions DROP COLUMN IF EXISTS search_tsv;

ALTER TABLE blobs DROP COLUMN IF EXISTS ref_count;

ALTER TABLE rpm_repos DROP COLUMN IF EXISTS signing_key_id;
ALTER TABLE rpm_repos DROP COLUMN IF EXISTS signing_key_fingerprint;

ALTER TABLE release_bundles DROP COLUMN IF EXISTS source_query;
ALTER TABLE scan_jobs DROP COLUMN IF EXISTS started_at;
