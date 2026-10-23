// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

// destState is what the destination repository holds at the coordinates of a
// source package.
type destState int

const (
	// destAbsent: nothing collides, the package is copied.
	destAbsent destState = iota
	// destSame: the same content is already there (an idempotent retry).
	destSame
	// destDiffers: different content sits at the coordinates (a conflict).
	destDiffers
)

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
