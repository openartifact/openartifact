// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// RawStore owns the raw (generic file) artifacts.
//
// Mapping onto the generic package tables: one package, one version and one
// file per path.
//
//	packages          name = name_key = the path (case-sensitive), attrs = {}
//	package_versions  version = version_key = '' (every raw file is a release
//	                  with version ''), metadata = {}
//	package_files     path = filename = the raw path (live-unique), unit_key =
//	                  path, blob_id = the blob, role primary, listed, arch '',
//	                  part '', attrs = {content_type, metadata}, checksums = {}
//
// Overwriting a path updates the file in place (id and created_at are kept);
// a path that was trashed and is written again gets a new version under the
// same package. Directory listings scan uq_package_files_path
// (repository_id, path) by prefix, as the former (repository_id, path) index.
//
// Cleanup note: the former retention partition was the DIRECTORY of the path
// (regexp_replace(path, '[^/]*$', ”)), ranked newest first; with one package
// per path the generic partition holds a single file each, so the cleanup
// engine needs a partition over the directory of package_files.path.
type RawStore struct{ *core }

// rawFileAttrs is package_files.attrs of a raw file.
type rawFileAttrs struct {
	ContentType string         `json:"content_type"`
	Metadata    map[string]any `json:"metadata"`
}

// UpsertRawArtifact inserts or updates a raw artifact.
func (p *RawStore) UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error {
	_, err := p.writeRawArtifact(ctx, artifact, false)

	return err
}

// InsertRawArtifactIfAbsent inserts a raw artifact only when no live row
// exists at its path, in one atomic statement. It reports whether the row was
// inserted; false means the path is taken and nothing was changed.
func (p *RawStore) InsertRawArtifactIfAbsent(ctx context.Context, artifact *models.RawArtifact) (bool, error) {
	return p.writeRawArtifact(ctx, artifact, true)
}

func (p *RawStore) writeRawArtifact(ctx context.Context, artifact *models.RawArtifact, ifAbsent bool) (bool, error) {
	metadata := artifact.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}

	attrs, err := json.Marshal(rawFileAttrs{ContentType: artifact.ContentType, Metadata: metadata})
	if err != nil {
		return false, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, artifact.ContentType, artifact.SizeBytes)
	if err != nil {
		return false, fmt.Errorf("failed to upsert blob for raw artifact: %w", err)
	}

	// Callers (the raw PUT handler) leave the timestamps unset; storing the
	// zero time made every raw artifact show as uploaded on 0001-01-01.
	now := time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}

	if artifact.UpdatedAt.IsZero() {
		artifact.UpdatedAt = now
	}

	id := artifact.ID
	if id == "" {
		id = uuid.NewString()
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to begin raw artifact write: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	pkgID, err := q.UpsertPackage(ctx, dbsqlc.UpsertPackageParams{
		RepositoryID: artifact.RepositoryID,
		Format:       "raw",
		Name:         artifact.Path,
		NameKey:      artifact.Path,
		Attrs:        json.RawMessage(`{}`),
	})
	if err != nil {
		return false, fmt.Errorf("failed to upsert raw package: %w", err)
	}

	versionID, _, err := p.liveVersion(ctx, q, &PackageWrite{RepoID: artifact.RepositoryID}, pkgID, json.RawMessage(`{}`))
	if err != nil {
		return false, fmt.Errorf("failed to resolve raw version: %w", err)
	}

	inserted := true

	if ifAbsent {
		n, err := q.InsertRawFileIfAbsent(ctx, dbsqlc.InsertRawFileIfAbsentParams{
			ID:           id,
			VersionID:    versionID,
			RepositoryID: artifact.RepositoryID,
			Path:         artifact.Path,
			BlobID:       &blobID,
			Attrs:        attrs,
			CreatedAt:    artifact.CreatedAt,
		})
		if err != nil {
			return false, fmt.Errorf("failed to insert raw artifact: %w", err)
		}

		inserted = n > 0
	} else if _, err := q.UpsertRawFile(ctx, dbsqlc.UpsertRawFileParams{
		ID:           id,
		VersionID:    versionID,
		RepositoryID: artifact.RepositoryID,
		Path:         artifact.Path,
		BlobID:       &blobID,
		Attrs:        attrs,
		CreatedAt:    artifact.CreatedAt,
	}); err != nil {
		return false, fmt.Errorf("failed to upsert raw artifact: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("failed to commit raw artifact write: %w", err)
	}

	artifact.BlobID = blobID

	return inserted, nil
}

// rawRow lists the sqlc row types rawFromRow converts; they share the columns of GetRawArtifactRow.
type rawRow interface {
	dbsqlc.GetRawArtifactRow | dbsqlc.ListRawArtifactsRow | dbsqlc.ListRawArtifactsByPrefixRow
}

func rawFromRow[R rawRow](in R) (*models.RawArtifact, error) {
	row := dbsqlc.GetRawArtifactRow(in)

	var attrs rawFileAttrs
	if err := json.Unmarshal(row.Attrs, &attrs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal raw attrs: %w", err)
	}

	if attrs.Metadata == nil {
		attrs.Metadata = map[string]any{}
	}

	return &models.RawArtifact{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Path:         row.Path,
		ContentType:  attrs.ContentType,
		BlobID:       derefStr(row.BlobID),
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		BlobKey:      row.BlobKey,
		Metadata:     attrs.Metadata,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// GetRawArtifact retrieves a raw artifact by repo ID and path.
func (p *RawStore) GetRawArtifact(ctx context.Context, repoID, path string) (*models.RawArtifact, error) {
	row, err := p.q.GetRawArtifact(ctx, dbsqlc.GetRawArtifactParams{
		RepositoryID: repoID,
		Path:         path,
	})
	if isNoRows(err) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get raw artifact: %w", err)
	}

	return rawFromRow(row)
}

// DeleteRawArtifact hard-deletes a raw artifact by repo ID and path, with the
// version and package it leaves empty.
func (p *RawStore) DeleteRawArtifact(ctx context.Context, repoID, path string) error {
	id, err := p.q.GetRawFileID(ctx, dbsqlc.GetRawFileIDParams{RepositoryID: repoID, Path: path})
	if err != nil {
		if isNoRows(err) {
			return nil
		}

		return fmt.Errorf("failed to delete raw artifact: %w", err)
	}

	if _, err := p.deleteFiles(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete raw artifact: %w", err)
	}

	return nil
}

// ListRawArtifacts lists raw artifacts in a repository, optionally filtered by prefix.
func (p *RawStore) ListRawArtifacts(ctx context.Context, repoID, prefix string) ([]*models.RawArtifact, error) {
	if prefix == "" {
		rows, err := p.q.ListRawArtifacts(ctx, repoID)
		if err != nil {
			return nil, fmt.Errorf("failed to list raw artifacts: %w", err)
		}

		return mapRowsAll(rows, rawFromRow)
	}

	rows, err := p.q.ListRawArtifactsByPrefix(ctx, dbsqlc.ListRawArtifactsByPrefixParams{
		RepositoryID: repoID,
		Path:         escapeLike(prefix) + "%",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list raw artifacts: %w", err)
	}

	return mapRowsAll(rows, rawFromRow)
}

// mapRowsAll converts rows with a decoding mapper into a non-nil slice.
func mapRowsAll[R, T any](rows []R, mapf func(R) (*T, error)) ([]*T, error) {
	out := make([]*T, 0, len(rows))

	for _, r := range rows {
		v, err := mapf(r)
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}
