-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Format fixes, round 2 (nuget, conda, conan).
--
-- Keeps the transaction short: metadata-only ADD COLUMN (constant defaults, no
-- table rewrite), NOT VALID constraints, and one bounded UPDATE for nuget. The
-- index swap and constraint validation live in 019_formats_b_indexes.no_tx.sql.
--
-- Each format owns one section below; add yours under its own heading.

SET LOCAL lock_timeout = '10s';

-- nuget ---------------------------------------------------------------------
-- Versions are stored in NuGet's normalized lower-case form (1.0.0.0 -> 1.0.0,
-- 1.0 -> 1.0.0, 01.2.3 -> 1.2.3, 1.0.0-Beta -> 1.0.0-beta, +metadata dropped),
-- the form clients request in PackageBaseAddress URLs. Rewrite the rows stored
-- verbatim before. A live row is left alone when its normalized form is already
-- taken by another live row (the live-row unique index would reject it), and of
-- several live rows that normalize to the same version only one is rewritten;
-- trashed rows are not part of the unique index and are always rewritten.
WITH parsed AS (
    SELECT id, repository_id, package_id, version AS old_version, deleted_at, created_at,
           regexp_match(version, '^[[:space:]]*([0-9]+)(?:\.([0-9]+))?(?:\.([0-9]+))?(?:\.([0-9]+))?(-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.+-]*)?[[:space:]]*$') AS m
    FROM nuget_packages
), normalized AS (
    SELECT id, repository_id, package_id, old_version, deleted_at, created_at,
           coalesce(nullif(ltrim(m[1], '0'), ''), '0') || '.'
           || coalesce(nullif(ltrim(m[2], '0'), ''), '0') || '.'
           || coalesce(nullif(ltrim(m[3], '0'), ''), '0')
           || coalesce('.' || nullif(ltrim(m[4], '0'), ''), '')
           || lower(coalesce(m[5], '')) AS new_version
    FROM parsed
    WHERE m IS NOT NULL
), chosen AS (
    SELECT n.id, n.new_version
    FROM normalized n
    WHERE n.old_version <> n.new_version AND n.deleted_at IS NOT NULL
    UNION ALL
    SELECT live.id, live.new_version
    FROM (
        SELECT DISTINCT ON (n.repository_id, n.package_id, n.new_version) n.id, n.new_version
        FROM normalized n
        WHERE n.old_version <> n.new_version
          AND n.deleted_at IS NULL
          AND NOT EXISTS (
              SELECT 1 FROM nuget_packages x
              WHERE x.repository_id = n.repository_id
                AND x.package_id = n.package_id
                AND x.version = n.new_version
                AND x.deleted_at IS NULL)
        ORDER BY n.repository_id, n.package_id, n.new_version, n.created_at, n.id
    ) live
)
UPDATE nuget_packages np SET version = chosen.new_version
FROM chosen
WHERE np.id = chosen.id;

-- conda ---------------------------------------------------------------------
-- One row per file: a .tar.bz2 and a .conda with the same name/version/build
-- are separate files that repodata lists in separate sections. The file name is
-- name-version-build<extension>. Rows stored before this migration were all
-- advertised as .tar.bz2, which is what the default records.
--
-- build_number, md5, timestamp_ms, depends and index_extra come from the
-- package's info/index.json at upload; rows stored before keep the defaults
-- (empty) until the package is uploaded again. index_extra holds the remaining
-- repodata fields (constrains, license, license_family, noarch, arch, platform,
-- features, track_features).
ALTER TABLE conda_packages
    ADD COLUMN extension text NOT NULL DEFAULT '.tar.bz2',
    ADD COLUMN build_number integer NOT NULL DEFAULT 0,
    ADD COLUMN md5 character varying(32) NOT NULL DEFAULT '',
    ADD COLUMN timestamp_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN index_extra jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD CONSTRAINT chk_conda_packages_extension CHECK (extension IN ('.tar.bz2', '.conda')) NOT VALID;

COMMENT ON COLUMN conda_packages.extension IS 'Package file extension: .tar.bz2 (legacy) or .conda (zip with zstd members).';
COMMENT ON COLUMN conda_packages.timestamp_ms IS 'info/index.json timestamp in milliseconds since the epoch; 0 when the package declares none.';
COMMENT ON COLUMN conda_packages.index_extra IS 'Remaining info/index.json fields emitted into repodata.json (constrains, license, noarch, ...).';

-- Earlier releases split <name>-<version>-<build> at the first two hyphens, so a
-- package named scikit-learn was stored as name 'scikit', version 'learn', build
-- '1.0-py39_0'. A build string never contains a hyphen, so such a row is
-- recognizable; re-split its file name (name-version-build, which is exactly
-- what repodata listed) from the right, as uploads and downloads do now. A
-- re-split name always contains a hyphen and no unsplit row's name does, so the
-- live-row unique index cannot conflict.
UPDATE conda_packages c
SET name = m.parts[1], version = m.parts[2], build_string = m.parts[3]
FROM (
    SELECT id, regexp_match(name || '-' || version || '-' || build_string, '^(.+)-([^-]+)-([^-]+)$') AS parts
    FROM conda_packages
    WHERE build_string LIKE '%-%'
) m
WHERE c.id = m.id AND m.parts IS NOT NULL;

-- conan ---------------------------------------------------------------------
-- One row per stored file. Until now a row was a whole package keyed by
-- (name, version, user/channel, recipe revision) with a single blob, so a second
-- file of the same recipe revision (conanmanifest.txt, conan_sources.tgz, ...)
-- could not be stored. A row is now one file of a recipe revision
-- (package_id = '') or of a package revision of it, identified by
-- (recipe_revision, package_id, package_revision, filename).
--
-- Rows stored before this migration keep filename = '': they held one unnamed
-- blob under the pseudo revision 0, which the revisions protocol cannot serve.
-- They stay listed (and deletable) but are not offered to Conan clients;
-- publish the package again with `conan upload`.
ALTER TABLE conan_packages
    ADD COLUMN package_id character varying(64) NOT NULL DEFAULT '',
    ADD COLUMN package_revision character varying(64) NOT NULL DEFAULT '',
    ADD COLUMN filename text NOT NULL DEFAULT '';

COMMENT ON COLUMN conan_packages.package_id IS 'Package id (binary configuration hash); empty for a file of the recipe revision itself.';
COMMENT ON COLUMN conan_packages.package_revision IS 'Package revision; empty for a file of the recipe revision itself.';
COMMENT ON COLUMN conan_packages.filename IS 'Path of the stored file inside its recipe or package revision (conanfile.py, conan_package.tgz, metadata/sign/...); empty for rows stored before migration 019.';

-- rpm -----------------------------------------------------------------------
-- repomd.xml.asc was written double-armored (no client could verify it) and
-- the installed/archive sizes of stored packages were never read from the
-- header (0 in primary.xml). Flag every repository once: the startup pass
-- (RPMetadataWorker.RegenerateStale) re-signs repomd.xml and re-reads the
-- stored packages to backfill the sizes. One row per RPM repository.
UPDATE rpm_repos SET metadata_stale = true;
