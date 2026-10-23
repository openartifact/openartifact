// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/storage"
)

const trashTestOrgID = "00000000-0000-0000-0000-000000000001"

// newTrashPurgeTestDB creates an empty database on the TEST_DATABASE_URL
// server, applies the embedded migrations and returns a PostgresDB over it with
// a 7 day default trash retention. The test is skipped (or failed under CI)
// when TEST_DATABASE_URL is not set.
func newTrashPurgeTestDB(t *testing.T) *database.PostgresDB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	dbName := fmt.Sprintf("oa_trashpurge_%d", time.Now().UnixNano())

	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host:         u.Hostname(),
		Port:         port,
		User:         u.User.Username(),
		Password:     pw,
		Database:     dbName,
		SSLMode:      sslmode,
		MaxOpenConns: 8,
		MaxIdleConns: 8,
	}, testLogger(t))
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	pg.SetTrashRetentionDays(7)

	return pg
}

// purgeSeed inserts maven rows and blobs straight into the tables, the way an
// upload leaves them, together with the blob bytes in an in-memory store.
type purgeSeed struct {
	t     *testing.T
	pg    *database.PostgresDB
	blobs *formattest.BlobStore
	seq   int
}

func newPurgeSeed(t *testing.T) *purgeSeed {
	t.Helper()

	return &purgeSeed{t: t, pg: newTrashPurgeTestDB(t), blobs: formattest.NewBlobStore()}
}

func (s *purgeSeed) exec(query string, args ...any) {
	s.t.Helper()

	if _, err := s.pg.GetDB().ExecContext(context.Background(), query, args...); err != nil {
		s.t.Fatalf("exec %q: %v", query, err)
	}
}

func (s *purgeSeed) count(query string, args ...any) int {
	s.t.Helper()

	var n int
	if err := s.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		s.t.Fatalf("query %q: %v", query, err)
	}

	return n
}

func (s *purgeSeed) repo(name string) string {
	s.t.Helper()

	org := trashTestOrgID

	repo := &models.Repository{Name: name, Format: "maven", RepoType: "hosted", ProjectID: &org}
	if err := s.pg.CreateRepository(context.Background(), repo); err != nil {
		s.t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	return repo.ID
}

// blob stores content in the blob store and its blobs row (created age ago) and
// returns the row id and the sha256.
func (s *purgeSeed) blob(content string, age time.Duration) (string, string) {
	s.t.Helper()

	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	if _, err := s.blobs.Put(context.Background(), bytes.NewReader([]byte(content)), map[string]string{"sha256": sha}); err != nil {
		s.t.Fatalf("put blob: %v", err)
	}

	var id string
	if err := s.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key, created_at)
		 VALUES ($1, $2, $3, now() - make_interval(secs => $4)) RETURNING id`,
		sha, len(content), sha, age.Seconds()).Scan(&id); err != nil {
		s.t.Fatalf("insert blob row: %v", err)
	}

	return id, sha
}

// maven inserts a maven row of repoID pointing at blobID and returns its id.
func (s *purgeSeed) maven(repoID, blobID string) string {
	s.t.Helper()

	s.seq++

	var id string
	if err := s.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id)
		 VALUES ($1, 'g', $2, '1.0', 'jar', $3, $4) RETURNING id`,
		repoID, fmt.Sprintf("a%d", s.seq), fmt.Sprintf("g/a%d/1.0/a%d-1.0.jar", s.seq, s.seq), blobID).Scan(&id); err != nil {
		s.t.Fatalf("insert maven row: %v", err)
	}

	return id
}

// trash soft-deletes the row and back-dates the deletion by ago.
func (s *purgeSeed) trash(repoID, id string, ago time.Duration) {
	s.t.Helper()

	soft, err := s.pg.TrashRows(context.Background(), repoID, models.TrashKindMaven, []string{id}, "alice")
	if err != nil || !soft {
		s.t.Fatalf("TrashRows = %v, %v; want a soft delete", soft, err)
	}

	s.exec(`UPDATE maven_artifacts SET deleted_at = now() - make_interval(secs => $2) WHERE id = $1`, id, ago.Seconds())
}

func (s *purgeSeed) rowExists(id string) bool {
	s.t.Helper()

	return s.count(`SELECT COUNT(*) FROM maven_artifacts WHERE id = $1`, id) == 1
}

func (s *purgeSeed) blobRowExists(sha string) bool {
	s.t.Helper()

	return s.count(`SELECT COUNT(*) FROM blobs WHERE sha256 = $1`, sha) == 1
}

// copyDeleter lets a plain blob store serve as the sweeper's byte store.
type copyDeleter struct{ storage.BlobStore }

func (c copyDeleter) DeleteCopy(ctx context.Context, _, sha string) error { return c.Delete(ctx, sha) }

// sweep runs the blob deletion sweeper once: deleting a blob only queues its
// bytes (grace 0 and no running backup here, so everything queued is due).
func (s *purgeSeed) sweep() {
	s.t.Helper()

	sw := NewBlobDeletionSweeper(s.pg, copyDeleter{s.blobs}, testLogger(s.t), 0, 500)

	if _, err := sw.RunOnce(context.Background()); err != nil {
		s.t.Fatalf("sweep queued blob deletions: %v", err)
	}
}

func (s *purgeSeed) storedBlob(sha string) bool {
	s.t.Helper()

	s.sweep()

	ok, err := s.blobs.Exists(context.Background(), sha)
	if err != nil {
		s.t.Fatalf("exists: %v", err)
	}

	return ok
}

func (s *purgeSeed) worker() (*TrashPurgeWorker, *formattest.Publisher) {
	pub := &formattest.Publisher{}
	w := NewTrashPurgeWorker(s.pg, s.blobs, testLogger(s.t), 0)
	w.SetPublisher(pub)

	return w, pub
}

const trashDay = 24 * time.Hour

func TestTrashPurgeIntegrationPurgesRowsPastRetention(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()

	repoA, repoB := s.repo("purge-a"), s.repo("purge-b")

	uniqueID, uniqueSHA := s.blob("only the expired row uses this", 2*time.Hour)
	sharedID, sharedSHA := s.blob("shared with a live row of another repository", 2*time.Hour)
	recentID, recentSHA := s.blob("belongs to a row that is still in its retention window", 2*time.Hour)

	expiredUnique := s.maven(repoA, uniqueID)
	expiredShared := s.maven(repoA, sharedID)
	recent := s.maven(repoA, recentID)
	liveElsewhere := s.maven(repoB, sharedID)

	// Quota as the uploads left it: every row counted until its purge.
	reservePurgeQuota(t, s, repoA, uniqueID, sharedID, recentID)

	s.trash(repoA, expiredUnique, 8*trashDay)
	s.trash(repoA, expiredShared, 8*trashDay)
	s.trash(repoA, recent, trashDay)

	w, pub := s.worker()

	stats, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if stats.Purged != 2 || stats.Failed != 0 || stats.Busy {
		t.Fatalf("stats = %+v, want 2 purged", stats)
	}

	for name, id := range map[string]string{"unique": expiredUnique, "shared": expiredShared} {
		if s.rowExists(id) {
			t.Errorf("expired %s row survived the purge", name)
		}
	}

	if !s.rowExists(recent) || !s.rowExists(liveElsewhere) {
		t.Error("a live row (inside its window, or of another repository) vanished")
	}

	if s.storedBlob(uniqueSHA) || s.blobRowExists(uniqueSHA) {
		t.Error("blob only the purged row used was not freed")
	}

	for name, sha := range map[string]string{"shared": sharedSHA, "unexpired": recentSHA} {
		if !s.storedBlob(sha) || !s.blobRowExists(sha) {
			t.Errorf("blob of the %s case was deleted though a live row references it", name)
		}
	}

	assertPurgeQuota(t, s, repoA, int64(len("belongs to a row that is still in its retention window")), 1)

	if events := pub.Events(); len(events) != 2 || events[0].Type != "artifact.purged" || events[0].RepoID != repoA {
		t.Errorf("webhook events = %+v, want two artifact.purged for repository A", events)
	}

	// A second pass has nothing left to do.
	if again, err := w.RunOnce(ctx); err != nil || again.Purged != 0 {
		t.Fatalf("second pass = %+v, %v; want a no-op", again, err)
	}
}

// reservePurgeQuota counts every blob as one quota-enforced upload of repoID.
func reservePurgeQuota(t *testing.T, s *purgeSeed, repoID string, blobIDs ...string) {
	t.Helper()

	for _, id := range blobIDs {
		var size int64
		if err := s.pg.GetDB().QueryRowContext(context.Background(), `SELECT size_bytes FROM blobs WHERE id = $1`, id).Scan(&size); err != nil {
			t.Fatal(err)
		}

		if ok, err := s.pg.ReserveStorageUsage(context.Background(), repoID, size, 1); err != nil || !ok {
			t.Fatalf("ReserveStorageUsage = %v, %v", ok, err)
		}
	}
}

func assertPurgeQuota(t *testing.T, s *purgeSeed, repoID string, wantSize int64, wantCount int) {
	t.Helper()

	var size int64

	var count int

	if err := s.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT current_size_bytes, current_artifact_count FROM storage_quotas WHERE repository_id = $1`, repoID,
	).Scan(&size, &count); err != nil {
		t.Fatalf("read quota usage: %v", err)
	}

	if size != wantSize || count != wantCount {
		t.Errorf("quota usage = (%d bytes, %d artifacts), want (%d, %d): the purge must release what it removed",
			size, count, wantSize, wantCount)
	}
}

// assertTrashedRepoKeptInsideWindow runs a pass while the repository is still
// inside its retention window and checks that nothing is removed.
func assertTrashedRepoKeptInsideWindow(t *testing.T, s *purgeSeed, w *TrashPurgeWorker, repoID, rowID string) {
	t.Helper()

	if stats, err := w.RunOnce(context.Background()); err != nil || stats.Purged != 0 {
		t.Fatalf("early pass = %+v, %v; want nothing purged", stats, err)
	}

	if s.count(`SELECT COUNT(*) FROM repositories WHERE id = $1`, repoID) != 1 || !s.rowExists(rowID) {
		t.Fatal("a trashed repository inside its window lost data")
	}
}

func TestTrashPurgeIntegrationPurgesTrashedRepositoryWithItsContent(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()

	trashedRepo, otherRepo := s.repo("purge-doomed"), s.repo("purge-survivor")

	// Both blobs are old enough for the orphan sweep (storage.OrphanMinAge).
	ownID, ownSHA := s.blob("only the doomed repository uses this", 2*time.Hour)
	sharedID, sharedSHA := s.blob("the surviving repository uses this too", 2*time.Hour)

	ownRow := s.maven(trashedRepo, ownID)
	sharedRow := s.maven(trashedRepo, sharedID)
	liveElsewhere := s.maven(otherRepo, sharedID)

	if soft, err := s.pg.TrashRepository(ctx, trashedRepo, "carol"); err != nil || !soft {
		t.Fatalf("TrashRepository = %v, %v; want a soft delete", soft, err)
	}

	w, pub := s.worker()

	assertTrashedRepoKeptInsideWindow(t, s, w, trashedRepo, ownRow)

	s.exec(`UPDATE repositories SET deleted_at = now() - make_interval(secs => $2) WHERE id = $1`, trashedRepo, (8 * trashDay).Seconds())

	stats, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if stats.Repositories != 1 || stats.Purged != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want the repository purged", stats)
	}

	if s.count(`SELECT COUNT(*) FROM repositories WHERE id = $1`, trashedRepo) != 0 || s.rowExists(ownRow) || s.rowExists(sharedRow) {
		t.Error("repository or its content survived the purge")
	}

	if s.storedBlob(ownSHA) || s.blobRowExists(ownSHA) || stats.OrphansRemoved != 1 {
		t.Errorf("blob only the purged repository used was not collected (orphans removed = %d, want 1)", stats.OrphansRemoved)
	}

	if !s.storedBlob(sharedSHA) || !s.blobRowExists(sharedSHA) || !s.rowExists(liveElsewhere) {
		t.Error("blob shared with a surviving repository was deleted")
	}

	events := pub.Events()
	if len(events) != 1 || events[0].Type != "repository.purged" || events[0].Payload["repository"] != "purge-doomed" {
		t.Errorf("webhook events = %+v, want one repository.purged", events)
	}

	// The name is free again once the repository is gone.
	s.repo("purge-doomed")
}

func TestTrashPurgeIntegrationSkipsWhileAnotherReplicaHoldsTheLock(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()

	repo := s.repo("purge-locked")
	blobID, blobSHA := s.blob("waiting for the lock", 2*time.Hour)
	row := s.maven(repo, blobID)
	s.trash(repo, row, 8*trashDay)

	// Another replica's purge: a session holding the advisory lock.
	other, err := s.pg.GetDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = other.Close() }()

	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, trashPurgeLockKey); err != nil {
		t.Fatal(err)
	}

	w, _ := s.worker()

	stats, err := w.RunOnce(ctx)
	if err != nil || !stats.Busy || stats.Purged != 0 {
		t.Fatalf("RunOnce under a held lock = %+v, %v; want Busy and nothing purged", stats, err)
	}

	if !s.rowExists(row) || !s.storedBlob(blobSHA) {
		t.Fatal("the pass purged without holding the lock")
	}

	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, trashPurgeLockKey); err != nil {
		t.Fatal(err)
	}

	if stats, err = w.RunOnce(ctx); err != nil || stats.Purged != 1 || stats.Busy {
		t.Fatalf("RunOnce after the lock was released = %+v, %v; want 1 purged", stats, err)
	}

	// The pass released its own lock: the other session can take it again.
	var acquired bool
	if err := other.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, trashPurgeLockKey).Scan(&acquired); err != nil || !acquired {
		t.Fatalf("lock after the pass = %v, %v; the pass must release it", acquired, err)
	}
}
