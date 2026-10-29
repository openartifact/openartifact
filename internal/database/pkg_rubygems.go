// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// RubyGemsStore owns the RubyGems gems.
//
// Gems live in the generic package tables (repository format "rubygems"; the
// mapping is the maintainers' reference, derived from migrations/001_baseline.sql):
//
//	packages          one per gem name: name = name_key = name, attrs {}
//	package_versions  one per (version, platform): version = version, version_key =
//	                  version for the pure-Ruby platform and "version|platform"
//	                  otherwise ('|' cannot occur in either, so the key is injective;
//	                  the text before '|' is what the cleanup rank sorts on);
//	                  metadata {"description", "summary", "homepage": text,
//	                  "authors", "licenses": arrays of text, "dependencies": array,
//	                  "metadata_json": object, "yanked": boolean (always written)}.
//	                  Empty text values are omitted; a missing key reads as "".
//	package_files    one primary listed file per version: path
//	                  "<name>/<version>/<filename>", filename "<name>-<version>[-<platform>].gem",
//	                  arch = the platform (the listing and cleanup partition),
//	                  size_bytes = the gem size (wins over the blob size, NULL
//	                  when 0), download_count
//
// Rubygems is not federated: the file carries no unit_key. Yanks apply per
// version (and so per platform build) as before.
type RubyGemsStore struct{ *core }

const (
	rubygemsFormat = "rubygems"
	// rubyPlatform is the platform of the pure-Ruby gem; it is not part of the
	// version key or the filename.
	rubyPlatform = "ruby"
)

// rubyGemVersionKey is the live key of a gem build inside its package.
func rubyGemVersionKey(version, platform string) string {
	if platform == "" || platform == rubyPlatform {
		return version
	}

	return version + "|" + platform
}

// rubyGemFilename is the name of the .gem file of a build.
func rubyGemFilename(name, version, platform string) string {
	if platform == "" || platform == rubyPlatform {
		return name + "-" + version + ".gem"
	}

	return name + "-" + version + "-" + platform + ".gem"
}

// rubyGemMetadata is package_versions.metadata of a gem build.
type rubyGemMetadata struct {
	Description  string          `json:"description,omitempty"`
	Summary      string          `json:"summary,omitempty"`
	Homepage     string          `json:"homepage,omitempty"`
	Authors      []string        `json:"authors,omitempty"`
	Licenses     []string        `json:"licenses,omitempty"`
	Dependencies json.RawMessage `json:"dependencies"`
	MetadataJSON json.RawMessage `json:"metadata_json"`
	Yanked       bool            `json:"yanked"`
}

// CreateRubyGem creates a new RubyGem build. A live build with the same name,
// version and platform is ErrDuplicate.
//
// gem.Size is persisted on the file (it wins over the blob size, for quota
// release and the listing) and recorded as the blob's size_bytes.
func (p *RubyGemsStore) CreateRubyGem(ctx context.Context, gem *models.RubyGem) error {
	deps := json.RawMessage(gem.Dependencies)
	if len(deps) == 0 {
		deps = json.RawMessage(`[]`)
	}

	meta := json.RawMessage(gem.MetadataJSON)
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}

	var size *int64
	if gem.Size != 0 {
		size = &gem.Size
	}

	filename := rubyGemFilename(gem.Name, gem.Version, gem.Platform)

	written, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     gem.RepositoryID,
		Format:     rubygemsFormat,
		Name:       gem.Name,
		NameKey:    gem.Name,
		Version:    gem.Version,
		VersionKey: rubyGemVersionKey(gem.Version, gem.Platform),
		VersionMeta: rubyGemMetadata{
			Description:  gem.Description,
			Summary:      gem.Summary,
			Homepage:     gem.Homepage,
			Authors:      gem.Authors,
			Licenses:     gem.Licenses,
			Dependencies: deps,
			MetadataJSON: meta,
			Yanked:       gem.Yanked,
		},
		Exclusive: true,
		CreatedAt: gem.CreatedAt,
		Files: []FileWrite{{
			Path:          gem.Name + "/" + gem.Version + "/" + filename,
			Filename:      filename,
			Blob:          BlobWrite{SHA256: gem.ChecksumSHA256, Key: gem.BlobKey, MediaType: "application/gzip", Size: gem.Size},
			SizeBytes:     size,
			Arch:          gem.Platform,
			DownloadCount: int64(gem.DownloadCount),
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create ruby gem: %w", err)
	}

	gem.ID = written.Files[0].ID
	gem.CreatedAt = written.Files[0].CreatedAt
	gem.UpdatedAt = written.Files[0].UpdatedAt

	return nil
}

// rubyGemRow lists the sqlc row types rubyGemFromRow converts; they share the columns of GetRubyGemRow.
type rubyGemRow interface {
	dbsqlc.GetRubyGemRow | dbsqlc.ListRubyGemsRow | dbsqlc.ListRubyGemVersionsRow | dbsqlc.SearchRubyGemsRow
}

func rubyGemFromRow[R rubyGemRow](in R) *models.RubyGem {
	row := dbsqlc.GetRubyGemRow(in)

	return &models.RubyGem{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Name:           row.Name,
		Version:        row.Version,
		Platform:       row.Platform,
		Description:    row.Description,
		Summary:        row.Summary,
		Homepage:       row.Homepage,
		Authors:        decodeStrings(row.Authors),
		Licenses:       decodeStrings(row.Licenses),
		Dependencies:   []byte(row.Dependencies),
		MetadataJSON:   []byte(row.MetadataJson),
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
		VersionKey:   rubyGemVersionKey(version, platform),
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
		gems = append(gems, rubyGemFromRow(row))
	}

	return gems, nil
}

func (p *RubyGemsStore) YankRubyGem(ctx context.Context, repoID, name, version, platform string, yanked bool) error {
	err := p.q.YankRubyGem(ctx, dbsqlc.YankRubyGemParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Platform:     platform,
		Yanked:       yanked,
	})
	if err != nil {
		return fmt.Errorf("failed to yank ruby gem: %w", err)
	}

	return nil
}

// RubyGemsSpecsStamp is a cheap validator of every specs index of a
// repository: it changes when a listed gem is added, removed or yanked.
func (p *RubyGemsStore) RubyGemsSpecsStamp(ctx context.Context, repoID string) (string, error) {
	stamp, err := p.q.RubyGemsSpecsStamp(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to stamp rubygems specs: %w", err)
	}

	return stamp, nil
}

// ListRubyGemSpecs returns the listed (not yanked) gems with only Name,
// Version and Platform set, by name then version descending.
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
