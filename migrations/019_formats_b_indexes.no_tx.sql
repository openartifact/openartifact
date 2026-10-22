-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Format fixes, round 2, part 2: index swaps and constraint validation, run
-- outside a transaction (one statement at a time) so CREATE INDEX CONCURRENTLY
-- works. Re-runnable: the first statement drops invalid leftovers of a failed
-- CONCURRENTLY build, everything else is IF [NOT] EXISTS.
--
-- Each format owns one section below; add yours under its own heading.

DO $$
DECLARE
    idx regclass;
BEGIN
    SET LOCAL lock_timeout = '10s';

    FOR idx IN
        SELECT i.indexrelid::regclass
        FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        WHERE NOT i.indisvalid
          AND c.relnamespace = to_regnamespace(current_schema())
          AND c.relname IN (
            'conda_packages_live_file_key',
            'conan_packages_live_file_key',
            'idx_cargo_crates_repo_lower_name'
          )
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

-- conda_packages: uniqueness is per file (extension included) so the .tar.bz2
-- and .conda builds of one package can coexist. The new index is built before
-- the old one goes, so uniqueness is never lost.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS conda_packages_live_file_key ON conda_packages (repository_id, name, version, build_string, platform, extension) WHERE deleted_at IS NULL;
DROP INDEX CONCURRENTLY IF EXISTS conda_packages_live_coords_key;
ALTER TABLE conda_packages VALIDATE CONSTRAINT chk_conda_packages_extension;

-- conan_packages: uniqueness is per stored file (recipe and package revision and
-- file name included) instead of per recipe revision, so the files of one
-- revision can coexist. The new index is built before the old one goes, so
-- uniqueness is never lost.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS conan_packages_live_file_key ON conan_packages (repository_id, name, version, user_channel, recipe_revision, package_id, package_revision, filename) WHERE deleted_at IS NULL;
DROP INDEX CONCURRENTLY IF EXISTS conan_packages_live_coords_key;

-- cargo_crates: crate names are matched case-insensitively (lower(name)), so the
-- lookup needs an expression index on the lowered name.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_cargo_crates_repo_lower_name ON cargo_crates (repository_id, lower(name)) WHERE deleted_at IS NULL;
