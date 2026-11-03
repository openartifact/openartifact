// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestDebDistributionPublishesGzipOnly: the hosted index is built as Packages
// and Packages.gz; no xz is produced (the pure-Go encoder costs ten times the
// CPU of gzip for a few percent) and Release lists exactly what is stored.
func TestDebDistributionPublishesGzipOnly(t *testing.T) {
	db := &benchWorkerDB{
		components:    []models.DebianComponent{{Name: "main"}},
		architectures: []models.DebianArchitecture{{Name: "amd64"}},
	}
	w := &DebMetadataWorker{db: db, log: testLogger(t)}
	dist := models.DebianDistribution{ID: "dist-1", Name: "bookworm"}
	entries := []models.DebPackageEntry{{
		ID: "1", Name: "hello", Version: "1.0-1", Arch: "amd64", Filename: "hello_1.0-1_amd64.deb",
		Distribution: "bookworm", ComponentName: "main", SHA256: strings.Repeat("a", 64), Size: 1234,
		ControlFields: map[string]string{"Maintainer": "J <j@example.org>", "Description": "hello"},
	}}

	blobs := make(map[string][]byte)
	if err := w.generateDistribution(context.Background(), dist, entries, nil, blobs); err != nil {
		t.Fatal(err)
	}

	for path := range blobs {
		if strings.HasSuffix(path, ".xz") {
			t.Errorf("unexpected xz blob %s", path)
		}
	}

	release := string(blobs["dists/bookworm/Release"])
	if strings.Contains(release, ".xz") {
		t.Errorf("Release lists xz:\n%s", release)
	}

	const base = "main/binary-amd64/Packages"

	gz := blobs["dists/bookworm/"+base+".gz"]
	if len(gz) == 0 {
		t.Fatal("Packages.gz was not generated")
	}

	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}

	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(plain, blobs["dists/bookworm/"+base]) {
		t.Error("Packages.gz does not decompress to Packages")
	}

	sum := sha256.Sum256(gz)
	if !strings.Contains(release, hex.EncodeToString(sum[:])) {
		t.Error("Release does not list the checksum of Packages.gz")
	}
}
