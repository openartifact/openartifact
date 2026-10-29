// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"

	"github.com/openartifact/openartifact/internal/database/models"
)

// destCollisions is what the destination repository holds at the coordinates
// (or file name) of a source package.
type destCollisions struct {
	// same: the same content is already there at the coordinates (an
	// idempotent retry).
	same bool
	// differing are the colliding rows with other content: a conflict, or, for
	// a forced promotion, the rows to replace.
	differing []destRow
}

// replace settles the differing rows of a forced promotion: without
// req.Force they are a conflict, with it they are moved to the trash and
// reported as replaced in res. kind is the trash kind of the table.
func (c *destCollisions) replace(
	ctx context.Context,
	tx Tx,
	req Request,
	kind models.TrashKind,
	format, name, version string,
	res *Result,
) error {
	if len(c.differing) == 0 {
		return nil
	}

	if !req.Force {
		return req.conflict(format, name, version)
	}

	ids := make([]string, len(c.differing))
	for i, d := range c.differing {
		ids[i] = d.id
		res.Replaced = append(res.Replaced, Artifact{Name: d.name, Version: d.version, Path: d.path, SHA256: d.sha, Size: d.size})
	}

	return req.displace(ctx, tx, kind, ids...)
}

// packageFile is a live source row of a deb or rpm repository: the candidate
// and the file name its package is served under.
type packageFile struct {
	candidate

	filename string
}

// file returns f itself; embedded in debRow and rpmRow it lets
// promoteDebRpmRows read the part of a row both formats share.
func (f *packageFile) file() *packageFile { return f }

// debRpmRow is a source row of a deb or rpm repository.
type debRpmRow interface {
	file() *packageFile
	quarantineKeys() []string
}

// debRpmPromoter is what the deb and rpm promoters do differently when they
// promote a source row of type R: the trash kind and the name of the format,
// the lookup of what the destination holds, and the copy of the row.
type debRpmPromoter[R debRpmRow] interface {
	Format() string
	trashKind() models.TrashKind
	destination(ctx context.Context, tx Tx, req Request, row R) (*destCollisions, error)
	copyRow(ctx context.Context, tx Tx, req Request, row R) error
}

// promoteDebRpmRows promotes every source row of a deb or rpm repository,
// copying each one unless the destination already holds it.
func promoteDebRpmRows[R debRpmRow](ctx context.Context, tx Tx, req Request, p debRpmPromoter[R], rows []R) (*Result, error) {
	res := &Result{}

	for _, row := range rows {
		res.QuarantineKeys = append(res.QuarantineKeys, row.quarantineKeys()...)

		dest, err := p.destination(ctx, tx, req, row)
		if err != nil {
			return nil, err
		}

		f := row.file()

		err = dest.promote(ctx, tx, req, res, p.trashKind(), p.Format(), f.artifact(f.filename, req.SHA256), f.licenses,
			func() error { return p.copyRow(ctx, tx, req, row) })
		if err != nil {
			return nil, err
		}
	}

	return res, nil
}

// promote completes the promotion of one source package art (declaring
// licenses) of a deb or rpm repository: the colliding rows with other content
// are settled by replace, then, unless the destination already holds the same
// content (an idempotent retry), copyRow copies the row and the copy is
// accounted in res.
func (c *destCollisions) promote(
	ctx context.Context,
	tx Tx,
	req Request,
	res *Result,
	kind models.TrashKind,
	format string,
	art Artifact,
	licenses []string,
	copyRow func() error,
) error {
	if err := c.replace(ctx, tx, req, kind, format, art.Name, art.Version, res); err != nil {
		return err
	}

	if c.same {
		res.Existing = append(res.Existing, art)

		return nil
	}

	if err := copyRow(); err != nil {
		return err
	}

	res.addCopied(art, art.Size)
	res.Licenses = append(res.Licenses, licenses...)

	return nil
}

// narrowRows keeps the rows whose identifiers equal path, with the semantics of
// narrow: when path names none of them every row stays. cand returns the
// candidate view of a row; candidate ids must be unique within rows.
func narrowRows[T any](rows []*T, cand func(*T) candidate, path string) []*T {
	cands := make([]candidate, len(rows))
	byID := make(map[string]*T, len(rows))

	for i, r := range rows {
		cands[i] = cand(r)
		byID[cands[i].id] = r
	}

	kept := narrow(cands, path)
	out := make([]*T, 0, len(kept))

	for _, c := range kept {
		out = append(out, byID[c.id])
	}

	return out
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
