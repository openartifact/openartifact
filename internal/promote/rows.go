// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// rowSpec describes a format whose artifacts are single rows of one table
// scoped by repository_id, each pointing at one blob through blob_id, with
// soft delete (deleted_at). The generic rowPromoter copies such rows.
//
// All SQL here is assembled from the constant fragments of the package-level
// specs below (never from request data); request values are bind parameters.
type rowSpec struct {
	format string
	// kind is the trash kind of the table's rows, for forced promotions.
	kind  models.TrashKind
	table string
	// name and version are SQL expressions over the source alias s that give
	// the reported package name and version ('' when the format has none).
	name, version string
	// idents are SQL expressions over s that spell the artifact's file name or
	// path; Request.Path is matched against them to pick one of several rows
	// sharing a blob. The row id is always an identity as well.
	idents []string
	// key are the columns of the live unique index (besides repository_id): a
	// destination row equal on all of them is the same artifact.
	key []string
	// cols are the columns copied verbatim, key columns included. Columns not
	// listed (id, created_at, download_count, generated columns...) take their
	// defaults.
	cols []string
	// licenses is an optional SQL expression over s yielding text[] with the
	// licenses the row declares, for the destination's license policy.
	licenses string
}

// rowPromoter promotes the rows of one rowSpec.
type rowPromoter struct{ spec rowSpec }

func (p rowPromoter) Format() string { return p.spec.format }

// candidate is a live source row holding the item's content.
type candidate struct {
	id       string
	name     string
	version  string
	idents   []string
	size     int64
	licenses []string
}

// Promote implements Promoter.
func (p rowPromoter) Promote(ctx context.Context, tx Tx, req Request) (*Result, error) {
	cands, err := p.candidates(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	res := &Result{}

	for _, c := range cands {
		art := Artifact{Name: c.name, Version: c.version, Path: pathOf(c), SHA256: req.SHA256, Size: c.size}
		res.QuarantineKeys = append(res.QuarantineKeys, c.id)
		res.QuarantineKeys = append(res.QuarantineKeys, c.idents...)

		dest, err := p.destinationRow(ctx, tx, req, c.id)
		if err != nil {
			return nil, err
		}

		existing := dest.sha

		if existing != "" && existing != req.SHA256 {
			if !req.Force {
				return nil, req.conflict(p.spec.format, c.name, c.version)
			}

			if err := req.displace(ctx, tx, p.spec.kind, dest.id); err != nil {
				return nil, err
			}

			res.Replaced = append(res.Replaced, Artifact{Name: c.name, Version: c.version, Path: pathOf(c), SHA256: existing, Size: dest.size})
			existing = ""
		}

		if existing == req.SHA256 {
			res.Existing = append(res.Existing, art)

			continue
		}

		if err := p.insert(ctx, tx, req, c.id); err != nil {
			return nil, err
		}

		res.Copied = append(res.Copied, art)
		res.Usage.Bytes += c.size
		res.Usage.Artifacts++
		res.Licenses = append(res.Licenses, c.licenses...)
	}

	return res, nil
}

func pathOf(c candidate) string {
	if len(c.idents) > 0 {
		return c.idents[0]
	}

	return ""
}

// candidates returns the live source rows with the item's content, narrowed to
// the one named by req.Path when it names one of them.
func (p rowPromoter) candidates(ctx context.Context, tx Tx, req Request) ([]candidate, error) {
	s := p.spec

	idents := "'{}'::text[]"
	if len(s.idents) > 0 {
		idents = "ARRAY[" + strings.Join(s.idents, ", ") + "]::text[]"
	}

	licenses := "'[]'::jsonb"
	if s.licenses != "" {
		licenses = "COALESCE(to_jsonb(" + s.licenses + "), '[]'::jsonb)"
	}

	query := fmt.Sprintf(
		`SELECT s.id::text, %s, %s, to_jsonb(%s), b.size_bytes, %s
		   FROM %s s JOIN blobs b ON b.id = s.blob_id
		  WHERE s.repository_id = $1 AND s.deleted_at IS NULL AND b.sha256 = $2
		  ORDER BY s.created_at, s.id`,
		orEmpty(s.name), orEmpty(s.version), idents, licenses, s.table)

	rows, err := tx.QueryContext(ctx, query, req.Src.ID, req.SHA256)
	if err != nil {
		return nil, fmt.Errorf("select %s source rows: %w", s.table, err)
	}

	defer func() { _ = rows.Close() }()

	var cands []candidate

	for rows.Next() {
		var (
			c                candidate
			identsJ, licensJ []byte
		)

		if err := rows.Scan(&c.id, &c.name, &c.version, &identsJ, &c.size, &licensJ); err != nil {
			return nil, fmt.Errorf("scan %s source row: %w", s.table, err)
		}

		if err := json.Unmarshal(identsJ, &c.idents); err != nil {
			return nil, fmt.Errorf("decode %s identifiers: %w", s.table, err)
		}

		if err := json.Unmarshal(licensJ, &c.licenses); err != nil {
			return nil, fmt.Errorf("decode %s licenses: %w", s.table, err)
		}

		cands = append(cands, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s source rows: %w", s.table, err)
	}

	if len(cands) == 0 {
		return nil, ErrItemNotFound
	}

	return narrow(cands, req.Path), nil
}

func orEmpty(expr string) string {
	if expr == "" {
		return "''"
	}

	return "(" + expr + ")::text"
}

// narrow keeps the candidates whose identifiers equal path (ignoring a leading
// slash); with no such candidate every candidate stays.
func narrow(cands []candidate, path string) []candidate {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return cands
	}

	var hit []candidate

	for _, c := range cands {
		for _, id := range c.idents {
			if strings.TrimPrefix(id, "/") == path {
				hit = append(hit, c)

				break
			}
		}
	}

	if len(hit) == 0 {
		return cands
	}

	return hit
}

// destRow is a live destination row: its id and its blob's digest and size.
// The zero value means there is none.
type destRow struct {
	id   string
	sha  string
	size int64
	// name, version and path describe the row where a promoter needs them
	// (deb and rpm report every colliding row).
	name, version, path string
}

// destinationRow returns the live destination row with the same natural key as
// source row srcID, or the zero destRow when there is none.
func (p rowPromoter) destinationRow(ctx context.Context, tx Tx, req Request, srcID string) (destRow, error) {
	s := p.spec

	conds := make([]string, 0, len(s.key))
	for _, k := range s.key {
		conds = append(conds, "d."+k+" = s."+k)
	}

	query := fmt.Sprintf(
		`SELECT d.id::text, db.sha256, db.size_bytes
		   FROM %[1]s s
		   JOIN %[1]s d ON d.repository_id = $1 AND d.deleted_at IS NULL AND %[2]s
		   JOIN blobs db ON db.id = d.blob_id
		  WHERE s.id = $2
		  LIMIT 1`,
		s.table, strings.Join(conds, " AND "))

	var d destRow

	err := tx.QueryRowContext(ctx, query, req.Dst.ID, srcID).Scan(&d.id, &d.sha, &d.size)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return destRow{}, nil
	case err != nil:
		return destRow{}, fmt.Errorf("look up %s in the target: %w", s.table, err)
	}

	return d, nil
}

// insert copies source row srcID into the destination repository.
func (p rowPromoter) insert(ctx context.Context, tx Tx, req Request, srcID string) error {
	s := p.spec

	cols := strings.Join(s.cols, ", ")
	srcCols := "s." + strings.Join(s.cols, ", s.")

	query := fmt.Sprintf(
		`INSERT INTO %s (repository_id, blob_id, %s)
		 SELECT $1, s.blob_id, %s FROM %s s WHERE s.id = $2`,
		s.table, cols, srcCols, s.table)

	if _, err := tx.ExecContext(ctx, query, req.Dst.ID, srcID); err != nil {
		return fmt.Errorf("copy %s row into the target: %w", s.table, err)
	}

	return nil
}
