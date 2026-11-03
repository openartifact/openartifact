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
	"maps"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// GetRepoComponentDetail resolves a row id of the package listing (a
// package_files id of any format) to the artifact with the metadata its format
// shows, scoped to repoID. The query loads the file with its version, package
// and side-table JSON; the format's storage.Detail descriptor says which fields
// of that document are the summary, license, info entries and dependencies, so
// no format is named here. It returns nil, nil when the id is not in that
// repository or is not a UUID.
func (p *CatalogStore) GetRepoComponentDetail(ctx context.Context, repoID, id string) (*models.ComponentDetail, error) {
	if !isUUID(id) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	row, err := p.q.GetRepoComponentDetail(ctx, dbsqlc.GetRepoComponentDetailParams{RepoID: repoID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get component detail: %w", err)
	}

	detail := &models.ComponentDetail{
		ID: row.ID, Format: row.Format, Name: row.Name, Version: row.Version, Arch: row.Arch,
		Filename: row.Filename, SHA256: row.Sha256, SizeBytes: row.SizeBytes,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Info: map[string]string{},
	}

	if len(row.Doc) > 0 {
		var doc storage.Document
		if err := json.Unmarshal(row.Doc, &doc); err != nil {
			return nil, fmt.Errorf("failed to decode component document: %w", err)
		}

		// A format without a registered descriptor shows the generic columns only.
		s, _ := storage.Lookup(row.Format)
		res := s.Detail.Resolve(doc)

		detail.Summary, detail.Description, detail.License, detail.Homepage = res.Summary, res.Description, res.License, res.Homepage
		detail.MD5, detail.SHA1 = res.MD5, res.SHA1
		detail.Info = res.Info
		detail.Deps, detail.DepsDecoder = res.Deps, s.Detail.Decoder
	}

	// The explicit non-package rows (proxy cache entry, docker tag) report their
	// display fields directly.
	if len(row.Info) > 0 {
		var info map[string]string
		if err := json.Unmarshal(row.Info, &info); err != nil {
			return nil, fmt.Errorf("failed to decode component info: %w", err)
		}

		maps.DeleteFunc(info, func(_, v string) bool { return v == "" })
		detail.Info = info
	}

	if row.VersionID == "" {
		detail.Files = []models.ComponentFile{{
			ID: row.ID, Filename: row.Filename, Arch: row.Arch, Size: row.SizeBytes, SHA256: row.Sha256,
		}}

		return detail, nil
	}

	files, err := p.q.ListVersionFiles(ctx, dbsqlc.ListVersionFilesParams{
		VersionID: row.VersionID, RepoID: repoID, MaxFiles: models.MaxDetailFiles,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list version files: %w", err)
	}

	detail.Files = make([]models.ComponentFile, len(files))
	for i, f := range files {
		detail.Files[i] = models.ComponentFile{ID: f.ID, Filename: f.Filename, Arch: f.Arch, Size: f.SizeBytes, SHA256: f.Sha256}
	}

	return detail, nil
}
