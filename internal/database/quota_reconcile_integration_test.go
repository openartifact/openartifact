// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// quotaSeeder writes package rows through the generic package writer, the way
// an upload leaves them (blob row + package, version and file rows), without
// going through a handler: the point is what the counters should be for given
// rows.
type quotaSeeder struct {
	tb   testing.TB
	pg   *PostgresDB
	repo string
	seq  int

	blobSeq int
	// blobs remembers what blob() stored, by blob row id: the writer finds a
	// blob by its digest.
	blobs map[string]BlobWrite
}

func newQuotaSeeder(tb testing.TB, pg *PostgresDB, name, format string) *quotaSeeder {
	tb.Helper()

	org := systemOrgID

	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		tb.Fatalf("CreateRepository(%s): %v", name, err)
	}

	return &quotaSeeder{tb: tb, pg: pg, repo: repo.ID, blobs: map[string]BlobWrite{}}
}

func (s *quotaSeeder) exec(query string, args ...any) {
	s.tb.Helper()

	if _, err := s.pg.GetDB().ExecContext(context.Background(), query, args...); err != nil {
		s.tb.Fatalf("seed %q: %v", query, err)
	}
}

func (s *quotaSeeder) queryID(query string, args ...any) string {
	s.tb.Helper()

	var id string
	if err := s.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&id); err != nil {
		s.tb.Fatalf("seed %q: %v", query, err)
	}

	return id
}

// blob stores a blob row of the given size and returns its id.
func (s *quotaSeeder) blob(size int64) string {
	s.blobSeq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", s.repo, s.blobSeq)))
	sha := hex.EncodeToString(sum[:])

	id := s.queryID(`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1, $2, $3) RETURNING id`, sha, size, sha)
	if s.blobs == nil {
		s.blobs = map[string]BlobWrite{}
	}

	s.blobs[id] = BlobWrite{SHA256: sha, Key: sha, Size: size}

	return id
}

// write stores one package of the given format through the generic writer: one
// primary file at path holding blobID. It returns what was written.
func (s *quotaSeeder) write(format, name, version, path, blobID string) *PackageWritten {
	s.tb.Helper()

	return s.writeFiles(&PackageWrite{
		RepoID: s.repo, Format: format, Name: name, NameKey: name, Version: version, VersionKey: version,
		Files: []FileWrite{{Path: path, Filename: path, Blob: s.blobs[blobID]}},
	})
}

func (s *quotaSeeder) writeFiles(w *PackageWrite) *PackageWritten {
	s.tb.Helper()

	got, err := s.pg.writePackage(context.Background(), w)
	if err != nil {
		s.tb.Fatalf("seed package %s %s: %v", w.Name, w.Version, err)
	}

	return got
}

func (s *quotaSeeder) helm(blobID string) {
	s.seq++
	name := fmt.Sprintf("chart-%d", s.seq)
	s.write("helm", name, "1.0.0", name+"-1.0.0.tgz", blobID)
}

func (s *quotaSeeder) maven(blobID string) {
	s.seq++
	name := fmt.Sprintf("g:a%d", s.seq)
	s.write("maven", name, "1.0", fmt.Sprintf("g/a%d/1.0/a-1.0.jar", s.seq), blobID)
}

func (s *quotaSeeder) rubygem(blobID string) {
	s.seq++
	name := fmt.Sprintf("gem-%d", s.seq)
	s.write("rubygems", name, "1.0.0", name+"-1.0.0.gem", blobID)
}

func (s *quotaSeeder) rpm(blobID string) {
	s.seq++
	name := fmt.Sprintf("rpm-%d", s.seq)
	s.writeFiles(&PackageWrite{
		RepoID: s.repo, Format: "rpm", Name: name, NameKey: name, Version: "1.0", VersionKey: "1.0|x86_64",
		Files: []FileWrite{{Path: name + "-1.0.x86_64.rpm", Filename: name + "-1.0.x86_64.rpm", Arch: "x86_64", Blob: s.blobs[blobID]}},
	})
}

func (s *quotaSeeder) deb(blobID string) {
	s.seq++
	name := fmt.Sprintf("deb-%d", s.seq)
	s.writeFiles(&PackageWrite{
		RepoID: s.repo, Format: "deb", Name: name, NameKey: name, Version: "1.0", VersionKey: "1.0|amd64",
		Files: []FileWrite{{
			Path: fmt.Sprintf("pool/main/%s_1.0_amd64.deb", name), Filename: name + "_1.0_amd64.deb",
			Arch: "amd64", Part: "dist", Blob: s.blobs[blobID],
		}},
	})
}

func (s *quotaSeeder) npm(blobID string) {
	s.seq++
	name := fmt.Sprintf("pkg-%d", s.seq)
	s.write("npm", name, "1.0.0", name+"/-/"+name+"-1.0.0.tgz", blobID)
}

// corrupt overwrites the repository's counters the way a crash with
// asynchronous commits (or a bad restore) could leave them.
func (s *quotaSeeder) corrupt(sizeBytes, artifacts int64) {
	s.tb.Helper()

	s.exec(`INSERT INTO storage_quotas (repository_id, current_size_bytes, current_artifact_count) VALUES ($1, $2, $3)
	        ON CONFLICT (repository_id) DO UPDATE SET current_size_bytes = EXCLUDED.current_size_bytes,
	                                                 current_artifact_count = EXCLUDED.current_artifact_count`,
		s.repo, sizeBytes, artifacts)
}

func requireUsage(tb testing.TB, pg *PostgresDB, repoID string, wantBytes, wantCount int64) {
	tb.Helper()

	b, c, found := quotaUsage(tb, pg, repoID)
	if !found || b != wantBytes || c != wantCount {
		tb.Fatalf("usage = %d bytes / %d artifacts (row found %v), want %d / %d", b, c, found, wantBytes, wantCount)
	}
}

func TestReconcileStorageUsageFixesCorruptedCounter(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()

	// One package per quota-enforced format, with distinct sizes so a wrong
	// table or a missed join shows up in the sum. The deb and rpm rows share a
	// blob: dedup stores it once, but each row reserved its full size.
	s := newQuotaSeeder(t, pg, "all-formats", "raw")
	shared := s.blob(7)

	s.helm(s.blob(10))
	s.maven(s.blob(20))
	s.rubygem(s.blob(30))
	s.rpm(shared)
	s.deb(shared)
	s.npm(s.blob(60))

	const wantBytes, wantCount = 10 + 20 + 30 + 7 + 7 + 60, 6

	// Real limits must survive the recount.
	q, err := pg.GetOrCreateStorageQuota(ctx, s.repo)
	if err != nil {
		t.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	q.MaxSizeBytes, q.MaxArtifactCount, q.WarnThresholdPercent = 5000, 50, 70
	if err := pg.UpdateStorageQuota(ctx, q); err != nil {
		t.Fatalf("UpdateStorageQuota: %v", err)
	}

	tests := []struct {
		name      string
		bytes     int64
		artifacts int64
	}{
		{"counter far too high", 999999, 99},
		{"counter back at zero (lost reservations after a crash)", 0, 0},
		{"bytes right, count wrong", wantBytes, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s.corrupt(tc.bytes, tc.artifacts)

			got, err := pg.ReconcileStorageUsage(ctx, s.repo)
			if err != nil {
				t.Fatalf("ReconcileStorageUsage: %v", err)
			}

			if got.CurrentSizeBytes != wantBytes || got.CurrentArtifactCount != wantCount {
				t.Errorf("returned usage = %d / %d, want %d / %d", got.CurrentSizeBytes, got.CurrentArtifactCount, wantBytes, wantCount)
			}

			requireUsage(t, pg, s.repo, wantBytes, wantCount)

			if got.MaxSizeBytes != 5000 || got.MaxArtifactCount != 50 || got.WarnThresholdPercent != 70 {
				t.Errorf(
					"limits changed by reconcile: %d bytes / %d artifacts / %d%%",
					got.MaxSizeBytes,
					got.MaxArtifactCount,
					got.WarnThresholdPercent,
				)
			}
		})
	}

	t.Run("reconciling correct counters changes nothing", func(t *testing.T) {
		changed, err := pg.ReconcileAllStorageUsage(ctx)
		if err != nil || changed != 0 {
			t.Fatalf("ReconcileAllStorageUsage = %d, %v; want 0, nil", changed, err)
		}

		requireUsage(t, pg, s.repo, wantBytes, wantCount)
	})
}

func TestReconcileStorageUsageScopesAndCounts(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()

	drifted := newQuotaSeeder(t, pg, "drifted", "helm")
	drifted.helm(drifted.blob(100))
	drifted.corrupt(1, 1)

	emptied := newQuotaSeeder(t, pg, "emptied", "helm") // rows all deleted, counter never released
	emptied.corrupt(5000, 8)

	noRow := newQuotaSeeder(t, pg, "restored", "helm") // rows but no quota row (restored backup)
	noRow.helm(noRow.blob(40))
	noRow.helm(noRow.blob(2))

	untouched := newQuotaSeeder(t, pg, "untouched", "helm") // no rows, no quota row
	correct := newQuotaSeeder(t, pg, "correct", "helm")
	correct.helm(correct.blob(9))
	correct.corrupt(9, 1)

	t.Run("one repository leaves the others alone", func(t *testing.T) {
		if _, err := pg.ReconcileStorageUsage(ctx, drifted.repo); err != nil {
			t.Fatalf("ReconcileStorageUsage: %v", err)
		}

		requireUsage(t, pg, drifted.repo, 100, 1)
		requireUsage(t, pg, emptied.repo, 5000, 8)

		if _, _, found := quotaUsage(t, pg, noRow.repo); found {
			t.Error("reconciling another repository created a quota row")
		}
	})

	t.Run("all repositories", func(t *testing.T) {
		changed, err := pg.ReconcileAllStorageUsage(ctx)
		if err != nil {
			t.Fatalf("ReconcileAllStorageUsage: %v", err)
		}

		// emptied is zeroed and the restored repository gets a row; the drifted
		// one was fixed above and the correct one never needed it.
		if changed != 2 {
			t.Errorf("corrected %d counters, want 2 (emptied, restored)", changed)
		}

		requireUsage(t, pg, emptied.repo, 0, 0)
		requireUsage(t, pg, noRow.repo, 42, 2)
		requireUsage(t, pg, correct.repo, 9, 1)
		requireUsage(t, pg, drifted.repo, 100, 1)

		if _, _, found := quotaUsage(t, pg, untouched.repo); found {
			t.Error("a repository without packages must not get a quota row")
		}
	})

	t.Run("a restored repository is unlimited and then enforced", func(t *testing.T) {
		q, err := pg.GetOrCreateStorageQuota(ctx, noRow.repo)
		if err != nil {
			t.Fatalf("GetOrCreateStorageQuota: %v", err)
		}

		if q.MaxSizeBytes != 0 || q.MaxArtifactCount != 0 {
			t.Fatalf("created quota has limits %d / %d, want unlimited", q.MaxSizeBytes, q.MaxArtifactCount)
		}

		q.MaxSizeBytes = 50
		if err := pg.UpdateStorageQuota(ctx, q); err != nil {
			t.Fatalf("UpdateStorageQuota: %v", err)
		}

		// 42 bytes are in use: 8 more fit, 9 do not.
		if ok, err := pg.ReserveStorageUsage(ctx, noRow.repo, 9, 1); err != nil || ok {
			t.Errorf("reserve 9 bytes = %v, %v; want refused (42 + 9 > 50)", ok, err)
		}

		if ok, err := pg.ReserveStorageUsage(ctx, noRow.repo, 8, 1); err != nil || !ok {
			t.Errorf("reserve 8 bytes = %v, %v; want accepted", ok, err)
		}
	})
}

// TestReconcileAllStorageUsageSerializesRuns shows that a second replica does
// not recount while one is running: it returns at once without touching
// anything, and counts again once the first is done.
func TestReconcileAllStorageUsageSerializesRuns(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()

	s := newQuotaSeeder(t, pg, "locked", "helm")
	s.helm(s.blob(25))
	s.corrupt(0, 0)

	// Another replica mid-run: it holds the advisory lock on a session.
	conn, err := pg.GetDB().Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(`+quotaReconcileLockKey+`)`); err != nil {
		t.Fatalf("take lock: %v", err)
	}

	changed, err := pg.ReconcileAllStorageUsage(ctx)
	if err != nil || changed != 0 {
		t.Fatalf("ReconcileAllStorageUsage while another run holds the lock = %d, %v; want 0, nil", changed, err)
	}

	requireUsage(t, pg, s.repo, 0, 0)

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock(`+quotaReconcileLockKey+`)`); err != nil {
		t.Fatalf("release lock: %v", err)
	}

	if changed, err := pg.ReconcileAllStorageUsage(ctx); err != nil || changed != 1 {
		t.Fatalf("ReconcileAllStorageUsage after the lock was released = %d, %v; want 1, nil", changed, err)
	}

	requireUsage(t, pg, s.repo, 25, 1)
}

// TestReleaseNBatchMatchesReserve covers the bookkeeping the cleanup worker
// relies on: n rows removed at once give back exactly what n uploads reserved.
func TestReleaseNBatchMatchesReserve(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()

	repoID := newQuotaRepo(t, pg, "batch", 0, 0)

	for _, size := range []int64{100, 250, 40} {
		if ok, err := pg.ReserveStorageUsage(ctx, repoID, size, 1); err != nil || !ok {
			t.Fatalf("reserve %d = %v, %v", size, ok, err)
		}
	}

	requireUsage(t, pg, repoID, 390, 3)

	if err := pg.DecrementStorageUsage(ctx, repoID, 350, 2); err != nil {
		t.Fatalf("DecrementStorageUsage: %v", err)
	}

	requireUsage(t, pg, repoID, 40, 1)
}
