-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- New package formats join the repositories.format list (added NOT VALID;
-- validated in 033_package_formats_validate.no_tx.sql):
--   cocoapods: CocoaPods proxy repositories (the CDN trunk)
--   gitlfs: Git LFS hosted repositories
--   p2: Eclipse p2 proxy repositories (update sites)
--   pub: Dart and Flutter Pub hosted, proxy and group repositories
-- They live in the generic package tables; no table is added.
SET LOCAL lock_timeout = '10s';

ALTER TABLE public.repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE public.repositories ADD CONSTRAINT chk_repo_format CHECK (format IN ('rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'composer', 'rubygems', 'raw', 'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman', 'nix', 'ansible', 'huggingface', 'cocoapods', 'gitlfs', 'p2', 'pub')) NOT VALID;

COMMENT ON COLUMN public.repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, composer, rubygems, raw, conda, conan, ivy, sbt, terraform, apk, pacman, nix, ansible, huggingface, cocoapods, gitlfs, p2, pub. Constrained by chk_repo_format.';
