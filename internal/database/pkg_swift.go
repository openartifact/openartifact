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
	"strings"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// SwiftStore owns the Swift package releases.
//
// Mapping of a release onto the generic package tables (one published source
// archive is one version and one file; the package is "scope.name"):
//
//	packages          name = "scope.name" as first published, name_key = its
//	                  lower case (scopes and names are case-insensitive),
//	                  attrs {scope, name}
//	package_versions  version = version_key = the SemVer string; metadata
//	                  {metadata: the publisher's metadata object, repository_urls:
//	                  normalized declared URLs, manifests: Package.swift and the
//	                  Package@swift-*.swift files read from the archive,
//	                  signature_format / signature / metadata_signature: base64,
//	                  as sent}
//	package_files     path scope/name/version.zip (lower-case scope and name),
//	                  filename "<name>-<version>.zip", the archive blob (its
//	                  sha256 is the checksum SwiftPM verifies), role primary, no
//	                  unit_key (swift is not federated)
//
// The id of a release is package_files.id. A duplicate publish is a live
// version with a live file (ErrDuplicate).
type SwiftStore struct{ *core }

const swiftMediaType = "application/zip"

// swiftMetadata is package_versions.metadata of a release.
type swiftMetadata struct {
	Metadata          json.RawMessage        `json:"metadata,omitempty"`
	RepositoryURLs    []string               `json:"repository_urls,omitempty"`
	Manifests         []models.SwiftManifest `json:"manifests"`
	SignatureFormat   string                 `json:"signature_format,omitempty"`
	Signature         string                 `json:"signature,omitempty"`
	MetadataSignature string                 `json:"metadata_signature,omitempty"`
}

func swiftNameKey(scope, name string) string { return strings.ToLower(scope + "." + name) }

// CreateSwiftRelease stores one release. A live release of the same package
// and version is ErrDuplicate.
func (p *SwiftStore) CreateSwiftRelease(ctx context.Context, r *models.SwiftRelease) error {
	key := swiftNameKey(r.Scope, r.Name)

	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     r.RepositoryID,
		Format:     "swift",
		Name:       r.Scope + "." + r.Name,
		NameKey:    key,
		PkgAttrs:   map[string]any{"scope": r.Scope, "name": r.Name},
		Version:    r.Version,
		VersionKey: r.Version,
		VersionMeta: swiftMetadata{
			Metadata:          r.Metadata,
			RepositoryURLs:    r.RepositoryURLs,
			Manifests:         r.Manifests,
			SignatureFormat:   r.SignatureFormat,
			Signature:         r.Signature,
			MetadataSignature: r.MetadataSignature,
		},
		Exclusive: true,
		CreatedAt: r.CreatedAt,
		Files: []FileWrite{{
			Path:     strings.ToLower(r.Scope+"/"+r.Name) + "/" + r.Version + ".zip",
			Filename: r.Filename,
			Blob:     BlobWrite{SHA256: r.SHA256, Key: r.BlobKey, MediaType: swiftMediaType, Size: r.SizeBytes},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create swift release: %w", err)
	}

	r.ID, r.CreatedAt, r.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

// GetSwiftRelease returns the live release of a package, or (nil, nil). The
// scope and name are matched ignoring case.
func (p *SwiftStore) GetSwiftRelease(ctx context.Context, repoID, scope, name, version string) (*models.SwiftRelease, error) {
	row, err := p.q.GetSwiftRelease(ctx, dbsqlc.GetSwiftReleaseParams{
		RepositoryID: repoID, NameKey: swiftNameKey(scope, name), Version: version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get swift release: %w", err)
	}

	var meta swiftMetadata

	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("decode swift release metadata: %w", err)
	}

	return &models.SwiftRelease{
		ID:                row.ID,
		RepositoryID:      row.RepositoryID,
		Scope:             row.Scope,
		Name:              row.Name,
		Version:           row.Version,
		Filename:          row.Filename,
		BlobKey:           row.BlobKey,
		SizeBytes:         row.SizeBytes,
		SHA256:            row.Sha256,
		Metadata:          meta.Metadata,
		RepositoryURLs:    meta.RepositoryURLs,
		Manifests:         meta.Manifests,
		SignatureFormat:   meta.SignatureFormat,
		Signature:         meta.Signature,
		MetadataSignature: meta.MetadataSignature,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}, nil
}

// ListSwiftReleaseVersions returns the live releases of a package in
// publication order; the caller orders them by version.
func (p *SwiftStore) ListSwiftReleaseVersions(ctx context.Context, repoID, scope, name string) ([]models.SwiftReleaseVersion, error) {
	rows, err := p.q.ListSwiftReleaseVersions(ctx, dbsqlc.ListSwiftReleaseVersionsParams{
		RepositoryID: repoID, NameKey: swiftNameKey(scope, name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list swift releases: %w", err)
	}

	out := make([]models.SwiftReleaseVersion, len(rows))
	for i, row := range rows {
		out[i] = models.SwiftReleaseVersion{Version: row.Version, CreatedAt: row.CreatedAt}
	}

	return out, nil
}

// ListSwiftIdentifiers returns the "scope.name" of the packages with a live
// release that declares the normalized repository URL (pkg/swift.NormalizeURL).
func (p *SwiftStore) ListSwiftIdentifiers(ctx context.Context, repoID, normalizedURL string) ([]string, error) {
	ids, err := p.q.ListSwiftIdentifiers(ctx, dbsqlc.ListSwiftIdentifiersParams{RepositoryID: repoID, Url: normalizedURL})
	if err != nil {
		return nil, fmt.Errorf("failed to list swift identifiers: %w", err)
	}

	return ids, nil
}

// DeleteSwiftRelease hard-deletes a live release (its file; the version and the
// package go with their last file).
func (p *SwiftStore) DeleteSwiftRelease(ctx context.Context, repoID, id string) error {
	if err := p.deleteLiveFilesByID(ctx, repoID, []string{id}); err != nil {
		return fmt.Errorf("failed to delete swift release: %w", err)
	}

	return nil
}
