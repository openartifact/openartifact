-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Trigram index for the catalog's `q` filter on the version column (the name
-- column already has idx_packages_name_trgm): a substring search for a rare
-- term reads a few index entries instead of every version of the repository.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_package_versions_version_trgm
    ON public.package_versions USING gin (version public.gin_trgm_ops) WHERE deleted_at IS NULL;

-- The repository_id foreign keys cascade on delete; without a plain index
-- every deleted repository scans the whole table (the partial indexes cannot
-- serve the cascade: it also has to find trashed rows).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_package_files_repository
    ON public.package_files (repository_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_package_versions_repository
    ON public.package_versions (repository_id);

-- Superseded by idx_package_versions_repository (a live-only filter is a cheap
-- recheck on it); one index fewer to maintain on every version write.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_package_versions_repo;
