// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// conanPromoter promotes Conan recipes. Every stored file of a recipe or
// package revision is one conan_packages row; a bundle item is the blob of one
// of them. A client can only install a complete recipe revision, so the item
// promotes the whole recipe revision its file belongs to: the files of the
// recipe itself and of every binary package revision beneath it.
//
// Conan has no stored "latest" bookkeeping: the latest revision is the one
// whose conanmanifest.txt row has the newest updated_at. The copied rows are
// timestamped with the wall clock at their insertion (not the transaction
// start), which makes a promoted revision the newest of its recipe in the
// destination like a fresh upload, and keeps the files of one promotion, and
// successive revisions of one bundle, in their source order instead of tied.
type conanPromoter struct{}

func (conanPromoter) Format() string { return "conan" }

// conanRevision identifies a recipe revision, the unit conan promotes.
type conanRevision struct {
	name, version, userChannel, revision string
}

// recipePath is the repository-relative identifier of the recipe.
func (r conanRevision) recipePath() string {
	return r.name + "/" + r.version + "/" + r.userChannel
}

// conanFile is one stored file of a recipe revision (package id empty) or of a
// binary package revision beneath it.
type conanFile struct {
	id, packageID, packageRevision, filename string
}

// path is the file's path inside its recipe revision: "<rrev>/<file>" for the
// recipe, "<rrev>/<package id>/<package revision>/<file>" for a binary package.
func (f conanFile) path(rev conanRevision) string {
	return strings.Join(nonEmpty(rev.revision, f.packageID, f.packageRevision, f.filename), "/")
}

// quarantineKeys are the identifiers the download path checks for quarantine:
// the row id, the file name and its recipe-relative and revision paths.
func (f conanFile) quarantineKeys(rev conanRevision) []string {
	keys := []string{f.id}
	if f.filename == "" {
		return keys
	}

	recipe := rev.recipePath()

	return append(keys, f.filename, recipe+"/"+f.filename, recipe+"/"+f.path(rev))
}

// conanMatch is a live source file holding the item's content.
type conanMatch struct {
	candidate

	rev  conanRevision
	file conanFile
}

// conanSourceQuery lists the live files of the source repository ($1) stored
// as the blob with sha256 $2.
const conanSourceQuery = `SELECT s.id::text, s.name, s.version, s.user_channel, s.recipe_revision,
	       s.package_id, s.package_revision, s.filename
	  FROM conan_packages s
	  JOIN blobs b ON b.id = s.blob_id
	 WHERE s.repository_id = $1::uuid AND s.deleted_at IS NULL AND b.sha256 = $2
	 ORDER BY s.created_at, s.id`

// conanFilesQuery lists every live file of recipe revision ($2 name, $3
// version, $4 user/channel, $5 revision) of source repository $1, in storage
// order, each with what destination repository $6 holds under the same live
// unique key: its id, digest and size (id empty when there is no such file) and, when there is, whether it is
// the same content (the same blob, or a blob with the same digest; two files
// without a blob are the same).
const conanFilesQuery = `SELECT s.id::text, s.package_id, s.package_revision, s.filename,
	       COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0)::bigint,
	       COALESCE(d.id::text, ''), COALESCE(db.sha256, ''), COALESCE(db.size_bytes, 0)::bigint,
	       COALESCE(d.blob_id IS NOT DISTINCT FROM s.blob_id OR db.sha256 = b.sha256, false)
	  FROM conan_packages s
	  LEFT JOIN blobs b ON b.id = s.blob_id
	  LEFT JOIN conan_packages d ON d.repository_id = $6::uuid AND d.deleted_at IS NULL
	       AND d.name = s.name AND d.version = s.version AND d.user_channel = s.user_channel
	       AND d.recipe_revision = s.recipe_revision AND d.package_id = s.package_id
	       AND d.package_revision = s.package_revision AND d.filename = s.filename
	  LEFT JOIN blobs db ON db.id = d.blob_id
	 WHERE s.repository_id = $1::uuid AND s.deleted_at IS NULL
	   AND s.name = $2 AND s.version = $3 AND s.user_channel = $4 AND s.recipe_revision = $5
	 ORDER BY s.created_at, s.id`

// conanCopyQuery copies file row $2 into repository $1: every column of
// conan_packages except the id, the soft-delete marks and download-style
// counters; the timestamps are the wall clock (see conanPromoter). A row
// without a blob (a metadata-only row) is copied as it is.
const conanCopyQuery = `INSERT INTO conan_packages (repository_id, blob_id, name, version, user_channel, recipe_revision,
	       package_id, package_revision, filename, conanfile, conaninfo, md5, sha1, created_at, updated_at)
	SELECT $1::uuid, s.blob_id, s.name, s.version, s.user_channel, s.recipe_revision,
	       s.package_id, s.package_revision, s.filename, s.conanfile, s.conaninfo, s.md5, s.sha1, t.ts, t.ts
	  FROM conan_packages s
	 CROSS JOIN LATERAL (SELECT clock_timestamp() AS ts) t
	 WHERE s.id = $2::uuid`

// conanDest is a source file of a revision with its state in the destination.
type conanDest struct {
	conanFile

	sha  string
	size int64
	dst  destRow
	same bool
}

// Promote implements Promoter.
func (conanPromoter) Promote(ctx context.Context, tx Tx, req Request) (*Result, error) {
	revs, err := conanRevisions(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	res := &Result{}

	for _, rev := range revs {
		revRes, err := promoteConanRevision(ctx, tx, req, rev)
		if err != nil {
			return nil, err
		}

		res.merge(revRes)
	}

	return res, nil
}

// conanRevisions returns the recipe revisions the item's content belongs to:
// those of the live source files with that content, narrowed to the file
// req.Path names when it names one of them, in storage order without repeats.
func conanRevisions(ctx context.Context, tx Tx, req Request) ([]conanRevision, error) {
	rs, err := tx.QueryContext(ctx, conanSourceQuery, req.Src.ID, req.SHA256)
	if err != nil {
		return nil, fmt.Errorf("select conan source files: %w", err)
	}

	defer func() { _ = rs.Close() }()

	var matches []*conanMatch

	for rs.Next() {
		var m conanMatch

		if err := rs.Scan(&m.file.id, &m.rev.name, &m.rev.version, &m.rev.userChannel, &m.rev.revision,
			&m.file.packageID, &m.file.packageRevision, &m.file.filename); err != nil {
			return nil, fmt.Errorf("scan conan source file: %w", err)
		}

		m.id = m.file.id
		m.idents = append(nonEmpty(m.file.path(m.rev), m.file.filename), m.file.quarantineKeys(m.rev)...)
		matches = append(matches, &m)
	}

	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("read conan source files: %w", err)
	}

	if len(matches) == 0 {
		return nil, ErrItemNotFound
	}

	var revs []conanRevision

	for _, m := range narrowRows(matches, func(m *conanMatch) candidate { return m.candidate }, req.Path) {
		if !slices.Contains(revs, m.rev) {
			revs = append(revs, m.rev)
		}
	}

	return revs, nil
}

// promoteConanRevision copies every file of recipe revision rev that the
// destination does not hold yet; a file held with different content is a
// conflict (or, forced, is replaced).
func promoteConanRevision(ctx context.Context, tx Tx, req Request, rev conanRevision) (*Result, error) {
	files, err := conanRevisionFiles(ctx, tx, req, rev)
	if err != nil {
		return nil, err
	}

	res := &Result{}

	for _, f := range files {
		art := Artifact{Name: rev.name, Version: rev.version, Path: f.path(rev), SHA256: f.sha, Size: f.size}
		res.QuarantineKeys = append(res.QuarantineKeys, f.quarantineKeys(rev)...)

		if f.dst.id != "" && !f.same {
			if !req.Force {
				return nil, req.conflict("conan", rev.name, rev.version)
			}

			if err := req.displace(ctx, tx, models.TrashKindConan, f.dst.id); err != nil {
				return nil, err
			}

			res.Replaced = append(res.Replaced, Artifact{Name: rev.name, Version: rev.version, Path: f.path(rev), SHA256: f.dst.sha, Size: f.dst.size})
			f.dst = destRow{}
		}

		switch {
		case f.dst.id != "":
			res.Existing = append(res.Existing, art)
		default:
			if _, err := tx.ExecContext(ctx, conanCopyQuery, req.Dst.ID, f.id); err != nil {
				return nil, fmt.Errorf("copy conan file into the target: %w", err)
			}

			// Like a file upload: its blob size and one artifact (none of the
			// size for a file without blob).
			res.Copied = append(res.Copied, art)
			res.Usage.Bytes += f.size
			res.Usage.Artifacts++
		}
	}

	slices.Sort(res.QuarantineKeys)
	res.QuarantineKeys = slices.Compact(res.QuarantineKeys)

	return res, nil
}

// conanRevisionFiles lists the live files of recipe revision rev in the source
// repository with their state in the destination.
func conanRevisionFiles(ctx context.Context, tx Tx, req Request, rev conanRevision) ([]conanDest, error) {
	rs, err := tx.QueryContext(ctx, conanFilesQuery, req.Src.ID, rev.name, rev.version, rev.userChannel, rev.revision, req.Dst.ID)
	if err != nil {
		return nil, fmt.Errorf("select conan recipe revision files: %w", err)
	}

	defer func() { _ = rs.Close() }()

	var files []conanDest

	for rs.Next() {
		var f conanDest

		if err := rs.Scan(&f.id, &f.packageID, &f.packageRevision, &f.filename, &f.sha, &f.size, &f.dst.id, &f.dst.sha, &f.dst.size, &f.same); err != nil {
			return nil, fmt.Errorf("scan conan recipe revision file: %w", err)
		}

		files = append(files, f)
	}

	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("read conan recipe revision files: %w", err)
	}

	return files, nil
}
