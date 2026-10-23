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

	"github.com/openartifact/openartifact/internal/database/models"
)

// MarkGroupMetadataStale records that the merged metadata of a deb/rpm group
// must be regenerated, creating its state row on first use.
func (p *PostgresDB) MarkGroupMetadataStale(ctx context.Context, groupID string) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO group_metadata_state (repository_id, stale) VALUES ($1, true)
		ON CONFLICT (repository_id) DO UPDATE SET stale = true, updated_at = clock_timestamp()`, groupID)
	if err != nil {
		return fmt.Errorf("mark group metadata stale: %w", err)
	}

	return nil
}

// GetGroupMetadataState returns the state row of a group, nil when it has none.
func (p *PostgresDB) GetGroupMetadataState(ctx context.Context, groupID string) (*models.GroupMetadataState, error) {
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

// ListMetadataGroupsOfMember returns the ids of the deb/rpm group repositories
// that list memberRepoID as a member.
func (p *PostgresDB) ListMetadataGroupsOfMember(ctx context.Context, memberRepoID string) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT g.id FROM repository_group_members gm
		JOIN repositories g ON g.id = gm.group_repo_id
		WHERE gm.member_repo_id = $1 AND g.deleted_at IS NULL AND g.repo_type = 'group' AND g.format IN ('deb', 'rpm')`, memberRepoID)
	if err != nil {
		return nil, fmt.Errorf("list groups of member: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan group id: %w", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list groups of member: %w", err)
	}

	return ids, nil
}

// ListStaleMetadataGroups returns the deb/rpm group repositories whose merged
// metadata is stale or was never generated.
func (p *PostgresDB) ListStaleMetadataGroups(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT r.id FROM repositories r
		LEFT JOIN group_metadata_state s ON s.repository_id = r.id
		WHERE r.deleted_at IS NULL AND r.repo_type = 'group' AND r.format IN ('deb', 'rpm')
		  AND (s.repository_id IS NULL OR s.stale)`)
	if err != nil {
		return nil, fmt.Errorf("list stale metadata groups: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan group id: %w", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list stale metadata groups: %w", err)
	}

	return ids, nil
}

// GetGroupPackageOwner returns the member repository that owns path (a Debian
// pool path or an RPM location) in the merged index of the group, ok=false when
// the index lists no such file.
func (p *PostgresDB) GetGroupPackageOwner(ctx context.Context, groupID, path string) (string, bool, error) {
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
