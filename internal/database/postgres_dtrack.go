// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

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
func (p *PostgresDB) GetRepositoryDependencyTrack(ctx context.Context, repoID string) (*models.RepositoryDependencyTrack, error) {
	row, err := p.q.GetRepositoryDependencyTrack(ctx, repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil means "no override"
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get repository Dependency-Track settings: %w", err)
	}

	return dtrackConfigFromRow(row), nil
}

// UpsertRepositoryDependencyTrack stores the override and fills the stored row back into c.
func (p *PostgresDB) UpsertRepositoryDependencyTrack(ctx context.Context, c *models.RepositoryDependencyTrack) error {
	row, err := p.q.UpsertRepositoryDependencyTrack(ctx, dbsqlc.UpsertRepositoryDependencyTrackParams{
		RepositoryID:   c.RepositoryID,
		Enabled:        c.Enabled,
		BaseUrl:        c.BaseURL,
		ApiKey:         c.APIKey,
		ProjectName:    c.ProjectName,
		ProjectVersion: c.ProjectVersion,
		AutoPush:       c.AutoPush,
		PushVex:        c.PushVEX,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert repository Dependency-Track settings: %w", err)
	}

	*c = *dtrackConfigFromRow(row)

	return nil
}

// DeleteRepositoryDependencyTrack removes the override; it reports whether one existed.
func (p *PostgresDB) DeleteRepositoryDependencyTrack(ctx context.Context, repoID string) (bool, error) {
	n, err := p.q.DeleteRepositoryDependencyTrack(ctx, repoID)
	if err != nil {
		return false, fmt.Errorf("failed to delete repository Dependency-Track settings: %w", err)
	}

	return n > 0, nil
}

// UpsertDependencyTrackPush records the state of an artifact's push and fills the stored row back into s.
func (p *PostgresDB) UpsertDependencyTrackPush(ctx context.Context, s *models.DependencyTrackPush) error {
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
	if err != nil {
		return fmt.Errorf("failed to upsert Dependency-Track push: %w", err)
	}

	*s = *dtrackPushFromRow(row)

	return nil
}

// GetDependencyTrackPush returns the push state of an artifact, nil when it was never pushed.
func (p *PostgresDB) GetDependencyTrackPush(ctx context.Context, repoID, artifactID string) (*models.DependencyTrackPush, error) {
	row, err := p.q.GetDependencyTrackPush(ctx, dbsqlc.GetDependencyTrackPushParams{RepositoryID: repoID, ArtifactID: artifactID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil means "never pushed"
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get Dependency-Track push: %w", err)
	}

	return dtrackPushFromRow(row), nil
}

// ListDependencyTrackPushes lists the pushes of a repository, most recently updated first.
func (p *PostgresDB) ListDependencyTrackPushes(ctx context.Context, repoID string, limit int) ([]*models.DependencyTrackPush, error) {
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

// CreateSystemSettingIfAbsent stores value under key unless the key exists; it
// reports whether it was stored. Concurrent callers (several replicas) agree on one winner.
func (p *PostgresDB) CreateSystemSettingIfAbsent(ctx context.Context, key, value string) (bool, error) {
	n, err := p.q.CreateSystemSettingIfAbsent(ctx, dbsqlc.CreateSystemSettingIfAbsentParams{Key: key, Value: value})
	if err != nil {
		return false, fmt.Errorf("failed to create system setting: %w", err)
	}

	return n > 0, nil
}

// GetRepositoryAttestationAttach reports whether the repository attaches signed
// attestations to its docker images as OCI referrers; false when never set.
func (p *PostgresDB) GetRepositoryAttestationAttach(ctx context.Context, repoID string) (bool, error) {
	enabled, err := p.q.GetRepositoryAttestationAttach(ctx, repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("get attestation attach setting: %w", err)
	}

	return enabled, nil
}

// SetRepositoryAttestationAttach stores the repository's attach switch.
func (p *PostgresDB) SetRepositoryAttestationAttach(ctx context.Context, repoID string, enabled bool) error {
	if err := p.q.SetRepositoryAttestationAttach(ctx, dbsqlc.SetRepositoryAttestationAttachParams{RepositoryID: repoID, Enabled: enabled}); err != nil {
		return fmt.Errorf("set attestation attach setting: %w", err)
	}

	return nil
}
