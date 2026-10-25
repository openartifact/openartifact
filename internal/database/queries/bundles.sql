-- name: CreatePromotionPipeline :one
INSERT INTO promotion_pipelines (name, description, stages, creator_id, created_at, updated_at)
VALUES ($1, $2, $3, (SELECT id FROM auth_users WHERE username = $4), $5, $6)
RETURNING id;

-- name: GetPromotionPipelineByName :one
SELECT pp.id, pp.name, COALESCE(pp.description, '') AS description, pp.stages,
       COALESCE(u.username, '') AS created_by, pp.created_at, pp.updated_at
FROM promotion_pipelines pp
LEFT JOIN auth_users u ON u.id = pp.creator_id
WHERE pp.name = $1;

-- name: GetPromotionPipelineByID :one
SELECT pp.id, pp.name, COALESCE(pp.description, '') AS description, pp.stages,
       COALESCE(u.username, '') AS created_by, pp.created_at, pp.updated_at
FROM promotion_pipelines pp
LEFT JOIN auth_users u ON u.id = pp.creator_id
WHERE pp.id = $1;

-- name: UpdatePromotionPipeline :exec
UPDATE promotion_pipelines SET name = $1, description = $2, stages = $3, updated_at = now() WHERE id = $4;

-- name: DeletePromotionPipeline :exec
DELETE FROM promotion_pipelines WHERE id = $1;

-- name: ListPromotionPipelines :many
SELECT pp.id, pp.name, COALESCE(pp.description, '') AS description, pp.stages,
       COALESCE(u.username, '') AS created_by, pp.created_at, pp.updated_at
FROM promotion_pipelines pp
LEFT JOIN auth_users u ON u.id = pp.creator_id
ORDER BY pp.name;

-- name: CreateReleaseBundle :one
INSERT INTO release_bundles (name, version, description, state, pipeline_id, current_stage, creator_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, (SELECT id FROM auth_users WHERE username = $7), $8, $9)
RETURNING id;

-- name: GetReleaseBundle :one
SELECT rb.id, rb.name, rb.version, COALESCE(rb.description, '') AS description, rb.state, rb.pipeline_id, rb.current_stage,
       COALESCE(uc.username, '') AS created_by, rb.sealed_at, us.username AS sealed_by, rb.archived_at, ua.username AS archived_by,
       rb.gpg_key_id, rb.gpg_signature, rb.created_at, rb.updated_at
FROM release_bundles rb
LEFT JOIN auth_users uc ON uc.id = rb.creator_id
LEFT JOIN auth_users us ON us.id = rb.sealer_id
LEFT JOIN auth_users ua ON ua.id = rb.archiver_id
WHERE rb.name = $1 AND rb.version = $2;

-- name: GetReleaseBundleByID :one
SELECT rb.id, rb.name, rb.version, COALESCE(rb.description, '') AS description, rb.state, rb.pipeline_id, rb.current_stage,
       COALESCE(uc.username, '') AS created_by, rb.sealed_at, us.username AS sealed_by, rb.archived_at, ua.username AS archived_by,
       rb.gpg_key_id, rb.gpg_signature, rb.created_at, rb.updated_at
FROM release_bundles rb
LEFT JOIN auth_users uc ON uc.id = rb.creator_id
LEFT JOIN auth_users us ON us.id = rb.sealer_id
LEFT JOIN auth_users ua ON ua.id = rb.archiver_id
WHERE rb.id = $1;

-- name: UpdateReleaseBundle :exec
UPDATE release_bundles AS rb
SET description = $1, state = $2, pipeline_id = $3, current_stage = $4,
    sealed_at = $5, sealer_id = (SELECT au.id FROM auth_users au WHERE au.username = $6),
    archived_at = $7, archiver_id = (SELECT au.id FROM auth_users au WHERE au.username = $8),
    gpg_key_id = $9, gpg_signature = $10, updated_at = now()
WHERE rb.id = $11;

-- name: DeleteReleaseBundle :exec
DELETE FROM release_bundles WHERE id = $1;

-- name: AddBundleItem :exec
WITH ref AS (
    INSERT INTO artifact_refs (repository_id, artifact_path, format)
    VALUES ($3, $5, $6)
    ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format
    RETURNING id
)
INSERT INTO release_bundle_items (id, bundle_id, source_repo_id, artifact_ref_id, sha256, artifact_path, format, size_bytes, adder_id, added_at)
SELECT $1, $2, $3, ref.id, $4, $5, $6, $7, (SELECT id FROM auth_users WHERE username = $8), $9
FROM ref
ON CONFLICT (bundle_id, sha256) DO NOTHING;

-- name: RemoveBundleItem :exec
DELETE FROM release_bundle_items WHERE bundle_id = $1 AND sha256 = $2;

-- name: ListBundleItems :many
SELECT rbi.id, rbi.bundle_id, rbi.source_repo_id, rbi.sha256, rbi.artifact_path, rbi.format, rbi.size_bytes,
       COALESCE(u.username, '') AS added_by, rbi.added_at
FROM release_bundle_items rbi
LEFT JOIN auth_users u ON u.id = rbi.adder_id
WHERE rbi.bundle_id = $1 ORDER BY rbi.added_at, rbi.id;

-- name: GetBundleItem :one
SELECT rbi.id, rbi.bundle_id, rbi.source_repo_id, rbi.sha256, rbi.artifact_path, rbi.format, rbi.size_bytes,
       COALESCE(u.username, '') AS added_by, rbi.added_at
FROM release_bundle_items rbi
LEFT JOIN auth_users u ON u.id = rbi.adder_id
WHERE rbi.bundle_id = $1 AND rbi.sha256 = $2;

-- name: CreateBundlePromotion :one
INSERT INTO bundle_promotions (bundle_id, from_stage, to_stage, target_repo_id, status, promoter_id, notes, item_count, started_at)
VALUES ($1, $2, $3, $4, $5, (SELECT id FROM auth_users WHERE username = $6), $7, $8, $9)
RETURNING id;

-- name: ListBundlePromotions :many
SELECT bp.id, bp.bundle_id, bp.from_stage, bp.to_stage, bp.target_repo_id, bp.status,
       COALESCE(u.username, '') AS promoted_by,
       COALESCE(bp.notes, '') AS notes, bp.error_message, bp.item_count, bp.started_at, bp.completed_at
FROM bundle_promotions bp
LEFT JOIN auth_users u ON u.id = bp.promoter_id
WHERE bp.bundle_id = $1 ORDER BY bp.started_at, bp.id;
