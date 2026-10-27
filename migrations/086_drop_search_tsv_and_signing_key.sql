-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Contract step of the expand/contract change started in 081/082: 0.10.x
-- still selected these columns, 0.11 and later never touch them.
SET LOCAL lock_timeout = '10s';

-- The GIN indexes on search_tsv go with the columns.
ALTER TABLE deb_packages DROP COLUMN IF EXISTS search_tsv;
ALTER TABLE rpm_packages DROP COLUMN IF EXISTS search_tsv;
ALTER TABLE maven_artifacts DROP COLUMN IF EXISTS search_tsv;
ALTER TABLE npm_versions DROP COLUMN IF EXISTS search_tsv;

-- Also drops idx_rpm_repos_signing_key.
ALTER TABLE rpm_repos DROP COLUMN IF EXISTS signing_key_id;
ALTER TABLE rpm_repos DROP COLUMN IF EXISTS signing_key_fingerprint;
