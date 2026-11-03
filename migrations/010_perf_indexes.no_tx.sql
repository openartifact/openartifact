-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Index changes found by the performance audit. Every build is CONCURRENTLY
-- and the replaced indexes are dropped only after their successor is valid.

-- 1. The package listing sorted by name (ListRepoComponentsByName) walks a
-- repository's packages in name order and stops at the page limit, instead of
-- joining and sorting every file of the repository.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_packages_repo_name
    ON public.packages (repository_id, name);

-- 2. The ETag stamp queries (helm index, conda repodata, rubygems specs,
-- terraform and cargo) count and hash the live file ids of a repository. A
-- covering index answers them without touching the table. It also still serves
-- the repository_id foreign key cascade, so it replaces the plain index.
DO $$
BEGIN
    -- an earlier run interrupted mid-build leaves an invalid index that IF NOT EXISTS would accept
    IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
               WHERE c.relname = 'idx_package_files_repository_cover' AND NOT i.indisvalid) THEN
        DROP INDEX public.idx_package_files_repository_cover;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_package_files_repository_cover
    ON public.package_files (repository_id) INCLUDE (id, deleted_at, arch, listed);

DROP INDEX CONCURRENTLY IF EXISTS public.idx_package_files_repository;

DO $$
BEGIN
    IF to_regclass('public.idx_package_files_repository_cover') IS NOT NULL THEN
        ALTER INDEX public.idx_package_files_repository_cover RENAME TO idx_package_files_repository;
    END IF;
END $$;

-- 3. uq_package_files_path (repository_id, path) and idx_package_files_path_prefix
-- (repository_id, path text_pattern_ops) index the same rows. One unique index
-- with the pattern operator class answers path equality, ON CONFLICT
-- (repository_id, path) and prefix scans (LIKE 'x/%', starts_with), so every
-- file write maintains one index fewer.
DO $$
BEGIN
    -- an invalid unique index must not pass for the successor: the old ones are dropped next
    IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
               WHERE c.relname = 'uq_package_files_path_pattern' AND NOT i.indisvalid) THEN
        DROP INDEX public.uq_package_files_path_pattern;
    END IF;
END $$;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_package_files_path_pattern
    ON public.package_files (repository_id, path text_pattern_ops) WHERE deleted_at IS NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.uq_package_files_path;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_package_files_path_prefix;

DO $$
BEGIN
    IF to_regclass('public.uq_package_files_path_pattern') IS NOT NULL THEN
        ALTER INDEX public.uq_package_files_path_pattern RENAME TO uq_package_files_path;
    END IF;
END $$;

-- 4. Nothing filters audit entries by resource_type or resource_id (the columns
-- are only written and displayed), so this index was pure write cost.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_audit_logs_resource;
