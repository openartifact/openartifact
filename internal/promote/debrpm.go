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
func (c *destCollisions) replace(ctx context.Context, tx Tx, req Request, kind models.TrashKind, format, name, version string, res *Result) error {
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
