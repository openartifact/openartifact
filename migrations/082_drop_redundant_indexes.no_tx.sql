-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Drops btree indexes that duplicate a UNIQUE/PRIMARY KEY index or are a
-- leading prefix of one (or of a larger index with the same predicate), so
-- they add write cost and disk without serving any query the wider index
-- cannot. Foreign keys keep an index on the referencing column through the
-- retained wider index. Re-runnable: IF EXISTS everywhere.
DROP INDEX CONCURRENTLY IF EXISTS idx_ai_results_scan_job;
DROP INDEX CONCURRENTLY IF EXISTS idx_license_results_scan_job;
DROP INDEX CONCURRENTLY IF EXISTS idx_secret_results_scan_job;
DROP INDEX CONCURRENTLY IF EXISTS idx_sbom_results_scan_job;
DROP INDEX CONCURRENTLY IF EXISTS idx_artifact_refs_repository_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_auth_groups_project_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_permission_targets_project_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_project_members_project_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_repositories_project_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_auth_sessions_token_hash;
DROP INDEX CONCURRENTLY IF EXISTS idx_auth_sessions_user_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_auth_tokens_token_hash;
DROP INDEX CONCURRENTLY IF EXISTS idx_debian_architectures_dist;
DROP INDEX CONCURRENTLY IF EXISTS idx_debian_components_dist;
DROP INDEX CONCURRENTLY IF EXISTS idx_debian_distributions_repo;
DROP INDEX CONCURRENTLY IF EXISTS idx_docker_blobs_repository_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_maven_artifacts_ga;
DROP INDEX CONCURRENTLY IF EXISTS idx_npm_dist_tags_package_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_npm_packages_repository_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_bundle_items_bundle_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_release_bundles_name;
DROP INDEX CONCURRENTLY IF EXISTS idx_repositories_name;
DROP INDEX CONCURRENTLY IF EXISTS idx_repositories_name_unique;
DROP INDEX CONCURRENTLY IF EXISTS idx_repo_group_members_group;
DROP INDEX CONCURRENTLY IF EXISTS idx_repository_metadata_path;
DROP INDEX CONCURRENTLY IF EXISTS idx_repository_metadata_repository_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_rpm_repos_repository_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_scan_jobs_artifact_ref;
DROP INDEX CONCURRENTLY IF EXISTS idx_storage_quotas_repository_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_webhook_deliveries_retry;
DROP INDEX CONCURRENTLY IF EXISTS idx_webhook_deliveries_webhook;
