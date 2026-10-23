-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Licenses read from an artifact are kept as written when they name no single
-- SPDX license ("BSD", "BSD License"). unresolved_licenses lists those entries
-- of licenses so the scan can flag them for review. license_source records
-- whether the licenses were read from the package file ('artifact') or only
-- taken from what a client declared ('client_declared': form fields, license
-- properties, an unreadable or unsupported file); rows written before this
-- column come from properties. Metadata-only change (constant defaults).
ALTER TABLE license_results
    ADD COLUMN unresolved_licenses text[] DEFAULT '{}'::text[] NOT NULL,
    ADD COLUMN license_source text DEFAULT 'client_declared' NOT NULL,
    ADD CONSTRAINT license_results_license_source_check CHECK (license_source IN ('artifact', 'client_declared'));

COMMENT ON COLUMN license_results.unresolved_licenses IS 'Entries of licenses that are not SPDX expressions (kept raw, flagged for review).';
COMMENT ON COLUMN license_results.license_source IS 'artifact: read from the package file; client_declared: only form fields or properties.';
