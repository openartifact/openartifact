-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Licenses read from an artifact are kept as written when they name no single
-- SPDX license ("BSD", "BSD License"). unresolved_licenses lists those entries
-- of licenses so the scan can flag them for review. Metadata-only change
-- (constant default).
ALTER TABLE license_results
    ADD COLUMN unresolved_licenses text[] DEFAULT '{}'::text[] NOT NULL;

COMMENT ON COLUMN license_results.unresolved_licenses IS 'Entries of licenses that are not SPDX expressions (kept raw, flagged for review).';
