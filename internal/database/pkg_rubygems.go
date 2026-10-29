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

// RubyGemsStore owns the RubyGems gems.
type RubyGemsStore struct{ *core }

// CreateRubyGem creates a new RubyGem.
//
// gem.Size is persisted on the row (for quota release) and recorded as the
// blob's size_bytes.
func (p *RubyGemsStore) CreateRubyGem(ctx context.Context, gem *models.RubyGem) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, gem.ChecksumSHA256, gem.BlobKey, "application/gzip", gem.Size)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for ruby gem: %w", err)
	}

	row, err := p.q.CreateRubyGem(ctx, dbsqlc.CreateRubyGemParams{
		CreatedAt:     createdAtOrNil(gem.CreatedAt),
		RepositoryID:  gem.RepositoryID,
		Name:          gem.Name,
		Version:       gem.Version,
		Platform:      gem.Platform,
		Description:   strPtr(gem.Description),
		Summary:       strPtr(gem.Summary),
		Homepage:      strPtr(gem.Homepage),
		Authors:       gem.Authors,
		Licenses:      gem.Licenses,
		Dependencies:  gem.Dependencies,
		MetadataJson:  gem.MetadataJSON,
		BlobID:        blobID,
		Yanked:        gem.Yanked,
		DownloadCount: int64(gem.DownloadCount),
		Size:          gem.Size,
	})

	return finishCreate(insertedRow(row), err, "create ruby gem", &gem.ID, &gem.CreatedAt, &gem.UpdatedAt)
}

// rubyGemFromRow maps a sqlc ruby gem row to the model. The List/Search row
// types share GetRubyGemRow's layout and are converted to it by the callers.
// rubyGemRow lists the sqlc row types rubyGemFromRow converts; they share the columns of GetRubyGemRow.
type rubyGemRow interface {
	dbsqlc.GetRubyGemRow | dbsqlc.ListRubyGemsRow | dbsqlc.ListRubyGemVersionsRow
}

func rubyGemFromRow[R rubyGemRow](in R) *models.RubyGem {
	row := dbsqlc.GetRubyGemRow(in)

	return &models.RubyGem{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Name:           row.Name,
		Version:        row.Version,
		Platform:       row.Platform,
		Description:    derefStr(row.Description),
		Summary:        derefStr(row.Summary),
		Homepage:       derefStr(row.Homepage),
		Authors:        row.Authors,
		Licenses:       row.Licenses,
		Dependencies:   row.Dependencies,
		MetadataJSON:   row.MetadataJson,
		BlobKey:        row.BlobKey,
		ChecksumSHA256: row.ChecksumSha256,
		Yanked:         row.Yanked,
		DownloadCount:  int(row.DownloadCount),
		Size:           row.Size,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func (p *RubyGemsStore) GetRubyGem(ctx context.Context, repoID, name, version, platform string) (*models.RubyGem, error) {
	row, err := p.q.GetRubyGem(ctx, dbsqlc.GetRubyGemParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Platform:     platform,
	})

	return mapRow(row, err, "get ruby gem", rubyGemFromRow)
}

func (p *RubyGemsStore) ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
	rows, err := p.q.ListRubyGems(ctx, repoID)

	return mapRowsOrNil(rows, err, "query ruby gems", rubyGemFromRow)
}

func (p *RubyGemsStore) ListRubyGemVersions(ctx context.Context, repoID, name string) ([]*models.RubyGem, error) {
	rows, err := p.q.ListRubyGemVersions(ctx, dbsqlc.ListRubyGemVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})

	return mapRowsOrNil(rows, err, "query ruby gem versions", rubyGemFromRow)
}

func (p *RubyGemsStore) SearchRubyGems(ctx context.Context, repoID, query string) ([]*models.RubyGem, error) {
	rows, err := p.q.SearchRubyGems(ctx, dbsqlc.SearchRubyGemsParams{
		RepositoryID: repoID,
		Name:         "%" + escapeLike(query) + "%",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search ruby gems: %w", err)
	}

	var gems []*models.RubyGem

	for _, row := range rows {
		gems = append(gems, rubyGemFromRow(dbsqlc.GetRubyGemRow(row)))
	}

	return gems, nil
}

func (p *RubyGemsStore) YankRubyGem(ctx context.Context, repoID, name, version, platform string, yanked bool) error {
	err := p.q.YankRubyGem(ctx, dbsqlc.YankRubyGemParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Column5:      platform,
		Yanked:       yanked,
	})
	if err != nil {
		return fmt.Errorf("failed to yank ruby gem: %w", err)
	}

	return nil
}

func (p *RubyGemsStore) RubyGemsSpecsStamp(ctx context.Context, repoID string) (string, error) {
	stamp, err := p.q.RubyGemsSpecsStamp(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to stamp rubygems specs: %w", err)
	}

	return stamp, nil
}

func (p *RubyGemsStore) ListRubyGemSpecs(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
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
