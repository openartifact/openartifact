// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestReleaseFileGenerator(t *testing.T) {
	gen := NewReleaseFileGenerator("TestOrigin", "TestLabel", "focal", "20.04")
	gen.AddArchitecture("amd64")
	gen.AddArchitecture("i386")
	gen.AddComponent("main")
	gen.AddComponent("universe")

	files := []FileInfo{
		{
			Filename: "dists/focal/main/binary-amd64/Packages.gz",
			Size:     1024,
			MD5sum:   "abc123",
			SHA1:     "def456",
			SHA256:   "ghi789",
		},
	}

	release := gen.GenerateRelease(files)

	if !strings.Contains(release, "Origin: TestOrigin") {
		t.Errorf("Release file missing Origin")
	}

	if !strings.Contains(release, "Suite: focal") {
		t.Errorf("Release file missing Suite")
	}

	if !strings.Contains(release, "amd64") {
		t.Errorf("Release file missing architecture")
	}

	if !strings.Contains(release, "main") {
		t.Errorf("Release file missing component")
	}

	if !strings.Contains(release, "MD5Sum:") {
		t.Errorf("Release file missing MD5Sum section")
	}
}

func TestPackagesFileGenerator(t *testing.T) {
	gen := NewPackagesFileGenerator()

	entry1 := "Package: test1\nVersion: 1.0\n\n"
	entry2 := "Package: test2\nVersion: 2.0\n\n"

	gen.AddEntry(entry1)
	gen.AddEntry(entry2)

	content := gen.Generate()

	if !strings.Contains(content, "Package: test1") {
		t.Errorf("Packages file missing test1")
	}

	if !strings.Contains(content, "Package: test2") {
		t.Errorf("Packages file missing test2")
	}
}

func TestPackagesFileGeneratorGzipped(t *testing.T) {
	gen := NewPackagesFileGenerator()
	gen.AddEntry("Package: test\nVersion: 1.0\n\n")

	gzipped, err := gen.GenerateGzipped()
	if err != nil {
		t.Fatalf("GenerateGzipped() error = %v", err)
	}

	// Verify it's valid gzip
	gzReader, err := gzip.NewReader(bytes.NewReader(gzipped))
	if err != nil {
		t.Fatalf("Failed to create gzip reader: %v", err)
	}
	defer func() { _ = gzReader.Close() }()

	data, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("Failed to read gzipped data: %v", err)
	}

	if !strings.Contains(string(data), "Package: test") {
		t.Errorf("Gzipped content missing package")
	}
}

func TestCalculateChecksums(t *testing.T) {
	data := []byte("test data")

	md5sum, sha1sum, sha256sum := CalculateChecksums(data)

	if md5sum == "" {
		t.Errorf("MD5sum is empty")
	}

	if sha1sum == "" {
		t.Errorf("SHA1sum is empty")
	}

	if sha256sum == "" {
		t.Errorf("SHA256sum is empty")
	}

	// Verify they're different
	if md5sum == sha1sum || sha1sum == sha256sum {
		t.Errorf("Checksums should be different")
	}
}

func TestReleaseFileGeneratorAddDuplicates(t *testing.T) {
	gen := NewReleaseFileGenerator("Origin", "Label", "suite", "codename")

	gen.AddArchitecture("amd64")
	gen.AddArchitecture("amd64") // duplicate
	gen.AddComponent("main")
	gen.AddComponent("main") // duplicate

	if len(gen.Architectures) != 1 {
		t.Errorf("Duplicate architecture added")
	}

	if len(gen.Components) != 1 {
		t.Errorf("Duplicate component added")
	}
}
