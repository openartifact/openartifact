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
)

// gomodPromoter promotes Go modules. A module version is one gomod_modules row
// that references two blobs, the zip and the go.mod file; a bundle item is
// either blob. The whole row is copied, so the destination serves the zip, the
// go.mod and the version info. Go module listings and @latest are computed from
// the rows, so nothing else needs refreshing.
type gomodPromoter struct{}

func (gomodPromoter) Format() string { return "go" }

// gomodRow is a live source module version holding the item's content. The
// candidate's size is that of the zip (the go.mod when there is no zip); usage
// is what the upload path reserves for the version: zip and go.mod bytes.
type gomodRow struct {
	candidate

	sha   string
	usage int64
}

// gomodSourceQuery lists the live module versions of the source repository
// ($1) with the blob sha256 $2 as zip or go.mod. sha and size describe the zip
// when the row has one, else the go.mod.
const gomodSourceQuery = `SELECT s.id::text, s.module_path, s.version,
	       COALESCE(zb.sha256, mb.sha256, ''), COALESCE(zb.size_bytes, mb.size_bytes, 0),
	       COALESCE(zb.size_bytes, 0) + COALESCE(mb.size_bytes, 0)
	  FROM gomod_modules s
	  LEFT JOIN blobs zb ON zb.id = s.zip_blob_id
	  LEFT JOIN blobs mb ON mb.id = s.mod_blob_id
	 WHERE s.repository_id = $1::uuid AND s.deleted_at IS NULL AND (zb.sha256 = $2 OR mb.sha256 = $2)
	 ORDER BY s.created_at, s.id`

// gomodSameQuery compares source module $2 with the live version of the same
// module path and version in destination repository $1. It returns no row when
// the destination has none, else the destination row's id, digest and size and whether the content is the same otherwise:
// the same zip (or, for a version without zip, the same go.mod). Blobs of an
// empty digest never compare equal by digest, only by being the same blob row.
const gomodSameQuery = `SELECT d.id::text, COALESCE(dzb.sha256, dmb.sha256, ''), COALESCE(dzb.size_bytes, dmb.size_bytes, 0),
	       COALESCE(CASE
	         WHEN s.zip_blob_id IS NOT NULL
	           THEN d.zip_blob_id = s.zip_blob_id OR (szb.sha256 <> '' AND dzb.sha256 = szb.sha256)
	         ELSE d.zip_blob_id IS NULL AND (d.mod_blob_id = s.mod_blob_id OR (smb.sha256 <> '' AND dmb.sha256 = smb.sha256))
	       END, false)
	  FROM gomod_modules s
	  JOIN gomod_modules d ON d.repository_id = $1::uuid AND d.deleted_at IS NULL
	                      AND d.module_path = s.module_path AND d.version = s.version
	  LEFT JOIN blobs szb ON szb.id = s.zip_blob_id
	  LEFT JOIN blobs dzb ON dzb.id = d.zip_blob_id
	  LEFT JOIN blobs smb ON smb.id = s.mod_blob_id
	  LEFT JOIN blobs dmb ON dmb.id = d.mod_blob_id
	 WHERE s.id = $2::uuid`

// gomodCopyQuery copies module row $2 into repository $1: every column of
// gomod_modules except the id, the timestamps and the soft-delete marks. Both
// blob references are kept; blobs are never copied.
const gomodCopyQuery = `INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id, mod_blob_id)
	SELECT $1::uuid, s.module_path, s.version, s.go_mod_content, s.zip_blob_id, s.mod_blob_id
	  FROM gomod_modules s WHERE s.id = $2::uuid`

// Promote implements Promoter.
func (gomodPromoter) Promote(ctx context.Context, tx Tx, req Request) (*Result, error) {
	rows, err := gomodSourceRows(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	res := &Result{}

	for _, r := range rows {
		res.QuarantineKeys = append(res.QuarantineKeys, gomodQuarantineKeys(r)...)

		art := Artifact{Name: r.name, Version: r.version, Path: r.version + ".zip", SHA256: r.sha, Size: r.size}

		dest, same, err := gomodSame(ctx, tx, req, r)
		if err != nil {
			return nil, err
		}

		if dest.id != "" && !same {
			if !req.Force {
				return nil, req.conflict("go", r.name, r.version)
			}

			if err := req.displace(ctx, tx, models.TrashKindGoMod, dest.id); err != nil {
				return nil, err
			}

			res.Replaced = append(res.Replaced, Artifact{Name: r.name, Version: r.version, Path: r.version + ".zip", SHA256: dest.sha, Size: dest.size})
			dest = destRow{}
		}

		switch {
		case dest.id != "":
			res.Existing = append(res.Existing, art)
		default:
			if _, err := tx.ExecContext(ctx, gomodCopyQuery, req.Dst.ID, r.id); err != nil {
				return nil, fmt.Errorf("copy go module into the target: %w", err)
			}

			// One module is one artifact; its go.mod is bytes only, like an upload reserves it.
			res.Copied = append(res.Copied, art)
			res.Usage.Bytes += r.usage
			res.Usage.Artifacts++
		}
	}

	return res, nil
}

// gomodQuarantineKeys are the identifiers the module download paths check for
// quarantine: the row id, each of the version's files and its coordinate.
func gomodQuarantineKeys(r *gomodRow) []string {
	base := r.name + "/@v/" + r.version

	return []string{r.id, base + ".zip", base + ".mod", base + ".info", r.name + "@" + r.version}
}

// gomodSourceRows returns the live source module versions with the item's
// zip or go.mod, narrowed to the one req.Path names when it names one of them.
func gomodSourceRows(ctx context.Context, tx Tx, req Request) ([]*gomodRow, error) {
	rs, err := tx.QueryContext(ctx, gomodSourceQuery, req.Src.ID, req.SHA256)
	if err != nil {
		return nil, fmt.Errorf("select go source modules: %w", err)
	}

	defer func() { _ = rs.Close() }()

	var rows []*gomodRow

	for rs.Next() {
		var r gomodRow

		if err := rs.Scan(&r.id, &r.name, &r.version, &r.sha, &r.size, &r.usage); err != nil {
			return nil, fmt.Errorf("scan go source module: %w", err)
		}

		// The identifiers a bundle item's path may spell: the file names, the
		// row id and the spellings the download paths check.
		r.idents = append([]string{r.version + ".zip", r.version + ".mod"}, gomodQuarantineKeys(&r)...)
		rows = append(rows, &r)
	}

	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("read go source modules: %w", err)
	}

	if len(rows) == 0 {
		return nil, ErrItemNotFound
	}

	return narrowRows(rows, func(r *gomodRow) candidate { return r.candidate }, req.Path), nil
}

// gomodSame returns the live destination version at the coordinates of source
// module r (the zero destRow when there is none) and, when there is one,
// whether it holds the same content.
func gomodSame(ctx context.Context, tx Tx, req Request, r *gomodRow) (destRow, bool, error) {
	var (
		d    destRow
		same bool
	)

	err := tx.QueryRowContext(ctx, gomodSameQuery, req.Dst.ID, r.id).Scan(&d.id, &d.sha, &d.size, &same)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return destRow{}, false, nil
	case err != nil:
		return destRow{}, false, fmt.Errorf("look up go module in the target: %w", err)
	}

	return d, same, nil
}
