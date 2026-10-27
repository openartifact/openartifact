-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Verbatim copy of the search view as it was before the two-stage rewrite (every
-- format table joined to blobs inside the union). It is the oracle of
-- TestSearchMatchesLegacyQuery.
(SELECT cc.repository_id, 'cargo'::text AS format, cc.name, cc.version, cc.created_at,
			cc.name || '-' || cc.version || '.crate' AS path,
			COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0) AS size,
			NULL::text AS group_id, NULL::text AS artifact_id_coord, NULL::text AS scope
		FROM cargo_crates cc LEFT JOIN blobs b ON b.id = cc.blob_id
	UNION ALL
	SELECT cp.repository_id, 'conda'::text, cp.name, cp.version, cp.created_at,
			''::text,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM conda_packages cp LEFT JOIN blobs b ON b.id = cp.blob_id
	UNION ALL
	SELECT co.repository_id, 'conan'::text, co.name, co.version, co.created_at,
			''::text,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM conan_packages co LEFT JOIN blobs b ON b.id = co.blob_id
	UNION ALL
	SELECT pk.repository_id, 'pacman'::text, pk.name, pk.version, pk.created_at,
			pk.filename,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM pacman_packages pk LEFT JOIN blobs b ON b.id = pk.blob_id
	UNION ALL
	SELECT tm.repository_id, 'terraform'::text, 'modules/' || tm.name || '/' || tm.system, tm.version, tm.created_at,
			tm.name || '-' || tm.system || '-' || tm.version || '.' || tm.archive_ext,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM terraform_modules tm LEFT JOIN blobs b ON b.id = tm.blob_id
	UNION ALL
	SELECT tp.repository_id, 'terraform'::text, 'providers/' || tp.type, tp.version, tp.created_at,
			'terraform-provider-' || tp.type || '_' || tp.version || '_' || tp.os || '_' || tp.arch || '.zip',
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM terraform_providers tp LEFT JOIN blobs b ON b.id = tp.blob_id
	UNION ALL
	SELECT dt.repository_id, 'docker'::text, dt.name, ''::text, dt.created_at,
			''::text,
			COALESCE(regexp_replace(dm.digest, '^sha256:', ''), ''),
			COALESCE(dm.size, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM docker_tags dt
		LEFT JOIN docker_manifests dm
			ON dm.repository_id = dt.repository_id AND dm.digest = dt.manifest_digest
	UNION ALL
	SELECT gm.repository_id, 'go'::text, gm.module_path, gm.version, gm.created_at,
			''::text,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM gomod_modules gm LEFT JOIN blobs b ON b.id = gm.zip_blob_id
	UNION ALL
	SELECT hc.repository_id, 'helm'::text, hc.name, hc.version, hc.created_at,
			hc.name || '-' || hc.version || '.tgz',
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM helm_charts hc LEFT JOIN blobs b ON b.id = hc.blob_id
	UNION ALL
	SELECT iv.repository_id, 'ivy'::text, iv.organisation || '/' || iv.module, iv.revision, iv.created_at,
			iv.artifact_name || '.' || iv.artifact_ext,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM ivy_artifacts iv LEFT JOIN blobs b ON b.id = iv.blob_id
	UNION ALL
	SELECT ma.repository_id, 'maven'::text, ma.group_id || ':' || ma.artifact_id_coord, ma.version, ma.created_at,
			ma.repo_path,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			ma.group_id, ma.artifact_id_coord, NULL::text
		FROM maven_artifacts ma LEFT JOIN blobs b ON b.id = ma.blob_id
	UNION ALL
	SELECT np.repository_id, 'npm'::text, np.full_name, nv.version, nv.created_at,
			nv.tarball_filename,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULLIF(np.scope, '')
		FROM npm_packages np
		JOIN npm_versions nv ON nv.package_id = np.id
		LEFT JOIN blobs b ON b.id = nv.blob_id
	UNION ALL
	SELECT ng.repository_id, 'nuget'::text, ng.package_id, ng.version, ng.created_at,
			ng.package_id || '.' || ng.version || '.nupkg',
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM nuget_packages ng LEFT JOIN blobs b ON b.id = ng.blob_id
	UNION ALL
	SELECT pp.repository_id, 'pypi'::text, pp.name, pp.version, pp.created_at,
			pp.filename,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM pypi_packages pp LEFT JOIN blobs b ON b.id = pp.blob_id
	UNION ALL
	SELECT ra.repository_id, 'raw'::text, ra.path, ''::text, ra.created_at,
			ra.path,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM raw_artifacts ra LEFT JOIN blobs b ON b.id = ra.blob_id
	UNION ALL
	SELECT rg.repository_id, 'rubygems'::text, rg.name, rg.version, rg.created_at,
			rg.name || '-' || rg.version || '.gem',
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM rubygems_gems rg LEFT JOIN blobs b ON b.id = rg.blob_id
	UNION ALL
	SELECT sa.repository_id, 'sbt'::text, sa.group_id || '/' || sa.artifact_id, sa.version, sa.created_at,
			sa.filename,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM sbt_artifacts sa LEFT JOIN blobs b ON b.id = sa.blob_id
	UNION ALL
	SELECT rp.repository_id, 'rpm'::text, rp.name, rp.version, rp.created_at,
			COALESCE(rp.filename, ''),
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM rpm_packages rp LEFT JOIN blobs b ON b.id = rp.blob_id
	UNION ALL
	SELECT dp.repository_id, 'deb'::text, dp.name, dp.version, dp.created_at,
			COALESCE(dp.filename, ''),
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM deb_packages dp LEFT JOIN blobs b ON b.id = dp.blob_id)
