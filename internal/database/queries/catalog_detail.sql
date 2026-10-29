-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- One package version of a repository, as the artifact (file) the listing row id
-- names (backs GET /api/v1/repositories/{name}/packages/{id}). The id is a
-- package_files.id, as for GetRepoComponentBlob.
--
-- doc is the JSON the format's storage.Detail descriptor reads its fields from:
-- p = packages.attrs, v = package_versions.metadata, f = package_files.attrs,
-- c = package_files.checksums, s = the side-table row of the file (bulk columns
-- left out), k = plain columns (arch, download_count, name, version_key), m = the
-- attrs of the unlisted sibling file of the version (go.mod next to the zip).
-- Two explicit non-package cases carry no doc: a proxy cache entry and a docker
-- tag (docker keeps its own tables); they report their display fields in info.
--
-- pref_size is the size the row records itself (it wins over the blob's, as in the
-- listing); blob_key is a proxy cache entry's blob, by sha256.

-- name: GetRepoComponentDetail :one
SELECT p.id::uuid AS id, p.format::text AS format, p.name::text AS name, p.version::text AS version,
       p.arch::text AS arch, p.filename::text AS filename,
       p.created_at::timestamptz AS created_at, p.updated_at::timestamptz AS updated_at,
       COALESCE(COALESCE(bi.sha256, bs.sha256), '')::text AS sha256,
       COALESCE(p.pref_size, bi.size_bytes, bs.size_bytes, p.fb_size, 0)::bigint AS size_bytes,
       p.doc::jsonb AS doc, p.info::jsonb AS info
FROM (
    SELECT f.id::uuid AS id, pk.format::text AS format, pk.name::text AS name, v.version::text AS version, f.arch::text AS arch,
           f.filename::text AS filename, f.created_at::timestamptz AS created_at, f.updated_at::timestamptz AS updated_at,
           f.blob_id::uuid AS blob_id, NULL::text AS blob_key, f.size_bytes::bigint AS pref_size, NULL::bigint AS fb_size,
           jsonb_build_object(
               'p', pk.attrs, 'v', v.metadata, 'f', f.attrs, 'c', f.checksums,
               's', COALESCE(to_jsonb(cf), to_jsonb(df), to_jsonb(mf), to_jsonb(pf) - 'file_list' - 'signature', to_jsonb(rf)),
               'k', jsonb_build_object('arch', f.arch, 'download_count', f.download_count, 'name', pk.name, 'version_key', v.version_key),
               'm', sib.attrs)::jsonb AS doc,
           NULL::jsonb AS info
    FROM package_files f
    JOIN package_versions v ON v.id = f.version_id
    JOIN packages pk ON pk.id = v.package_id
    LEFT JOIN conan_files cf ON cf.id = f.id
    LEFT JOIN deb_files df ON df.id = f.id
    LEFT JOIN maven_files mf ON mf.id = f.id
    LEFT JOIN pacman_files pf ON pf.id = f.id
    LEFT JOIN rpm_files rf ON rf.id = f.id
    LEFT JOIN LATERAL (
        SELECT m.attrs FROM package_files m
        WHERE m.version_id = f.version_id AND NOT m.listed AND m.deleted_at IS NULL
        ORDER BY m.created_at, m.id LIMIT 1
    ) AS sib ON true
    WHERE f.deleted_at IS NULL AND f.listed AND f.repository_id = sqlc.arg(repo_id)::uuid AND f.id = sqlc.arg(id)::uuid
    UNION ALL
    -- proxy cache: a cached upstream file of a proxy repository
    SELECT t.id::uuid, r.format::text, t.cache_key::text, ''::text, ''::text,
           t.cache_key::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           NULL::uuid, t.blob_key::text, NULL::bigint, t.size_bytes::bigint,
           NULL::jsonb,
           jsonb_build_object('content_type', COALESCE(t.content_type, ''), 'hit_count', t.hit_count::text)::jsonb
    FROM proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id
    WHERE r.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
      AND t.status_code = 200 AND t.blob_key IS NOT NULL
    UNION ALL
    -- docker: one row per tag; the manifest is the file
    SELECT t.id::uuid, 'docker'::text, (CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)::text, t.name::text, ''::text,
           t.manifest_digest::text, t.created_at::timestamptz, t.updated_at::timestamptz,
           dm.blob_id::uuid, NULL::text, COALESCE(dm.size, 0)::bigint, NULL::bigint,
           NULL::jsonb,
           jsonb_build_object('digest', t.manifest_digest, 'media_type', COALESCE(dm.media_type, ''))::jsonb
    FROM docker_tags t
    JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL
    LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest = t.manifest_digest AND dm.deleted_at IS NULL
    WHERE t.deleted_at IS NULL AND t.repository_id = sqlc.arg(repo_id)::uuid AND t.id = sqlc.arg(id)::uuid
) AS p
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE id = p.blob_id) AS bi ON true
LEFT JOIN LATERAL (SELECT sha256, size_bytes FROM blobs WHERE sha256 = p.blob_key) AS bs ON true
LIMIT 1;
