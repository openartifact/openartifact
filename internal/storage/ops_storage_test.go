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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()

	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestOCIErrorBodyIsCapped(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<20))
	}))
	t.Cleanup(srv.Close)

	store, err := NewOCIBlobStore(StorageConfig{Endpoint: srv.URL, Bucket: "r", OCIInsecure: true})
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte("data"))

	_, err = store.Put(context.Background(), strings.NewReader("data"), map[string]string{"sha256": hex.EncodeToString(sum[:])})
	if err == nil {
		t.Fatal("want error")
	}

	if len(err.Error()) > ociErrBodyLimit+256 {
		t.Fatalf("error message is %d bytes, the body was not capped", len(err.Error()))
	}
}

func TestOCIInsecureFlagGatesPlainHTTP(t *testing.T) {
	t.Parallel()

	if _, err := NewOCIBlobStore(StorageConfig{Endpoint: "http://registry.local", Bucket: "r"}); !errors.Is(err, ErrOCIInsecureEndpoint) {
		t.Fatalf("err = %v, want ErrOCIInsecureEndpoint", err)
	}

	if _, err := NewOCIBlobStore(StorageConfig{Endpoint: "http://registry.local", Bucket: "r", OCIInsecure: true}); err != nil {
		t.Fatalf("insecure http: %v", err)
	}

	if _, err := NewOCIBlobStore(StorageConfig{Endpoint: "https://registry.local", Bucket: "r"}); err != nil {
		t.Fatalf("https: %v", err)
	}
}

func TestOCIRelayRequiresStagingPath(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]StorageConfig{
		"public url":   {PublicURL: "https://oa.example.com"},
		"from request": {PublicURLFromRequest: true},
	} {
		cfg.Endpoint, cfg.Bucket = "https://registry.local", "r"

		if _, err := NewOCIBlobStore(cfg); !errors.Is(err, ErrOCIStagingPathRequired) {
			t.Fatalf("%s: err = %v, want ErrOCIStagingPathRequired", name, err)
		}

		cfg.StagingPath = t.TempDir()

		if _, err := NewOCIBlobStore(cfg); err != nil {
			t.Fatalf("%s with stagingPath: %v", name, err)
		}
	}
}

type captureHandler struct {
	slog.Handler

	msgs *[]string
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	*h.msgs = append(*h.msgs, r.Message)

	return nil
}

func TestOpenRelayFailureIsLogged(t *testing.T) {
	// Not parallel: swaps the default logger.
	var msgs []string

	prev := slog.Default()

	slog.SetDefault(slog.New(captureHandler{Handler: slog.NewTextHandler(os.Stderr, nil), msgs: &msgs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if r := openRelay(filepath.Join(file, "staging"), "https://oa.example.com", false); r != nil {
		t.Fatal("relay must not open under a file")
	}

	if len(msgs) != 1 || !strings.Contains(msgs[0], "relayed uploads disabled") {
		t.Fatalf("log = %v, want one 'relayed uploads disabled' record", msgs)
	}
}

func TestFilesystemPutDedupeRestoresMissingSidecar(t *testing.T) {
	t.Parallel()

	fs, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	id, err := fs.Put(ctx, strings.NewReader("payload"), map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}

	blobPath, _ := fs.getBlobPath(id)
	if err := os.Remove(blobPath + ".meta"); err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Put(ctx, strings.NewReader("payload"), map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}

	md, err := fs.GetMetadata(ctx, id)
	if err != nil || md["k"] != "v" {
		t.Fatalf("metadata = %v, %v; want the sidecar restored", md, err)
	}
}

func TestFilesystemSweepsStaleShardTemps(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	shard := filepath.Join(base, "ab")

	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(shard, "blob-upload-meta-111.tmp")
	staleCopy := filepath.Join(shard, "blob-upload-222.tmp")
	fresh := filepath.Join(shard, "blob-upload-meta-333.tmp")

	for _, p := range []string{stale, staleCopy, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ageFile(t, stale, 2*time.Hour)
	ageFile(t, staleCopy, 2*time.Hour)

	if _, err := NewFilesystemStore(base); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{stale, staleCopy} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived the sweep", p)
		}
	}

	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp removed: %v", err)
	}
}

func TestSpoolDirSweptAndUsed(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	stale := map[string]bool{"s3-put-1.tmp": true, "azure-put-2.tmp": true, "oci-put-3.tmp": true, "other.tmp": false}

	for name := range stale {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		ageFile(t, p, 2*time.Hour)
	}

	got, err := prepareSpoolDir(dir)
	if err != nil || got != dir {
		t.Fatalf("prepareSpoolDir = %q, %v", got, err)
	}

	for name, swept := range stale {
		_, err := os.Stat(filepath.Join(dir, name))
		if swept != os.IsNotExist(err) {
			t.Fatalf("%s: swept=%v want %v (err %v)", name, os.IsNotExist(err), swept, err)
		}
	}

	// Put spools inside the configured dir (the OCI registry rejects, the
	// spool must still be created there and cleaned up).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entries, _ := os.ReadDir(dir)
		if len(entries) < 2 { // other.tmp + the live spool
			t.Errorf("spool file not created in %s", dir)
		}

		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	store, err := NewOCIBlobStore(StorageConfig{Endpoint: srv.URL, Bucket: "r", OCIInsecure: true, SpoolPath: dir})
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte("d"))
	_, _ = store.Put(context.Background(), strings.NewReader("d"), map[string]string{"sha256": hex.EncodeToString(sum[:])})
}

func TestRelayPartMarkerDroppedBeforeOverwrite(t *testing.T) {
	t.Parallel()

	r := newRelay(t.TempDir(), "https://oa.example.com")
	if r == nil {
		t.Fatal("relay not opened")
	}

	marker := r.markerPath(relayTestID, 1)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	meta := relayMeta{Size: 16, PartSize: 16, PartCount: 1}

	// A retry whose body dies mid-part: the old marker must not vouch for it.
	if _, err := r.writePart(relayTestID, 1, meta, 16, bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("want short-body error")
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stale marker survived an interrupted overwrite: %v", err)
	}
}
