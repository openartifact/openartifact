-- name: CreateOrganization :one
INSERT INTO organizations (id, name, slug, display_name, description, owner_id, member_role)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
RETURNING id, created_at, updated_at;

-- name: GetOrganization :one
SELECT id, name, display_name, description, owner_id, member_role, slug, created_at, updated_at
FROM organizations WHERE id = $1;

-- name: GetOrganizationByName :one
SELECT id, name, display_name, description, owner_id, member_role, slug, created_at, updated_at
FROM organizations WHERE name = $1;

-- name: UpdateOrganization :exec
UPDATE organizations SET display_name = $1, description = $2, owner_id = $3, member_role = $4, updated_at = now() WHERE id = $5;

-- name: DeleteOrganization :exec
DELETE FROM organizations WHERE id = $1;

-- name: ListOrganizationsByUser :many
SELECT DISTINCT o.id, o.name, o.display_name, o.description, o.owner_id, o.member_role, o.slug, o.created_at, o.updated_at
FROM organizations o
LEFT JOIN organization_members om ON o.id = om.org_id
WHERE o.owner_id = $1 OR om.user_id = $1
ORDER BY o.created_at DESC;

-- name: AddOrgMember :one
INSERT INTO organization_members (org_id, user_id, role)
VALUES ($1, $2, $3)
RETURNING created_at;

-- name: RemoveOrgMember :exec
DELETE FROM organization_members WHERE org_id = $1 AND user_id = $2;

-- name: ListOrgMembers :many
SELECT (org_id::text || ':' || user_id::text) AS id, org_id, user_id, role, created_at
FROM organization_members WHERE org_id = $1 ORDER BY created_at ASC;

-- name: GetOrgMember :one
SELECT (org_id::text || ':' || user_id::text) AS id, org_id, user_id, role, created_at
FROM organization_members WHERE org_id = $1 AND user_id = $2;

-- name: UpdateOrgMemberRole :exec
UPDATE organization_members SET role = $1 WHERE org_id = $2 AND user_id = $3;

-- name: ListOrgGrantsByUser :many
-- Every organization the user belongs to (membership row or recorded owner)
-- with the effective membership role; the recorded owner always counts as owner.
SELECT o.id AS org_id,
       (CASE WHEN o.owner_id = $1 THEN 'owner' ELSE om.role END)::text AS role,
       o.member_role
FROM organizations o
LEFT JOIN organization_members om ON om.org_id = o.id AND om.user_id = $1
WHERE o.owner_id = $1 OR om.user_id IS NOT NULL;
