// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// CargoStore owns the Cargo crates.
//
// Crates live in the generic package tables (repository format "cargo"; the
// mapping is the maintainers' reference, derived from migrations/001_baseline.sql):
//
//	packages          one per crate: name = the spelling of the first publish,
//	                  name_key = lower(name) (crate names are case-insensitive), attrs {}
//	package_versions  one per crate version: version = version_key = version;
//	                  metadata {"name": <name as published with this version>,
//	                  "description", "homepage", "documentation", "repository",
//	                  "license", "readme": text, "keywords", "categories", "authors":
//	                  arrays of text, "metadata_json": the publish metadata the sparse
//	                  index is built from, "yanked": boolean (always written: the
//	                  sparse-index stamp hashes the file id and the yanked flag)}.
//	                  Empty values are omitted; a missing key reads as ""/empty.
//	package_files     one primary listed file per version: path
//	                  "<name>/<version>/<name>-<version>.crate", filename
//	                  "<name>-<version>.crate", download_count, unit_key
//	                  "<name>@<version>" (the federation unit, name as published). The
//	                  blob's sha256 is the crate checksum of the sparse index.
//
// A second publish of an equal version in any spelling of the name is
// ErrDuplicate (the live version key of the package).
type CargoStore struct{ *core }

const cargoFormat = "cargo"

// cargoMetadata is package_versions.metadata of a crate version.
type cargoMetadata struct {
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Homepage      string          `json:"homepage,omitempty"`
	Documentation string          `json:"documentation,omitempty"`
	Repository    string          `json:"repository,omitempty"`
	License       string          `json:"license,omitempty"`
	Readme        string          `json:"readme,omitempty"`
	Keywords      []string        `json:"keywords,omitempty"`
	Categories    []string        `json:"categories,omitempty"`
	Authors       []string        `json:"authors,omitempty"`
	MetadataJSON  json.RawMessage `json:"metadata_json"`
	Yanked        bool            `json:"yanked"`
}

// CreateCargoCrate creates a new Cargo crate version. The crate blob row takes
// its size from crate.SizeBytes; a live version of the same crate (any
// spelling of the name) is ErrDuplicate.
func (p *CargoStore) CreateCargoCrate(ctx context.Context, crate *models.CargoCrate) error {
	meta := json.RawMessage(crate.MetadataJSON)
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}

	filename := crate.Name + "-" + crate.Version + ".crate"

	written, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     crate.RepositoryID,
		Format:     cargoFormat,
		Name:       crate.Name,
		NameKey:    strings.ToLower(crate.Name),
		Version:    crate.Version,
		VersionKey: crate.Version,
		VersionMeta: cargoMetadata{
			Name:          crate.Name,
			Description:   crate.Description,
			Homepage:      crate.Homepage,
			Documentation: crate.Documentation,
			Repository:    crate.Repository,
			License:       crate.License,
			Readme:        crate.Readme,
			Keywords:      crate.Keywords,
			Categories:    crate.Categories,
			Authors:       crate.Authors,
			MetadataJSON:  meta,
			Yanked:        crate.Yanked,
		},
		Exclusive: true,
		CreatedAt: crate.CreatedAt,
		Files: []FileWrite{{
			Path:          crate.Name + "/" + crate.Version + "/" + filename,
			Filename:      filename,
			Blob:          BlobWrite{SHA256: crate.Checksum, Key: crate.BlobKey, MediaType: "application/gzip", Size: crate.SizeBytes},
			UnitKey:       crate.Name + "@" + crate.Version,
			DownloadCount: int64(crate.DownloadCount),
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create cargo crate: %w", err)
	}

	crate.ID = written.Files[0].ID
	crate.CreatedAt = written.Files[0].CreatedAt
	crate.UpdatedAt = written.Files[0].UpdatedAt

	return nil
}

func (p *CargoStore) GetCargoCrate(ctx context.Context, repoID, name, version string) (*models.CargoCrate, error) {
	row, err := p.q.GetCargoCrate(ctx, dbsqlc.GetCargoCrateParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})

	return mapRow(row, err, "get cargo crate", cargoCrateFromRow)
}

// decodeStrings reads a JSON array of text; a malformed value reads as none.
func decodeStrings(raw json.RawMessage) []string {
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil || len(out) == 0 {
		return nil
	}

	return out
}

// cargoCrateFromRow maps a sqlc cargo crate row; ListCargoCrateVersionsRow converts to it.
func cargoCrateFromRow[R cargoRow](in R) *models.CargoCrate {
	row := dbsqlc.GetCargoCrateRow(in)

	return &models.CargoCrate{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Name:          row.Name,
		Version:       row.Version,
		Description:   row.Description,
		Homepage:      row.Homepage,
		Documentation: row.Documentation,
		Repository:    row.Repository,
		License:       row.License,
		Keywords:      decodeStrings(row.Keywords),
		Categories:    decodeStrings(row.Categories),
		Authors:       decodeStrings(row.Authors),
		Readme:        row.Readme,
		MetadataJSON:  []byte(row.MetadataJson),
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		SizeBytes:     row.SizeBytes,
		Yanked:        row.Yanked,
		DownloadCount: int(row.DownloadCount),
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

// cargoRow lists the sqlc row types cargoCrateFromRow converts.
type cargoRow interface {
	dbsqlc.GetCargoCrateRow | dbsqlc.ListCargoCrateVersionsRow
}

func (p *CargoStore) ListCargoCrateVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	rows, err := p.q.ListCargoCrateVersions(ctx, dbsqlc.ListCargoCrateVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})

	return mapRowsOrNil(rows, err, "query cargo crate versions", cargoCrateFromRow)
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
