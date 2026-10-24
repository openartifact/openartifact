// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"testing"
	"testing/iotest"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

func uploadTempFiles(t *testing.T, dir string) int {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	return len(entries)
}

// A body too big for memory is spooled to disk, then stored byte for byte under
// the SHA-256 recorded on the row and sent as Put metadata; the spool file is
// gone when the request ends.
func TestUploadLargeBodyIsStoredIntact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	h, db, bs, _ := newTestHandler(t)

	body := make([]byte, 3<<20+17)
	rand.NewChaCha8([32]byte{4}).Read(body)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, artifactURL, bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])

	if db.art.SHA256 != want || db.art.SizeBytes != int64(len(body)) {
		t.Errorf("row sha256=%s size=%d, want %s/%d", db.art.SHA256, db.art.SizeBytes, want, len(body))
	}

	if !bytes.Equal(bs.Blobs[want], body) {
		t.Errorf("blob under %s differs from the upload", want)
	}

	if n := uploadTempFiles(t, dir); n != 0 {
		t.Errorf("%d spool files left behind", n)
	}
}

// A body that dies mid-transfer is a bad request; nothing is stored, recorded
// or left on disk.
func TestUploadTruncatedBodyStoresNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	for name, size := range map[string]int{"small": 1 << 10, "large": 3 << 20} {
		t.Run(name, func(t *testing.T) {
			h, db, bs, pub := newTestHandler(t)

			body := io.MultiReader(bytes.NewReader(make([]byte, size)), iotest.ErrReader(io.ErrUnexpectedEOF))

			w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, artifactURL, body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}

			if db.art != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
				t.Error("a truncated upload stored something")
			}

			if n := uploadTempFiles(t, dir); n != 0 {
				t.Errorf("%d spool files left behind", n)
			}
		})
	}
}
