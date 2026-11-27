-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Git LFS hosted repositories: the gitlfs format joins the repositories.format
-- list (added NOT VALID; validated in 033_gitlfs_validate.no_tx.sql). The objects
-- live in the generic package tables; no table is added.
SET LOCAL lock_timeout = '10s';

ALTER TABLE public.repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE public.repositories ADD CONSTRAINT chk_repo_format CHECK (format IN ('rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'composer', 'rubygems', 'raw', 'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman', 'nix', 'ansible', 'huggingface', 'gitlfs')) NOT VALID;

COMMENT ON COLUMN public.repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, composer, rubygems, raw, conda, conan, ivy, sbt, terraform, apk, pacman, nix, ansible, huggingface, gitlfs. Constrained by chk_repo_format.';
