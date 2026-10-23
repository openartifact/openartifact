// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"context"
	"fmt"
	"io"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Authorization and quota state. Without these tables a restore would leave
// every non-admin user without grants and every limited repository unlimited
// while still ending "completed", so they are part of the archive; a failed
// list call fails the backup like every other table.

// redactRepositorySecrets returns copies of repos without their upstream
// credentials: like webhook secrets, OIDC client secrets and storage
// credentials they never leave the database inside an archive. Proxy
// repositories need their upstream password entered again after a restore.
func redactRepositorySecrets(repos []*models.Repository) []*models.Repository {
	out := make([]*models.Repository, len(repos))

	for i, r := range repos {
		if r == nil {
			continue
		}

		c := *r
		c.UpstreamPasswordEnc = ""
		out[i] = &c
	}

	return out
}

// exportAccessControl writes permission targets, their assignments, user to
// group memberships and repository quota limits.
func (e *exporter) exportAccessControl(ctx context.Context, tw *tar.Writer, manifest *BackupManifest) error {
	targets, err := e.db.ListPermissionTargets(ctx)
	if err := writeListedTable(e, tw, manifest, "permission_targets", targets, err); err != nil {
		return err
	}

	assignments, err := e.db.ListPermissionAssignments(ctx)
	if err := writeListedTable(e, tw, manifest, "permission_assignments", assignments, err); err != nil {
		return err
	}

	memberships, err := e.db.ListUserGroupMemberships(ctx)
	if err := writeListedTable(e, tw, manifest, "user_groups", memberships, err); err != nil {
		return err
	}

	quotas, err := e.db.ListStorageQuotas(ctx)

	return writeListedTable(e, tw, manifest, "storage_quotas", quotas, err)
}

// restorePermissionTargets recreates permission targets. A target whose name
// already exists (the built-in admin-all and anonymous-read, or one of a
// non-empty instance) is kept as is and only mapped, so the assignments that
// follow bind to it.
func (e *Engine) restorePermissionTargets(ctx context.Context, r io.Reader, st *restoreState) error {
	targets, err := decodeSlice[*models.PermissionTarget](r)
	if err != nil {
		return fmt.Errorf("failed to decode permission targets: %w", err)
	}

	for _, t := range targets {
		oldID := t.ID

		existing, err := e.db.GetPermissionTargetByName(ctx, t.Name)
		if err != nil {
			e.restoreWarn("failed to look up permission target", "name", t.Name, "error", err)
			continue
		}

		if existing != nil {
			st.targetIDMap[oldID] = existing.ID
			continue
		}

		created, err := e.db.CreatePermissionTarget(ctx, t)
		if err != nil || created == nil {
			e.restoreWarn("failed to restore permission target", "name", t.Name, "error", err)
			continue
		}

		st.targetIDMap[oldID] = created.ID
	}

	return nil
}

// restorePermissionAssignments rebinds targets to their users and groups
// through the id remaps; an assignment whose target or principal was not
// restored counts as a failure instead of silently dropping a grant.
func (e *Engine) restorePermissionAssignments(ctx context.Context, r io.Reader, st *restoreState) error {
	assignments, err := decodeSlice[*models.PermissionAssignment](r)
	if err != nil {
		return fmt.Errorf("failed to decode permission assignments: %w", err)
	}

	for _, a := range assignments {
		targetID, ok := st.targetIDMap[a.PermissionTargetID]
		if !ok {
			e.restoreWarn("skipping permission assignment: target not restored", "target_id", a.PermissionTargetID)
			continue
		}

		var principalID string

		switch a.PrincipalType {
		case "user":
			principalID, ok = st.userIDMap[a.PrincipalID]
		case "group":
			principalID, ok = st.groupIDMap[a.PrincipalID]
		default:
			ok = false
		}

		if !ok {
			if a.PrincipalType == "user" {
				e.restoreMissingUser(st, a.PrincipalID, "skipping permission assignment: principal not restored", "principal_type", a.PrincipalType, "principal_id", a.PrincipalID)
			} else {
				e.restoreWarn("skipping permission assignment: principal not restored", "principal_type", a.PrincipalType, "principal_id", a.PrincipalID)
			}

			continue
		}

		if err := e.db.GetOrCreatePermissionAssignment(ctx, targetID, a.PrincipalType, principalID); err != nil {
			e.restoreWarn("failed to restore permission assignment", "target_id", a.PermissionTargetID, "principal_id", a.PrincipalID, "error", err)
		}
	}

	return nil
}

// restoreUserGroups re-adds user to group memberships. SSO-managed
// memberships are not restored: the next SSO login of the user derives them
// again from the identity provider's claims, and restoring them as manual
// memberships would make them permanent.
func (e *Engine) restoreUserGroups(ctx context.Context, r io.Reader, st *restoreState) error {
	memberships, err := decodeSlice[*models.UserGroup](r)
	if err != nil {
		return fmt.Errorf("failed to decode user group memberships: %w", err)
	}

	for _, m := range memberships {
		if m.SSOManaged {
			continue
		}

		userID, uok := st.userIDMap[m.UserID]
		groupID, gok := st.groupIDMap[m.GroupID]

		if !uok || !gok {
			if _, kept := st.keptUsers[m.UserID]; kept && gok {
				e.restoreMissingUser(st, m.UserID, "skipping group membership: user not restored", "user_id", m.UserID, "group_id", m.GroupID)
			} else {
				e.restoreWarn("skipping group membership: user or group not restored", "user_id", m.UserID, "group_id", m.GroupID)
			}

			continue
		}

		if err := e.db.AddUserToGroup(ctx, userID, groupID); err != nil {
			e.restoreWarn("failed to restore group membership", "user_id", m.UserID, "group_id", m.GroupID, "error", err)
		}
	}

	return nil
}

// restoreStorageQuotas reapplies per-repository limits (usage is recounted
// from the restored rows afterwards).
func (e *Engine) restoreStorageQuotas(ctx context.Context, r io.Reader, st *restoreState) error {
	quotas, err := decodeSlice[*models.StorageQuota](r)
	if err != nil {
		return fmt.Errorf("failed to decode storage quotas: %w", err)
	}

	for _, q := range quotas {
		repoID, ok := st.repoIDMap[q.RepositoryID]
		if !ok {
			e.restoreWarn("skipping storage quota: repository not restored", "repo_id", q.RepositoryID)
			continue
		}

		cur, err := e.db.GetOrCreateStorageQuota(ctx, repoID)
		if err != nil || cur == nil {
			e.restoreWarn("failed to restore storage quota", "repo_id", q.RepositoryID, "error", err)
			continue
		}

		cur.MaxSizeBytes = q.MaxSizeBytes
		cur.MaxArtifactCount = q.MaxArtifactCount
		cur.WarnThresholdPercent = q.WarnThresholdPercent

		if err := e.db.UpdateStorageQuota(ctx, cur); err != nil {
			e.restoreWarn("failed to restore storage quota", "repo_id", q.RepositoryID, "error", err)
		}
	}

	return nil
}
