// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"compress/gzip"
	"testing"

	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// ---- compression ----

func TestCompressMetadataStream_Valid(t *testing.T) {
	data := []byte("<root><item>hello</item></root>")

	meta, err := compressXML(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(meta.GzipData) == 0 {
		t.Error("GzipData should not be empty")
	}

	if len(meta.XZData) == 0 {
		t.Error("XZData should not be empty")
	}

	if meta.GzSHA256 == "" {
		t.Error("GzSHA256 should not be empty")
	}

	if meta.XZSHA256 == "" {
		t.Error("XZSHA256 should not be empty")
	}

	if meta.OpenSize != int64(len(data)) {
		t.Errorf("OpenSize = %d, want %d", meta.OpenSize, len(data))
	}
	// Verify gzip decompression round-trips
	r, err := gzip.NewReader(bytes.NewReader(meta.GzipData))
	if err != nil {
		t.Fatalf("gzip NewReader: %v", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("gzip read: %v", err)
	}

	if !bytes.Equal(buf.Bytes(), data) {
		t.Error("gzip decompressed data does not match original")
	}
}

// ---- ValidateCompsXML ----

func TestValidateCompsXML_Valid(t *testing.T) {
	xml := []byte(`<comps><group><id>core</id><name>Core</name></group></comps>`)
	if err := ValidateCompsXML(xml); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateCompsXML_Empty(t *testing.T) {
	// Empty bytes: no tokens → EOF → nil
	if err := ValidateCompsXML([]byte{}); err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
}

func TestValidateCompsXML_Invalid(t *testing.T) {
	if err := ValidateCompsXML([]byte(`<broken<<`)); err == nil {
		t.Fatal("expected error on invalid XML")
	}
}

// ---- GenerateRepomdXML ----

func TestGenerateRepomdXML_Empty(t *testing.T) {
	data, err := GenerateRepomdXML("rev1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("rev1")) {
		t.Error("output should contain revision")
	}

	if !bytes.Contains(data, []byte("repomd")) {
		t.Error("output should contain repomd element")
	}
}

func TestGenerateRepomdXML_WithEntries(t *testing.T) {
	entries := []RepomdData{
		{
			Type:         "primary",
			Location:     "repodata/primary.xml.gz",
			Checksum:     "deadbeef",
			ChecksumType: "sha256",
			Timestamp:    1_700_000_000,
			Size:         512,
			OpenSize:     2048,
		},
	}

	data, err := GenerateRepomdXML("42", entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("primary")) {
		t.Error("output should contain 'primary' data type")
	}

	if !bytes.Contains(data, []byte("deadbeef")) {
		t.Error("output should contain checksum")
	}
}

// ---- WritePrimaryXML ----

func makeTestPackage() pkgrpm.Package {
	return pkgrpm.Package{
		Name:        "testpkg",
		Version:     "1.0",
		Release:     "1",
		Arch:        "x86_64",
		Summary:     "A test package",
		Description: "Description of test package",
		License:     "MIT",
		PackageSize: 4096,
		SHA256:      "aabbccdd",
	}
}

func TestGeneratePrimaryXML_Valid(t *testing.T) {
	pkg := makeTestPackage()

	data, err := renderXML(WritePrimaryXML, []pkgrpm.Package{pkg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("testpkg")) {
		t.Error("output should contain package name")
	}

	if !bytes.Contains(data, []byte("metadata")) {
		t.Error("output should contain metadata element")
	}
}

func TestGeneratePrimaryXML_Empty(t *testing.T) {
	data, err := renderXML(WritePrimaryXML, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(data) == 0 {
		t.Error("output should not be empty even for no packages")
	}
}

// ---- WriteFilelistsXML ----

func TestGenerateFilelistsXML_Valid(t *testing.T) {
	pkg := makeTestPackage()
	pkg.Files = []pkgrpm.FileEntry{{Path: "/usr/bin/testpkg"}}

	data, err := renderXML(WriteFilelistsXML, []pkgrpm.Package{pkg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("filelists")) {
		t.Error("output should contain filelists element")
	}
}

// ---- WriteOtherXML ----

func TestGenerateOtherXML_Valid(t *testing.T) {
	pkg := makeTestPackage()
	pkg.Changelogs = []pkgrpm.ChangelogEntry{{Author: "dev@example.com", Description: "- init"}}

	data, err := renderXML(WriteOtherXML, []pkgrpm.Package{pkg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("otherdata")) {
		t.Error("output should contain otherdata element")
	}
}
