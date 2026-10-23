-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Capture triggers of federated repositories (docs/design/federation.md): one
-- AFTER INSERT/UPDATE/DELETE row trigger per replicated unit table. They run in
-- the transaction of the artifact change, so the event log can never disagree
-- with the data. For a repository that is not federated each costs one primary
-- key lookup (federation_active).
--
-- The unit key of each table is defined here, in the Go codecs
-- (internal/federation) and in docs/design/federation.md; keep the three in step.

SET LOCAL lock_timeout = '10s';

DO $$
DECLARE
    spec record;
BEGIN
    FOR spec IN
        SELECT * FROM (VALUES
            ('raw_artifacts',    'artifact',
                '%R%->>''path''',
                'federation_blob_sha((%R%->>''blob_id'')::uuid)',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('maven_artifacts',  'artifact',
                '%R%->>''repo_path''',
                'federation_blob_sha((%R%->>''blob_id'')::uuid)',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('pypi_packages',    'artifact',
                '(%R%->>''name'') || ''/'' || (%R%->>''version'') || ''/'' || (%R%->>''filename'')',
                'federation_blob_sha((%R%->>''blob_id'')::uuid)',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('helm_charts',      'artifact',
                '(%R%->>''name'') || ''@'' || (%R%->>''version'')',
                'federation_blob_sha((%R%->>''blob_id'')::uuid)',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('cargo_crates',     'artifact',
                '(%R%->>''name'') || ''@'' || (%R%->>''version'')',
                'federation_blob_sha((%R%->>''blob_id'')::uuid)',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('gomod_modules',    'artifact',
                '(%R%->>''module_path'') || ''@'' || (%R%->>''version'')',
                'federation_blob_sha(COALESCE((%R%->>''zip_blob_id'')::uuid, (%R%->>''mod_blob_id'')::uuid))',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('nuget_packages',   'artifact',
                '(%R%->>''package_id'') || ''@'' || (%R%->>''version'')',
                'federation_blob_sha((%R%->>''blob_id'')::uuid)',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('docker_manifests', 'artifact',
                '''m:'' || (%R%->>''image'') || ''@'' || (%R%->>''digest'')',
                '%R%->>''digest''',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}'),
            ('docker_tags',      'ref',
                '''t:'' || (%R%->>''image'') || '':'' || (%R%->>''name'')',
                '%R%->>''manifest_digest''',
                '{updated_at,deleted_at,deleted_by,search_tsv,download_count}')
        ) AS t(tbl, kind, key_tpl, ref_tpl, ignore)
    LOOP
        EXECUTE format($f$
            CREATE FUNCTION %1$I() RETURNS trigger LANGUAGE plpgsql AS $body$
            DECLARE
                o jsonb;
                n jsonb;
                v_repo uuid;
            BEGIN
                IF TG_OP = 'DELETE' THEN v_repo := OLD.repository_id; ELSE v_repo := NEW.repository_id; END IF;

                IF NOT federation_active(v_repo) THEN
                    RETURN NULL;
                END IF;

                IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
                IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;

                PERFORM federation_capture(v_repo, TG_OP, %2$L,
                    CASE WHEN o IS NULL THEN NULL ELSE %3$s END,
                    CASE WHEN n IS NULL THEN NULL ELSE %4$s END,
                    o, n,
                    CASE WHEN o IS NULL THEN NULL ELSE %5$s END,
                    CASE WHEN n IS NULL THEN NULL ELSE %6$s END,
                    %7$L::text[]);

                RETURN NULL;
            END
            $body$
        $f$,
            'federation_trg_' || spec.tbl, spec.kind,
            replace(spec.key_tpl, '%R%', 'o'), replace(spec.key_tpl, '%R%', 'n'),
            replace(spec.ref_tpl, '%R%', 'o'), replace(spec.ref_tpl, '%R%', 'n'),
            spec.ignore);

        EXECUTE format('CREATE TRIGGER federation_capture AFTER INSERT OR UPDATE OR DELETE ON %1$I FOR EACH ROW EXECUTE FUNCTION %2$I()',
                       spec.tbl, 'federation_trg_' || spec.tbl);
    END LOOP;
END
$$;

-- npm versions and dist-tags belong to a package row (npm_packages) that holds
-- the repository and the full name; both are one primary key lookup away.
CREATE FUNCTION federation_trg_npm_versions() RETURNS trigger LANGUAGE plpgsql AS $body$
DECLARE
    o jsonb;
    n jsonb;
    v_repo uuid;
    v_name text;
BEGIN
    SELECT repository_id, full_name INTO v_repo, v_name
      FROM npm_packages WHERE id = CASE WHEN TG_OP = 'DELETE' THEN OLD.package_id ELSE NEW.package_id END;

    IF v_repo IS NULL OR NOT federation_active(v_repo) THEN
        RETURN NULL;
    END IF;

    IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
    IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;

    PERFORM federation_capture(v_repo, TG_OP, 'artifact',
        CASE WHEN o IS NULL THEN NULL ELSE v_name || '@' || (o->>'version') END,
        CASE WHEN n IS NULL THEN NULL ELSE v_name || '@' || (n->>'version') END,
        o, n,
        CASE WHEN o IS NULL THEN NULL ELSE federation_blob_sha((o->>'blob_id')::uuid) END,
        CASE WHEN n IS NULL THEN NULL ELSE federation_blob_sha((n->>'blob_id')::uuid) END,
        '{updated_at,deleted_at,deleted_by,search_tsv}'::text[]);

    RETURN NULL;
END
$body$;

CREATE TRIGGER federation_capture AFTER INSERT OR UPDATE OR DELETE ON npm_versions
    FOR EACH ROW EXECUTE FUNCTION federation_trg_npm_versions();

CREATE FUNCTION federation_trg_npm_dist_tags() RETURNS trigger LANGUAGE plpgsql AS $body$
DECLARE
    o jsonb;
    n jsonb;
    v_repo uuid;
    v_name text;
BEGIN
    SELECT repository_id, full_name INTO v_repo, v_name
      FROM npm_packages WHERE id = CASE WHEN TG_OP = 'DELETE' THEN OLD.package_id ELSE NEW.package_id END;

    IF v_repo IS NULL OR NOT federation_active(v_repo) THEN
        RETURN NULL;
    END IF;

    IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
    IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;

    PERFORM federation_capture(v_repo, TG_OP, 'ref',
        CASE WHEN o IS NULL THEN NULL ELSE v_name || '#' || (o->>'tag') END,
        CASE WHEN n IS NULL THEN NULL ELSE v_name || '#' || (n->>'tag') END,
        o, n,
        o->>'version', n->>'version',
        '{updated_at}'::text[]);

    RETURN NULL;
END
$body$;

CREATE TRIGGER federation_capture AFTER INSERT OR UPDATE OR DELETE ON npm_dist_tags
    FOR EACH ROW EXECUTE FUNCTION federation_trg_npm_dist_tags();
