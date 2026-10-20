-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Index every blob_id FK so BlobReferenceCount / orphan GC / ON DELETE RESTRICT checks
-- do not sequentially scan package tables.

CREATE INDEX IF NOT EXISTS idx_rpm_packages_blob_id ON rpm_packages (blob_id);
CREATE INDEX IF NOT EXISTS idx_deb_packages_blob_id ON deb_packages (blob_id);
CREATE INDEX IF NOT EXISTS idx_npm_versions_blob_id ON npm_versions (blob_id);
CREATE INDEX IF NOT EXISTS idx_maven_artifacts_blob_id ON maven_artifacts (blob_id);
CREATE INDEX IF NOT EXISTS idx_docker_blobs_blob_id ON docker_blobs (blob_id);
CREATE INDEX IF NOT EXISTS idx_docker_manifests_blob_id ON docker_manifests (blob_id);
CREATE INDEX IF NOT EXISTS idx_pypi_packages_blob_id ON pypi_packages (blob_id);
CREATE INDEX IF NOT EXISTS idx_nuget_packages_blob_id ON nuget_packages (blob_id);
CREATE INDEX IF NOT EXISTS idx_cargo_crates_blob_id ON cargo_crates (blob_id);
CREATE INDEX IF NOT EXISTS idx_rubygems_gems_blob_id ON rubygems_gems (blob_id);
CREATE INDEX IF NOT EXISTS idx_conda_packages_blob_id ON conda_packages (blob_id);
CREATE INDEX IF NOT EXISTS idx_conan_packages_blob_id ON conan_packages (blob_id);
CREATE INDEX IF NOT EXISTS idx_ivy_artifacts_blob_id ON ivy_artifacts (blob_id);
CREATE INDEX IF NOT EXISTS idx_sbt_artifacts_blob_id ON sbt_artifacts (blob_id);
CREATE INDEX IF NOT EXISTS idx_helm_charts_blob_id ON helm_charts (blob_id);
CREATE INDEX IF NOT EXISTS idx_raw_artifacts_blob_id ON raw_artifacts (blob_id);
CREATE INDEX IF NOT EXISTS idx_gomod_modules_zip_blob_id ON gomod_modules (zip_blob_id);
CREATE INDEX IF NOT EXISTS idx_gomod_modules_mod_blob_id ON gomod_modules (mod_blob_id);
