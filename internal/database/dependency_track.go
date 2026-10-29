// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"math"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// DependencyTrackStore owns the per-repository Dependency-Track settings and push records.
type DependencyTrackStore struct{ *core }

func dtrackConfigFromRow(r dbsqlc.RepositoryDependencyTrack) *models.RepositoryDependencyTrack {
	return &models.RepositoryDependencyTrack{
		RepositoryID:   r.RepositoryID,
		Enabled:        r.Enabled,
		BaseURL:        r.BaseUrl,
		APIKey:         r.ApiKey,
		ProjectName:    r.ProjectName,
		ProjectVersion: r.ProjectVersion,
		AutoPush:       r.AutoPush,
		PushVEX:        r.PushVex,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func dtrackPushFromRow(r dbsqlc.DependencyTrackPush) *models.DependencyTrackPush {
	return &models.DependencyTrackPush{
		RepositoryID:   r.RepositoryID,
		ArtifactID:     r.ArtifactID,
		State:          r.State,
		Detail:         r.Detail,
		ProjectUUID:    r.ProjectUuid,
		ProjectName:    r.ProjectName,
		ProjectVersion: r.ProjectVersion,
		Components:     int(r.Components),
		VEXStatements:  int(r.VexStatements),
		VEXApplied:     int(r.VexApplied),
		BOMPushedAt:    r.BomPushedAt,
		VEXPushedAt:    r.VexPushedAt,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

// clampInt32 narrows a count to the integer column width.
func clampInt32(n int) int32 {
	return int32(min(max(n, 0), math.MaxInt32))
}

// GetRepositoryDependencyTrack returns the repository's override, nil when none is set.
func (p *DependencyTrackStore) GetRepositoryDependencyTrack(ctx context.Context, repoID string) (*models.RepositoryDependencyTrack, error) {
	row, err := p.q.GetRepositoryDependencyTrack(ctx, repoID)

	return mapRow(row, err, "get repository Dependency-Track settings", dtrackConfigFromRow)
}

// UpsertRepositoryDependencyTrack stores the override and fills the stored row back into c.
func (p *DependencyTrackStore) UpsertRepositoryDependencyTrack(ctx context.Context, c *models.RepositoryDependencyTrack) error {
	row, err := p.q.UpsertRepositoryDependencyTrack(ctx, dtrackConfigParams(c))

	return storeRow(c, row, err, "upsert repository Dependency-Track settings", dtrackConfigFromRow)
}

func dtrackConfigParams(c *models.RepositoryDependencyTrack) dbsqlc.UpsertRepositoryDependencyTrackParams {
	return dbsqlc.UpsertRepositoryDependencyTrackParams{
		RepositoryID:   c.RepositoryID,
		Enabled:        c.Enabled,
		BaseUrl:        c.BaseURL,
		ApiKey:         c.APIKey,
		ProjectName:    c.ProjectName,
		ProjectVersion: c.ProjectVersion,
		AutoPush:       c.AutoPush,
		PushVex:        c.PushVEX,
	}
}

// DeleteRepositoryDependencyTrack removes the override; it reports whether one existed.
func (p *DependencyTrackStore) DeleteRepositoryDependencyTrack(ctx context.Context, repoID string) (bool, error) {
	n, err := p.q.DeleteRepositoryDependencyTrack(ctx, repoID)
	if err != nil {
		return false, fmt.Errorf("failed to delete repository Dependency-Track settings: %w", err)
	}

	return n > 0, nil
}

// UpsertDependencyTrackPush records the state of an artifact's push and fills the stored row back into s.
func (p *DependencyTrackStore) UpsertDependencyTrackPush(ctx context.Context, s *models.DependencyTrackPush) error {
	row, err := p.q.UpsertDependencyTrackPush(ctx, dbsqlc.UpsertDependencyTrackPushParams{
		RepositoryID:   s.RepositoryID,
		ArtifactID:     s.ArtifactID,
		State:          s.State,
		Detail:         s.Detail,
		ProjectUuid:    s.ProjectUUID,
		ProjectName:    s.ProjectName,
		ProjectVersion: s.ProjectVersion,
		Components:     clampInt32(s.Components),
		VexStatements:  clampInt32(s.VEXStatements),
		VexApplied:     clampInt32(s.VEXApplied),
		BomPushedAt:    s.BOMPushedAt,
		VexPushedAt:    s.VEXPushedAt,
	})

	return storeRow(s, row, err, "upsert Dependency-Track push", dtrackPushFromRow)
}

// GetDependencyTrackPush returns the push state of an artifact, nil when it was never pushed.
func (p *DependencyTrackStore) GetDependencyTrackPush(ctx context.Context, repoID, artifactID string) (*models.DependencyTrackPush, error) {
	row, err := p.q.GetDependencyTrackPush(ctx, dbsqlc.GetDependencyTrackPushParams{RepositoryID: repoID, ArtifactID: artifactID})

	return mapRow(row, err, "get Dependency-Track push", dtrackPushFromRow)
}

// ListDependencyTrackPushes lists the pushes of a repository, most recently updated first.
func (p *DependencyTrackStore) ListDependencyTrackPushes(
	ctx context.Context,
	repoID string,
	limit int,
) ([]*models.DependencyTrackPush, error) {
	rows, err := p.q.ListDependencyTrackPushes(ctx, dbsqlc.ListDependencyTrackPushesParams{RepositoryID: repoID, Limit: clampInt32(limit)})
	if err != nil {
		return nil, fmt.Errorf("failed to list Dependency-Track pushes: %w", err)
	}

	out := make([]*models.DependencyTrackPush, 0, len(rows))
	for _, r := range rows {
		out = append(out, dtrackPushFromRow(r))
	}

	return out, nil
}
