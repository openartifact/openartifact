// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// RepoGroupStore owns group repository membership and the stale/owner state of generated group metadata.
type RepoGroupStore struct{ *core }

// MarkGroupMetadataStale records that the merged metadata of an apk, deb, pacman or rpm group
// must be regenerated, creating its state row on first use.
func (p *RepoGroupStore) MarkGroupMetadataStale(ctx context.Context, groupID string) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO group_metadata_state (repository_id, stale) VALUES ($1, true)
		ON CONFLICT (repository_id) DO UPDATE SET stale = true, updated_at = clock_timestamp()`, groupID)
	if err != nil {
		return fmt.Errorf("mark group metadata stale: %w", err)
	}

	return nil
}

// GetGroupMetadataState returns the state row of a group, nil when it has none.
func (p *RepoGroupStore) GetGroupMetadataState(ctx context.Context, groupID string) (*models.GroupMetadataState, error) {
	var (
		st    models.GroupMetadataState
		dists []byte
	)

	err := p.db.QueryRowContext(ctx, `
		SELECT repository_id, stale, updated_at, generated_at, refresh_after, dists
		FROM group_metadata_state WHERE repository_id = $1`, groupID).
		Scan(&st.RepositoryID, &st.Stale, &st.UpdatedAt, &st.GeneratedAt, &st.RefreshAfter, &dists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // not found is not an error
	}

	if err != nil {
		return nil, fmt.Errorf("get group metadata state: %w", err)
	}

	if err := json.Unmarshal(dists, &st.Dists); err != nil {
		return nil, fmt.Errorf("decode group metadata dists: %w", err)
	}

	return &st, nil
}

// ListMetadataGroupsOfMember returns the ids of the apk, deb, pacman or rpm group repositories
// that list memberRepoID as a member.
func (p *RepoGroupStore) ListMetadataGroupsOfMember(ctx context.Context, memberRepoID string) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT g.id FROM repository_group_members gm
		JOIN repositories g ON g.id = gm.group_repo_id
		WHERE gm.member_repo_id = $1 AND g.deleted_at IS NULL AND g.repo_type = 'group'
		AND g.format IN ('apk', 'deb', 'pacman', 'rpm')`, memberRepoID)
	if err != nil {
		return nil, fmt.Errorf("list groups of member: %w", err)
	}

	return collectRows(rows, "scan group id", "list groups of member", scanString)
}

// ListStaleMetadataGroups returns the apk, deb, pacman or rpm group repositories whose merged
// metadata is stale or was never generated.
func (p *RepoGroupStore) ListStaleMetadataGroups(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT r.id FROM repositories r
		LEFT JOIN group_metadata_state s ON s.repository_id = r.id
		WHERE r.deleted_at IS NULL AND r.repo_type = 'group' AND r.format IN ('apk', 'deb', 'pacman', 'rpm')
		  AND (s.repository_id IS NULL OR s.stale)`)
	if err != nil {
		return nil, fmt.Errorf("list stale metadata groups: %w", err)
	}

	return collectRows(rows, "scan group id", "list stale metadata groups", scanString)
}

// GetGroupPackageOwner returns the member repository that owns path (a Debian
// pool path or an RPM location) in the merged index of the group, ok=false when
// the index lists no such file.
func (p *RepoGroupStore) GetGroupPackageOwner(ctx context.Context, groupID, path string) (string, bool, error) {
	var member string

	err := p.db.QueryRowContext(ctx,
		`SELECT member_repo_id FROM group_package_owners WHERE group_repo_id = $1 AND path = $2`, groupID, path).Scan(&member)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("get group package owner: %w", err)
	}

	return member, true, nil
}

func (p *RepoGroupStore) AddGroupMember(ctx context.Context, groupRepoID, memberRepoID string, priority int) error {
	if err := p.q.AddGroupMember(ctx, dbsqlc.AddGroupMemberParams{
		GroupRepoID:  groupRepoID,
		MemberRepoID: memberRepoID,
		Priority:     int32(priority), //nolint:gosec // G115: priority is bounded
	}); err != nil {
		return fmt.Errorf("failed to add group member: %w", err)
	}

	return nil
}

func (p *RepoGroupStore) ListGroupMembers(ctx context.Context, groupRepoID string) ([]*models.RepositoryGroupMember, error) {
	rows, err := p.q.ListGroupMembers(ctx, groupRepoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list group members: %w", err)
	}

	members := make([]*models.RepositoryGroupMember, len(rows))
	for i, row := range rows {
		members[i] = &models.RepositoryGroupMember{
			GroupRepoID:  row.GroupRepoID,
			MemberRepoID: row.MemberRepoID,
			Priority:     int(row.Priority),
			CreatedAt:    row.CreatedAt,
		}
	}

	return members, nil
}

func (p *RepoGroupStore) ListGroupMemberRepositories(ctx context.Context, groupRepoID string) ([]*models.Repository, error) {
	rows, err := p.db.QueryContext(ctx, `
		WITH links AS (
			SELECT member_repo_id, priority, created_at AS linked_at
			FROM repository_group_members WHERE group_repo_id = $1
		)
		SELECT `+repoColumns+`
		FROM repositories JOIN links ON links.member_repo_id = repositories.id
		WHERE repositories.deleted_at IS NULL
		ORDER BY links.priority, links.linked_at, links.member_repo_id`, groupRepoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list group member repositories: %w", err)
	}

	return collectRows(rows, "failed to scan group member repository", "failed to list group member repositories", scanRepository)
}

func (p *RepoGroupStore) ListGroupsOfMember(ctx context.Context, memberRepoID string) ([]*models.Repository, error) {
	rows, err := p.db.QueryContext(ctx, `
		WITH links AS (
			SELECT group_repo_id, created_at AS linked_at
			FROM repository_group_members WHERE member_repo_id = $1
		)
		SELECT `+repoColumns+`
		FROM repositories JOIN links ON links.group_repo_id = repositories.id
		WHERE repositories.deleted_at IS NULL
		ORDER BY links.linked_at, links.group_repo_id`, memberRepoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups of member: %w", err)
	}

	return collectRows(rows, "failed to scan group repository", "failed to list groups of member", scanRepository)
}

// ReplaceGroupMembers atomically replaces the member list of a group
// repository. An empty memberIDs slice clears the group.
func (p *RepoGroupStore) ReplaceGroupMembers(ctx context.Context, groupRepoID string, memberIDs []string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := replaceGroupMembersTx(ctx, tx, groupRepoID, memberIDs); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit group members: %w", err)
	}

	return nil
}
