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
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// NixStore owns the Nix store paths, pending NARs and signing key.
//
// Mapping of a store path onto the generic package tables (one narinfo plus
// the NAR it points at is one version and one file; the store path name is
// the package, the store hash the version):
//
//	packages          name = name_key = store path name (e.g. "hello-2.12.1")
//	package_versions  version = version_key = store hash; metadata
//	                  {nar_hash, nar_size, nar_file, compression, refs,
//	                  deriver, ca, signatures}: every key is always written
//	                  (refs and signatures as [] when empty, deriver and ca
//	                  as ""); nar_file is the URL path of the NAR and is what
//	                  GetNixPathByNarFile looks up
//	package_files     path = filename = "<store hash>.narinfo", the NAR blob
//	                  (several paths may share one NAR blob), role primary,
//	                  download_count (the NAR's counter), no unit_key (nix is
//	                  not federated)
//
// The store hash is unique across names in a repository because the narinfo
// path is: a duplicate is a unique violation on the live path (ErrDuplicate).
// nix_pending_nars (staging) and nix_signing_keys keep their own tables.
type NixStore struct{ *core }

// nixMetadata is package_versions.metadata of a store path.
type nixMetadata struct {
	NarHash     string   `json:"nar_hash"`
	NarSize     int64    `json:"nar_size"`
	NarFile     string   `json:"nar_file"`
	Compression string   `json:"compression"`
	Refs        []string `json:"refs"`
	Deriver     string   `json:"deriver"`
	CA          string   `json:"ca"`
	Signatures  []string `json:"signatures"`
}

// nixNarinfoPath is the path (and file name) of the narinfo of a store hash.
func nixNarinfoPath(storeHash string) string {
	return storeHash + ".narinfo"
}

// nixMediaType is the media type of a stored NAR file.
const nixMediaType = "application/x-nix-nar"

// nixPathRow lists the sqlc row types nixPathFromRow converts; they share the columns of GetNixPathByHashRow.
type nixPathRow interface {
	dbsqlc.GetNixPathByHashRow | dbsqlc.ListNixPathsRow | dbsqlc.GetNixPathByNarFileRow
}

func nixPathFromRow[R nixPathRow](in R) *models.NixPath {
	row := dbsqlc.GetNixPathByHashRow(in)

	var meta nixMetadata

	_ = json.Unmarshal(row.Metadata, &meta)

	return &models.NixPath{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		StoreHash:     row.StoreHash,
		Name:          row.Name,
		NarHash:       meta.NarHash,
		NarSize:       meta.NarSize,
		NarFile:       meta.NarFile,
		Compression:   meta.Compression,
		References:    meta.Refs,
		Deriver:       meta.Deriver,
		CA:            meta.CA,
		Signatures:    meta.Signatures,
		DownloadCount: row.DownloadCount,
		BlobKey:       row.BlobKey,
		SHA256:        row.BlobSha256,
		SizeBytes:     row.SizeBytes,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

// PutNixPendingNar stores a NAR file that awaits its narinfo. It is idempotent
// per (repository, sha256).
func (p *NixStore) PutNixPendingNar(ctx context.Context, n *models.NixPendingNar) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, n.SHA256, n.BlobKey, nixMediaType, n.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for nix nar: %w", err)
	}

	err = p.q.PutNixPendingNar(ctx, dbsqlc.PutNixPendingNarParams{
		RepositoryID: n.RepositoryID, Sha256: n.SHA256, NarFile: n.NarFile, BlobID: blobID,
	})
	if err != nil {
		return fmt.Errorf("failed to stage nix nar: %w", err)
	}

	return nil
}

// GetNixPendingNar returns the staged NAR file, or (nil, nil).
func (p *NixStore) GetNixPendingNar(ctx context.Context, repoID, sha256 string) (*models.NixPendingNar, error) {
	row, err := p.q.GetNixPendingNar(ctx, dbsqlc.GetNixPendingNarParams{RepositoryID: repoID, Sha256: sha256})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get nix pending nar: %w", err)
	}

	return &models.NixPendingNar{
		RepositoryID: row.RepositoryID, SHA256: row.Sha256, NarFile: row.NarFile,
		BlobKey: row.BlobKey, SizeBytes: row.SizeBytes, CreatedAt: row.CreatedAt,
	}, nil
}

// GetNixPathByNarFile returns a live path whose NAR file is narFile, or (nil, nil).
func (p *NixStore) GetNixPathByNarFile(ctx context.Context, repoID, narFile string) (*models.NixPath, error) {
	row, err := p.q.GetNixPathByNarFile(ctx, dbsqlc.GetNixPathByNarFileParams{RepositoryID: repoID, NarFile: narFile})

	return mapRow(row, err, "get nix path by nar file", nixPathFromRow)
}

// GetNixPathByHash returns the live path of the store hash, or (nil, nil).
func (p *NixStore) GetNixPathByHash(ctx context.Context, repoID, storeHash string) (*models.NixPath, error) {
	row, err := p.q.GetNixPathByHash(ctx, dbsqlc.GetNixPathByHashParams{RepositoryID: repoID, Path: nixNarinfoPath(storeHash)})

	return mapRow(row, err, "get nix path", nixPathFromRow)
}

// CreateNixPath stores the path and drops the staging row of its NAR file in
// one transaction. A live path with the same store hash is ErrDuplicate.
func (p *NixStore) CreateNixPath(ctx context.Context, np *models.NixPath) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, np.SHA256, np.BlobKey, nixMediaType, np.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for nix path: %w", err)
	}

	verMeta, err := jsonOrEmpty(nixMetadata{
		NarHash:     np.NarHash,
		NarSize:     np.NarSize,
		NarFile:     np.NarFile,
		Compression: np.Compression,
		Refs:        nonNilStrings(np.References),
		Deriver:     np.Deriver,
		CA:          np.CA,
		Signatures:  nonNilStrings(np.Signatures),
	})
	if err != nil {
		return err
	}

	pkgAttrs, err := jsonOrEmpty(nil)
	if err != nil {
		return err
	}

	path := nixNarinfoPath(np.StoreHash)

	w := &PackageWrite{
		RepoID:     np.RepositoryID,
		Format:     "nix",
		Name:       np.Name,
		NameKey:    np.Name,
		Version:    np.StoreHash,
		VersionKey: np.StoreHash,
		Exclusive:  true,
		CreatedAt:  np.CreatedAt,
		Files:      []FileWrite{{Path: path, Filename: path}},
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := p.q.WithTx(tx)

	out, err := p.writePackageTx(ctx, q, w, []*string{&blobID}, pkgAttrs, verMeta)
	if err != nil {
		return fmt.Errorf("failed to create nix path: %w", err)
	}

	if err := q.DeleteNixPendingNar(ctx, dbsqlc.DeleteNixPendingNarParams{RepositoryID: np.RepositoryID, Sha256: np.SHA256}); err != nil {
		return fmt.Errorf("failed to drop nix pending nar: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit nix path: %w", err)
	}

	np.ID = out.Files[0].ID
	np.CreatedAt = out.Files[0].CreatedAt
	np.UpdatedAt = out.Files[0].UpdatedAt

	return nil
}

// ListNixPaths returns every live path of the repository ordered by store hash.
func (p *NixStore) ListNixPaths(ctx context.Context, repoID string) ([]*models.NixPath, error) {
	rows, err := p.q.ListNixPaths(ctx, repoID)

	return mapRows(rows, err, "list nix paths", nixPathFromRow)
}

// DeleteNixPath hard-deletes one live path (its narinfo file); the version and
// the package go with it.
func (p *NixStore) DeleteNixPath(ctx context.Context, repoID, id string) error {
	if err := p.deleteLiveFiles(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete nix path: %w", err)
	}

	return nil
}

// IncrementNixDownloadCount counts one download of a path's NAR file.
func (p *NixStore) IncrementNixDownloadCount(ctx context.Context, id, repoID string) error {
	if err := p.q.IncrementNixDownloadCount(ctx, dbsqlc.IncrementNixDownloadCountParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to count nix download: %w", err)
	}

	return nil
}

// DeleteStaleNixPendingNars deletes up to limit staging rows older than before
// and returns the sha256 of their NAR files so the caller can delete the blobs.
func (p *NixStore) DeleteStaleNixPendingNars(ctx context.Context, before time.Time, limit int) ([]string, error) {
	shas, err := p.q.DeleteStaleNixPendingNars(ctx, dbsqlc.DeleteStaleNixPendingNarsParams{
		Before: before, BatchLimit: int32(limit), //nolint:gosec // a small batch size
	})
	if err != nil {
		return nil, fmt.Errorf("failed to delete stale nix pending nars: %w", err)
	}

	return shas, nil
}

// GetNixSigningKey returns the repository's signing key, or (nil, nil).
func (p *NixStore) GetNixSigningKey(ctx context.Context, repoID string) (*models.NixSigningKey, error) {
	row, err := p.q.GetNixSigningKey(ctx, repoID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	case err != nil:
		return nil, fmt.Errorf("failed to get nix signing key: %w", err)
	}

	return &models.NixSigningKey{
		RepositoryID:  row.RepositoryID,
		KeyName:       row.KeyName,
		PublicKey:     row.PublicKey,
		PrivateKeyEnc: row.PrivateKey,
		CreatedBy:     row.CreatedBy,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// CreateNixSigningKeyIfAbsent stores the key unless the repository has one;
// created reports whether it did.
func (p *NixStore) CreateNixSigningKeyIfAbsent(ctx context.Context, k *models.NixSigningKey) (bool, error) {
	n, err := p.q.CreateNixSigningKeyIfAbsent(ctx, dbsqlc.CreateNixSigningKeyIfAbsentParams{
		RepositoryID: k.RepositoryID, KeyName: k.KeyName, PublicKey: k.PublicKey, PrivateKey: k.PrivateKeyEnc, CreatedBy: k.CreatedBy,
	})
	if err != nil {
		return false, fmt.Errorf("failed to create nix signing key: %w", err)
	}

	return n > 0, nil
}

// ReplaceNixSigningKey stores the key, replacing the repository's current one.
func (p *NixStore) ReplaceNixSigningKey(ctx context.Context, k *models.NixSigningKey) error {
	err := p.q.ReplaceNixSigningKey(ctx, dbsqlc.ReplaceNixSigningKeyParams{
		RepositoryID: k.RepositoryID, KeyName: k.KeyName, PublicKey: k.PublicKey, PrivateKey: k.PrivateKeyEnc, CreatedBy: k.CreatedBy,
	})
	if err != nil {
		return fmt.Errorf("failed to replace nix signing key: %w", err)
	}

	return nil
}
