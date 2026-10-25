// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensemeta"
)

// npmPromoter promotes npm package versions. A bundle item is the sha256 of a
// version's tarball: the whole version row (manifest, integrity digests,
// tarball file name, same blob) is copied into the package of the same name in
// the destination, which is created when it is missing, together with the
// dist-tags that point at the copied version where the destination has no tag
// of that name; a destination package left without a "latest" tag gets one on
// the copied version, as a first publish does. The packument is persisted and
// refreshed by the npm metadata worker, so the destination package is marked
// stale and the result asks for a refresh.
type npmPromoter struct{}

func (npmPromoter) Format() string { return "npm" }

// npmRow is a live source npm_versions row holding the item's tarball. The
// candidate carries the package's full name as name.
type npmRow struct {
	candidate

	packageID string
	filename  string
}

// npmSourceQuery lists the live versions of the source repository whose
// tarball is the blob with sha256 $2.
const npmSourceQuery = `SELECT v.id::text, v.package_id::text, p.full_name, v.version, v.tarball_filename,
	       b.size_bytes, v.manifest_json
	  FROM npm_versions v
	  JOIN npm_packages p ON p.id = v.package_id
	  JOIN blobs b ON b.id = v.blob_id
	 WHERE p.repository_id = $1::uuid AND v.deleted_at IS NULL AND b.sha256 = $2
	 ORDER BY v.created_at, v.id`

// npmDestinationQuery returns the id, tarball digest and size of the live version $3 of
// package $2 in destination repository $1.
const npmDestinationQuery = `SELECT d.id::text, b.sha256, b.size_bytes
	  FROM npm_packages p
	  JOIN npm_versions d ON d.package_id = p.id AND d.deleted_at IS NULL
	  JOIN blobs b ON b.id = d.blob_id
	 WHERE p.repository_id = $1::uuid AND p.full_name = $2 AND d.version = $3`

// npmEnsurePackageQuery gets or creates the destination package (repository $1)
// named like source package $2, which gets its first version $3 as latest, and
// marks it stale. An existing package keeps its fields (its latest version is
// only filled in when it has none); the upload path creates packages the same
// way and recomputes latest_version in the worker.
const npmEnsurePackageQuery = `INSERT INTO npm_packages AS d (repository_id, scope, name, full_name, latest_version, packument_stale)
	SELECT $1::uuid, s.scope, s.name, s.full_name, $3::text, true FROM npm_packages s WHERE s.id = $2::uuid
	ON CONFLICT (repository_id, full_name) DO UPDATE
	   SET latest_version = CASE WHEN d.latest_version = '' THEN EXCLUDED.latest_version ELSE d.latest_version END,
	       packument_stale = true,
	       updated_at = now()
	RETURNING d.id::text`

// npmCopyVersionQuery copies version row $2 into package $1. Every column of
// npm_versions is copied except the id, the timestamps, the soft-delete marks.
const npmCopyVersionQuery = `INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id)
	SELECT $1::uuid, s.version, s.manifest_json, s.integrity, s.shasum, s.tarball_filename, s.blob_id
	  FROM npm_versions s WHERE s.id = $2::uuid`

// npmCopyDistTagsQuery copies the dist-tags of source package $2 that point at
// version $3 into package $1, except the ones the destination already has.
const npmCopyDistTagsQuery = `INSERT INTO npm_dist_tags (package_id, tag, version)
	SELECT $1::uuid, t.tag, t.version FROM npm_dist_tags t WHERE t.package_id = $2::uuid AND t.version = $3
	ON CONFLICT (package_id, tag) DO NOTHING`

// npmEnsureLatestQuery gives package $1 a "latest" dist-tag pointing at version
// $2 when it has none, as the first publish of a package does.
const npmEnsureLatestQuery = `INSERT INTO npm_dist_tags (package_id, tag, version) VALUES ($1::uuid, 'latest', $2::text)
	ON CONFLICT (package_id, tag) DO NOTHING`

// npmSyncLatestQuery keeps the package's latest_version consistent with its
// "latest" dist-tag: it becomes version $2 when that tag points at it.
const npmSyncLatestQuery = `UPDATE npm_packages p SET latest_version = $2::text
	 WHERE p.id = $1::uuid
	   AND EXISTS (SELECT 1 FROM npm_dist_tags t WHERE t.package_id = p.id AND t.tag = 'latest' AND t.version = $2::text)`

// Promote implements Promoter.
func (npmPromoter) Promote(ctx context.Context, tx Tx, req Request) (*Result, error) {
	rows, err := npmSourceRows(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	res := &Result{}

	for _, r := range rows {
		art := Artifact{Name: r.name, Version: r.version, Path: r.filename, SHA256: req.SHA256, Size: r.size}
		res.QuarantineKeys = append(res.QuarantineKeys, r.id, r.name, r.name+"@"+r.version, r.name+"/-/"+r.filename, r.filename)

		dest, err := npmDestination(ctx, tx, req, r)
		if err != nil {
			return nil, err
		}

		if dest.id != "" && dest.sha != req.SHA256 {
			if !req.Force {
				return nil, req.conflict("npm", r.name, r.version)
			}

			if err := req.displace(ctx, tx, models.TrashKindNPM, dest.id); err != nil {
				return nil, err
			}

			res.Replaced = append(res.Replaced, Artifact{Name: r.name, Version: r.version, Path: r.filename, SHA256: dest.sha, Size: dest.size})
			dest = destRow{}
		}

		switch {
		case dest.id != "":
			res.Existing = append(res.Existing, art)
		default:
			pkgID, err := npmCopy(ctx, tx, req, r)
			if err != nil {
				return nil, err
			}

			res.Copied = append(res.Copied, art)
			res.Usage.Bytes += r.size
			res.Usage.Artifacts++
			res.Licenses = append(res.Licenses, r.licenses...)
			res.Regen = append(res.Regen, Regen{Kind: RegenNPM, PackageID: pkgID})
		}
	}

	return res, nil
}

// npmSourceRows returns the live source versions with the item's tarball,
// narrowed to the one req.Path names when it names one of them.
func npmSourceRows(ctx context.Context, tx Tx, req Request) ([]*npmRow, error) {
	rs, err := tx.QueryContext(ctx, npmSourceQuery, req.Src.ID, req.SHA256)
	if err != nil {
		return nil, fmt.Errorf("select npm source versions: %w", err)
	}

	defer func() { _ = rs.Close() }()

	var rows []*npmRow

	for rs.Next() {
		var (
			r        npmRow
			manifest []byte
		)

		if err := rs.Scan(&r.id, &r.packageID, &r.name, &r.version, &r.filename, &r.size, &manifest); err != nil {
			return nil, fmt.Errorf("scan npm source version: %w", err)
		}

		r.idents = nonEmpty(r.filename, r.name+"/-/"+r.filename, r.name+"@"+r.version, r.id)
		r.licenses = licensemeta.ParsePackageJSON(manifest)
		rows = append(rows, &r)
	}

	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("read npm source versions: %w", err)
	}

	if len(rows) == 0 {
		return nil, ErrItemNotFound
	}

	return narrowRows(rows, func(r *npmRow) candidate { return r.candidate }, req.Path), nil
}

// npmDestination returns the live destination version at the coordinates of
// source version r, or the zero destRow when there is none.
func npmDestination(ctx context.Context, tx Tx, req Request, r *npmRow) (destRow, error) {
	var d destRow

	err := tx.QueryRowContext(ctx, npmDestinationQuery, req.Dst.ID, r.name, r.version).Scan(&d.id, &d.sha, &d.size)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return destRow{}, nil
	case err != nil:
		return destRow{}, fmt.Errorf("look up npm version in the target: %w", err)
	}

	return d, nil
}

// npmCopy writes source version r into the destination: the package (created
// or reused, and marked stale), the version row and the dist-tags that point at
// it. It returns the destination package id.
func npmCopy(ctx context.Context, tx Tx, req Request, r *npmRow) (string, error) {
	var pkgID string

	if err := tx.QueryRowContext(ctx, npmEnsurePackageQuery, req.Dst.ID, r.packageID, r.version).Scan(&pkgID); err != nil {
		return "", fmt.Errorf("get or create npm package in the target: %w", err)
	}

	if _, err := tx.ExecContext(ctx, npmCopyVersionQuery, pkgID, r.id); err != nil {
		return "", fmt.Errorf("copy npm version: %w", err)
	}

	if _, err := tx.ExecContext(ctx, npmCopyDistTagsQuery, pkgID, r.packageID, r.version); err != nil {
		return "", fmt.Errorf("copy npm dist-tags: %w", err)
	}

	if _, err := tx.ExecContext(ctx, npmEnsureLatestQuery, pkgID, r.version); err != nil {
		return "", fmt.Errorf("ensure npm latest dist-tag: %w", err)
	}

	if _, err := tx.ExecContext(ctx, npmSyncLatestQuery, pkgID, r.version); err != nil {
		return "", fmt.Errorf("sync npm latest version: %w", err)
	}

	return pkgID, nil
}
