-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Maven artifact checksums beyond the blob's sha256 (md5/sha1/sha512 are
-- served as .md5/.sha1/.sha512 sidecar files).
ALTER TABLE maven_artifacts ADD COLUMN IF NOT EXISTS md5 text NOT NULL DEFAULT '';
ALTER TABLE maven_artifacts ADD COLUMN IF NOT EXISTS sha1 text NOT NULL DEFAULT '';
ALTER TABLE maven_artifacts ADD COLUMN IF NOT EXISTS sha512 text NOT NULL DEFAULT '';
