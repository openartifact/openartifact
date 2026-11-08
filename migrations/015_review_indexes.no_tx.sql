-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Indexes for predicates that had none. Every build is CONCURRENTLY; a build
-- interrupted by an earlier run leaves an invalid index that IF NOT EXISTS
-- would accept, so it is dropped first.

DO $$
DECLARE
    idx text;
BEGIN
    FOREACH idx IN ARRAY ARRAY[
        'idx_package_versions_package',
        'idx_proxy_cache_negative',
        'idx_proxy_cache_listing',
        'idx_docker_blob_images_digest',
        'idx_proxy_cache_key_prefix',
        'idx_proxy_cache_docker_image',
        'idx_scan_jobs_completed',
        'idx_dtpushes_updated'
    ] LOOP
        IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
                   WHERE c.relname = idx AND NOT i.indisvalid) THEN
            EXECUTE format('DROP INDEX public.%I', idx);
        END IF;
    END LOOP;
END $$;

-- 1. Every package_id index of package_versions is partial on deleted_at, so
-- the unfiltered lookups (PruneEmptyPackages, the npm empty-package delete, and
-- the ON DELETE CASCADE from packages) scanned the whole table per package.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_package_versions_package
    ON public.package_versions (package_id);

-- 2. UpsertNegativeProxyCacheEntry counts a repository's live 404 entries on
-- every upstream miss; without this it read every cache entry of the repository.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxy_cache_negative
    ON public.proxy_cache_entries (repository_id, expires_at) WHERE status_code = 404;

-- 3. The proxy-cache branch of ListRepoComponents pages newest first; without an
-- ordered index each page sorted the whole cache of the repository.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxy_cache_listing
    ON public.proxy_cache_entries (repository_id, created_at DESC, id)
    WHERE status_code = 200 AND blob_key IS NOT NULL;

-- 4. Blob visibility (cross-image mounts and references), blob deletes and the
-- (repository_id, digest) foreign key cascade look links up by digest; the
-- primary key (repository_id, image, digest) only bounds the repository.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_docker_blob_images_digest
    ON public.docker_blob_images (repository_id, digest);

-- 5. ListCachedProxyKeys filters on starts_with(cache_key, prefix); with
-- text_pattern_ops the planner turns that into an index range whatever the
-- database collation (the unique (repository_id, cache_key) index cannot).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxy_cache_key_prefix
    ON public.proxy_cache_entries (repository_id, cache_key text_pattern_ops)
    WHERE status_code = 200 AND blob_key IS NOT NULL;

-- 6. The proxied branch of ListDockerCatalog probes the next cached image name
-- of each proxy repository in order (a loose index scan); the expression must
-- match the query text exactly.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxy_cache_docker_image
    ON public.proxy_cache_entries (repository_id, (substring(cache_key FROM '^(.+)/manifests/[^/]+$')))
    WHERE status_code = 200 AND blob_key IS NOT NULL;

-- 7. SecurityHealthStats probes the newest finished scan of each scan type
-- (one ordered index probe per type).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_scan_jobs_completed
    ON public.scan_jobs (scan_type, (COALESCE(completed_at, updated_at)) DESC)
    WHERE status IN ('completed', 'partial');

-- 8. LatestFinishedDependencyTrackPush reads the newest finished push.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dtpushes_updated
    ON public.dependency_track_pushes (updated_at DESC) WHERE state IN ('done', 'failed');
