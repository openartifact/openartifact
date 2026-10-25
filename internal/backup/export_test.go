// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

func shaOf(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

func newExporter(t *testing.T, store *fakeBlobStore, snap database.SnapshotReader) *exporter {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	return &exporter{Engine: NewEngine(nil, store, log, t.TempDir()), db: snap}
}

// blobEntries lists the blobs/ entries of a tar stream.
func blobEntries(t *testing.T, raw []byte) []string {
	t.Helper()

	var out []string

	tr := tar.NewReader(bytes.NewReader(raw))

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}

		if err != nil {
			t.Fatalf("read archive: %v", err)
		}

		if name, ok := strings.CutPrefix(h.Name, "blobs/"); ok {
			out = append(out, name)
		}
	}
}

// TestExportBlobs_PaginatesUntilExhausted: the blobs table is walked page by
// page; a table slightly larger than a page must not be truncated at the page.
func TestExportBlobs_PaginatesUntilExhausted(t *testing.T) {
	const total = blobPageSize + 5

	store := newFakeBlobStore(total)
	x := newExporter(t, store, &database.LiveSnapshot{Blobs: store.rows()})

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	manifest := &BackupManifest{TableCounts: map[string]int{}}

	if err := x.exportBlobs(context.Background(), tw, manifest); err != nil {
		t.Fatalf("exportBlobs: %v", err)
	}

	_ = tw.Close()

	if got := len(blobEntries(t, buf.Bytes())); got != total {
		t.Fatalf("archive holds %d blobs, want %d", got, total)
	}

	if manifest.TableCounts["blobs"] != total || manifest.BlobCount != total {
		t.Errorf("manifest counts = %d / %d, want %d", manifest.TableCounts["blobs"], manifest.BlobCount, total)
	}
}

// TestExportBlobs_FailsOnMissingBytes: a row whose bytes are gone fails the
// export instead of producing an archive that cannot be restored.
func TestExportBlobs_FailsOnMissingBytes(t *testing.T) {
	store := newFakeBlobStore(3)
	rows := store.rows()
	delete(store.data, rows[1].SHA256)

	x := newExporter(t, store, &database.LiveSnapshot{Blobs: rows})

	err := x.exportBlobs(context.Background(), tar.NewWriter(io.Discard), &BackupManifest{TableCounts: map[string]int{}})
	if !errors.Is(err, ErrBlobMissing) || !strings.Contains(err.Error(), rows[1].SHA256) {
		t.Fatalf("exportBlobs = %v, want ErrBlobMissing naming %s", err, rows[1].SHA256)
	}
}

// TestExportBlobs_FailsOnCorruptBytes: bytes that do not hash to the blob id
// (bit rot, a wrong object) are detected while streaming.
func TestExportBlobs_FailsOnCorruptBytes(t *testing.T) {
	for name, mutate := range map[string]func(data []byte) []byte{
		"flipped byte": func(d []byte) []byte { d[0] ^= 0xff; return d },
		"truncated":    func(d []byte) []byte { return d[:len(d)-2] },
		"extended":     func(d []byte) []byte { return append(d, "extra"...) },
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeBlobStore(2)
			rows := store.rows()
			store.data[rows[0].SHA256] = mutate(append([]byte{}, store.data[rows[0].SHA256]...))

			x := newExporter(t, store, &database.LiveSnapshot{Blobs: rows})

			err := x.exportBlobs(context.Background(), tar.NewWriter(io.Discard), &BackupManifest{TableCounts: map[string]int{}})
			if !errors.Is(err, ErrBlobCorrupt) {
				t.Fatalf("exportBlobs = %v, want ErrBlobCorrupt", err)
			}
		})
	}
}

// shortSnapshot claims one more blob than it will ever list.
type shortSnapshot struct{ *database.LiveSnapshot }

func (s shortSnapshot) CountBlobs(ctx context.Context) (int64, error) {
	n, err := s.LiveSnapshot.CountBlobs(ctx)

	return n + 1, err
}

// TestExportBlobs_FailsWhenArchiveHasFewerBlobsThanCounted guards the
// manifest's promise: fewer blobs than the snapshot counted is an error.
func TestExportBlobs_FailsWhenArchiveHasFewerBlobsThanCounted(t *testing.T) {
	store := newFakeBlobStore(2)
	x := newExporter(t, store, shortSnapshot{&database.LiveSnapshot{Blobs: store.rows()}})

	err := x.exportBlobs(context.Background(), tar.NewWriter(io.Discard), &BackupManifest{TableCounts: map[string]int{}})
	if !errors.Is(err, ErrBlobCountMismatch) {
		t.Fatalf("exportBlobs = %v, want ErrBlobCountMismatch", err)
	}
}

// TestRunBackup_ArchivesBlobRowsNotStorageListing: a full backup contains the
// blobs the snapshot's table lists (with their manifest count and snapshot
// time), ignores bytes no row references, and ends the snapshot.
func TestRunBackup_ArchivesBlobRowsNotStorageListing(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB("src")
	eng, store := newTestEngine(t, db)

	for _, content := range []string{"first", "second"} {
		if _, err := store.Put(ctx, strings.NewReader(content), map[string]string{"sha256": shaOf(content)}); err != nil {
			t.Fatal(err)
		}

		db.addBlobRow(shaOf(content), int64(len(content)))
	}

	// Bytes without a blobs row (an orphan awaiting cleanup) are not backed up.
	if _, err := store.Put(ctx, strings.NewReader("orphan"), map[string]string{"sha256": shaOf("orphan")}); err != nil {
		t.Fatal(err)
	}

	job := &models.BackupJob{ID: "job-rows", Status: "pending", Type: "full"}
	if err := db.CreateBackupJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := eng.RunBackup(ctx, job.ID, "full"); err != nil {
		t.Fatalf("RunBackup: %v (%s)", err, db.jobs[job.ID].ErrorMessage)
	}

	manifest, blobs := readArchive(t, db.jobs[job.ID].StoragePath)

	if len(blobs) != 2 || blobs[shaOf("first")] != "first" || blobs[shaOf("second")] != "second" {
		t.Errorf("archive blobs = %v, want exactly first and second", blobs)
	}

	if manifest.BlobCount != 2 || manifest.TableCounts["blobs"] != 2 {
		t.Errorf("manifest blob counts = %d / %d, want 2", manifest.BlobCount, manifest.TableCounts["blobs"])
	}

	if manifest.SnapshotAt.IsZero() {
		t.Error("manifest has no snapshot_at")
	}

	if db.snapshots != 1 || db.released != 1 {
		t.Errorf("snapshots taken/released = %d/%d, want 1/1", db.snapshots, db.released)
	}
}

// TestRunBackup_FailureReleasesSnapshotAndFailsJob: a blob that cannot be read
// fails the job with the reason and still ends the snapshot (so deferred
// deletions are not held back by a dead backup).
func TestRunBackup_FailureReleasesSnapshotAndFailsJob(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB("src")
	eng, _ := newTestEngine(t, db)

	db.addBlobRow(shaOf("never stored"), 12)

	job := &models.BackupJob{ID: "job-missing", Status: "pending", Type: "full"}
	if err := db.CreateBackupJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := eng.RunBackup(ctx, job.ID, "full"); !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("RunBackup = %v, want ErrBlobMissing", err)
	}

	got := db.jobs[job.ID]
	if got.Status != "failed" || !strings.Contains(got.ErrorMessage, shaOf("never stored")) {
		t.Errorf("job = %q / %q, want failed naming the blob", got.Status, got.ErrorMessage)
	}

	if db.released != 1 {
		t.Errorf("snapshot released %d times, want 1", db.released)
	}
}

// TestRunBackup_SnapshotFailureFailsJob: no snapshot, no backup (and nothing to release).
func TestRunBackup_SnapshotFailureFailsJob(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB("src")
	db.snapshotErr = errors.New("connection refused")
	eng, _ := newTestEngine(t, db)

	job := &models.BackupJob{ID: "job-nosnap", Status: "pending", Type: "metadata-only"}
	if err := db.CreateBackupJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := eng.RunBackup(ctx, job.ID, "metadata-only"); !errors.Is(err, db.snapshotErr) {
		t.Fatalf("RunBackup = %v, want the snapshot error", err)
	}

	if got := db.jobs[job.ID]; got.Status != "failed" || !strings.Contains(got.ErrorMessage, "take database snapshot") {
		t.Errorf("job = %q / %q, want failed with a snapshot message", got.Status, got.ErrorMessage)
	}
}

// TestRunBackup_MetadataOnlySkipsBlobs: a metadata-only backup still runs on a
// snapshot but archives no blobs.
func TestRunBackup_MetadataOnlySkipsBlobs(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB("src")
	eng, store := newTestEngine(t, db)

	if _, err := store.Put(ctx, strings.NewReader("data"), map[string]string{"sha256": shaOf("data")}); err != nil {
		t.Fatal(err)
	}

	db.addBlobRow(shaOf("data"), 4)

	job := &models.BackupJob{ID: "job-meta", Status: "pending", Type: "metadata-only"}
	if err := db.CreateBackupJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := eng.RunBackup(ctx, job.ID, "metadata-only"); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	if _, blobs := readArchive(t, db.jobs[job.ID].StoragePath); len(blobs) != 0 {
		t.Errorf("metadata-only archive holds blobs: %v", blobs)
	}

	if db.snapshots != 1 || db.released != 1 {
		t.Errorf("snapshots taken/released = %d/%d, want 1/1", db.snapshots, db.released)
	}
}

// readArchive returns the manifest and the blobs (sha -> content) of a backup file.
func readArchive(t *testing.T, path string) (BackupManifest, map[string]string) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip archive: %v", err)
	}

	var manifest BackupManifest

	blobs := map[string]string{}

	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return manifest, blobs
		}

		if err != nil {
			t.Fatalf("read archive: %v", err)
		}

		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", h.Name, err)
		}

		if name, ok := strings.CutPrefix(h.Name, "blobs/"); ok {
			blobs[name] = string(body)
		}

		if h.Name == "manifest.json" {
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatalf("decode manifest: %v", err)
			}
		}
	}
}

// TestRunBackup_CancelledExportIsDiscarded: a backup cancelled mid-export
// (server shutdown) fails its job and leaves no partial archive behind.
func TestRunBackup_CancelledExportIsDiscarded(t *testing.T) {
	db := newFakeDB("src")
	eng, store := newTestEngine(t, db)

	if _, err := store.Put(t.Context(), strings.NewReader("payload"), map[string]string{"sha256": shaOf("payload")}); err != nil {
		t.Fatal(err)
	}

	db.addBlobRow(shaOf("payload"), int64(len("payload")))

	job := &models.BackupJob{ID: "job-cancelled", Status: "pending", Type: "full"}
	if err := db.CreateBackupJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := eng.RunBackup(ctx, job.ID, "full"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunBackup = %v, want context.Canceled", err)
	}

	if got := db.jobs[job.ID]; got.Status != "failed" || got.StoragePath != "" {
		t.Errorf("job = %q (path %q), want failed without an archive", got.Status, got.StoragePath)
	}

	left, err := os.ReadDir(eng.backupDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(left) != 0 {
		t.Errorf("backup dir holds %d leftover file(s), want none", len(left))
	}
}
