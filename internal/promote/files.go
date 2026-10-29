// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// rolePrimary is the package_files.role of the files that count as artifacts
// (the quota counts them, the report lists them).
const rolePrimary = "primary"

// filePromoter copies the files of one format from a source repository to a
// destination: it is driven by the format's storage.Promotion and works on
// packages, package_versions, package_files, package_tags and the side tables
// the descriptor lists, whatever the format.
//
// Every SQL string is assembled from compile-time constants (the descriptor's
// fragments, validated table names); request values are bind parameters.
type filePromoter struct {
	format string
	p      storage.Promotion
	sides  []storage.SideTable
}

// srcFile is a live source file of the promotion: the file, its version and
// its package.
type srcFile struct {
	id, versionID, packageID string
	pkg, version             string
	path, filename, role     string
	sha                      string
	size                     int64
	licenses                 []string
	// matched is set on the files that hold the item's content, as opposed to
	// the siblings a WholeVersion copy brings along.
	matched bool
}

// reported says whether the file is listed in the promotion's report: the
// artifacts of the item, not their companions.
func (f srcFile) reported() bool { return f.role == rolePrimary || f.matched }

func (f srcFile) artifact() Artifact {
	return Artifact{Name: f.pkg, Version: f.version, Path: f.path, SHA256: f.sha, Size: f.size}
}

// occupant is a live destination file that stands in the way of a source file.
type occupant struct {
	id, path, sha string
	size          int64
	pkg, version  string
}

// destVersion is a destination version a copy wrote to, with the source
// version it came from.
type destVersion struct {
	packageID, versionID string
	srcPackageID         string
	srcVersion           string
}

// newFilePromoter validates the descriptor's table names; they are spliced
// into SQL.
func newFilePromoter(format string, d storage.Storage) (*filePromoter, error) {
	for _, t := range d.SideTables {
		if !plainIdent(t.Name) {
			return nil, fmt.Errorf("%w: side table %q of format %s", ErrUnsupportedFormat, t.Name, format)
		}
	}

	return &filePromoter{format: format, p: d.Promotion, sides: d.SideTables}, nil
}

// plainIdent reports whether s is a plain lower-case SQL identifier.
func plainIdent(s string) bool {
	if s == "" {
		return false
	}

	for i, c := range s {
		switch {
		case c == '_' || (c >= 'a' && c <= 'z'):
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}

	return true
}

// promote implements the copy of one item: the source files, what the
// destination holds in their way, the replaced files of a forced promotion and
// the rows of the copy.
func (fp *filePromoter) promote(ctx context.Context, tx Tx, req Request) (*Result, error) {
	files, err := fp.sourceFiles(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	res := &Result{}

	for _, f := range files {
		res.QuarantineKeys = append(res.QuarantineKeys, nonEmpty(f.id, f.path, f.filename)...)

		for _, stmt := range fp.p.Prepare {
			if _, err := tx.ExecContext(ctx, stmt, stmtArgs(stmt, req.Dst.ID, f.id)...); err != nil {
				return nil, fmt.Errorf("prepare the target for %s: %w", f.path, err)
			}
		}
	}

	toCopy, err := fp.settle(ctx, tx, req, files, res)
	if err != nil {
		return nil, err
	}

	if err := fp.copyFiles(ctx, tx, req, toCopy, res); err != nil {
		return nil, err
	}

	return res, nil
}

// sourceFileSelect is the SELECT of srcFile columns over the aliases s (the
// file), sv and sp; where is a constant predicate on them. $1 is a placeholder
// the callers bind.
func (fp *filePromoter) sourceFileSelect(where string) string {
	licenses := `'[]'::jsonb`
	if fp.p.Licenses != "" {
		licenses = `COALESCE(to_jsonb(` + fp.p.Licenses + `), '[]'::jsonb)`
	}

	return `SELECT s.id::text, s.version_id::text, sv.package_id::text, sp.name, sv.version, s.path, s.filename, s.role,
	       COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0), ` + licenses + `
	  FROM package_files s
	  JOIN package_versions sv ON sv.id = s.version_id
	  JOIN packages sp ON sp.id = sv.package_id
	  LEFT JOIN blobs b ON b.id = s.blob_id
	 WHERE s.repository_id = $1::uuid AND s.deleted_at IS NULL AND ` + where + `
	 ORDER BY s.created_at, s.id`
}

// sourceFiles returns the live source files holding the item's content,
// narrowed to the one req.Path names when it names one of them; with
// WholeVersion, with every live file of their versions too.
func (fp *filePromoter) sourceFiles(ctx context.Context, tx Tx, req Request) ([]srcFile, error) {
	files, err := fp.scanFiles(ctx, tx, fp.sourceFileSelect(`b.sha256 = $2::text`), req.Src.ID, req.SHA256)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return nil, ErrItemNotFound
	}

	cands := make([]candidate, len(files))
	for i, f := range files {
		cands[i] = candidate{id: f.id, idents: nonEmpty(f.path, f.filename, f.id)}
	}

	kept := map[string]bool{}
	for _, c := range narrow(cands, req.Path) {
		kept[c.id] = true
	}

	var matched []srcFile

	for _, f := range files {
		if kept[f.id] {
			f.matched = true
			matched = append(matched, f)
		}
	}

	if !fp.p.WholeVersion {
		return matched, nil
	}

	return fp.wholeVersions(ctx, tx, req, matched)
}

// wholeVersions expands matched to every live file of their versions, the
// matched ones first.
func (fp *filePromoter) wholeVersions(ctx context.Context, tx Tx, req Request, matched []srcFile) ([]srcFile, error) {
	versionIDs := make([]string, 0, len(matched))
	for _, f := range matched {
		versionIDs = append(versionIDs, f.versionID)
	}

	siblings, err := fp.scanFiles(ctx, tx, fp.sourceFileSelect(`s.version_id = ANY($2::uuid[])`), req.Src.ID, uuidList(versionIDs))
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	out := make([]srcFile, 0, len(siblings))

	for _, f := range matched {
		seen[f.id] = true
		out = append(out, f)
	}

	for _, f := range siblings {
		if !seen[f.id] {
			seen[f.id] = true
			out = append(out, f)
		}
	}

	return out, nil
}

// uuidList renders ids as a Postgres uuid[] literal.
func uuidList(ids []string) string { return "{" + strings.Join(ids, ",") + "}" }

func (fp *filePromoter) scanFiles(ctx context.Context, tx Tx, query string, args ...any) ([]srcFile, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select %s source files: %w", fp.format, err)
	}

	defer func() { _ = rows.Close() }()

	var files []srcFile

	for rows.Next() {
		var (
			f        srcFile
			licenseJ []byte
		)

		if err := rows.Scan(&f.id, &f.versionID, &f.packageID, &f.pkg, &f.version, &f.path, &f.filename, &f.role,
			&f.sha, &f.size, &licenseJ); err != nil {
			return nil, fmt.Errorf("scan %s source file: %w", fp.format, err)
		}

		if err := json.Unmarshal(licenseJ, &f.licenses); err != nil {
			return nil, fmt.Errorf("decode %s licenses: %w", fp.format, err)
		}

		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s source files: %w", fp.format, err)
	}

	return files, nil
}

// versionKey is the SQL expression of the destination version_key.
func (fp *filePromoter) versionKey() string {
	if fp.p.VersionKey != "" {
		return "(" + fp.p.VersionKey + ")"
	}

	return "sv.version_key"
}

// part is the SQL expression of the destination package_files.part.
func (fp *filePromoter) part() string {
	if fp.p.Part != "" {
		return "(" + fp.p.Part + ")"
	}

	return "s.part"
}

// occupantColumns are the columns every branch of the occupant probe selects.
const occupantColumns = `d.id::text, d.path, COALESCE(db.sha256, ''), COALESCE(db.size_bytes, 0), dp.name, dv.version`

// occupantFrom is the source side of every probe branch.
const occupantFrom = `
	  FROM package_files s
	  JOIN package_versions sv ON sv.id = s.version_id
	  JOIN packages sp ON sp.id = sv.package_id`

// occupantQuery finds the live destination files ($1 = destination repository)
// that stand in the way of source file $2: the file at the same path, every
// file of the destination version the copy would join (unless the format shares
// versions between files), and the files the descriptor says serve the same
// download. Each branch is served by its own index.
func (fp *filePromoter) occupantQuery() string {
	dest := `
	  JOIN package_versions dv ON dv.id = d.version_id
	  JOIN packages dp ON dp.id = dv.package_id
	  LEFT JOIN blobs db ON db.id = d.blob_id
	 WHERE s.id = $2::uuid`

	branches := []string{
		`SELECT ` + occupantColumns + occupantFrom + `
	  JOIN package_files d ON d.repository_id = $1::uuid AND d.deleted_at IS NULL AND d.path = s.path` + dest,
	}

	if !fp.p.SharedVersion {
		branches = append(branches, `SELECT `+occupantColumns+occupantFrom+`
	  JOIN packages dp ON dp.repository_id = $1::uuid AND dp.name_key = sp.name_key
	  JOIN package_versions dv ON dv.package_id = dp.id AND dv.deleted_at IS NULL AND dv.version_key = `+fp.versionKey()+`
	  JOIN package_files d ON d.version_id = dv.id AND d.deleted_at IS NULL
	  LEFT JOIN blobs db ON db.id = d.blob_id
	 WHERE s.id = $2::uuid`)
	}

	if fp.p.Collide != "" {
		branches = append(branches, `SELECT `+occupantColumns+occupantFrom+`
	  JOIN package_files d ON d.repository_id = $1::uuid AND d.deleted_at IS NULL`+dest+`
	   AND (`+fp.p.Collide+`)`)
	}

	return strings.Join(branches, "\nUNION\n")
}

func (fp *filePromoter) occupants(ctx context.Context, tx Tx, req Request, f srcFile) ([]occupant, error) {
	rows, err := tx.QueryContext(ctx, fp.occupantQuery(), req.Dst.ID, f.id)
	if err != nil {
		return nil, fmt.Errorf("look up %s files in the target: %w", fp.format, err)
	}

	defer func() { _ = rows.Close() }()

	var out []occupant

	for rows.Next() {
		var o occupant
		if err := rows.Scan(&o.id, &o.path, &o.sha, &o.size, &o.pkg, &o.version); err != nil {
			return nil, fmt.Errorf("scan %s target file: %w", fp.format, err)
		}

		out = append(out, o)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s target files: %w", fp.format, err)
	}

	return out, nil
}

// settle decides, per source file, whether it still has to be copied: not when
// the destination holds the same file (reported as existing); yes when nothing
// stands in its way or, forced, other content does, which is moved to the trash
// (reported as replaced). Other content without req.Force is a conflict. It
// returns the files to copy.
func (fp *filePromoter) settle(ctx context.Context, tx Tx, req Request, files []srcFile, res *Result) ([]srcFile, error) {
	unitPaths := make(map[string]bool, len(files))
	for _, f := range files {
		unitPaths[f.path] = true
	}

	var (
		toCopy   []srcFile
		displace []string
		replaced = map[string]bool{}
	)

	for _, f := range files {
		occ, err := fp.occupants(ctx, tx, req, f)
		if err != nil {
			return nil, err
		}

		same := false

		for _, o := range occ {
			switch {
			case o.path == f.path && o.sha == f.sha:
				same = true
			case o.path == f.path, o.sha != f.sha && !unitPaths[o.path]:
				// Other content at the file's path, or in its version or under its
				// download name: the file this one would replace. A sibling of the
				// copied unit is no obstacle, and neither is the same content under
				// another path.
				if !req.Force {
					return nil, req.conflict(fp.format, f.pkg, f.version)
				}

				if !replaced[o.id] {
					replaced[o.id] = true

					displace = append(displace, o.id)
					res.Replaced = append(res.Replaced, Artifact{
						Name: o.pkg, Version: o.version, Path: o.path, SHA256: o.sha, Size: o.size,
					})
				}
			}
		}

		switch {
		case !same:
			toCopy = append(toCopy, f)
		case f.reported():
			res.Existing = append(res.Existing, f.artifact())
		}
	}

	if len(displace) > 0 {
		if err := database.TrashFilesInTx(ctx, tx, req.Dst.ID, displace, req.Actor); err != nil {
			return nil, fmt.Errorf("replace %s content in the target: %w", fp.format, err)
		}
	}

	return toCopy, nil
}

// Statements of a copy. $1 is the destination repository id in all of them.
const (
	// $2 = source package.
	insertPackageHead = `INSERT INTO packages (repository_id, format, name, name_key, attrs)
	SELECT $1::uuid, sp.format, sp.name, sp.name_key, `
	insertPackageTail = ` FROM packages sp WHERE sp.id = $2::uuid
	ON CONFLICT (repository_id, name_key) DO UPDATE SET updated_at = now()
	RETURNING id::text`

	// The destination version of source file $3 in destination package $2; an
	// existing live version with the same version_key is joined.
	insertVersionHead = `INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata, created_at, updated_at)
	SELECT $2::uuid, $1::uuid, sv.version, `
	insertVersionTail = `, sv.metadata, clock_timestamp(), clock_timestamp()
	  FROM package_files s
	  JOIN package_versions sv ON sv.id = s.version_id
	  JOIN packages sp ON sp.id = sv.package_id
	 WHERE s.id = $3::uuid
	ON CONFLICT (package_id, version_key) WHERE deleted_at IS NULL DO UPDATE SET version = EXCLUDED.version
	RETURNING id::text`

	// Source file $3 as a file of destination version $2. The timestamps are the
	// clock, not the transaction start: a format that orders by recency (conan's
	// latest) must see a promoted file as newer than the ones it joined.
	insertFileHead = `INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, size_bytes, role, listed,
	       arch, part, attrs, checksums, unit_key, created_at, updated_at)
	SELECT $2::uuid, $1::uuid, s.path, s.filename, s.blob_id, s.size_bytes, s.role, s.listed, s.arch, `
	insertFileTail = `, s.attrs, s.checksums, s.unit_key, clock_timestamp(), clock_timestamp()
	  FROM package_files s
	  JOIN package_versions sv ON sv.id = s.version_id
	  JOIN packages sp ON sp.id = sv.package_id
	 WHERE s.id = $3::uuid AND s.deleted_at IS NULL
	RETURNING id::text`

	// The tags of source package $3 that name version $4, into destination
	// package $2; a tag the destination has keeps its version.
	copyTagsSQL = `INSERT INTO package_tags (package_id, tag, version, unit_key)
	SELECT $2::uuid, t.tag, t.version, t.unit_key FROM package_tags t
	 WHERE t.package_id = $3::uuid AND t.version = $4::text AND $1::uuid IS NOT NULL
	ON CONFLICT (package_id, tag) DO NOTHING`
)

// copySideSQL copies the side row of source file $3 as the side row of the new
// file $2. The row is rebuilt with jsonb_populate_record so that every column
// the table has is carried, and set overrides some of them.
func copySideSQL(table, set string) string {
	override := ""
	if set != "" {
		override = " || (" + set + ")"
	}

	return `INSERT INTO ` + table + ` SELECT (jsonb_populate_record(NULL::` + table +
		`, to_jsonb(x) || jsonb_build_object('id', $2::text)` + override + `)).*
	  FROM ` + table + ` x
	  JOIN package_files s ON s.id = x.id
	  JOIN package_versions sv ON sv.id = s.version_id
	  JOIN packages sp ON sp.id = sv.package_id
	 WHERE x.id = $3::uuid AND $1::uuid IS NOT NULL`
}

func (fp *filePromoter) copyFiles(ctx context.Context, tx Tx, req Request, files []srcFile, res *Result) error {
	attrs := "sp.attrs"
	if fp.p.PackageAttrs != "" {
		attrs = "(" + fp.p.PackageAttrs + ")"
	}

	var (
		destPkgs    = map[string]string{}
		versions    []destVersion
		versionSeen = map[string]bool{}
		licensed    = map[string]bool{}
	)

	for _, f := range files {
		pkgID, ok := destPkgs[f.packageID]
		if !ok {
			if err := tx.QueryRowContext(ctx, insertPackageHead+attrs+insertPackageTail, req.Dst.ID, f.packageID).Scan(&pkgID); err != nil {
				return fmt.Errorf("get or create %s package in the target: %w", fp.format, err)
			}

			destPkgs[f.packageID] = pkgID
		}

		var versionID, fileID string

		versionSQL := insertVersionHead + fp.versionKey() + insertVersionTail

		if err := tx.QueryRowContext(ctx, versionSQL, req.Dst.ID, pkgID, f.id).Scan(&versionID); err != nil {
			return fmt.Errorf("get or create %s version in the target: %w", fp.format, err)
		}

		if err := tx.QueryRowContext(ctx, insertFileHead+fp.part()+insertFileTail, req.Dst.ID, versionID, f.id).Scan(&fileID); err != nil {
			return fmt.Errorf("copy %s file %s into the target: %w", fp.format, f.path, err)
		}

		for _, t := range fp.sides {
			if _, err := tx.ExecContext(ctx, copySideSQL(t.Name, fp.p.SideSet[t.Name]), req.Dst.ID, fileID, f.id); err != nil {
				return fmt.Errorf("copy %s row of %s into the target: %w", t.Name, f.path, err)
			}
		}

		if !versionSeen[versionID] {
			versionSeen[versionID] = true

			versions = append(versions, destVersion{
				packageID: pkgID, versionID: versionID, srcPackageID: f.packageID, srcVersion: f.version,
			})
		}

		if f.reported() {
			res.Copied = append(res.Copied, f.artifact())
		}

		res.Usage.Bytes += f.size

		if f.role == rolePrimary {
			res.Usage.Artifacts++
		}

		if !licensed[f.versionID] {
			licensed[f.versionID] = true

			res.Licenses = append(res.Licenses, f.licenses...)
		}
	}

	return fp.finishVersions(ctx, tx, req, versions, res)
}

// finishVersions copies the tags of the copied versions, runs the descriptor's
// post-copy statements for every destination version written to and records
// the metadata refresh the copy needs.
func (fp *filePromoter) finishVersions(ctx context.Context, tx Tx, req Request, versions []destVersion, res *Result) error {
	for _, v := range versions {
		if fp.p.Tags {
			if _, err := tx.ExecContext(ctx, copyTagsSQL, req.Dst.ID, v.packageID, v.srcPackageID, v.srcVersion); err != nil {
				return fmt.Errorf("copy %s tags: %w", fp.format, err)
			}
		}

		for _, stmt := range fp.p.AfterCopy {
			if _, err := tx.ExecContext(ctx, stmt, stmtArgs(stmt, req.Dst.ID, v.packageID, v.versionID)...); err != nil {
				return fmt.Errorf("finish the %s copy: %w", fp.format, err)
			}
		}
	}

	if fp.p.Regen == "" || len(versions) == 0 {
		return nil
	}

	if fp.p.Regen != storage.RegenNPM {
		res.Regen = append(res.Regen, Regen{Format: fp.format, RepoID: req.Dst.ID})

		return nil
	}

	seen := map[string]bool{}

	for _, v := range versions {
		if !seen[v.packageID] {
			seen[v.packageID] = true

			res.Regen = append(res.Regen, Regen{Format: fp.format, PackageID: v.packageID})
		}
	}

	return nil
}

// stmtArgs keeps the leading args a descriptor statement binds: the driver
// rejects a statement given more arguments than it has placeholders, and a
// fragment may need only $1.
func stmtArgs(stmt string, args ...any) []any {
	highest := 0

	for i := range len(stmt) {
		if stmt[i] != '$' {
			continue
		}

		n, j := 0, i+1
		for ; j < len(stmt) && stmt[j] >= '0' && stmt[j] <= '9'; j++ {
			n = n*10 + int(stmt[j]-'0')
		}

		highest = max(highest, n)
	}

	return args[:min(highest, len(args))]
}

// nonEmpty returns the values that are not the empty string, in order.
func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))

	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}

	return out
}
