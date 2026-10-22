-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Search: trigram matching for the name filter (ILIKE '%term%'). pg_trgm ships
-- with the PostgreSQL contrib modules (it is in the official images and every
-- managed service) and is a trusted extension, so the database owner may
-- create it. The GIN indexes that use it are built in
-- 030_perf_indexes_build.no_tx.sql.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
