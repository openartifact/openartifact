-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Drops columns that the previous release (0.10.0) never reads or writes, so
-- 0.10.0 pods keep serving while this runs during a rolling upgrade:
--   * blobs.ref_count (+ CHECK, partial index): always 0; blob garbage
--     collection uses blob_deletions.
--   * release_bundles.source_query and scan_jobs.started_at.
-- Columns 0.10.0 still selects (the stored search_tsv columns and
-- rpm_repos.signing_key_id / signing_key_fingerprint) stay until a later
-- release; nothing in this release reads them. The redundant plain indexes are
-- dropped in 082_drop_redundant_indexes.no_tx.sql.

SET LOCAL lock_timeout = '10s';

ALTER TABLE blobs DROP COLUMN IF EXISTS ref_count;

ALTER TABLE release_bundles DROP COLUMN IF EXISTS source_query;
ALTER TABLE scan_jobs DROP COLUMN IF EXISTS started_at;
