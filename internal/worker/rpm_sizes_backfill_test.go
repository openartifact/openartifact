// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/rpmextra"
	"github.com/openartifact/openartifact/internal/storage"
)

// TestBackfillRepairsInstalledAndArchiveSize: rows stored before the header
// size tags were read hold 0 installed/archive size, which dnf shows as a zero
// footprint. The regeneration re-reads the blob and persists the real sizes,
// even for rows whose weak dependencies were already backfilled.
func TestBackfillRepairsInstalledAndArchiveSize(t *testing.T) {
	data, err := os.ReadFile("../../testdata/rpm/sizes-v4.rpm")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	if _, err := store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	db := &weakDepsDB{}
	w := NewRPMetadataWorker(db, store, nil, testLogger(t), nil)
	pkgs := []rpmextra.Stored{
		{Name: "oa-sizes-test", SHA256: sha, WeakDepsParsed: true},
		{Name: "done", SHA256: "x", InstalledSize: 5, WeakDepsParsed: true, SizesParsed: true},
	}

	w.backfillFromBlob(context.Background(), "repo1", pkgs)

	if pkgs[0].InstalledSize != 100012 || pkgs[0].ArchiveSize != 100696 || !pkgs[0].SizesParsed {
		t.Errorf("sizes not backfilled: installed=%d archive=%d parsed=%v", pkgs[0].InstalledSize, pkgs[0].ArchiveSize, pkgs[0].SizesParsed)
	}

	if pkgs[1].InstalledSize != 5 {
		t.Errorf("a row with sizes was re-read: installed=%d", pkgs[1].InstalledSize)
	}

	if db.sha != sha || !bytes.Contains(db.extra, []byte(`"sizes_parsed":true`)) ||
		!bytes.Contains(db.extra, []byte(`"installed_size":100012`)) {
		t.Errorf("extra not persisted with sizes: %q %s", db.sha, db.extra)
	}
}
