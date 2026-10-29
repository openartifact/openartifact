// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// CargoStore owns the Cargo crates.
type CargoStore struct{ *core }

// CreateCargoCrate creates a new Cargo crate.
//
// cargo_crates has no size_bytes column; the crate blob row takes its size
// from crate.SizeBytes.
func (p *CargoStore) CreateCargoCrate(ctx context.Context, crate *models.CargoCrate) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, crate.Checksum, crate.BlobKey, "application/gzip", crate.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for cargo crate: %w", err)
	}

	row, err := p.q.CreateCargoCrate(ctx, dbsqlc.CreateCargoCrateParams{
		CreatedAt:     createdAtOrNil(crate.CreatedAt),
		RepositoryID:  crate.RepositoryID,
		Name:          crate.Name,
		Version:       crate.Version,
		Description:   strPtr(crate.Description),
		Homepage:      strPtr(crate.Homepage),
		Documentation: strPtr(crate.Documentation),
		Repository:    strPtr(crate.Repository),
		License:       strPtr(crate.License),
		Keywords:      crate.Keywords,
		Categories:    crate.Categories,
		Authors:       crate.Authors,
		Readme:        strPtr(crate.Readme),
		MetadataJson:  crate.MetadataJSON,
		BlobID:        blobID,
		Yanked:        crate.Yanked,
		DownloadCount: int64(crate.DownloadCount),
	})

	return finishCreate(insertedRow(row), err, "create cargo crate", &crate.ID, &crate.CreatedAt, &crate.UpdatedAt)
}

func (p *CargoStore) GetCargoCrate(ctx context.Context, repoID, name, version string) (*models.CargoCrate, error) {
	row, err := p.q.GetCargoCrate(ctx, dbsqlc.GetCargoCrateParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get cargo crate: %w", err)
	}

	return &models.CargoCrate{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Name:          row.Name,
		Version:       row.Version,
		Description:   derefStr(row.Description),
		Homepage:      derefStr(row.Homepage),
		Documentation: derefStr(row.Documentation),
		Repository:    derefStr(row.Repository),
		License:       derefStr(row.License),
		Keywords:      row.Keywords,
		Categories:    row.Categories,
		Authors:       row.Authors,
		Readme:        derefStr(row.Readme),
		MetadataJSON:  row.MetadataJson,
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		SizeBytes:     row.SizeBytes,
		Yanked:        row.Yanked,
		DownloadCount: int(row.DownloadCount),
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// cargoCrateFromRow maps a sqlc cargo crate row; SearchCargoCratesRow converts to it.
func cargoCrateFromRow(row dbsqlc.ListCargoCrateVersionsRow) *models.CargoCrate {
	return &models.CargoCrate{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Name:          row.Name,
		Version:       row.Version,
		Description:   derefStr(row.Description),
		Homepage:      derefStr(row.Homepage),
		Documentation: derefStr(row.Documentation),
		Repository:    derefStr(row.Repository),
		License:       derefStr(row.License),
		Keywords:      row.Keywords,
		Categories:    row.Categories,
		Authors:       row.Authors,
		Readme:        derefStr(row.Readme),
		MetadataJSON:  row.MetadataJson,
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		Yanked:        row.Yanked,
		DownloadCount: int(row.DownloadCount),
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func (p *CargoStore) ListCargoCrateVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	rows, err := p.q.ListCargoCrateVersions(ctx, dbsqlc.ListCargoCrateVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})

	return mapRowsOrNil(rows, err, "query cargo crate versions", cargoCrateFromRow)
}

func (p *CargoStore) SearchCargoCrates(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrate, error) {
	rows, err := p.q.SearchCargoCrates(ctx, dbsqlc.SearchCargoCratesParams{
		RepositoryID: repoID,
		Name:         "%" + escapeLike(query) + "%",
		//nolint:gosec // G115: value is bounded by API validation
		Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search cargo crates: %w", err)
	}

	var crates []*models.CargoCrate

	for _, row := range rows {
		crates = append(crates, cargoCrateFromRow(dbsqlc.ListCargoCrateVersionsRow(row)))
	}

	return crates, nil
}

// SearchCargoCrateSummaries matches crates by name or description and
// returns one summary per crate (at most limit, by name) together with the
// total number of matching crates.
func (p *CargoStore) SearchCargoCrateSummaries(
	ctx context.Context,
	repoID, query string,
	limit int,
) ([]*models.CargoCrateSummary, int, error) {
	rows, err := p.q.SearchCargoCrateSummaries(ctx, dbsqlc.SearchCargoCrateSummariesParams{
		RepositoryID: repoID,
		Name:         "%" + escapeLike(query) + "%",
		//nolint:gosec // G115: value is bounded by API validation
		Limit: int32(limit),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search cargo crates: %w", err)
	}

	var total int

	summaries := make([]*models.CargoCrateSummary, 0, len(rows))

	for _, row := range rows {
		total = int(row.Total)

		summaries = append(summaries, &models.CargoCrateSummary{
			Name:         row.Name,
			Description:  row.Description,
			Downloads:    row.Downloads,
			Versions:     row.Versions,
			LiveVersions: row.LiveVersions,
		})
	}

	return summaries, total, nil
}

func (p *CargoStore) YankCargoCrate(ctx context.Context, repoID, name, version string, yanked bool) error {
	err := p.q.YankCargoCrate(ctx, dbsqlc.YankCargoCrateParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Yanked:       yanked,
	})
	if err != nil {
		return fmt.Errorf("failed to yank cargo crate: %w", err)
	}

	return nil
}

func (p *CargoStore) IncrementCargoDownloadCount(ctx context.Context, repoID, name, version string) error {
	err := p.q.IncrementCargoDownloadCount(ctx, dbsqlc.IncrementCargoDownloadCountParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("failed to increment cargo download count: %w", err)
	}

	return nil
}

// CargoIndexStamp is a cheap validator of one crate's sparse index entry:
// it changes when a live version is added, removed or (un)yanked.
func (p *CargoStore) CargoIndexStamp(ctx context.Context, repoID, name string) (string, error) {
	stamp, err := p.q.CargoIndexStamp(ctx, dbsqlc.CargoIndexStampParams{RepositoryID: repoID, Name: name})
	if err != nil {
		return "", fmt.Errorf("failed to stamp cargo index: %w", err)
	}

	return stamp, nil
}

// ListCargoIndexVersions returns the live versions of a crate with only the
// columns the sparse index needs (Name, Version, MetadataJSON, Checksum,
// Yanked), oldest first.
func (p *CargoStore) ListCargoIndexVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
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
