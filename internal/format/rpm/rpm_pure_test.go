// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"strings"
	"testing"

	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// ---- CompressMetadata ----

func TestCompressMetadata_Empty(t *testing.T) {
	_, err := CompressMetadata([]byte{})
	if err == nil {
		t.Fatal("expected error on empty input")
	}
}

func TestCompressMetadata_Valid(t *testing.T) {
	data := []byte("<root><item>hello</item></root>")

	meta, err := CompressMetadata(data)
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

// ---- FormatTimestamp ----

func TestFormatTimestamp(t *testing.T) {
	ts := int64(0)
	got := FormatTimestamp(ts)

	want := "1970-01-01T00:00:00Z"
	if got != want {
		t.Errorf("FormatTimestamp(0) = %q, want %q", got, want)
	}

	ts = int64(1_700_000_000)

	got = FormatTimestamp(ts)
	if !strings.HasSuffix(got, "Z") {
		t.Errorf("FormatTimestamp should end in Z, got %q", got)
	}
}

// ---- GeneratePrestodeltaXML ----

func TestGeneratePrestodeltaXML_Empty(t *testing.T) {
	result, err := GeneratePrestodeltaXML(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != nil {
		t.Errorf("expected nil for empty input, got %d bytes", len(result))
	}
}

func TestGeneratePrestodeltaXML_OnePackage(t *testing.T) {
	pkgs := []DeltaRPM{
		{
			NewName:      "bash",
			NewVer:       "5.1",
			NewRel:       "1",
			NewArch:      "x86_64",
			OldVer:       "5.0",
			OldRel:       "1",
			Filename:     "bash-5.0-1_5.1-1.x86_64.drpm",
			Size:         1024,
			Checksum:     "abc123",
			ChecksumType: "sha256",
		},
	}

	data, err := GeneratePrestodeltaXML(pkgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("bash")) {
		t.Error("output should contain package name 'bash'")
	}

	if !bytes.Contains(data, []byte("prestodelta")) {
		t.Error("output should contain 'prestodelta' root element")
	}
	// Should be valid XML
	if err := xml.Unmarshal(data[len(xml.Header):], &PrestoDeltaXML{}); err != nil {
		t.Errorf("output is not valid XML: %v", err)
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

// ---- GeneratePrimaryXML ----

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

	data, err := GeneratePrimaryXML([]pkgrpm.Package{pkg}, "https://example.com/repo")
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
	data, err := GeneratePrimaryXML(nil, "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(data) == 0 {
		t.Error("output should not be empty even for no packages")
	}
}

// ---- GenerateFilelistsXML ----

func TestGenerateFilelistsXML_Valid(t *testing.T) {
	pkg := makeTestPackage()
	pkg.Files = []pkgrpm.FileEntry{{Path: "/usr/bin/testpkg"}}

	data, err := GenerateFilelistsXML([]pkgrpm.Package{pkg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("filelists")) {
		t.Error("output should contain filelists element")
	}
}

// ---- GenerateOtherXML ----

func TestGenerateOtherXML_Valid(t *testing.T) {
	pkg := makeTestPackage()
	pkg.Changelogs = []pkgrpm.ChangelogEntry{{Author: "dev@example.com", Description: "- init"}}

	data, err := GenerateOtherXML([]pkgrpm.Package{pkg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte("otherdata")) {
		t.Error("output should contain otherdata element")
	}
}

// The checksum is a child element with a plain type attribute; the former
// `xml:"checksum type,attr"` tag put the attribute in a "checksum" namespace.
func TestGeneratePrestodeltaXML_ChecksumElement(t *testing.T) {
	data, err := GeneratePrestodeltaXML([]DeltaRPM{{
		NewName: "bash", NewVer: "5.1", NewRel: "1", NewArch: "x86_64", OldVer: "5.0", OldRel: "1",
		Filename: "bash.drpm", Size: 1, Checksum: "abc123", ChecksumType: "sha256",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(data, []byte(`<checksum type="sha256">abc123</checksum>`)) {
		t.Errorf("checksum element not rendered as <checksum type=\"sha256\">: %s", data)
	}

	if bytes.Contains(data, []byte("xmlns")) {
		t.Errorf("output carries a namespace declaration: %s", data)
	}
}
