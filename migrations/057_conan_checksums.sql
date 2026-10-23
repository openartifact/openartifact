-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Conan 1 clients (docs/formats/conan.md) identify files by MD5 (the recipe and
-- package snapshots, checked against the download cache) and by SHA-1 (checksum
-- deploy: `PUT` with `X-Checksum-Deploy: true` stores a file the repository
-- already holds without uploading it). Both are stored per file when it is
-- uploaded. Rows stored before this migration keep an empty value; the MD5 of
-- those is computed from the blob the first time a snapshot needs it, and they
-- take no part in checksum deploy until the file is uploaded again.
--
-- A conan revision's time is the updated_at of its conanmanifest.txt. The
-- updated_at trigger would move it when only the digests are filled in, so it
-- skips an UPDATE that changes them (the upsert on upload sets updated_at
-- itself).
--
-- Metadata-only ADD COLUMN (constant defaults); the lookup index is built in
-- 057_conan_checksums_indexes.no_tx.sql.

SET LOCAL lock_timeout = '10s';

ALTER TABLE conan_packages
    ADD COLUMN md5 character varying(32) NOT NULL DEFAULT '',
    ADD COLUMN sha1 character varying(40) NOT NULL DEFAULT '';

COMMENT ON COLUMN conan_packages.md5 IS 'Lowercase hex MD5 of the file, as Conan 1 snapshots report it. Empty until computed.';
COMMENT ON COLUMN conan_packages.sha1 IS 'Lowercase hex SHA-1 of the file, for Conan checksum deploy (X-Checksum-Sha1). Empty for files stored before it was tracked.';

DROP TRIGGER IF EXISTS update_conan_packages_updated_at ON conan_packages;

CREATE TRIGGER update_conan_packages_updated_at
    BEFORE UPDATE ON conan_packages
    FOR EACH ROW
    WHEN (OLD.md5 IS NOT DISTINCT FROM NEW.md5 AND OLD.sha1 IS NOT DISTINCT FROM NEW.sha1)
    EXECUTE FUNCTION update_updated_at_column();
