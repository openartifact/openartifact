// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/gpg"
)

func pacmanRow(id, name, version, arch, repoArch string) *models.PacmanPackage {
	return &models.PacmanPackage{
		ID: id, Name: name, Base: name, Version: version, Arch: arch, RepoArch: repoArch,
		Filename: name + "-" + version + "-" + arch + ".pkg.tar.zst", SHA256: id, MD5: id, SizeBytes: 10,
	}
}

// tarMembers returns the member names of a gzip tar.
func tarMembers(t *testing.T, data []byte) []string {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	var names []string

	for tr := tar.NewReader(gz); ; {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			sort.Strings(names)

			return names
		}

		if err != nil {
			t.Fatal(err)
		}

		names = append(names, hdr.Name)
	}
}

func TestRenderPacmanDatabases(t *testing.T) {
	t.Parallel()

	pkgs := []*models.PacmanPackage{
		pacmanRow("a1", "tool", "1.0-1", "x86_64", "x86_64"),
		pacmanRow("a2", "tool", "1.10-1", "x86_64", "x86_64"),
		pacmanRow("a3", "tool", "1.9-1", "x86_64", "x86_64"),
		pacmanRow("b1", "docs", "2-1", "any", "x86_64"),
		pacmanRow("c1", "arm", "1-1", "aarch64", "aarch64"),
	}

	entity, err := gpg.GenerateEntity("t", "", "t@example.org", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := gpg.NewLocalSignerFromEntity(entity)
	if err != nil {
		t.Fatal(err)
	}

	blobs, err := renderPacmanDatabases(pkgs, map[string]string{"a2": "usr/bin/tool\nusr/share/tool"}, signer)
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	for p := range blobs {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	want := "os/aarch64/db.tar.gz,os/aarch64/db.tar.gz.sig,os/aarch64/files.tar.gz,os/aarch64/files.tar.gz.sig," +
		"os/x86_64/db.tar.gz,os/x86_64/db.tar.gz.sig,os/x86_64/files.tar.gz,os/x86_64/files.tar.gz.sig"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %s", got)
	}

	// Only the newest version of a name is listed (1.10 beats 1.9), and an
	// any package is listed in the database of every architecture.
	if got := strings.Join(
		tarMembers(t, blobs["os/x86_64/db.tar.gz"]),
		",",
	); got != "docs-2-1/,docs-2-1/desc,tool-1.10-1/,tool-1.10-1/desc" {
		t.Errorf("x86_64 db = %s", got)
	}

	if got := strings.Join(tarMembers(t, blobs["os/aarch64/db.tar.gz"]), ","); got != "arm-1-1/,arm-1-1/desc,docs-2-1/,docs-2-1/desc" {
		t.Errorf("aarch64 db = %s", got)
	}

	if got := tarMembers(t, blobs["os/x86_64/files.tar.gz"]); !strings.Contains(strings.Join(got, ","), "tool-1.10-1/files") {
		t.Errorf("x86_64 files db = %v", got)
	}

	// The detached signature (binary) verifies against the key.
	for _, path := range []string{"os/x86_64/db.tar.gz", "os/aarch64/files.tar.gz"} {
		if _, err := openpgp.CheckDetachedSignature(
			openpgp.EntityList{entity},
			bytes.NewReader(blobs[path]),
			bytes.NewReader(blobs[path+".sig"]),
			nil,
		); err != nil {
			t.Errorf("signature of %s: %v", path, err)
		}
	}

	unsigned, err := renderPacmanDatabases(pkgs, nil, nil)
	if err != nil || len(unsigned) != 4 {
		t.Errorf("unsigned: %d blobs, %v", len(unsigned), err)
	}
}

func TestPacmanEntriesPreferTheArchitectureOverAny(t *testing.T) {
	t.Parallel()

	got := pacmanEntries([]*models.PacmanPackage{
		pacmanRow("any", "tool", "1-1", "any", "x86_64"),
		pacmanRow("spec", "tool", "1-1", "x86_64", "x86_64"),
	}, nil, "x86_64")

	if len(got) != 1 || got[0].Arch != "x86_64" {
		t.Errorf("entries = %+v", got)
	}

	if got := pacmanEntries(nil, nil, "x86_64"); len(got) != 0 {
		t.Errorf("empty repository lists %d entries", len(got))
	}
}
