-- name: GetRepositoryDependencyTrack :one
SELECT repository_id, enabled, base_url, api_key, project_name, project_version,
       auto_push, push_vex, created_at, updated_at
FROM repository_dependency_track
WHERE repository_id = $1;

-- name: UpsertRepositoryDependencyTrack :one
INSERT INTO repository_dependency_track (repository_id, enabled, base_url, api_key,
                                         project_name, project_version, auto_push, push_vex)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (repository_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    base_url = EXCLUDED.base_url,
    api_key = EXCLUDED.api_key,
    project_name = EXCLUDED.project_name,
    project_version = EXCLUDED.project_version,
    auto_push = EXCLUDED.auto_push,
    push_vex = EXCLUDED.push_vex,
    updated_at = now()
RETURNING repository_id, enabled, base_url, api_key, project_name, project_version,
          auto_push, push_vex, created_at, updated_at;

-- name: DeleteRepositoryDependencyTrack :execrows
DELETE FROM repository_dependency_track WHERE repository_id = $1;

-- name: UpsertDependencyTrackPush :one
INSERT INTO dependency_track_pushes (repository_id, artifact_id, state, detail, project_uuid,
                                     project_name, project_version, components, vex_statements,
                                     vex_applied, bom_pushed_at, vex_pushed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (repository_id, artifact_id) DO UPDATE SET
    state = EXCLUDED.state,
    detail = EXCLUDED.detail,
    project_uuid = EXCLUDED.project_uuid,
    project_name = EXCLUDED.project_name,
    project_version = EXCLUDED.project_version,
    components = EXCLUDED.components,
    vex_statements = EXCLUDED.vex_statements,
    vex_applied = EXCLUDED.vex_applied,
    bom_pushed_at = EXCLUDED.bom_pushed_at,
    vex_pushed_at = EXCLUDED.vex_pushed_at,
    updated_at = now()
RETURNING repository_id, artifact_id, state, detail, project_uuid, project_name, project_version,
          components, vex_statements, vex_applied, bom_pushed_at, vex_pushed_at, created_at, updated_at;

-- name: GetDependencyTrackPush :one
SELECT repository_id, artifact_id, state, detail, project_uuid, project_name, project_version,
       components, vex_statements, vex_applied, bom_pushed_at, vex_pushed_at, created_at, updated_at
FROM dependency_track_pushes
WHERE repository_id = $1 AND artifact_id = $2;

-- name: ListDependencyTrackPushes :many
SELECT repository_id, artifact_id, state, detail, project_uuid, project_name, project_version,
       components, vex_statements, vex_applied, bom_pushed_at, vex_pushed_at, created_at, updated_at
FROM dependency_track_pushes
WHERE repository_id = $1
ORDER BY updated_at DESC, artifact_id
LIMIT $2;

-- name: CreateSystemSettingIfAbsent :execrows
INSERT INTO system_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO NOTHING;

-- name: GetRepositoryAttestationAttach :one
SELECT enabled FROM repository_attestation_attach WHERE repository_id = $1;

-- name: SetRepositoryAttestationAttach :exec
INSERT INTO repository_attestation_attach (repository_id, enabled)
VALUES ($1, $2)
ON CONFLICT (repository_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now();
