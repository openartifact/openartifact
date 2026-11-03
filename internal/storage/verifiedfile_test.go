// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fileReader is a VerifiedFile over a file, like base.SpoolReader.
type fileReader struct {
	io.Reader

	path, sha string
	size      int64
}

func (f *fileReader) VerifiedFile() (string, string, int64, bool) {
	return f.path, f.sha, f.size, true
}

func newFileReader(t *testing.T, dir string, data []byte) *fileReader {
	t.Helper()

	path := filepath.Join(dir, "spool")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)

	return &fileReader{Reader: bytes.NewReader(data), path: path, sha: hex.EncodeToString(sum[:]), size: int64(len(data))}
}

func TestFilesystemPutVerifiedFileLinksInsteadOfCopying(t *testing.T) {
	t.Parallel()

	store := newTestFilesystemStore(t)
	data := bytes.Repeat([]byte("spooled upload "), 1000)
	src := newFileReader(t, t.TempDir(), data)

	id, err := store.Put(context.Background(), src, map[string]string{"sha256": src.sha, "format": "raw"})
	if err != nil || id != src.sha {
		t.Fatalf("Put = %q, %v", id, err)
	}

	blobPath, err := store.getBlobPath(id)
	if err != nil {
		t.Fatal(err)
	}

	srcInfo, err := os.Stat(src.path)
	if err != nil {
		t.Fatal(err)
	}

	blobInfo, err := os.Stat(blobPath)
	if err != nil {
		t.Fatal(err)
	}

	// The test directories share one filesystem: no byte was written.
	if !os.SameFile(srcInfo, blobInfo) {
		t.Fatal("the blob is a copy of the spool, want a hard link")
	}

	// The spool's owner removes its name; the blob survives and reads back.
	if err := os.Remove(src.path); err != nil {
		t.Fatal(err)
	}

	rc, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := io.ReadAll(rc)
	_ = rc.Close()

	if !bytes.Equal(got, data) {
		t.Fatal("blob content differs after the spool was removed")
	}

	if meta, err := store.GetMetadata(context.Background(), id); err != nil || meta["format"] != "raw" || meta["_size"] != "15000" {
		t.Fatalf("metadata = %v, %v", meta, err)
	}

	if left := tempFilesIn(t, store.basePath); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestFilesystemPutVerifiedFileCopiesAcrossFilesystems(t *testing.T) {
	t.Parallel()

	shm, err := os.MkdirTemp("/dev/shm", "oa-verified-")
	if err != nil {
		t.Skip("no /dev/shm to stand in for another filesystem")
	}

	t.Cleanup(func() { _ = os.RemoveAll(shm) })

	store := newTestFilesystemStore(t)
	src := newFileReader(t, shm, []byte("on another filesystem"))

	blobPath, _ := store.getBlobPath(src.sha)
	if _, err := store.Put(context.Background(), src, map[string]string{"sha256": src.sha}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(blobPath)
	if err != nil || string(got) != "on another filesystem" {
		t.Fatalf("blob = %q, %v", got, err)
	}

	if left := tempFilesIn(t, store.basePath); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestFilesystemPutVerifiedFileDigestMismatch(t *testing.T) {
	t.Parallel()

	store := newTestFilesystemStore(t)
	src := newFileReader(t, t.TempDir(), []byte("real content"))
	other := sha256.Sum256([]byte("claimed content"))
	claimed := hex.EncodeToString(other[:])

	_, err := store.Put(context.Background(), src, map[string]string{"sha256": claimed})
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err = %v, want ErrDigestMismatch", err)
	}

	if ok, _ := store.Exists(context.Background(), claimed); ok {
		t.Fatal("a blob became visible under the claimed digest")
	}

	if ok, _ := store.Exists(context.Background(), src.sha); ok {
		t.Fatal("a rejected upload became visible")
	}
}

func TestFilesystemPutVerifiedFileKeepsExistingBlob(t *testing.T) {
	t.Parallel()

	store := newTestFilesystemStore(t)
	src := newFileReader(t, t.TempDir(), []byte("already stored"))

	if _, err := store.Put(
		context.Background(),
		bytes.NewReader([]byte("already stored")),
		map[string]string{"owner": "first"},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Put(context.Background(), src, map[string]string{"sha256": src.sha, "owner": "second"}); err != nil {
		t.Fatal(err)
	}

	blobPath, _ := store.getBlobPath(src.sha)
	srcInfo, _ := os.Stat(src.path)
	blobInfo, _ := os.Stat(blobPath)

	if os.SameFile(srcInfo, blobInfo) {
		t.Fatal("an existing blob was replaced by the spool")
	}

	if meta, _ := store.GetMetadata(context.Background(), src.sha); meta["owner"] != "first" {
		t.Fatalf("metadata = %v, the first sidecar must stay", meta)
	}
}

func TestOpenVerifiedUsesTheFileWithoutCopying(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	src := newFileReader(t, t.TempDir(), []byte("body"))

	f, size, release, err := openVerified(src, src.sha, spoolDir, "x-*.tmp")
	if err != nil {
		t.Fatal(err)
	}

	if f.Name() != src.path || size != 4 {
		t.Fatalf("opened %q size %d, want the source file", f.Name(), size)
	}

	if entries, _ := os.ReadDir(spoolDir); len(entries) != 0 {
		t.Fatal("a second spool was created")
	}

	release()

	if _, err := os.Stat(src.path); err != nil {
		t.Fatal("release removed the caller's file")
	}
}

func TestOpenVerifiedFallsBackToSpooling(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()

	if _, _, _, err := openVerified(bytes.NewReader([]byte("plain reader")), "", spoolDir, "x-*.tmp"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("an empty claimed digest verified: %v", err)
	}

	sum := sha256.Sum256([]byte("plain reader"))

	f, size, release, err := openVerified(bytes.NewReader([]byte("plain reader")), hex.EncodeToString(sum[:]), spoolDir, "x-*.tmp")
	if err != nil || size != 12 {
		t.Fatalf("openVerified = %d, %v", size, err)
	}

	name := f.Name()
	release()

	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("the private spool outlived release")
	}
}

func TestCountingReaderCountsFileTakenOver(t *testing.T) {
	t.Parallel()

	src := newFileReader(t, t.TempDir(), []byte("12345"))
	cr := &countingReader{r: src}

	if _, _, _, ok := verifiedFileOf(cr); !ok {
		t.Fatal("countingReader hides the file")
	}

	if cr.written() != 5 {
		t.Fatalf("written = %d, want 5", cr.written())
	}
}

func TestSpoolDirFor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	spool := filepath.Join(root, FilesystemSpoolDir)

	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(spool, "oa-spool-stale")
	fresh := filepath.Join(spool, "oa-spool-fresh")

	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if got := SpoolDirFor(StorageConfig{Type: "filesystem", Path: root}); got != spool {
		t.Fatalf("filesystem spool dir = %q, want %q", got, spool)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale spool survived the startup sweep")
	}

	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a recent spool was swept")
	}

	own := filepath.Join(root, "own")
	if got := SpoolDirFor(StorageConfig{Type: "s3", SpoolPath: own}); got != own {
		t.Fatalf("s3 spool dir = %q, want %q", got, own)
	}

	if got := SpoolDirFor(StorageConfig{Type: "s3"}); got != "" {
		t.Fatalf("s3 without a spool path = %q, want the OS temp dir", got)
	}
}
