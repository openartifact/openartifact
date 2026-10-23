// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"errors"
	"testing"
)

// rowsWithBlob counts the live (deleted false) or soft-deleted rows of the
// tables of repoID that reference blobID in any column.
func (h *harness) rowsWithBlob(tables []string, repoID, blobID string, deleted bool) int {
	h.t.Helper()

	cond := "t.deleted_at IS NULL"
	if deleted {
		cond = "t.deleted_at IS NOT NULL"
	}

	total := 0

	for _, table := range tables {
		total += h.count(
			`SELECT COUNT(*) FROM `+table+` t
			  WHERE t.repository_id = $1 AND `+cond+`
			    AND EXISTS (SELECT 1 FROM jsonb_each_text(to_jsonb(t)) e WHERE e.value = $2)`, repoID, blobID)
	}

	return total
}

// TestForcePromotesOverDifferentContent: with Force a destination holding
// different content at the same coordinates is replaced (moved to the trash)
// instead of being a conflict, and a repeat is a no-op.
func TestForcePromotesOverDifferentContent(t *testing.T) {
	t.Parallel()

	for _, fx := range fixtures() {
		t.Run(fx.format, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			p := promoterFor(t, fx.format)

			src, dst := h.repo(fx.format, true), h.repo(fx.format, true)
			blobA, shaA := h.blob("content of " + fx.format)
			blobB, _ := h.blob("different content of " + fx.format)
			fx.seed(h, src.ID, blobA)
			fx.seed(h, dst.ID, blobB)

			req := Request{Src: src, Dst: dst, SHA256: shaA, Path: fx.path, Force: true, Actor: "admin"}

			res, err := h.promoteOnce(p, req)
			if err != nil {
				t.Fatalf("forced promotion: %v", err)
			}

			if len(res.Replaced) == 0 {
				t.Errorf("Replaced is empty: %+v", res)
			}

			for _, a := range res.Replaced {
				if a.SHA256 == "" || a.SHA256 == shaA {
					t.Errorf("replaced artifact %+v, want the destination's old content digest", a)
				}
			}

			// A recipe's files that hold the same content in both repositories stay Existing.
			if len(res.Copied) == 0 || len(res.Copied)+len(res.Existing) != fx.wantCopies() {
				t.Fatalf("copied %d existing %d, want %d in all and some copied: %+v", len(res.Copied), len(res.Existing), fx.wantCopies(), res)
			}

			if res.Usage.Artifacts == 0 {
				t.Errorf("usage = %+v, want a charge for the copy", res.Usage)
			}

			assertForced(t, h, fx, dst.ID, blobA, blobB)

			again, err := h.promoteOnce(p, req)
			if err != nil {
				t.Fatalf("repeated forced promotion: %v", err)
			}

			if len(again.Replaced) != 0 || len(again.Copied) != 0 || len(again.Existing) != fx.wantCopies() {
				t.Errorf("repeat = %+v, want everything existing and nothing replaced", again)
			}
		})
	}
}

// assertForced checks the destination after a forced promotion: the live rows
// reference the promoted blob a and the old rows are soft-deleted.
func assertForced(t *testing.T, h *harness, fx fixture, dstID, blobA, blobB string) {
	t.Helper()

	// Docker replaces a tag and keeps the destination's other manifest.
	if fx.format == "docker" {
		if n := h.count(`SELECT COUNT(*) FROM docker_tags WHERE repository_id = $1 AND deleted_at IS NOT NULL`, dstID); n == 0 {
			t.Error("no docker tag was moved to the trash")
		}

		if n := h.rowsWithBlob([]string{"docker_manifests"}, dstID, blobA, false); n == 0 {
			t.Error("the promoted manifest is not live in the target")
		}

		return
	}

	if n := h.rowsWithBlob(fx.tables, dstID, blobA, false); n == 0 {
		t.Errorf("no live target row references the promoted blob")
	}

	if n := h.rowsWithBlob(fx.tables, dstID, blobB, false); n != 0 {
		t.Errorf("%d live target rows still reference the old blob", n)
	}

	if n := h.rowsWithBlob(fx.tables, dstID, blobB, true); n == 0 {
		t.Errorf("no soft-deleted target row references the old blob")
	}

	for _, table := range fx.tables {
		if n := h.count(`SELECT COUNT(*) FROM `+table+` WHERE repository_id = $1 AND deleted_at IS NOT NULL AND deleted_by = 'admin'`, dstID); n == 0 {
			t.Errorf("%s: no row was trashed by the actor", table)
		}
	}
}

// TestForceOffStillConflicts: without Force different content in the
// destination stays a ConflictError and changes nothing.
func TestForceOffStillConflicts(t *testing.T) {
	t.Parallel()

	for _, fx := range fixtures() {
		t.Run(fx.format, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			p := promoterFor(t, fx.format)

			src, dst := h.repo(fx.format, true), h.repo(fx.format, true)
			blobA, shaA := h.blob("content of " + fx.format)
			blobB, _ := h.blob("different content of " + fx.format)
			fx.seed(h, src.ID, blobA)
			fx.seed(h, dst.ID, blobB)

			_, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: shaA, Path: fx.path})

			var conflict *ConflictError
			if !errors.As(err, &conflict) || !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want a ConflictError", err)
			}

			if n := h.rowsWithBlob(fx.tables, dst.ID, blobB, true); n != 0 {
				t.Errorf("%d target rows were trashed by a refused promotion", n)
			}
		})
	}
}
