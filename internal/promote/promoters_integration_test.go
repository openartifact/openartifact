// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/trash"
)

const defaultIgnored = "id,repository_id,created_at,updated_at,download_count,deleted_at,deleted_by"

// sameRows counts the rows of src and dst that agree on every column but the
// ignored ones.
func (h *harness) sameRows(table, src, dst string, ignore []string) (int, int) {
	h.t.Helper()

	cols := "{" + strings.Join(append(strings.Split(defaultIgnored, ","), ignore...), ",") + "}"

	total := h.count(`SELECT COUNT(*) FROM `+table+` WHERE repository_id = $1`, src)
	matching := h.count(
		`SELECT COUNT(*) FROM `+table+` s JOIN `+table+` d
		   ON (to_jsonb(s) - $3::text[]) = (to_jsonb(d) - $3::text[])
		WHERE s.repository_id = $1 AND d.repository_id = $2`, src, dst, cols)

	return matching, total
}

func TestPromotersCopyEveryFormat(t *testing.T) {
	t.Parallel()

	for _, fx := range fixtures() {
		t.Run(fx.format, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			p := promoterFor(t, fx.format)

			src, dst := h.repo(fx.format, true), h.repo(fx.format, true)
			blobA, shaA := h.blob("content of " + fx.format)
			fx.seed(h, src.ID, blobA)

			blobsBefore := h.count(`SELECT COUNT(*) FROM blobs`)
			req := Request{Src: src, Dst: dst, SHA256: shaA, Path: fx.path}

			res, err := h.promoteOnce(p, req)
			if err != nil {
				t.Fatalf("first promotion: %v", err)
			}

			if len(res.Copied) != fx.wantCopies() || len(res.Existing) != 0 {
				t.Fatalf("first promotion copied %d existing %d, want %d and 0: %+v", len(res.Copied), len(res.Existing), fx.wantCopies(), res)
			}

			if res.Usage.Artifacts == 0 || res.Usage.Bytes == 0 {
				t.Errorf("usage = %+v, want a charge for the copy", res.Usage)
			}

			if len(res.QuarantineKeys) == 0 {
				t.Error("no quarantine keys reported")
			}

			for _, table := range fx.compare {
				if matching, total := h.sameRows(table, src.ID, dst.ID, fx.ignore); total == 0 || matching != total {
					t.Errorf("%s: %d of %d source rows have an identical copy in the target", table, matching, total)
				}
			}

			if got := h.count(`SELECT COUNT(*) FROM blobs`); got != blobsBefore {
				t.Errorf("blobs rows %d -> %d: a promotion must not create blobs", blobsBefore, got)
			}

			if fx.verify != nil {
				fx.verify(h, src.ID, dst.ID)
			}

			t.Run("idempotent", func(t *testing.T) { assertIdempotent(t, h, p, req, fx) })
			t.Run("conflict", func(t *testing.T) { assertConflict(t, h, p, src, fx, shaA) })
			t.Run("trashed target row", func(t *testing.T) { assertTrashedTargetIgnored(t, h, p, src, fx, shaA) })
			t.Run("missing", func(t *testing.T) {
				_, err := h.promoteOnce(p, Request{Src: dst, Dst: h.repo(fx.format, false), SHA256: strings.Repeat("0", 64), Path: fx.path})
				if !errors.Is(err, ErrItemNotFound) {
					t.Errorf("unknown content: err = %v, want ErrItemNotFound", err)
				}
			})
		})
	}
}

func assertIdempotent(t *testing.T, h *harness, p Promoter, req Request, fx fixture) {
	t.Helper()

	res, err := h.promoteOnce(p, req)
	if err != nil {
		t.Fatalf("second promotion: %v", err)
	}

	if len(res.Copied) != 0 || len(res.Existing) != fx.wantCopies() || res.Usage.Artifacts != 0 || res.Usage.Bytes != 0 {
		t.Errorf("second promotion = %+v, want everything existing and nothing charged", res)
	}

	for _, table := range fx.tables {
		if n := h.count(`SELECT COUNT(*) FROM `+table+` WHERE repository_id = $1`, req.Dst.ID); n != h.count(`SELECT COUNT(*) FROM `+table+` WHERE repository_id = $1`, req.Src.ID) {
			t.Errorf("%s: target has %d rows after a retry, want the source's count", table, n)
		}
	}
}

// assertConflict: a target holding different bytes at the same coordinates is
// refused, as an immutable-release refusal for release versions.
func assertConflict(t *testing.T, h *harness, p Promoter, src *models.Repository, fx fixture, sha string) {
	t.Helper()

	dst := h.repo(fx.format, true)
	other, _ := h.blob("different content of " + fx.format)
	fx.seed(h, dst.ID, other)

	_, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: sha, Path: fx.path})

	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want a ConflictError", err)
	}

	if want := trash.IsRelease(fx.format, fx.version); conflict.Immutable != want {
		t.Errorf("Immutable = %v, want %v for version %q", conflict.Immutable, want, fx.version)
	}

	// The same conflict in a mutable repository is a plain conflict.
	mutable := h.repo(fx.format, false)
	fx.seed(h, mutable.ID, other)

	_, err = h.promoteOnce(p, Request{Src: src, Dst: mutable, SHA256: sha, Path: fx.path})
	if !errors.As(err, &conflict) || conflict.Immutable {
		t.Errorf("mutable target: err = %v, want a non-immutable ConflictError", err)
	}
}

// assertTrashedTargetIgnored: a soft-deleted row of the target does not block a copy.
func assertTrashedTargetIgnored(t *testing.T, h *harness, p Promoter, src *models.Repository, fx fixture, sha string) {
	t.Helper()

	dst := h.repo(fx.format, true)
	other, _ := h.blob("trashed content of " + fx.format)
	fx.seed(h, dst.ID, other)

	for _, table := range fx.tables {
		h.exec(`UPDATE `+table+` SET deleted_at = now() WHERE repository_id = $1 AND deleted_at IS NULL`, dst.ID)
	}

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: sha, Path: fx.path})
	if err != nil {
		t.Fatalf("promotion over a trashed row: %v", err)
	}

	if len(res.Copied) != fx.wantCopies() {
		t.Errorf("copied %d, want %d", len(res.Copied), fx.wantCopies())
	}
}

// TestRowSpecsCoverEveryColumn fails when a migration adds a column to a table
// the generic promoter copies without the spec learning about it, so a copy
// can never silently drop metadata.
func TestRowSpecsCoverEveryColumn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	skipped := map[string]bool{
		"id": true, "repository_id": true, "blob_id": true, "created_at": true, "updated_at": true,
		"deleted_at": true, "deleted_by": true, "download_count": true,
	}

	for _, spec := range rowSpecs() {
		t.Run(spec.format, func(t *testing.T) {
			t.Parallel()

			rows, err := h.pg.GetDB().QueryContext(context.Background(),
				`SELECT column_name FROM information_schema.columns
				  WHERE table_schema = 'public' AND table_name = $1 AND is_generated = 'NEVER'`, spec.table)
			if err != nil {
				t.Fatalf("list columns: %v", err)
			}

			defer func() { _ = rows.Close() }()

			table := map[string]bool{}

			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					t.Fatalf("scan: %v", err)
				}

				table[c] = true
			}

			if err := rows.Err(); err != nil {
				t.Fatalf("columns: %v", err)
			}

			copied := map[string]bool{}
			for _, c := range spec.cols {
				copied[c] = true

				if !table[c] {
					t.Errorf("spec copies %s, which %s does not have", c, spec.table)
				}
			}

			for _, k := range spec.key {
				if !copied[k] {
					t.Errorf("key column %s is not copied", k)
				}
			}

			for c := range table {
				if !copied[c] && !skipped[c] {
					t.Errorf("%s.%s is neither copied nor deliberately skipped", spec.table, c)
				}
			}
		})
	}
}
