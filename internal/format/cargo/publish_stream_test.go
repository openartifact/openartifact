// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"net/http"
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
)

const publishMeta = `{"name":"demo","vers":"0.1.0"}`

// framedPublish frames meta and a crate declared as declared bytes long.
func framedPublish(crate []byte, declared uint32) *bytes.Buffer {
	var body bytes.Buffer

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(publishMeta)))
	body.WriteString(publishMeta)
	_ = binary.Write(&body, binary.LittleEndian, declared)
	body.Write(crate)

	return &body
}

// bigCrate is a valid crate over the spool's in-memory threshold, with an
// incompressible payload so the archive really is that large.
func bigCrate(t *testing.T) []byte {
	t.Helper()

	payload := make([]byte, 3<<20)
	rand.NewChaCha8([32]byte{3}).Read(payload)

	return archivetest.TarGz(t,
		archivetest.File("demo-0.1.0/Cargo.toml", "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n"),
		archivetest.File("demo-0.1.0/data.bin", string(payload)),
	)
}

func spoolFilesIn(t *testing.T, dir string) int {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	return len(entries)
}

// A crate too big for memory is spooled to disk, stored byte for byte under its
// SHA-256, and leaves no temp file behind.
func TestPublishLargeCrateIsStoredIntact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	h, db, bs, _ := newCargoHandler(t)
	crate := bigCrate(t)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/api/v1/crates/new", framedPublish(crate, uint32(len(crate)))))
	if w.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", w.Code, w.Body.String())
	}

	sum := sha256.Sum256(crate)
	want := hex.EncodeToString(sum[:])

	stored := bs.Blobs[want]
	if !bytes.Equal(stored, crate) {
		t.Fatalf("stored blob under %s differs from the upload (%d vs %d bytes)", want, len(stored), len(crate))
	}

	if n := spoolFilesIn(t, dir); n != 0 {
		t.Errorf("%d spool files left behind", n)
	}

	if len(db.crates) != 1 || db.crates[0].SizeBytes != int64(len(crate)) {
		t.Errorf("crate rows = %+v", db.crates)
	}
}

// A body that ends before the declared crate length is a bad request and
// stores nothing, whether the crate fits in memory or was already on disk.
func TestPublishTruncatedCrateStoresNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	for name, crate := range map[string][]byte{"small": plainCrate(t), "large": bigCrate(t)} {
		t.Run(name, func(t *testing.T) {
			h, db, bs, pub := newCargoHandler(t)

			// Declare 100 bytes more than are sent.
			w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/api/v1/crates/new", framedPublish(crate, uint32(len(crate))+100)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}

			if len(bs.Blobs) != 0 || len(pub.Events()) != 0 || len(db.crates) != 0 {
				t.Error("a truncated upload stored something")
			}

			if n := spoolFilesIn(t, dir); n != 0 {
				t.Errorf("%d spool files left behind", n)
			}
		})
	}
}
