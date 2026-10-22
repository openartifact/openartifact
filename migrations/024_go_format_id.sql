-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- The Go module format is "go" everywhere (repositories.format, package
-- listings, search, scan ecosystems). Package listings and the UI used to emit
-- "gomod" for Go components, so scans and AI analyses of Go modules stored that
-- id in artifact_refs.format and vuln_results.ecosystem. Rewrite those rows.
SET LOCAL lock_timeout = '5s';

UPDATE artifact_refs SET format = 'go' WHERE format = 'gomod';

UPDATE vuln_results SET ecosystem = 'go' WHERE ecosystem = 'gomod';
