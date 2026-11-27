// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func gitlfsObject(repoID, content string) *models.GitLFSObject {
	oid := testSHA(content)

	return &models.GitLFSObject{RepositoryID: repoID, OID: oid, BlobKey: oid, SizeBytes: int64(len(content))}
}

func getGitLFSOIDs(t *testing.T, pg *PostgresDB, repoID string, oids ...string) []string {
	t.Helper()

	got, err := pg.GetGitLFSObjects(context.Background(), repoID, oids)
	if err != nil {
		t.Fatalf("GetGitLFSObjects: %v", err)
	}

	out := make([]string, 0, len(got))
	for _, o := range got {
		out = append(out, o.OID)
	}

	slices.Sort(out)

	return out
}

func TestGitLFSObjectsRoundTripAndScope(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	a := newQuotaSeeder(t, pg, "lfs-a", "gitlfs")
	b := newQuotaSeeder(t, pg, "lfs-b", "gitlfs")
	ctx := context.Background()

	one, two := gitlfsObject(a.repo, "one"), gitlfsObject(a.repo, "two")
	for _, o := range []*models.GitLFSObject{one, two} {
		if err := pg.PutGitLFSObject(ctx, o); err != nil {
			t.Fatalf("PutGitLFSObject: %v", err)
		}

		if o.ID == "" || o.CreatedAt.IsZero() {
			t.Fatalf("PutGitLFSObject left %+v unfilled", o)
		}
	}

	got, err := pg.GetGitLFSObjects(ctx, a.repo, []string{one.OID, "f" + one.OID[1:], two.OID})
	if err != nil {
		t.Fatal(err)
	}

	byOID := map[string]*models.GitLFSObject{}
	for _, o := range got {
		byOID[o.OID] = o
	}

	if len(byOID) != 2 || byOID[one.OID].ID != one.ID || byOID[one.OID].SizeBytes != 3 || byOID[one.OID].BlobKey != one.OID {
		t.Fatalf("round trip = %+v", byOID)
	}

	// The same object in another repository is not visible here, and the same
	// oid can be stored there on its own.
	if oids := getGitLFSOIDs(t, pg, b.repo, one.OID); len(oids) != 0 {
		t.Fatalf("repository b sees %v", oids)
	}

	if err := pg.PutGitLFSObject(ctx, gitlfsObject(b.repo, "one")); err != nil {
		t.Fatalf("same oid in another repository: %v", err)
	}

	// One package per object, a file path derived from the oid.
	var path string

	err = pg.GetDB().QueryRowContext(ctx, `SELECT f.path FROM package_files f WHERE f.id = $1`, one.ID).Scan(&path)
	if err != nil || path != "objects/"+one.OID[:2]+"/"+one.OID[2:4]+"/"+one.OID {
		t.Fatalf("file path = %q, %v", path, err)
	}

	requireGenericCounts(t, a, "two objects", pypiGenericCounts{2, 2, 2})
}

func TestGitLFSPutIsIdempotentUntilTrashed(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	s := newQuotaSeeder(t, pg, "lfs-idem", "gitlfs")
	ctx := context.Background()

	obj := gitlfsObject(s.repo, "payload")
	if err := pg.PutGitLFSObject(ctx, obj); err != nil {
		t.Fatal(err)
	}

	if err := pg.PutGitLFSObject(ctx, gitlfsObject(s.repo, "payload")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second put = %v, want ErrDuplicate", err)
	}

	requireGenericCounts(t, s, "after a duplicate", pypiGenericCounts{1, 1, 1})

	// Trashing hides the object; uploading it again stores it as a new file.
	s.exec(`UPDATE package_files SET deleted_at = now() WHERE repository_id = $1`, s.repo)
	s.exec(`UPDATE package_versions SET deleted_at = now() WHERE repository_id = $1`, s.repo)

	if oids := getGitLFSOIDs(t, pg, s.repo, obj.OID); len(oids) != 0 {
		t.Fatalf("a trashed object is still served: %v", oids)
	}

	again := gitlfsObject(s.repo, "payload")
	if err := pg.PutGitLFSObject(ctx, again); err != nil {
		t.Fatalf("put after trash: %v", err)
	}

	if oids := getGitLFSOIDs(t, pg, s.repo, obj.OID); len(oids) != 1 {
		t.Fatalf("re-uploaded object not served: %v", oids)
	}
}
