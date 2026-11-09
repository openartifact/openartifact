// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestSearchByPropertyResolvesFileIDs covers the artifacts a property search returns: an id
// the web UI and oa write (a package_files.id of the same repository) resolves to its
// package version; an external id, a file id of another repository, a UUID that names no
// file and the id of a deleted file stay unresolved, and are still listed.
func TestSearchByPropertyResolvesFileIDs(t *testing.T) {
	ctx := context.Background()
	f := newPropertySearchFixture(t)

	got, err := f.pg.SearchByProperty(ctx, f.repo, "status", "approved")
	if err != nil {
		t.Fatal(err)
	}

	byID := requireSortedDistinct(t, got, 5)

	m := byID[f.design]
	if !m.Resolved() || *m.Name != "landing/v2/index.html" || m.Version == nil || *m.Version != "" ||
		m.Format == nil || *m.Format != "raw" {
		t.Fatalf("file id of repository a = %+v, want name landing/v2/index.html, version \"\", format raw", describe(m))
	}

	for name, id := range map[string]string{
		"external id":                   "build-482",
		"file id of another repository": f.otherRepoFile,
		"UUID that names no file":       f.unknownUUID,
		"deleted file":                  f.deleted,
	} {
		requireUnresolved(t, byID, name, id)
	}
}

// propertySearchFixture is repository a with five ids tagged status=approved, and
// repository b with one file carrying the same tag.
type propertySearchFixture struct {
	pg                                          *PostgresDB
	repo                                        string
	design, deleted, otherRepoFile, unknownUUID string
}

func newPropertySearchFixture(t *testing.T) *propertySearchFixture {
	t.Helper()

	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	a := &rawFixture{pg: pg, s: newQuotaSeeder(t, pg, "props-a", "raw")}
	b := &rawFixture{pg: pg, s: newQuotaSeeder(t, pg, "props-b", "raw")}

	f := &propertySearchFixture{
		pg: pg, repo: a.s.repo,
		design:        a.put(t, "landing/v2/index.html", "design").ID,
		deleted:       a.put(t, "old/draft.html", "old").ID,
		otherRepoFile: b.put(t, "elsewhere/file.txt", "other repo").ID,
		unknownUUID:   strings.ToUpper(testUUID("names no file")),
	}

	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE package_files SET deleted_at = now() WHERE id = $1`, f.deleted); err != nil {
		t.Fatal(err)
	}

	tag := map[string]string{"status": "approved"}
	for _, id := range []string{f.design, "build-482", f.otherRepoFile, f.unknownUUID, f.deleted} {
		if err := pg.SetArtifactProperties(ctx, a.s.repo, id, tag, false); err != nil {
			t.Fatalf("SetArtifactProperties(%s): %v", id, err)
		}
	}
	// The same tag in the other repository must not show up in repository a.
	if err := pg.SetArtifactProperties(ctx, b.s.repo, f.otherRepoFile, tag, false); err != nil {
		t.Fatal(err)
	}

	return f
}

// requireSortedDistinct checks there are want matches, sorted by id and without
// duplicates, and returns them by id.
func requireSortedDistinct(t *testing.T, got []models.PropertyMatch, want int) map[string]models.PropertyMatch {
	t.Helper()

	byID := map[string]models.PropertyMatch{}
	for i, m := range got {
		byID[m.ArtifactID] = m

		if i > 0 && got[i-1].ArtifactID > m.ArtifactID {
			t.Fatalf("matches not sorted by id: %q before %q", got[i-1].ArtifactID, m.ArtifactID)
		}
	}

	if len(got) != want || len(byID) != want {
		t.Fatalf("got %d matches (%d distinct), want %d: %+v", len(got), len(byID), want, got)
	}

	return byID
}

func requireUnresolved(t *testing.T, byID map[string]models.PropertyMatch, name, id string) {
	t.Helper()

	m, ok := byID[id]
	if !ok {
		t.Errorf("%s (%s) missing from the result", name, id)
		return
	}

	if m.Resolved() || m.Version != nil || m.Format != nil {
		t.Errorf("%s (%s) resolved to %+v, want unresolved", name, id, describe(m))
	}
}

func describe(m models.PropertyMatch) map[string]any {
	deref := func(s *string) any {
		if s == nil {
			return nil
		}

		return *s
	}

	return map[string]any{"id": m.ArtifactID, "name": deref(m.Name), "version": deref(m.Version), "format": deref(m.Format)}
}
