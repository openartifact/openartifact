// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/internal/worker"
)

// deletionRig is a real PostgreSQL plus a filesystem store, a backup engine and
// a blob deletion sweeper over them. The grace period is zero unless a test
// raises it, so what keeps bytes alive is the backup lease, not the clock.
type deletionRig struct {
	t       *testing.T
	pg      *database.PostgresDB
	store   storage.BlobStore
	dir     string
	sweeper *worker.BlobDeletionSweeper
}

func newDeletionRig(t *testing.T) *deletionRig {
	t.Helper()

	r := &deletionRig{t: t, pg: freshPostgres(t, 20), dir: t.TempDir()}

	var err error

	r.store, err = storage.NewFilesystemStore(filepath.Join(r.dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	r.sweeper = r.newSweeper(r.store, 50)

	return r
}

type byteDeleter interface {
	Delete(ctx context.Context, sha256 string) error
	DeleteCopy(ctx context.Context, backendID, sha256 string) error
}

// plainDeleter adapts a single store (no tiers) to the sweeper's interface.
type plainDeleter struct{ storage.BlobStore }

func (p plainDeleter) DeleteCopy(ctx context.Context, _, sha string) error { return p.Delete(ctx, sha) }

func (r *deletionRig) newSweeper(store any, batch int) *worker.BlobDeletionSweeper {
	r.t.Helper()

	var bytesStore byteDeleter

	switch s := store.(type) {
	case byteDeleter:
		bytesStore = s
	case storage.BlobStore:
		bytesStore = plainDeleter{s}
	default:
		r.t.Fatalf("unsupported store %T", store)
	}

	return worker.NewBlobDeletionSweeper(r.pg, bytesStore, newTestLogger(r.t), 0, batch)
}

// upload stores content and records its blobs row, like an upload does.
func (r *deletionRig) upload(content string) string {
	r.t.Helper()

	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	if _, err := r.store.Put(context.Background(), strings.NewReader(content), map[string]string{"sha256": sha}); err != nil {
		r.t.Fatalf("put: %v", err)
	}

	if _, err := r.pg.UpsertBlob(context.Background(), sha, sha, int64(len(content))); err != nil {
		r.t.Fatalf("upsert blob: %v", err)
	}

	return sha
}

// remove deletes a blob the way deleting its last package does.
func (r *deletionRig) remove(sha string) {
	r.t.Helper()

	if err := storage.SafeDeleteBlob(context.Background(), r.pg, r.store, sha); err != nil {
		r.t.Fatalf("SafeDeleteBlob: %v", err)
	}
}

func (r *deletionRig) exists(sha string) bool {
	r.t.Helper()

	ok, err := r.store.Exists(context.Background(), sha)
	if err != nil {
		r.t.Fatalf("exists: %v", err)
	}

	return ok
}

func (r *deletionRig) sweep(sweeper *worker.BlobDeletionSweeper) worker.BlobDeletionStats {
	r.t.Helper()

	st, err := sweeper.RunOnce(context.Background())
	if err != nil {
		r.t.Fatalf("sweep: %v", err)
	}

	return st
}

func (r *deletionRig) engine(store storage.BlobStore) *backup.Engine {
	return backup.NewEngine(r.pg, store, newTestLogger(r.t), filepath.Join(r.dir, "backups"))
}

// runFullBackup runs a full backup and returns its archive's blobs (sha -> content).
func (r *deletionRig) runFullBackup(store storage.BlobStore) (map[string]string, backup.BackupManifest) {
	r.t.Helper()

	ctx := context.Background()
	job := &models.BackupJob{Status: "pending", Type: "full"}

	if err := r.pg.CreateBackupJob(ctx, job); err != nil {
		r.t.Fatal(err)
	}

	if err := r.engine(store).RunBackup(ctx, job.ID, "full"); err != nil {
		r.t.Fatalf("RunBackup: %v", err)
	}

	done, err := r.pg.GetBackupJob(ctx, job.ID)
	if err != nil || done.Status != "completed" {
		r.t.Fatalf("backup job = %+v, %v", done, err)
	}

	return readBackupArchive(r.t, done.StoragePath)
}

func readBackupArchive(t *testing.T, path string) (map[string]string, backup.BackupManifest) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	blobs := map[string]string{}

	var manifest backup.BackupManifest

	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return blobs, manifest
		}

		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}

		if name, ok := strings.CutPrefix(h.Name, "blobs/"); ok {
			blobs[name] = string(body)
		}

		if h.Name == "manifest.json" {
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// sweepQueuedBlobs runs the blob deletion sweeper once. Deleting a blob only
// queues its bytes; with grace 0 and no running backup everything queued is due.
func sweepQueuedBlobs(t *testing.T, pg *database.PostgresDB, store storage.BlobStore) {
	t.Helper()

	sw := worker.NewBlobDeletionSweeper(pg, plainDeleter{store}, newTestLogger(t), 0, 500)

	if _, err := sw.RunOnce(context.Background()); err != nil {
		t.Fatalf("sweep queued blob deletions: %v", err)
	}
}

// getHookStore runs hook once, right before the first blob is read: that is
// after the backup took its database snapshot, i.e. "while the backup runs".
type getHookStore struct {
	storage.BlobStore

	once sync.Once
	hook func()
}

func (s *getHookStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	s.once.Do(s.hook)

	return s.BlobStore.Get(ctx, id)
}

// (a) A blob deleted while a backup runs is still in the archive, and its bytes
// are removed only once the backup has ended.
func TestBackupKeepsBlobsDeletedWhileItRuns(t *testing.T) {
	r := newDeletionRig(t)

	contents := []string{"first blob", "second blob", "third blob"}
	shas := make([]string, len(contents))

	for i, c := range contents {
		shas[i] = r.upload(c)
	}

	var duringBackup worker.BlobDeletionStats

	hooked := &getHookStore{BlobStore: r.store}
	hooked.hook = func() {
		for _, sha := range shas {
			r.remove(sha)
		}

		// A sweeper pass in the middle of the backup must not touch the bytes.
		duringBackup = r.sweep(r.sweeper)
	}

	blobs, manifest := r.runFullBackup(hooked)

	if duringBackup.Deleted != 0 || duringBackup.Pending != int64(len(shas)) {
		t.Errorf("sweep during the backup = %+v, want all %d deletions held", duringBackup, len(shas))
	}

	for i, sha := range shas {
		if blobs[sha] != contents[i] {
			t.Errorf("blob %q deleted during the backup is missing or wrong in the archive (got %q)", contents[i], blobs[sha])
		}
	}

	if manifest.BlobCount != len(shas) || manifest.SnapshotAt.IsZero() {
		t.Errorf("manifest = blob_count %d snapshot_at %v, want %d blobs and a snapshot time", manifest.BlobCount, manifest.SnapshotAt, len(shas))
	}

	// The backup has ended and released its lease: the sweep may now delete.
	if st := r.sweep(r.sweeper); st.Deleted != len(shas) || st.Pending != 0 {
		t.Fatalf("sweep after the backup = %+v, want %d deleted and an empty queue", st, len(shas))
	}

	for _, sha := range shas {
		if r.exists(sha) {
			t.Errorf("bytes of %s still stored after the sweep", sha[:8])
		}
	}
}

// (b) A blob moved to another tier while a backup runs is still archived (it is
// read through the tiered store), and the retired copy survives until the
// backup ended.
func TestBackupKeepsBlobsMovedBetweenTiersWhileItRuns(t *testing.T) {
	r := newDeletionRig(t)
	ctx := context.Background()
	now := time.Now()

	hotCfg := storage.StorageConfig{Type: "filesystem", Path: filepath.Join(r.dir, "blobs")}
	coldDir := filepath.Join(r.dir, "cold")
	coldID, coldTier := uuid.New().String(), uuid.New().String()
	coldCfg, _ := json.Marshal(map[string]string{"path": coldDir})

	if err := r.pg.CreateStorageBackend(ctx, &models.StorageBackend{
		ID: coldID, Name: "cold-fs", Kind: "filesystem", Config: coldCfg, Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.pg.CreateStorageTier(ctx, &models.StorageTier{ID: coldTier, Name: "cold", BackendID: coldID, Rank: 100}); err != nil {
		t.Fatal(err)
	}

	reg := tiering.NewRegistry(r.pg, r.store, hotCfg, nil)
	if err := reg.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	tiered := tiering.NewStore(reg, r.pg, nil, nil, nil)
	mover := tiering.NewMover(r.pg, reg, tiered, nil, tiering.MoverConfig{VerifyReadback: true}, nil, nil)
	sweeper := r.newSweeper(tiered, 50)

	hotSHA := r.upload("stays hot")
	movedSHA := r.upload("moves to cold during the backup")

	onHot := func(sha string) bool {
		_, err := os.Stat(filepath.Join(r.dir, "blobs", sha[:2], sha[2:]))
		return err == nil
	}

	var duringBackup worker.BlobDeletionStats

	hooked := &getHookStore{BlobStore: tiered}
	hooked.hook = func() {
		if out, err := mover.Move(ctx, movedSHA, coldID, "test"); err != nil || out != tiering.OutcomeMoved {
			t.Errorf("Move = %v, %v", out, err)
		}

		duringBackup = r.sweep(sweeper)
	}

	blobs, _ := r.runFullBackup(hooked)

	if blobs[hotSHA] != "stays hot" || blobs[movedSHA] != "moves to cold during the backup" {
		t.Errorf("archive blobs = %v, want both blobs with their content", blobs)
	}

	if duringBackup.Deleted != 0 || duringBackup.Pending != 1 {
		t.Errorf("sweep during the backup = %+v, want the retired primary copy held", duringBackup)
	}

	if !onHot(movedSHA) {
		t.Error("old primary copy was removed before the sweep")
	}

	if st := r.sweep(sweeper); st.Deleted != 1 {
		t.Fatalf("sweep after the backup = %+v, want the old copy retired", st)
	}

	if onHot(movedSHA) {
		t.Error("old primary copy still stored after the sweep")
	}

	if rc, err := tiered.Get(ctx, movedSHA); err != nil {
		t.Errorf("moved blob unreadable after the sweep: %v", err)
	} else {
		_ = rc.Close()
	}
}

// insertLease registers a backup lease the way a (soon to crash) backup process
// does, with the given snapshot (NULL = not taken yet).
func insertLease(t *testing.T, pg *database.PostgresDB, withSnapshot bool) string {
	t.Helper()

	id := uuid.New().String()

	query := `INSERT INTO backup_leases (id, snapshot, expires_at) VALUES ($1, NULL, now() + interval '10 minutes')`
	if withSnapshot {
		query = `INSERT INTO backup_leases (id, snapshot, expires_at) VALUES ($1, pg_current_snapshot()::text, now() + interval '10 minutes')`
	}

	if _, err := pg.GetDB().ExecContext(context.Background(), query, id); err != nil {
		t.Fatalf("insert lease: %v", err)
	}

	return id
}

// (c) The lease of a crashed backup stops holding deletions once it expires.
func TestCrashedBackupLeaseExpiresAndSweepProceeds(t *testing.T) {
	for name, withSnapshot := range map[string]bool{"died after its snapshot": true, "died before its snapshot": false} {
		t.Run(name, func(t *testing.T) {
			r := newDeletionRig(t)
			ctx := context.Background()

			insertLease(t, r.pg, withSnapshot)

			sha := r.upload("held by a dead backup")
			r.remove(sha)

			if st := r.sweep(r.sweeper); st.Deleted != 0 || st.Pending != 1 || !r.exists(sha) {
				t.Fatalf("with a live lease: %+v exists=%v, want the deletion held", st, r.exists(sha))
			}

			// The backup process is gone: its heartbeat stops and the lease runs out.
			if _, err := r.pg.GetDB().ExecContext(ctx, `UPDATE backup_leases SET expires_at = now() - interval '1 second'`); err != nil {
				t.Fatal(err)
			}

			st := r.sweep(r.sweeper)
			if st.Deleted != 1 || st.Pending != 0 || r.exists(sha) {
				t.Fatalf("after the lease expired: %+v exists=%v, want the bytes deleted", st, r.exists(sha))
			}

			var leases int

			if err := r.pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM backup_leases`).Scan(&leases); err != nil || leases != 0 {
				t.Errorf("expired lease not purged: %d rows (err %v)", leases, err)
			}
		})
	}
}

// A live backup renews its lease while it runs and removes it when done.
func TestBackupSnapshotRenewsAndReleasesItsLease(t *testing.T) {
	r := newDeletionRig(t)
	ctx := context.Background()

	snap, err := r.pg.BeginBackupSnapshot(ctx, 3*time.Second)
	if err != nil {
		t.Fatalf("BeginBackupSnapshot: %v", err)
	}

	expiry := func() time.Time {
		var at time.Time

		if err := r.pg.GetDB().QueryRowContext(ctx, `SELECT expires_at FROM backup_leases`).Scan(&at); err != nil {
			t.Fatalf("read lease: %v", err)
		}

		return at
	}

	first := expiry()
	deadline := time.Now().Add(15 * time.Second)

	for !expiry().After(first) {
		if time.Now().After(deadline) {
			t.Fatal("lease was never renewed by the heartbeat")
		}

		time.Sleep(100 * time.Millisecond)
	}

	sha := r.upload("blob deleted during a long backup")
	r.remove(sha)

	if st := r.sweep(r.sweeper); st.Deleted != 0 || !r.exists(sha) {
		t.Fatalf("sweep with a live snapshot = %+v, want the deletion held", st)
	}

	snap.Release()
	snap.Release() // idempotent

	if st := r.sweep(r.sweeper); st.Deleted != 1 || r.exists(sha) {
		t.Fatalf("sweep after release = %+v, want the bytes deleted", st)
	}
}

// (d) A blob uploaded again during the grace period keeps its bytes.
func TestReuploadDuringGraceKeepsBytes(t *testing.T) {
	r := newDeletionRig(t)
	ctx := context.Background()

	r.pg.SetBlobDeletionGrace(time.Hour)

	sha := r.upload("uploaded, deleted, uploaded again")
	r.remove(sha)

	if st := r.sweep(r.sweeper); st.Deleted != 0 || st.Pending != 1 || !r.exists(sha) {
		t.Fatalf("within the grace period: %+v exists=%v, want nothing deleted", st, r.exists(sha))
	}

	// The same bytes are uploaded again: a new blobs row, the stored object is reused.
	r.upload("uploaded, deleted, uploaded again")

	// The grace period passes.
	if _, err := r.pg.GetDB().ExecContext(ctx, `UPDATE blob_deletions SET not_before = now()`); err != nil {
		t.Fatal(err)
	}

	st := r.sweep(r.sweeper)
	if st.Kept != 1 || st.Deleted != 0 || st.Pending != 0 || !r.exists(sha) {
		t.Fatalf("after grace with a live re-upload: %+v exists=%v, want the bytes kept and the entry dropped", st, r.exists(sha))
	}

	// Deleting it once more starts a fresh grace period.
	r.remove(sha)

	if st := r.sweep(r.sweeper); st.Deleted != 0 || st.Pending != 1 || !r.exists(sha) {
		t.Fatalf("second delete: %+v exists=%v, want a new grace period", st, r.exists(sha))
	}
}

// With grace 0 the bytes go as soon as no backup holds them.
func TestZeroGraceDeletesOnceNoBackupHoldsTheBlob(t *testing.T) {
	r := newDeletionRig(t)

	sha := r.upload("immediate")
	r.remove(sha)

	if !r.exists(sha) {
		t.Fatal("SafeDeleteBlob removed the bytes itself instead of queueing them")
	}

	if st := r.sweep(r.sweeper); st.Deleted != 1 || r.exists(sha) {
		t.Fatalf("sweep = %+v exists=%v, want the bytes deleted", st, r.exists(sha))
	}
}

// countingStore counts byte deletions per blob.
type countingStore struct {
	storage.BlobStore

	mu     sync.Mutex
	counts map[string]int
}

func (s *countingStore) Delete(ctx context.Context, sha string) error {
	s.mu.Lock()
	s.counts[sha]++
	s.mu.Unlock()

	return s.BlobStore.Delete(ctx, sha)
}

func (s *countingStore) DeleteCopy(ctx context.Context, _, sha string) error {
	return s.Delete(ctx, sha)
}

// (e) Two sweepers (two replicas) draining the same queue never process a
// deletion twice.
func TestConcurrentSweepersDoNotDoubleProcess(t *testing.T) {
	r := newDeletionRig(t)

	const n = 120

	store := &countingStore{BlobStore: r.store, counts: map[string]int{}}
	shas := make([]string, n)

	for i := range n {
		shas[i] = r.upload(fmt.Sprintf("blob %03d", i))
		r.remove(shas[i])
	}

	var (
		wg    sync.WaitGroup
		stats [2]worker.BlobDeletionStats
	)

	for i := range stats {
		wg.Go(func() {
			var err error

			stats[i], err = r.newSweeper(store, 7).RunOnce(context.Background())
			if err != nil {
				t.Errorf("sweeper %d: %v", i, err)
			}
		})
	}

	wg.Wait()

	if got := stats[0].Deleted + stats[1].Deleted; got != n {
		t.Errorf("sweepers deleted %d + %d = %d, want %d", stats[0].Deleted, stats[1].Deleted, got, n)
	}

	for _, sha := range shas {
		if c := store.counts[sha]; c != 1 {
			t.Errorf("blob %s deleted %d times, want exactly once", sha[:8], c)
		}

		if r.exists(sha) {
			t.Errorf("bytes of %s still stored", sha[:8])
		}
	}

	if left, err := r.pg.CountBlobDeletions(context.Background()); err != nil || left != 0 {
		t.Errorf("queue left %d (err %v)", left, err)
	}
}

// A backup of a database with an unrelated, slower deletion path: the archive
// holds exactly the rows of its snapshot even when more blobs appear meanwhile.
func TestBackupArchivesOnlyBlobsOfItsSnapshot(t *testing.T) {
	r := newDeletionRig(t)

	before := r.upload("present at the snapshot")

	var late string

	hooked := &getHookStore{BlobStore: r.store}
	hooked.hook = func() { late = r.upload("uploaded after the snapshot") }

	blobs, manifest := r.runFullBackup(hooked)

	if blobs[before] == "" || manifest.BlobCount != 1 {
		t.Errorf("archive = %v (blob_count %d), want only the blob present at the snapshot", blobs, manifest.BlobCount)
	}

	if _, ok := blobs[late]; ok {
		t.Error("a blob uploaded after the snapshot leaked into the archive")
	}

	if len(blobs) != 1 {
		t.Errorf("archive holds %d blobs, want 1", len(blobs))
	}
}

// ReclaimMetadataBlob frees generated metadata, which has no blobs row. When a
// blobs row of byte-identical content was deleted while a backup runs, the
// reclaim must not remove the bytes under that backup: they are queued like any
// other deleted blob and removed only after the lease ends.
func TestReclaimMetadataBlobKeepsBytesOfARunningBackupsBlob(t *testing.T) {
	r := newDeletionRig(t)
	ctx := context.Background()

	sha := r.upload("identical to a generated metadata file")

	snap, err := r.pg.BeginBackupSnapshot(ctx, time.Minute)
	if err != nil {
		t.Fatalf("BeginBackupSnapshot: %v", err)
	}

	defer snap.Release()

	// The snapshot lists the blobs row; it is deleted afterwards, then the
	// metadata worker reclaims the same content (no row, no reference left).
	r.remove(sha)

	if err := storage.ReclaimMetadataBlob(ctx, r.pg, r.store, sha); err != nil {
		t.Fatalf("ReclaimMetadataBlob: %v", err)
	}

	if !r.exists(sha) {
		t.Fatal("ReclaimMetadataBlob removed bytes a running backup's snapshot still lists")
	}

	if st := r.sweep(r.sweeper); st.Deleted != 0 || !r.exists(sha) {
		t.Fatalf("sweep during the backup = %+v exists=%v, want the bytes held", st, r.exists(sha))
	}

	snap.Release()

	if st := r.sweep(r.sweeper); st.Deleted != 1 || r.exists(sha) {
		t.Fatalf("sweep after the backup = %+v exists=%v, want the bytes deleted", st, r.exists(sha))
	}
}

// A metadata blob that a repository_metadata row references again (a concurrent
// regeneration of the same content) keeps its bytes however the queue runs.
func TestReclaimMetadataBlobKeepsBytesReferencedAgain(t *testing.T) {
	r := newDeletionRig(t)
	ctx := context.Background()

	sha := r.upload("regenerated metadata")
	r.remove(sha) // no blobs row any more, bytes queued

	system := models.SystemProjectID
	repo := &models.Repository{Name: "meta-repo", Format: "maven", RepoType: "hosted", ProjectID: &system}
	if err := r.pg.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	if _, err := r.pg.GetDB().ExecContext(ctx,
		`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, 'g/a/maven-metadata.xml', $2, 20)`,
		repo.ID, sha); err != nil {
		t.Fatalf("insert repository_metadata: %v", err)
	}

	if err := storage.ReclaimMetadataBlob(ctx, r.pg, r.store, sha); err != nil {
		t.Fatalf("ReclaimMetadataBlob: %v", err)
	}

	if st := r.sweep(r.sweeper); st.Deleted != 0 || st.Kept != 1 || !r.exists(sha) {
		t.Fatalf("sweep = %+v exists=%v, want the referenced metadata blob kept", st, r.exists(sha))
	}
}
