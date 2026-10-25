// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The npm codec serves two unit families that hang off a package row
// (npm_packages, which carries the repository and the full name):
//
//   - a version, key `<full_name>@<version>`: an artifact unit (tarball blob
//     plus the npm_versions row);
//   - a dist-tag, key `<full_name>#<tag>`: a ref unit pointing at a version.
//
// Package rows are not units of their own: importing either family finds or
// creates the package the way the publish handler does (scope and name derive
// from the full name) and marks its packument stale so the regeneration the
// engine queues from Change.Regen rebuilds it, latest_version included.

func init() { //nolint:gochecknoinits // codec registry
	register(npmCodec{})
}

type npmCodec struct{}

func (npmCodec) Format() string { return kindNPM }

// npmVersionData is the replicated state of an npm_versions row. blob_id
// carries the tarball's sha256, not the local blobs.id; package_id, id,
// search_tsv, updated_at and the trash marks stay local.
type npmVersionData struct {
	FullName        string          `json:"full_name"`
	Version         string          `json:"version"`
	ManifestJSON    json.RawMessage `json:"manifest_json"`
	Integrity       string          `json:"integrity"`
	Shasum          string          `json:"shasum"`
	TarballFilename string          `json:"tarball_filename"`
	BlobID          string          `json:"blob_id"`
	CreatedAt       *time.Time      `json:"created_at,omitempty"`
}

// npmTagData is the replicated state of an npm_dist_tags row.
type npmTagData struct {
	FullName string `json:"full_name"`
	Tag      string `json:"tag"`
	Version  string `json:"version"`
}

// parseNPMKey splits a unit key into the package full name and the version (or
// the tag when isTag). Versions split at the last '@' (a scoped name starts
// with one), tags at the last '#'.
func parseNPMKey(key string) (string, string, bool, bool) {
	sep, isTag := "@", false
	if strings.Contains(key, "#") {
		sep, isTag = "#", true
	}

	fullName, rest, found := strings.CutLast(key, sep)

	return fullName, rest, isTag, found && fullName != "" && rest != ""
}

// splitNPMName derives the scope ("@scope", or "" when unscoped) and the bare
// name from a full name, as the publish handler does.
func splitNPMName(fullName string) (string, string) {
	if strings.HasPrefix(fullName, "@") {
		if idx := strings.Index(fullName, "/"); idx > 0 {
			return fullName[:idx], fullName[idx+1:]
		}
	}

	return "", fullName
}

func npmKeyError(key string) error {
	return fmt.Errorf("%w: malformed unit key %q", ErrInvalid, key)
}

// ensureNPMPackage finds or creates the package row of fullName in the
// repository and marks its packument stale. A new package gets latest as its
// latest_version; an existing one keeps its own unless it has none. It returns
// the package id.
func ensureNPMPackage(ctx context.Context, tx *sql.Tx, repoID, fullName, latest string) (string, error) {
	scope, name := splitNPMName(fullName)

	var id string

	err := tx.QueryRowContext(ctx,
		`INSERT INTO npm_packages AS p (repository_id, scope, name, full_name, latest_version, packument_stale)
		 VALUES ($1::uuid, $2, $3, $4, $5, true)
		 ON CONFLICT (repository_id, full_name) DO UPDATE
		    SET latest_version = CASE WHEN p.latest_version = '' THEN EXCLUDED.latest_version ELSE p.latest_version END,
		        packument_stale = true
		 RETURNING p.id::text`,
		repoID, scope, name, fullName, latest).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("ensure npm package %s: %w", fullName, err)
	}

	return id, nil
}

func npmRegen(repoID, packageID string) []Regen {
	return []Regen{{Kind: kindNPM, RepoID: repoID, PackageID: packageID}}
}

func markNPMStale(ctx context.Context, tx *sql.Tx, packageID string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE npm_packages SET packument_stale = true WHERE id = $1::uuid`, packageID); err != nil {
		return fmt.Errorf("mark npm packument stale: %w", err)
	}

	return nil
}

// --- Export -----------------------------------------------------------------

func (c npmCodec) Export(ctx context.Context, q Querier, repoID, key string) (*Content, error) {
	fullName, rest, isTag, ok := parseNPMKey(key)
	if !ok {
		return nil, npmKeyError(key)
	}

	if isTag {
		return c.exportTag(ctx, q, repoID, fullName, rest)
	}

	return c.exportVersion(ctx, q, repoID, fullName, rest)
}

func (npmCodec) exportVersion(ctx context.Context, q Querier, repoID, fullName, version string) (*Content, error) {
	d := npmVersionData{FullName: fullName, Version: version}

	var (
		manifest string
		blobID   string
		created  time.Time
	)

	err := q.QueryRowContext(ctx,
		`SELECT v.manifest_json::text, v.integrity, v.shasum, v.tarball_filename, v.blob_id::text, v.created_at
		   FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id
		  WHERE p.repository_id = $1::uuid AND p.full_name = $2 AND v.version = $3 AND v.deleted_at IS NULL`,
		repoID, fullName, version).Scan(&manifest, &d.Integrity, &d.Shasum, &d.TarballFilename, &blobID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // documented: no live row
	}

	if err != nil {
		return nil, fmt.Errorf("load npm version: %w", err)
	}

	b, err := blobByID(ctx, q, blobID)
	if err != nil {
		return nil, err
	}

	d.ManifestJSON = json.RawMessage(manifest)
	d.BlobID = b.sha256
	d.CreatedAt = &created

	data, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode npm version: %w", err)
	}

	return &Content{
		Kind:       KindArtifact,
		Data:       data,
		Blobs:      []BlobRef{{SHA256: b.sha256, Size: b.size, MediaType: b.mediaType}},
		ContentRef: b.sha256,
	}, nil
}

func (npmCodec) exportTag(ctx context.Context, q Querier, repoID, fullName, tag string) (*Content, error) {
	d := npmTagData{FullName: fullName, Tag: tag}

	var versionLive bool

	err := q.QueryRowContext(ctx,
		`SELECT d.version,
		        EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = d.package_id AND v.version = d.version AND v.deleted_at IS NULL)
		   FROM npm_dist_tags d JOIN npm_packages p ON p.id = d.package_id
		  WHERE p.repository_id = $1::uuid AND p.full_name = $2 AND d.tag = $3`,
		repoID, fullName, tag).Scan(&d.Version, &versionLive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // documented: no live row
	}

	if err != nil {
		return nil, fmt.Errorf("load npm dist-tag: %w", err)
	}

	data, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode npm dist-tag: %w", err)
	}

	content := &Content{Kind: KindRef, Data: data, ContentRef: d.Version}

	// A tag of a trashed version is hidden at the origin but kept (a restore
	// brings it back): it travels without a dependency the origin cannot serve.
	if versionLive {
		content.Deps = []string{fullName + "@" + d.Version}
	}

	return content, nil
}

// --- Usage ------------------------------------------------------------------

func (npmCodec) Usage(_ context.Context, _ Querier, _, key string, c *Content) (int64, int, error) {
	if _, _, isTag, ok := parseNPMKey(key); !ok || isTag {
		return 0, 0, nil
	}

	var size int64
	for _, b := range c.Blobs {
		size += b.Size
	}

	return size, 1, nil
}

// --- Import -----------------------------------------------------------------

func (c npmCodec) Import(ctx context.Context, tx *sql.Tx, repoID, key string, content *Content, blobIDs map[string]string) (*Change, error) {
	fullName, rest, isTag, ok := parseNPMKey(key)
	if !ok {
		return nil, npmKeyError(key)
	}

	if isTag {
		return c.importTag(ctx, tx, repoID, key, fullName, rest, content)
	}

	return c.importVersion(ctx, tx, repoID, key, fullName, rest, content, blobIDs)
}

func (npmCodec) importTag(ctx context.Context, tx *sql.Tx, repoID, key, fullName, tag string, content *Content) (*Change, error) {
	var d npmTagData
	if err := json.Unmarshal(content.Data, &d); err != nil {
		return nil, fmt.Errorf("%w: decode unit data: %v", ErrInvalid, err) //nolint:errorlint // client-safe text only
	}

	if d.Version == "" || (d.FullName != "" && d.FullName != fullName) || (d.Tag != "" && d.Tag != tag) {
		return nil, fmt.Errorf("%w: unit data does not match key %q", ErrInvalid, key)
	}

	pkgID, err := ensureNPMPackage(ctx, tx, repoID, fullName, "")
	if err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO npm_dist_tags (package_id, tag, version) VALUES ($1::uuid, $2, $3)
		 ON CONFLICT (package_id, tag) DO UPDATE SET version = EXCLUDED.version`,
		pkgID, tag, d.Version)
	if err != nil {
		return nil, fmt.Errorf("upsert npm dist-tag: %w", err)
	}

	return &Change{Regen: npmRegen(repoID, pkgID)}, nil
}

func (npmCodec) importVersion(ctx context.Context, tx *sql.Tx, repoID, key, fullName, version string, content *Content, blobIDs map[string]string) (*Change, error) {
	var d npmVersionData
	if err := json.Unmarshal(content.Data, &d); err != nil {
		return nil, fmt.Errorf("%w: decode unit data: %v", ErrInvalid, err) //nolint:errorlint // client-safe text only
	}

	if (d.FullName != "" && d.FullName != fullName) || (d.Version != "" && d.Version != version) {
		return nil, fmt.Errorf("%w: unit data does not match key %q", ErrInvalid, key)
	}

	newBlobID, ok := blobIDs[d.BlobID]
	if !ok {
		return nil, fmt.Errorf("%w: blob %s of unit %s was not provided", ErrInvalid, d.BlobID, key)
	}

	nb, err := blobByID(ctx, tx, newBlobID)
	if err != nil {
		return nil, err
	}

	pkgID, err := ensureNPMPackage(ctx, tx, repoID, fullName, version)
	if err != nil {
		return nil, err
	}

	change := &Change{
		Regen: npmRegen(repoID, pkgID),
		Artifact: &ArtifactInfo{
			Name: fullName, Version: version, Path: d.TarballFilename, SHA256: d.BlobID, Size: nb.size,
		},
	}

	var (
		existingID string
		oldBlobID  string
	)

	err = tx.QueryRowContext(ctx,
		`SELECT id::text, blob_id::text FROM npm_versions
		  WHERE package_id = $1::uuid AND version = $2 AND deleted_at IS NULL FOR UPDATE`,
		pkgID, version).Scan(&existingID, &oldBlobID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return change, insertNPMVersion(ctx, tx, pkgID, version, newBlobID, &d)
	case err != nil:
		return nil, fmt.Errorf("lock npm version: %w", err)
	}

	old, err := blobByID(ctx, tx, oldBlobID)
	if err != nil {
		return nil, err
	}

	change.Replaced = true
	change.ReplacedSize = old.size
	change.ReplacedCount = 1

	if old.sha256 != nb.sha256 {
		change.ReleasedBlobs = []string{old.sha256}
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE npm_versions
		    SET manifest_json = $2::jsonb, integrity = $3, shasum = $4, tarball_filename = $5, blob_id = $6::uuid,
		        created_at = COALESCE($7::timestamptz, created_at)
		  WHERE id = $1::uuid`,
		existingID, string(d.ManifestJSON), d.Integrity, d.Shasum, d.TarballFilename, newBlobID, d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("update npm version: %w", err)
	}

	return change, nil
}

func insertNPMVersion(ctx context.Context, tx *sql.Tx, pkgID, version, blobID string, d *npmVersionData) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id, created_at)
		 VALUES ($1::uuid, $2, $3::jsonb, $4, $5, $6, $7::uuid, COALESCE($8::timestamptz, now()))`,
		pkgID, version, string(d.ManifestJSON), d.Integrity, d.Shasum, d.TarballFilename, blobID, d.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert npm version: %w", err)
	}

	return nil
}

// --- Delete -----------------------------------------------------------------

func (c npmCodec) Delete(ctx context.Context, tx *sql.Tx, repoID, key string, soft bool, deletedBy string) (*Change, error) {
	fullName, rest, isTag, ok := parseNPMKey(key)
	if !ok {
		return nil, npmKeyError(key)
	}

	if isTag {
		return c.deleteTag(ctx, tx, repoID, fullName, rest)
	}

	return c.deleteVersion(ctx, tx, repoID, fullName, rest, soft, deletedBy)
}

// deleteTag always removes the row: npm_dist_tags has no trash.
func (npmCodec) deleteTag(ctx context.Context, tx *sql.Tx, repoID, fullName, tag string) (*Change, error) {
	var pkgID string

	err := tx.QueryRowContext(ctx,
		`DELETE FROM npm_dist_tags d USING npm_packages p
		  WHERE p.id = d.package_id AND p.repository_id = $1::uuid AND p.full_name = $2 AND d.tag = $3
		RETURNING d.package_id::text`,
		repoID, fullName, tag).Scan(&pkgID)
	if errors.Is(err, sql.ErrNoRows) {
		return &Change{Missing: true}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("delete npm dist-tag: %w", err)
	}

	if err := markNPMStale(ctx, tx, pkgID); err != nil {
		return nil, err
	}

	return &Change{Regen: npmRegen(repoID, pkgID)}, nil
}

func (npmCodec) deleteVersion(ctx context.Context, tx *sql.Tx, repoID, fullName, version string, soft bool, deletedBy string) (*Change, error) {
	var (
		id, pkgID, filename, blobID string
	)

	err := tx.QueryRowContext(ctx,
		`SELECT v.id::text, p.id::text, v.tarball_filename, v.blob_id::text
		   FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id
		  WHERE p.repository_id = $1::uuid AND p.full_name = $2 AND v.version = $3 AND v.deleted_at IS NULL
		    FOR UPDATE OF v`,
		repoID, fullName, version).Scan(&id, &pkgID, &filename, &blobID)
	if errors.Is(err, sql.ErrNoRows) {
		return &Change{Missing: true}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("lock npm version: %w", err)
	}

	b, err := blobByID(ctx, tx, blobID)
	if err != nil {
		return nil, err
	}

	change := &Change{
		RemovedSize:  b.size,
		RemovedCount: 1,
		Regen:        npmRegen(repoID, pkgID),
		Artifact:     &ArtifactInfo{Name: fullName, Version: version, Path: filename, SHA256: b.sha256, Size: b.size},
	}

	if err := markNPMStale(ctx, tx, pkgID); err != nil {
		return nil, err
	}

	if soft {
		_, err = tx.ExecContext(ctx,
			`UPDATE npm_versions SET deleted_at = now(), deleted_by = $2 WHERE id = $1::uuid`, id, deletedBy)
		if err != nil {
			return nil, fmt.Errorf("trash npm version: %w", err)
		}

		return change, nil
	}

	if err := hardDeleteNPMVersion(ctx, tx, id, pkgID, version); err != nil {
		return nil, err
	}

	change.ReleasedBlobs = []string{b.sha256}

	return change, nil
}

// hardDeleteNPMVersion removes the version row and, as the permanent unpublish
// does, the dist-tags that point at it and the package row once no version row
// (trashed ones included) is left.
func hardDeleteNPMVersion(ctx context.Context, tx *sql.Tx, id, pkgID, version string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM npm_versions WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("delete npm version: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM npm_dist_tags WHERE package_id = $1::uuid AND version = $2`, pkgID, version); err != nil {
		return fmt.Errorf("delete npm dist-tags of version: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM npm_packages WHERE id = $1::uuid AND NOT EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = npm_packages.id)`,
		pkgID); err != nil {
		return fmt.Errorf("delete empty npm package: %w", err)
	}

	return nil
}

// --- Backfill ---------------------------------------------------------------

func (npmCodec) Backfill(ctx context.Context, tx *sql.Tx, repoID string) error {
	_, err := tx.ExecContext(ctx,
		`SELECT federation_record($1::uuid, p.full_name || '@' || t.version, $2, $3, federation_blob_sha(t.blob_id), false, t.updated_at)
		   FROM npm_versions t JOIN npm_packages p ON p.id = t.package_id
		  WHERE p.repository_id = $1::uuid AND t.deleted_at IS NULL`,
		repoID, KindArtifact, EventArtifactAdded)
	if err != nil {
		return fmt.Errorf("backfill npm_versions: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`SELECT federation_record($1::uuid, p.full_name || '#' || t.tag, $2, $3, t.version, false, t.updated_at)
		   FROM npm_dist_tags t JOIN npm_packages p ON p.id = t.package_id
		  WHERE p.repository_id = $1::uuid`,
		repoID, KindRef, EventRefMoved)
	if err != nil {
		return fmt.Errorf("backfill npm_dist_tags: %w", err)
	}

	return nil
}
