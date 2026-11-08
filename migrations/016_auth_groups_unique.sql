-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Global groups (project_id IS NULL) were never unique by name: the
-- (project_id, name) constraint treats NULLs as distinct. Merge existing
-- duplicates into the oldest row, then enforce uniqueness.

SET LOCAL lock_timeout = '10s';

CREATE TEMP TABLE _group_merge ON COMMIT DROP AS
SELECT id AS dup_id,
       first_value(id) OVER (PARTITION BY name ORDER BY created_at, id) AS keep_id
FROM auth_groups
WHERE project_id IS NULL;

DELETE FROM _group_merge WHERE dup_id = keep_id;

-- Memberships: keep the survivor's row on conflict; an SSO-managed flag is
-- preserved only if no row exists yet.
INSERT INTO auth_user_groups (user_id, group_id, is_sso_managed)
SELECT ug.user_id, m.keep_id, ug.is_sso_managed
FROM auth_user_groups ug
JOIN _group_merge m ON m.dup_id = ug.group_id
ON CONFLICT (user_id, group_id) DO NOTHING;

-- Project grants: keep the survivor's row on conflict.
INSERT INTO project_groups (project_id, group_id, role, created_by)
SELECT pg.project_id, m.keep_id, pg.role, pg.created_by
FROM project_groups pg
JOIN _group_merge m ON m.dup_id = pg.group_id
ON CONFLICT (project_id, group_id) DO NOTHING;

-- Permission assignments name a group by principal_id without a foreign key,
-- so they neither follow nor cascade: move them to the survivor explicitly.
INSERT INTO permission_assignments (permission_target_id, principal_type, principal_id, created_at)
SELECT pa.permission_target_id, 'group', m.keep_id, pa.created_at
FROM permission_assignments pa
JOIN _group_merge m ON m.dup_id = pa.principal_id
WHERE pa.principal_type = 'group'
ON CONFLICT (permission_target_id, principal_type, principal_id) DO NOTHING;

DELETE FROM permission_assignments pa
USING _group_merge m
WHERE pa.principal_type = 'group' AND pa.principal_id = m.dup_id;

-- The remaining referencing rows (memberships, project grants) cascade away
-- with the duplicates.
DELETE FROM auth_groups WHERE id IN (SELECT dup_id FROM _group_merge);

CREATE UNIQUE INDEX IF NOT EXISTS auth_groups_global_name_unique
    ON auth_groups (name) WHERE project_id IS NULL;
