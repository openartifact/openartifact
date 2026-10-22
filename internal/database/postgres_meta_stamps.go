// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// The *Stamp methods return a fingerprint of the rows a rendered metadata
// document is derived from. They scan one narrow index range of a single table
// (no blob join, no row decoding), so a revalidation can be answered, and a
// cached rendering looked up, without loading the rows themselves.

func (p *PostgresDB) HelmIndexStamp(ctx context.Context, repoID string) (string, error) {
	stamp, err := p.q.HelmIndexStamp(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to stamp helm index: %w", err)
	}

	return stamp, nil
}

func (p *PostgresDB) CondaRepodataStamp(ctx context.Context, repoID, platform string) (string, error) {
	stamp, err := p.q.CondaRepodataStamp(ctx, dbsqlc.CondaRepodataStampParams{RepositoryID: repoID, Platform: platform})
	if err != nil {
		return "", fmt.Errorf("failed to stamp conda repodata: %w", err)
	}

	return stamp, nil
}

func (p *PostgresDB) RubyGemsSpecsStamp(ctx context.Context, repoID string) (string, error) {
	stamp, err := p.q.RubyGemsSpecsStamp(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to stamp rubygems specs: %w", err)
	}

	return stamp, nil
}

func (p *PostgresDB) ListRubyGemSpecs(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
	rows, err := p.q.ListRubyGemSpecs(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list ruby gem specs: %w", err)
	}

	gems := make([]*models.RubyGem, len(rows))
	for i, row := range rows {
		gems[i] = &models.RubyGem{RepositoryID: repoID, Name: row.Name, Version: row.Version, Platform: row.Platform}
	}

	return gems, nil
}

func (p *PostgresDB) CargoIndexStamp(ctx context.Context, repoID, name string) (string, error) {
	stamp, err := p.q.CargoIndexStamp(ctx, dbsqlc.CargoIndexStampParams{RepositoryID: repoID, Name: name})
	if err != nil {
		return "", fmt.Errorf("failed to stamp cargo index: %w", err)
	}

	return stamp, nil
}

func (p *PostgresDB) ListCargoIndexVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	rows, err := p.q.ListCargoIndexVersions(ctx, dbsqlc.ListCargoIndexVersionsParams{RepositoryID: repoID, Name: name})
	if err != nil {
		return nil, fmt.Errorf("failed to list cargo index versions: %w", err)
	}

	crates := make([]*models.CargoCrate, len(rows))
	for i, row := range rows {
		crates[i] = &models.CargoCrate{
			RepositoryID: repoID,
			Name:         row.Name,
			Version:      row.Version,
			MetadataJSON: row.MetadataJson,
			Checksum:     row.Checksum,
			Yanked:       row.Yanked,
		}
	}

	return crates, nil
}
