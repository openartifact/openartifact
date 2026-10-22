// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// FMTA-5: the snapshot file lookup filters on classifier and extension and
// returns the newest build, with the blob size filled in.
func TestGetLatestSnapshotFile(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "fmta-mvn", "maven")

	add := func(ts, cls, ext string, build int, size int64) {
		name := "foo-1.0-" + ts
		if cls != "" {
			name += "-" + cls
		}

		s.exec(`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, classifier, extension,
		        snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path, blob_id)
		        VALUES ($1,'com.ex','foo','1.0-SNAPSHOT',$2,$3,$4,$5,true,$6,$7)`,
			s.repo, cls, ext, ts, build, "com/ex/foo/1.0-SNAPSHOT/"+name+"."+ext, s.blob(size))
	}

	add("20240101.000000-1", "", "jar", 1, 11)
	add("20240102.000000-2", "", "jar", 2, 22)
	add("20240102.000000-2", "", "pom", 2, 33)
	add("20240102.000000-2", "sources", "jar", 2, 44)

	for _, tc := range []struct {
		cls, ext string
		size     int64
		ts       string
	}{
		{"", "jar", 22, "20240102.000000-2"},
		{"", "pom", 33, "20240102.000000-2"},
		{"sources", "jar", 44, "20240102.000000-2"},
	} {
		a, err := pg.GetLatestSnapshotFile(ctx, s.repo, "com.ex", "foo", "1.0-SNAPSHOT", tc.cls, tc.ext)
		if err != nil || a == nil {
			t.Fatalf("GetLatestSnapshotFile(%q,%q) = %v, %v", tc.cls, tc.ext, a, err)
		}

		if a.SnapshotTimestamp != tc.ts || a.BlobSizeBytes != tc.size || a.Classifier != tc.cls || a.Extension != tc.ext {
			t.Errorf("(%q,%q) = ts %s size %d cls %q ext %q", tc.cls, tc.ext, a.SnapshotTimestamp, a.BlobSizeBytes, a.Classifier, a.Extension)
		}
	}

	if a, err := pg.GetLatestSnapshotFile(ctx, s.repo, "com.ex", "foo", "1.0-SNAPSHOT", "javadoc", "jar"); err != nil || a != nil {
		t.Errorf("unknown classifier = %v, %v; want nil, nil", a, err)
	}
}

// FMTA-8/9: lookup by filename is unique, ambiguous when two packages share it.
func TestGetPyPIPackageByFilename(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "fmta-pypi", "pypi")

	add := func(name, filename string) {
		s.exec(`INSERT INTO pypi_packages (repository_id, name, version, filename, package_type, blob_id)
		        VALUES ($1,$2,'1.0',$3,'sdist',$4)`, s.repo, name, filename, s.blob(5))
	}

	add("foo", "foo-1.0.tar.gz")
	add("bar", "bar-1.0.tar.gz")

	p, err := pg.GetPyPIPackageByFilename(ctx, s.repo, "bar-1.0.tar.gz")
	if err != nil || p == nil || p.Name != "bar" {
		t.Fatalf("by filename = %+v, %v", p, err)
	}

	if p, err := pg.GetPyPIPackageByFilename(ctx, s.repo, "nope.whl"); err != nil || p != nil {
		t.Fatalf("missing = %+v, %v", p, err)
	}

	add("foo-legacy", "foo-1.0.tar.gz")

	if _, err := pg.GetPyPIPackageByFilename(ctx, s.repo, "foo-1.0.tar.gz"); !errors.Is(err, ErrAmbiguousPyPIFilename) {
		t.Fatalf("duplicate = %v, want ErrAmbiguousPyPIFilename", err)
	}

	// Rows stored by older versions with a run of '-' stay reachable under the PEP 503 name.
	add("my--pkg", "my_pkg-1.0.tar.gz")

	vs, err := pg.ListPyPIPackageVersions(ctx, s.repo, "my-pkg")
	if err != nil || len(vs) != 1 {
		t.Fatalf("legacy name = %d rows, %v", len(vs), err)
	}
}

// FMTA-12: only sessions idle past the cutoff are deleted.
func TestDeleteStaleDockerUploads(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "fmta-docker", "docker")

	s.exec(`INSERT INTO docker_uploads (repository_id, upload_uuid, updated_at) VALUES ($1,'old', now() - interval '2 days')`, s.repo)
	s.exec(`INSERT INTO docker_uploads (repository_id, upload_uuid, updated_at) VALUES ($1,'fresh', now())`, s.repo)

	got, err := pg.DeleteStaleDockerUploads(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || len(got) != 1 || got[0] != "old" {
		t.Fatalf("DeleteStaleDockerUploads = %v, %v; want [old]", got, err)
	}

	var n int
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM docker_uploads WHERE upload_uuid = 'fresh'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("fresh session survived: %d, %v", n, err)
	}
}

// STOR-10: a repository's negative proxy cache is bounded; refreshing an
// existing entry still works at the cap.
func TestNegativeProxyCacheEntryCap(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "stor10-proxy", "maven")
	exp := time.Now().Add(time.Hour)

	put := func(key string) {
		t.Helper()

		if err := pg.q.UpsertNegativeProxyCacheEntry(ctx, dbsqlc.UpsertNegativeProxyCacheEntryParams{
			RepositoryID: s.repo, CacheKey: key, ExpiresAt: exp, Limit: 2,
		}); err != nil {
			t.Fatal(err)
		}
	}

	put("a")
	put("b")
	put("c") // over the cap: dropped
	put("a") // existing: refreshed

	for key, want := range map[string]bool{"a": true, "b": true, "c": false} {
		e, err := pg.GetProxyCacheEntry(ctx, s.repo, key)
		if got := err == nil && e != nil; got != want {
			t.Errorf("entry %q present = %v, want %v (err %v)", key, got, want, err)
		}
	}
}
