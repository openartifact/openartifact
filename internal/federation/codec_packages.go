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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	formatstorage "github.com/openartifact/openartifact/internal/format/storage"
)

// The generic package codec serves every federated format except docker: its
// units are rows of packages, package_versions, package_files (and the side
// tables of the format) and package_tags, found by package_files.unit_key and
// package_tags.unit_key. Per-format differences are the format's
// storage.Federation descriptor:
//
//	UnitVersion  a unit is a version with all its live files; the unit key is
//	             carried by the version's primary file
//	UnitFile     a unit is one file, with its package and version rows
//	Tags         package_tags rows with a unit_key are ref units
//
// Wire format (Content.Data): {package, version, files[], tag}. A file travels
// with its blob as a sha256 digest and the rows of the format's side tables
// (every column but id). Never replicated: ids, repository_id, trash state,
// local timestamps, download_count and the descriptor's LocalPackageAttrs.

type pkgWire struct {
	Name    string          `json:"name"`
	NameKey string          `json:"name_key"`
	Attrs   json.RawMessage `json:"attrs,omitempty"`
}

type versionWire struct {
	Version    string          `json:"version"`
	VersionKey string          `json:"version_key"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type fileWire struct {
	Path      string                     `json:"path"`
	Filename  string                     `json:"filename"`
	Blob      string                     `json:"blob,omitempty"`
	SizeBytes *int64                     `json:"size_bytes,omitempty"`
	Role      string                     `json:"role"`
	Listed    bool                       `json:"listed"`
	Arch      string                     `json:"arch,omitempty"`
	Part      string                     `json:"part,omitempty"`
	Attrs     json.RawMessage            `json:"attrs,omitempty"`
	Checksums json.RawMessage            `json:"checksums,omitempty"`
	Keyed     bool                       `json:"keyed,omitempty"`
	CreatedAt time.Time                  `json:"created_at"`
	Side      map[string]json.RawMessage `json:"side,omitempty"`
}

type tagWire struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
}

type packageUnit struct {
	Package pkgWire      `json:"package"`
	Version *versionWire `json:"version,omitempty"`
	Files   []fileWire   `json:"files,omitempty"`
	Tag     *tagWire     `json:"tag,omitempty"`
}

type packageCodec struct {
	format string
	fed    formatstorage.Federation
	sides  []formatstorage.SideTable
}

func newPackageCodec(format string, st formatstorage.Storage) *packageCodec {
	c := &packageCodec{format: format, fed: *st.Federation}
	c.sides = append(c.sides, st.SideTables...)

	return c
}

func (c *packageCodec) LockTables() []string {
	t := []string{"package_files", "package_versions"}
	if c.fed.Tags {
		t = append(t, "package_tags")
	}

	return t
}

// --- rows -------------------------------------------------------------------

type pkgRow struct {
	id, name, nameKey string
	attrs             json.RawMessage
}

type verRow struct {
	id, version, versionKey string
	metadata                json.RawMessage
	createdAt               time.Time
}

type fileRow struct {
	id, versionID, path, filename, role string
	blobID, sha                         string
	blobSize                            int64
	mediaType                           string
	sizeBytes                           *int64
	listed                              bool
	arch, part                          string
	attrs, checksums                    json.RawMessage
	unitKey                             string
	createdAt                           time.Time
}

type unitRows struct {
	pkg   pkgRow
	ver   verRow
	files []fileRow
}

const fileColumns = `f.id::text, f.version_id::text, f.path, f.filename, COALESCE(f.blob_id::text, ''),
	COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0), COALESCE(b.media_type, ''), f.size_bytes, f.role, f.listed,
	f.arch, f.part, f.attrs, f.checksums, COALESCE(f.unit_key, ''), f.created_at`

// loadFiles reads the package_files rows matching where (alias f; blob b).
func loadFiles(ctx context.Context, q Querier, where, suffix string, args ...any) ([]fileRow, error) {
	rows, err := q.QueryContext(ctx,

		`SELECT `+fileColumns+` FROM package_files f LEFT JOIN blobs b ON b.id = f.blob_id WHERE `+where+` ORDER BY f.path `+suffix,
		args...)
	if err != nil {
		return nil, fmt.Errorf("load files of unit: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []fileRow

	for rows.Next() {
		var (
			f    fileRow
			size sql.NullInt64
		)

		err := rows.Scan(&f.id, &f.versionID, &f.path, &f.filename, &f.blobID, &f.sha, &f.blobSize, &f.mediaType, &size,
			&f.role, &f.listed, &f.arch, &f.part, &f.attrs, &f.checksums, &f.unitKey, &f.createdAt)
		if err != nil {
			return nil, fmt.Errorf("scan file of unit: %w", err)
		}

		if size.Valid {
			f.sizeBytes = &size.Int64
		}

		out = append(out, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load files of unit: %w", err)
	}

	return out, nil
}

// loadUnit finds the live artifact unit of key: its package, version and files
// (the whole version for UnitVersion, the keyed file for UnitFile). nil when
// the key has no live file.
func (c *packageCodec) loadUnit(ctx context.Context, q Querier, repoID, key string, lock bool) (*unitRows, error) {
	query := `SELECT p.id::text, p.name, p.name_key, p.attrs, v.id::text, v.version, v.version_key, v.metadata, v.created_at, f.id::text
	  FROM package_files f
	  JOIN package_versions v ON v.id = f.version_id AND v.deleted_at IS NULL
	  JOIN packages p ON p.id = v.package_id
	 WHERE f.repository_id = $1::uuid AND f.unit_key = $2 AND f.deleted_at IS NULL LIMIT 1`
	if lock {
		query += ` FOR UPDATE OF f, v`
	}

	var (
		u       unitRows
		keyedID string
	)

	err := q.QueryRowContext(ctx, query, repoID, key).Scan(&u.pkg.id, &u.pkg.name, &u.pkg.nameKey, &u.pkg.attrs,
		&u.ver.id, &u.ver.version, &u.ver.versionKey, &u.ver.metadata, &u.ver.createdAt, &keyedID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no live unit is a result, not an error
	}

	if err != nil {
		return nil, fmt.Errorf("load unit %s: %w", key, err)
	}

	if c.fed.Unit == formatstorage.UnitFile {
		u.files, err = loadFiles(ctx, q, `f.id = $1::uuid AND f.deleted_at IS NULL`, "", keyedID)
	} else {
		u.files, err = loadFiles(ctx, q, `f.version_id = $1::uuid AND f.deleted_at IS NULL`, "", u.ver.id)
	}

	if err != nil {
		return nil, err
	}

	return &u, nil
}

func countOf(role string) int {
	if role == "primary" {
		return 1
	}

	return 0
}

// stripAttrs removes the instance-local keys from a package's attrs.
func stripAttrs(raw json.RawMessage, local []string) json.RawMessage {
	attrs := map[string]json.RawMessage{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &attrs)
	}

	for _, k := range local {
		delete(attrs, k)
	}

	out, _ := json.Marshal(attrs) //nolint:errchkjson // a map of raw messages always marshals

	return out
}

// rawObject is the JSON object of a wire value, {} when absent.
func rawObject(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}

	return string(raw)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}

	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}

	return t
}

// --- Export -----------------------------------------------------------------

func (c *packageCodec) Export(ctx context.Context, q Querier, repoID, key string) (*Content, error) {
	u, err := c.loadUnit(ctx, q, repoID, key, false)
	if err != nil {
		return nil, err
	}

	if u != nil {
		return c.exportArtifact(ctx, q, key, u)
	}

	if c.fed.Tags {
		return c.exportTag(ctx, q, repoID, key)
	}

	return nil, nil //nolint:nilnil // an unknown unit has no content
}

func (c *packageCodec) exportArtifact(ctx context.Context, q Querier, key string, u *unitRows) (*Content, error) {
	unit := packageUnit{
		Package: pkgWire{Name: u.pkg.name, NameKey: u.pkg.nameKey, Attrs: stripAttrs(u.pkg.attrs, c.fed.LocalPackageAttrs)},
		Version: &versionWire{Version: u.ver.version, VersionKey: u.ver.versionKey, Metadata: u.ver.metadata, CreatedAt: u.ver.createdAt},
	}

	ids := make([]string, 0, len(u.files))
	for _, f := range u.files {
		ids = append(ids, f.id)
	}

	sides, err := c.loadSides(ctx, q, ids)
	if err != nil {
		return nil, err
	}

	content := &Content{Kind: KindArtifact}
	seen := map[string]bool{}

	for _, f := range u.files {
		keyed := f.unitKey == key

		unit.Files = append(unit.Files, fileWire{
			Path: f.path, Filename: f.filename, Blob: f.sha, SizeBytes: f.sizeBytes, Role: f.role, Listed: f.listed,
			Arch: f.arch, Part: f.part, Attrs: f.attrs, Checksums: f.checksums, Keyed: keyed, CreatedAt: f.createdAt,
			Side: sides[f.id],
		})

		if f.sha != "" && !seen[f.sha] {
			seen[f.sha] = true
			content.Blobs = append(content.Blobs, BlobRef{SHA256: f.sha, Size: f.blobSize, MediaType: f.mediaType})
		}

		if keyed {
			content.ContentRef = f.sha
		}
	}

	content.Data, err = json.Marshal(unit)
	if err != nil {
		return nil, fmt.Errorf("encode unit %s: %w", key, err)
	}

	return content, nil
}

// loadSides reads the side-table rows of the files: file id -> table -> row
// without its id.
func (c *packageCodec) loadSides(ctx context.Context, q Querier, ids []string) (map[string]map[string]json.RawMessage, error) {
	out := map[string]map[string]json.RawMessage{}

	for _, side := range c.sides {
		if err := loadSideTable(ctx, q, side.Name, ids, out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// loadSideTable adds the rows of one side table of the files to out.
func loadSideTable(ctx context.Context, q Querier, table string, ids []string, out map[string]map[string]json.RawMessage) error {
	rows, err := q.QueryContext(ctx,

		`SELECT s.id::text, to_jsonb(s) - 'id' FROM `+pgx.Identifier{table}.Sanitize()+` s WHERE s.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("load %s rows: %w", table, err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id  string
			row json.RawMessage
		)

		if err := rows.Scan(&id, &row); err != nil {
			return fmt.Errorf("scan %s row: %w", table, err)
		}

		if out[id] == nil {
			out[id] = map[string]json.RawMessage{}
		}

		out[id][table] = row
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("load %s rows: %w", table, err)
	}

	return nil
}

func (c *packageCodec) exportTag(ctx context.Context, q Querier, repoID, key string) (*Content, error) {
	var (
		p   pkgRow
		tag tagWire
	)

	err := q.QueryRowContext(ctx,
		`SELECT p.id::text, p.name, p.name_key, p.attrs, t.tag, t.version
		   FROM package_tags t JOIN packages p ON p.id = t.package_id
		  WHERE p.repository_id = $1::uuid AND t.unit_key = $2`, repoID, key).
		Scan(&p.id, &p.name, &p.nameKey, &p.attrs, &tag.Tag, &tag.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // an unknown unit has no content
	}

	if err != nil {
		return nil, fmt.Errorf("load tag unit %s: %w", key, err)
	}

	content := &Content{Kind: KindRef, ContentRef: tag.Version}

	var dep string

	err = q.QueryRowContext(ctx,
		`SELECT f.unit_key FROM package_versions v JOIN package_files f ON f.version_id = v.id
		  WHERE v.package_id = $1::uuid AND v.version = $2 AND v.deleted_at IS NULL
		    AND f.deleted_at IS NULL AND f.unit_key IS NOT NULL LIMIT 1`, p.id, tag.Version).Scan(&dep)

	switch {
	case err == nil:
		content.Deps = []string{dep}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("resolve version of tag %s: %w", key, err)
	}

	content.Data, err = json.Marshal(packageUnit{
		Package: pkgWire{Name: p.name, NameKey: p.nameKey, Attrs: stripAttrs(p.attrs, c.fed.LocalPackageAttrs)},
		Tag:     &tag,
	})
	if err != nil {
		return nil, fmt.Errorf("encode tag unit %s: %w", key, err)
	}

	return content, nil
}

// --- Usage ------------------------------------------------------------------

func (c *packageCodec) Usage(ctx context.Context, q Querier, repoID, _ string, content *Content) ([]Reservation, error) {
	if content.Kind != KindArtifact {
		return nil, nil
	}

	var u packageUnit
	if err := json.Unmarshal(content.Data, &u); err != nil {
		return nil, fmt.Errorf("%w: decode unit data: %v", ErrInvalid, err) //nolint:errorlint // client-safe text only
	}

	paths := make([]string, 0, len(u.Files))
	for _, f := range u.Files {
		paths = append(paths, f.Path)
	}

	held := map[string]string{}

	rows, err := q.QueryContext(ctx,
		`SELECT f.path, COALESCE(b.sha256, '') FROM package_files f LEFT JOIN blobs b ON b.id = f.blob_id
		  WHERE f.repository_id = $1::uuid AND f.deleted_at IS NULL AND f.path = ANY($2::text[])`, repoID, paths)
	if err != nil {
		return nil, fmt.Errorf("read held files: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var path, sha string
		if err := rows.Scan(&path, &sha); err != nil {
			return nil, fmt.Errorf("scan held file: %w", err)
		}

		held[path] = sha
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read held files: %w", err)
	}

	sizes := map[string]int64{}
	for _, b := range content.Blobs {
		sizes[b.SHA256] = b.Size
	}

	var out []Reservation

	for _, f := range u.Files {
		if sha, ok := held[f.Path]; ok && sha == f.Blob {
			continue
		}

		if r := (Reservation{Size: sizes[f.Blob], Count: countOf(f.Role)}); r.Size > 0 || r.Count > 0 {
			out = append(out, r)
		}
	}

	return out, nil
}

// --- Import -----------------------------------------------------------------

func (c *packageCodec) Import(
	ctx context.Context,
	tx *sql.Tx,
	repoID, key string,
	content *Content,
	blobIDs map[string]string,
) (*Change, error) {
	var u packageUnit
	if err := json.Unmarshal(content.Data, &u); err != nil {
		return nil, fmt.Errorf("%w: decode unit data: %v", ErrInvalid, err) //nolint:errorlint // client-safe text only
	}

	if u.Package.NameKey == "" {
		return nil, fmt.Errorf("%w: unit %s has no package", ErrInvalid, key)
	}

	if content.Kind == KindRef {
		return c.importTag(ctx, tx, repoID, key, &u)
	}

	return c.importArtifact(ctx, tx, repoID, key, &u, blobIDs)
}

func (c *packageCodec) importTag(ctx context.Context, tx *sql.Tx, repoID, key string, u *packageUnit) (*Change, error) {
	if !c.fed.Tags || u.Tag == nil || u.Tag.Tag == "" || u.Tag.Version == "" {
		return nil, fmt.Errorf("%w: unit %s is not a valid tag", ErrInvalid, key)
	}

	pkgID, err := c.ensurePackage(ctx, tx, repoID, u.Package)
	if err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO package_tags (package_id, tag, version, unit_key) VALUES ($1::uuid, $2, $3, $4)
		 ON CONFLICT (package_id, tag) DO UPDATE SET version = EXCLUDED.version, unit_key = EXCLUDED.unit_key
		 WHERE (package_tags.version, package_tags.unit_key) IS DISTINCT FROM (EXCLUDED.version, EXCLUDED.unit_key)`,
		pkgID, u.Tag.Tag, u.Tag.Version, key)
	if err != nil {
		return nil, fmt.Errorf("upsert tag %s: %w", key, err)
	}

	if err := c.touchPackage(ctx, tx, pkgID); err != nil {
		return nil, err
	}

	return &Change{Regen: c.regen(repoID, pkgID, u.Package)}, nil
}

func (c *packageCodec) regen(repoID, pkgID string, p pkgWire) []Regen {
	return []Regen{{Format: c.format, RepoID: repoID, PackageID: pkgID, Name: p.Name, Attrs: p.Attrs}}
}

// touchPackage bumps the package's updated_at (the validator some formats use
// as an ETag) and sets the format's stale marker, when the descriptor asks.
func (c *packageCodec) touchPackage(ctx context.Context, tx *sql.Tx, pkgID string) error {
	if !c.fed.TouchPackage && c.fed.StaleAttr == "" {
		return nil
	}

	_, err := tx.ExecContext(ctx,
		`UPDATE packages SET updated_at = now(),
		        attrs = CASE WHEN $2::text = '' THEN attrs ELSE jsonb_set(attrs, ARRAY[$2::text], 'true'::jsonb) END
		  WHERE id = $1::uuid`, pkgID, c.fed.StaleAttr)
	if err != nil {
		return fmt.Errorf("touch package: %w", err)
	}

	return nil
}

// ensurePackage finds the package of the repository by name_key or creates it
// (without the instance-local attrs); an existing package is never changed.
func (c *packageCodec) ensurePackage(ctx context.Context, tx *sql.Tx, repoID string, p pkgWire) (string, error) {
	find := func() (string, error) {
		var id string

		err := tx.QueryRowContext(ctx,
			`SELECT id::text FROM packages WHERE repository_id = $1::uuid AND name_key = $2`, repoID, p.NameKey).Scan(&id)

		return id, err //nolint:wrapcheck // the callers distinguish sql.ErrNoRows
	}

	id, err := find()
	if err == nil {
		return id, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("find package %s: %w", p.NameKey, err)
	}

	err = tx.QueryRowContext(ctx,
		`INSERT INTO packages (repository_id, format, name, name_key, attrs) VALUES ($1::uuid, $2, $3, $4, $5::jsonb)
		 ON CONFLICT (repository_id, name_key) DO NOTHING RETURNING id::text`,
		repoID, c.format, p.Name, p.NameKey, string(stripAttrs(p.Attrs, c.fed.LocalPackageAttrs))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) { // created concurrently
		id, err = find()
	}

	if err != nil {
		return "", fmt.Errorf("create package %s: %w", p.NameKey, err)
	}

	return id, nil
}

// ensureVersion finds the live version of the package by version_key or creates
// it; the metadata of an existing one follows the incoming unit, its created_at
// only when syncCreated is set.
func (c *packageCodec) ensureVersion(
	ctx context.Context,
	tx *sql.Tx,
	repoID, pkgID string,
	v *versionWire,
	syncCreated bool,
) (string, error) {
	var id string

	err := tx.QueryRowContext(ctx,
		`SELECT id::text FROM package_versions WHERE package_id = $1::uuid AND version_key = $2 AND deleted_at IS NULL FOR UPDATE`,
		pkgID, v.VersionKey).Scan(&id)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = tx.QueryRowContext(ctx,
			`INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata, created_at)
			 VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, COALESCE($6::timestamptz, now())) RETURNING id::text`,
			pkgID, repoID, v.Version, v.VersionKey, rawObject(v.Metadata), nullTime(v.CreatedAt)).Scan(&id)
		if err != nil {
			return "", fmt.Errorf("create version %s: %w", v.VersionKey, err)
		}

		return id, nil
	case err != nil:
		return "", fmt.Errorf("find version %s: %w", v.VersionKey, err)
	}

	return id, c.syncVersion(ctx, tx, id, v, syncCreated)
}

func (c *packageCodec) syncVersion(ctx context.Context, tx *sql.Tx, id string, v *versionWire, syncCreated bool) error {
	created := nullTime(v.CreatedAt)
	if !syncCreated {
		created = nil
	}

	_, err := tx.ExecContext(ctx,
		`UPDATE package_versions SET metadata = $2::jsonb, created_at = COALESCE($3::timestamptz, created_at)
		  WHERE id = $1::uuid AND (metadata, created_at) IS DISTINCT FROM ($2::jsonb, COALESCE($3::timestamptz, created_at))`,
		id, rawObject(v.Metadata), created)
	if err != nil {
		return fmt.Errorf("update version: %w", err)
	}

	return nil
}

type importTarget struct {
	pkgID, verID string
	existing     []fileRow
}

func (c *packageCodec) importArtifact(
	ctx context.Context,
	tx *sql.Tx,
	repoID, key string,
	u *packageUnit,
	blobIDs map[string]string,
) (*Change, error) {
	if err := c.validateUnit(key, u, blobIDs); err != nil {
		return nil, err
	}

	var (
		t   importTarget
		err error
	)

	if c.fed.Unit == formatstorage.UnitFile {
		t, err = c.locateFile(ctx, tx, repoID, key, u)
	} else {
		t, err = c.locateVersion(ctx, tx, repoID, u)
	}

	if err != nil {
		return nil, err
	}

	change, err := c.reconcileFiles(ctx, tx, repoID, key, t, u, blobIDs)
	if err != nil {
		return nil, err
	}

	if err := c.touchPackage(ctx, tx, t.pkgID); err != nil {
		return nil, err
	}

	change.Regen = c.regen(repoID, t.pkgID, u.Package)
	change.Artifact = c.artifactInfo(u, change)

	return change, nil
}

// validateUnit checks the shape of an incoming artifact unit: a version, files
// with paths whose blobs were provided, and exactly one keyed file.
func (c *packageCodec) validateUnit(key string, u *packageUnit, blobIDs map[string]string) error {
	if u.Version == nil || len(u.Files) == 0 {
		return fmt.Errorf("%w: unit %s has no version or files", ErrInvalid, key)
	}

	keyed := 0

	for _, f := range u.Files {
		if f.Path == "" {
			return fmt.Errorf("%w: unit %s has a file without a path", ErrInvalid, key)
		}

		if f.Keyed {
			keyed++
		}

		if _, ok := blobIDs[f.Blob]; f.Blob != "" && !ok {
			return fmt.Errorf("%w: blob %s of unit %s was not provided", ErrInvalid, f.Blob, key)
		}
	}

	if keyed != 1 || (c.fed.Unit == formatstorage.UnitFile && len(u.Files) != 1) {
		return fmt.Errorf("%w: unit %s must carry exactly one keyed file", ErrInvalid, key)
	}

	return nil
}

// reconcileFiles writes the unit's files over the target's existing ones by
// path and books what the replacement releases; a version unit also removes the
// files the incoming version no longer has.
func (c *packageCodec) reconcileFiles(
	ctx context.Context,
	tx *sql.Tx,
	repoID, key string,
	t importTarget,
	u *packageUnit,
	blobIDs map[string]string,
) (*Change, error) {
	change := &Change{Replaced: len(t.existing) > 0}
	byPath := map[string]*fileRow{}

	for i := range t.existing {
		byPath[t.existing[i].path] = &t.existing[i]
	}

	for i := range u.Files {
		wf := &u.Files[i]

		id, err := c.writeFile(ctx, tx, repoID, key, t.verID, byPath[wf.Path], wf, blobIDs)
		if err != nil {
			return nil, err
		}

		if old := byPath[wf.Path]; old != nil {
			delete(byPath, wf.Path)

			if old.sha != wf.Blob {
				releaseOld(change, old)
			}
		}

		if err := c.writeSides(ctx, tx, repoID, id, wf.Side); err != nil {
			return nil, err
		}
	}

	if c.fed.Unit != formatstorage.UnitVersion {
		return change, nil
	}

	var extra []string

	for _, old := range byPath {
		extra = append(extra, old.id)
		releaseOld(change, old)
	}

	if len(extra) > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM package_files WHERE id = ANY($1::uuid[])`, extra); err != nil {
			return nil, fmt.Errorf("remove replaced files: %w", err)
		}
	}

	return change, nil
}

func (c *packageCodec) artifactInfo(u *packageUnit, _ *Change) *ArtifactInfo {
	for _, f := range u.Files {
		if f.Keyed {
			info := &ArtifactInfo{Name: u.Package.Name, Version: u.Version.Version, Path: f.Path, SHA256: f.Blob}
			if f.SizeBytes != nil {
				info.Size = *f.SizeBytes
			}

			return info
		}
	}

	return nil
}

// releaseOld books the usage and blob of a replaced or removed file.
func releaseOld(ch *Change, old *fileRow) {
	ch.ReplacedSize += old.blobSize
	ch.ReplacedCount += countOf(old.role)

	if old.sha != "" {
		ch.ReleasedBlobs = append(ch.ReleasedBlobs, old.sha)
	}
}

// locateVersion finds or creates the package and version of a version unit and
// locks the live files of the version.
func (c *packageCodec) locateVersion(ctx context.Context, tx *sql.Tx, repoID string, u *packageUnit) (importTarget, error) {
	pkgID, err := c.ensurePackage(ctx, tx, repoID, u.Package)
	if err != nil {
		return importTarget{}, err
	}

	verID, err := c.ensureVersion(ctx, tx, repoID, pkgID, u.Version, true)
	if err != nil {
		return importTarget{}, err
	}

	files, err := loadFiles(ctx, tx, `f.version_id = $1::uuid AND f.deleted_at IS NULL`, `FOR UPDATE OF f`, verID)
	if err != nil {
		return importTarget{}, err
	}

	return importTarget{pkgID: pkgID, verID: verID, existing: files}, nil
}

// locateFile finds the live file of a file unit by its unit key, else by its
// path, and its version; without one it finds or creates the version.
func (c *packageCodec) locateFile(ctx context.Context, tx *sql.Tx, repoID, key string, u *packageUnit) (importTarget, error) {
	files, err := loadFiles(
		ctx,
		tx,
		`f.repository_id = $1::uuid AND f.deleted_at IS NULL AND f.unit_key = $2`,
		`FOR UPDATE OF f`,
		repoID,
		key,
	)
	if err != nil {
		return importTarget{}, err
	}

	if len(files) == 0 {
		files, err = loadFiles(
			ctx,
			tx,
			`f.repository_id = $1::uuid AND f.deleted_at IS NULL AND f.path = $2`,
			`FOR UPDATE OF f`,
			repoID,
			u.Files[0].Path,
		)
		if err != nil {
			return importTarget{}, err
		}
	}

	if len(files) > 0 {
		t := importTarget{verID: files[0].versionID, existing: files[:1]}

		err := tx.QueryRowContext(ctx, `SELECT package_id::text FROM package_versions WHERE id = $1::uuid`, t.verID).Scan(&t.pkgID)
		if err != nil {
			return importTarget{}, fmt.Errorf("find package of unit %s: %w", key, err)
		}

		return t, c.syncVersion(ctx, tx, t.verID, u.Version, false)
	}

	pkgID, err := c.ensurePackage(ctx, tx, repoID, u.Package)
	if err != nil {
		return importTarget{}, err
	}

	verID, err := c.ensureVersion(ctx, tx, repoID, pkgID, u.Version, false)
	if err != nil {
		return importTarget{}, err
	}

	return importTarget{pkgID: pkgID, verID: verID}, nil
}

// writeFile inserts the file, or updates the existing one when it differs.
func (c *packageCodec) writeFile(
	ctx context.Context,
	tx *sql.Tx,
	repoID, key, verID string,
	old *fileRow,
	wf *fileWire,
	blobIDs map[string]string,
) (string, error) {
	unitKey := any(nil)
	if wf.Keyed {
		unitKey = key
	}

	args := []any{
		wf.Path, wf.Filename, nullStr(blobIDs[wf.Blob]), wf.SizeBytes, wf.Role, wf.Listed, wf.Arch, wf.Part,
		rawObject(wf.Attrs), rawObject(wf.Checksums), unitKey,
	}

	if old == nil {
		var id string

		err := tx.QueryRowContext(ctx,
			`INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, size_bytes, role, listed, arch, part,
			                            attrs, checksums, unit_key, created_at)
			 VALUES ($1::uuid, $2::uuid, $3::text, $4::text, $5::uuid, $6::bigint, $7::text, $8::boolean, $9::text, $10::text,
			         $11::jsonb, $12::jsonb, $13::text, COALESCE($14::timestamptz, now()))
			 RETURNING id::text`,
			append([]any{verID, repoID}, append(args, nullTime(wf.CreatedAt))...)...).Scan(&id)
		if err != nil {
			return "", fmt.Errorf("insert file %s: %w", wf.Path, err)
		}

		return id, nil
	}

	_, err := tx.ExecContext(ctx,
		`UPDATE package_files SET path = $2::text, filename = $3::text, blob_id = $4::uuid, size_bytes = $5::bigint, role = $6::text,
		        listed = $7::boolean, arch = $8::text, part = $9::text, attrs = $10::jsonb, checksums = $11::jsonb, unit_key = $12::text
		  WHERE id = $1::uuid AND (path, filename, blob_id, size_bytes, role, listed, arch, part, attrs, checksums, unit_key)
		        IS DISTINCT FROM ($2::text, $3::text, $4::uuid, $5::bigint, $6::text, $7::boolean, $8::text, $9::text,
		                          $10::jsonb, $11::jsonb, $12::text)`,
		append([]any{old.id}, args...)...)
	if err != nil {
		return "", fmt.Errorf("update file %s: %w", wf.Path, err)
	}

	return old.id, nil
}

// writeSides replaces the side-table rows of a file with the incoming ones.
//
// The row is the peer's JSON, so it chooses every column of it. The columns
// listed in SideTable.Refs point at rows of other tables (ids that only mean
// something on the instance that wrote them), and are accepted only when a file
// of this repository already uses that very value: a peer must not be able to
// attach its file to the row of another repository.
func (c *packageCodec) writeSides(ctx context.Context, tx *sql.Tx, repoID, fileID string, rows map[string]json.RawMessage) error {
	for _, side := range c.sides {
		row, ok := rows[side.Name]
		if !ok {
			continue
		}

		ident := pgx.Identifier{side.Name}.Sanitize()

		if err := checkSideRefs(ctx, tx, repoID, ident, side, row); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM `+ident+` WHERE id = $1::uuid`, fileID); err != nil { //nolint:gosec // quoted name
			return fmt.Errorf("replace %s row: %w", side.Name, err)
		}

		_, err := tx.ExecContext(ctx,
			//nolint:gosec // quoted name
			`INSERT INTO `+ident+` SELECT (jsonb_populate_record(NULL::`+ident+`, $1::jsonb || jsonb_build_object('id', $2::text))).*`,
			string(row), fileID)
		if err != nil {
			return fmt.Errorf("write %s row: %w", side.Name, err)
		}
	}

	return nil
}

// checkSideRefs verifies the reference columns of a side-table row (see
// writeSides): each must be a uuid that a file of the repository already
// carries in the same column.
func checkSideRefs(ctx context.Context, tx *sql.Tx, repoID, ident string, side formatstorage.SideTable, row json.RawMessage) error {
	if len(side.Refs) == 0 {
		return nil
	}

	var cols map[string]json.RawMessage
	if err := json.Unmarshal(row, &cols); err != nil {
		return fmt.Errorf("%w: %s row is not an object", ErrInvalid, side.Name)
	}

	for _, col := range side.Refs {
		var ref string
		if err := json.Unmarshal(cols[col], &ref); err != nil {
			return fmt.Errorf("%w: %s.%s is not an id", ErrInvalid, side.Name, col)
		}

		if _, err := uuid.Parse(ref); err != nil {
			return fmt.Errorf("%w: %s.%s is not an id", ErrInvalid, side.Name, col)
		}

		var owned bool

		err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM `+ident+` s JOIN package_files f ON f.id = s.id
			                 WHERE f.repository_id = $1::uuid AND s.`+pgx.Identifier{col}.Sanitize()+` = $2::uuid)`,
			repoID, ref).Scan(&owned)
		if err != nil {
			return fmt.Errorf("check %s.%s: %w", side.Name, col, err)
		}

		if !owned {
			return fmt.Errorf("%w: %s.%s does not belong to this repository", ErrInvalid, side.Name, col)
		}
	}

	return nil
}

// --- Delete -----------------------------------------------------------------

func (c *packageCodec) Delete(ctx context.Context, tx *sql.Tx, repoID, key string, soft bool, deletedBy string) (*Change, error) {
	u, err := c.loadUnit(ctx, tx, repoID, key, true)
	if err != nil {
		return nil, err
	}

	if u == nil {
		if c.fed.Tags {
			return c.deleteTag(ctx, tx, repoID, key)
		}

		return &Change{Missing: true}, nil
	}

	change := &Change{}
	ids := make([]string, 0, len(u.files))
	attrs := u.pkg.attrs

	for i := range u.files {
		f := &u.files[i]
		ids = append(ids, f.id)
		change.RemovedSize += f.blobSize
		change.RemovedCount += countOf(f.role)

		if f.unitKey == key {
			change.Artifact = &ArtifactInfo{Name: u.pkg.name, Version: u.ver.version, Path: f.path, SHA256: f.sha, Size: f.blobSize}
		}
	}

	change.Regen = c.regen(repoID, u.pkg.id, pkgWire{Name: u.pkg.name, Attrs: attrs})

	if err := c.touchPackage(ctx, tx, u.pkg.id); err != nil {
		return nil, err
	}

	if soft {
		return change, c.trash(ctx, tx, u, ids, deletedBy)
	}

	for i := range u.files {
		if u.files[i].sha != "" {
			change.ReleasedBlobs = append(change.ReleasedBlobs, u.files[i].sha)
		}
	}

	return change, c.purge(ctx, tx, u, ids)
}

func (c *packageCodec) trash(ctx context.Context, tx *sql.Tx, u *unitRows, ids []string, by string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE package_files SET deleted_at = now(), deleted_by = $2 WHERE id = ANY($1::uuid[])`, ids, by); err != nil {
		return fmt.Errorf("trash files: %w", err)
	}

	// A version is trashed with its last live file.
	_, err := tx.ExecContext(ctx,
		`UPDATE package_versions v SET deleted_at = now(), deleted_by = $2
		  WHERE v.id = $1::uuid AND NOT EXISTS (SELECT 1 FROM package_files f WHERE f.version_id = v.id AND f.deleted_at IS NULL)`,
		u.ver.id, by)
	if err != nil {
		return fmt.Errorf("trash version: %w", err)
	}

	return nil
}

// purge removes the files and, once nothing is left of them, the version (with
// the tags that name it) and the package.
func (c *packageCodec) purge(ctx context.Context, tx *sql.Tx, u *unitRows, ids []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM package_files WHERE id = ANY($1::uuid[])`, ids); err != nil {
		return fmt.Errorf("delete files: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`DELETE FROM package_versions v WHERE v.id = $1::uuid AND NOT EXISTS (SELECT 1 FROM package_files f WHERE f.version_id = v.id)`,
		u.ver.id)
	if err != nil {
		return fmt.Errorf("delete version: %w", err)
	}

	if n, _ := res.RowsAffected(); n > 0 && c.fed.Tags {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM package_tags WHERE package_id = $1::uuid AND version = $2`, u.pkg.id, u.ver.version); err != nil {
			return fmt.Errorf("delete tags of version: %w", err)
		}
	}

	_, err = tx.ExecContext(ctx,
		`DELETE FROM packages p WHERE p.id = $1::uuid AND NOT EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id)`,
		u.pkg.id)
	if err != nil {
		return fmt.Errorf("delete empty package: %w", err)
	}

	return nil
}

// deleteTag always removes the row: tags have no trash.
func (c *packageCodec) deleteTag(ctx context.Context, tx *sql.Tx, repoID, key string) (*Change, error) {
	var (
		p     pkgRow
		pkgID string
	)

	err := tx.QueryRowContext(ctx,
		`DELETE FROM package_tags t USING packages p
		  WHERE p.id = t.package_id AND p.repository_id = $1::uuid AND t.unit_key = $2
		RETURNING p.id::text, p.name, p.attrs`, repoID, key).Scan(&pkgID, &p.name, &p.attrs)
	if errors.Is(err, sql.ErrNoRows) {
		return &Change{Missing: true}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("delete tag %s: %w", key, err)
	}

	if err := c.touchPackage(ctx, tx, pkgID); err != nil {
		return nil, err
	}

	return &Change{Regen: c.regen(repoID, pkgID, pkgWire{Name: p.name, Attrs: p.attrs})}, nil
}

// --- Backfill ---------------------------------------------------------------

func (c *packageCodec) Backfill(ctx context.Context, tx *sql.Tx, repoID string) error {
	sources := []backfillSource{{
		table: "package_files",
		kind:  KindArtifact,
		key:   "t.unit_key",
		ref:   "federation_blob_sha(t.blob_id)",
		stamp: "GREATEST(t.updated_at, v.updated_at)",
		from: "package_files t JOIN package_versions v ON v.id = t.version_id" +
			" WHERE t.repository_id = $1::uuid AND t.deleted_at IS NULL AND v.deleted_at IS NULL AND t.unit_key IS NOT NULL",
	}}

	if c.fed.Tags {
		sources = append(sources, backfillSource{
			table: "package_tags",
			kind:  KindRef,
			key:   "t.unit_key",
			ref:   "t.version",
			from:  "package_tags t JOIN packages p ON p.id = t.package_id WHERE p.repository_id = $1::uuid AND t.unit_key IS NOT NULL",
		})
	}

	return backfillUnits(ctx, tx, repoID, sources...)
}
