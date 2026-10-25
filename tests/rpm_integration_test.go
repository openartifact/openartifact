// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

func buildTestPackage() pkgrpm.Package {
	return pkgrpm.Package{
		Name:          "test-package",
		Version:       "1.0.0",
		Release:       "1.el9",
		Epoch:         2,
		Arch:          "x86_64",
		Summary:       "A test RPM package",
		Description:   "This is a test package for integration testing",
		License:       "MIT",
		URL:           "https://example.com/test-package",
		Vendor:        "TestVendor",
		Group:         "Development/Tools",
		Packager:      "Test Packager <test@example.com>",
		SourceRPM:     "test-package-1.0.0-1.el9.src.rpm",
		BuildHost:     "builder.example.com",
		BuildTime:     1700000000,
		PackageSize:   50000,
		InstalledSize: 100000,
		ArchiveSize:   200000,
		Provides: []pkgrpm.Dependency{
			{Name: "test-package", Flags: pkgrpm.SENSE_EQUAL, Version: "1.0.0-1.el9", Epoch: 2},
			{Name: "libtest", Flags: pkgrpm.SENSE_EQUAL, Version: "1.0.0", Epoch: 0},
		},
		Requires: []pkgrpm.Dependency{
			{Name: "libc", Flags: pkgrpm.SENSE_GREATEREQUAL, Version: "2.31", Epoch: 0},
			{Name: "libssl", Flags: pkgrpm.SENSE_GREATEREQUAL, Version: "3.0", Epoch: 0},
		},
		Files: []pkgrpm.FileEntry{
			{Path: "/usr/bin/test-app", Size: 50000, Mode: 0o755, MTime: 1700000000, Digest: "abc123"},
			{Path: "/etc/test.conf", Size: 256, Mode: 0o644, MTime: 1700000000},
		},
		Changelogs: []pkgrpm.ChangelogEntry{
			{Time: 1700000000, Author: "Test Packager <test@example.com>", Description: "Initial package"},
		},
	}
}

// renderRPMXML runs one of the formatrpm.Write*XML streamers into memory.
func renderRPMXML(t *testing.T, write func(io.Writer, []pkgrpm.Package) error, pkgs []pkgrpm.Package) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := write(&buf, pkgs); err != nil {
		t.Fatalf("render rpm xml: %v", err)
	}

	return buf.Bytes()
}

// compressRPMXML compresses an in-memory XML document the way the worker does.
func compressRPMXML(t *testing.T, data []byte) *formatrpm.CompressedMetadata {
	t.Helper()

	c, err := formatrpm.CompressMetadataStream(func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}, len(data))
	if err != nil {
		t.Fatalf("CompressMetadataStream() error: %v", err)
	}

	return c
}

func TestMetadataGenerationPipeline(t *testing.T) {
	pkg := buildTestPackage()
	packages := []pkgrpm.Package{pkg}

	primaryXML := renderRPMXML(t, formatrpm.WritePrimaryXML, packages)
	filelistsXML := renderRPMXML(t, formatrpm.WriteFilelistsXML, packages)
	otherXML := renderRPMXML(t, formatrpm.WriteOtherXML, packages)

	if !strings.Contains(string(primaryXML), "test-package") {
		t.Error("primary.xml missing package name")
	}

	if !strings.Contains(string(primaryXML), "1.0.0") {
		t.Error("primary.xml missing version")
	}

	if !strings.Contains(string(primaryXML), "x86_64") {
		t.Error("primary.xml missing arch")
	}

	if !strings.Contains(string(filelistsXML), "test-package") {
		t.Error("filelists.xml missing package name")
	}

	if !strings.Contains(string(otherXML), "test-package") {
		t.Error("other.xml missing package name")
	}

	if !strings.Contains(string(otherXML), "Test Packager") {
		t.Error("other.xml missing changelog author")
	}
}

func TestCompressionAndChecksums(t *testing.T) {
	pkg := buildTestPackage()
	packages := []pkgrpm.Package{pkg}

	primaryXML := renderRPMXML(t, formatrpm.WritePrimaryXML, packages)
	compressed := compressRPMXML(t, primaryXML)

	if len(compressed.GzipData) == 0 {
		t.Error("gzip data is empty")
	}

	if len(compressed.XZData) == 0 {
		t.Error("xz data is empty")
	}

	if compressed.GzSHA256 == "" {
		t.Error("gzip SHA-256 is empty")
	}

	if compressed.XZSHA256 == "" {
		t.Error("xz SHA-256 is empty")
	}

	if compressed.GzSize == 0 {
		t.Error("gzip size is 0")
	}

	if compressed.XZSize == 0 {
		t.Error("xz size is 0")
	}

	if compressed.OpenSize == 0 {
		t.Error("open size is 0")
	}

	if compressed.OpenSize != int64(len(primaryXML)) {
		t.Errorf("open size = %d, want %d", compressed.OpenSize, len(primaryXML))
	}
}

func TestRepomdXMLGeneration(t *testing.T) {
	pkg := buildTestPackage()
	packages := []pkgrpm.Package{pkg}

	primaryCompressed := compressRPMXML(t, renderRPMXML(t, formatrpm.WritePrimaryXML, packages))
	filelistsCompressed := compressRPMXML(t, renderRPMXML(t, formatrpm.WriteFilelistsXML, packages))
	otherCompressed := compressRPMXML(t, renderRPMXML(t, formatrpm.WriteOtherXML, packages))

	entries := []formatrpm.RepomdData{
		{
			Type: "primary", Location: "repodata/primary.xml.gz",
			Checksum: primaryCompressed.GzSHA256, ChecksumType: "sha256",
			Timestamp: 1700000000, Size: primaryCompressed.GzSize, OpenSize: primaryCompressed.OpenSize,
		},
		{
			Type: "filelists", Location: "repodata/filelists.xml.gz",
			Checksum: filelistsCompressed.GzSHA256, ChecksumType: "sha256",
			Timestamp: 1700000000, Size: filelistsCompressed.GzSize, OpenSize: filelistsCompressed.OpenSize,
		},
		{
			Type: "other", Location: "repodata/other.xml.gz",
			Checksum: otherCompressed.GzSHA256, ChecksumType: "sha256",
			Timestamp: 1700000000, Size: otherCompressed.GzSize, OpenSize: otherCompressed.OpenSize,
		},
	}

	repomdXML, err := formatrpm.GenerateRepomdXML("1700000000", entries)
	if err != nil {
		t.Fatalf("GenerateRepomdXML() error: %v", err)
	}

	if !strings.Contains(string(repomdXML), "repomd") {
		t.Error("repomd.xml missing root element")
	}

	if !strings.Contains(string(repomdXML), "primary") {
		t.Error("repomd.xml missing primary data entry")
	}

	if !strings.Contains(string(repomdXML), "sha256") {
		t.Error("repomd.xml missing sha256 checksum type")
	}

	if !strings.Contains(string(repomdXML), "1700000000") {
		t.Error("repomd.xml missing revision")
	}

	var repomd struct {
		XMLName  xml.Name `xml:"repomd"`
		Revision string   `xml:"revision"`
		Data     []struct {
			Type string `xml:"type,attr"`
		} `xml:"data"`
	}
	if err := xml.Unmarshal(repomdXML, &repomd); err != nil {
		t.Fatalf("repomd.xml XML parse error: %v", err)
	}

	if repomd.Revision != "1700000000" {
		t.Errorf("revision = %q, want %q", repomd.Revision, "1700000000")
	}

	if len(repomd.Data) != 3 {
		t.Errorf("data entries = %d, want 3", len(repomd.Data))
	}

	types := map[string]bool{}
	for _, d := range repomd.Data {
		types[d.Type] = true
	}

	for _, want := range []string{"primary", "filelists", "other"} {
		if !types[want] {
			t.Errorf("missing data type %q in repomd.xml", want)
		}
	}
}

func TestPrimaryXMLStructure(t *testing.T) {
	pkg := buildTestPackage()
	packages := []pkgrpm.Package{pkg}

	primaryXML := renderRPMXML(t, formatrpm.WritePrimaryXML, packages)

	var metadata struct {
		XMLName  xml.Name `xml:"metadata"`
		Packages int      `xml:"packages,attr"`
		PkgList  []struct {
			Type    string `xml:"type,attr"`
			Name    string `xml:"name"`
			Arch    string `xml:"arch"`
			Version struct {
				Epoch string `xml:"epoch,attr,omitempty"`
				Ver   string `xml:"ver,attr"`
				Rel   string `xml:"rel,attr"`
			} `xml:"version"`
		} `xml:"package"`
	}

	if err := xml.Unmarshal(primaryXML, &metadata); err != nil {
		t.Fatalf("primary.xml XML parse error: %v", err)
	}

	if metadata.Packages != 1 {
		t.Errorf("packages count = %d, want 1", metadata.Packages)
	}

	if len(metadata.PkgList) != 1 {
		t.Fatalf("package list length = %d, want 1", len(metadata.PkgList))
	}

	p := metadata.PkgList[0]
	if p.Name != "test-package" {
		t.Errorf("name = %q, want %q", p.Name, "test-package")
	}

	if p.Arch != "x86_64" {
		t.Errorf("arch = %q, want %q", p.Arch, "x86_64")
	}

	if p.Version.Ver != "1.0.0" {
		t.Errorf("ver = %q, want %q", p.Version.Ver, "1.0.0")
	}

	if p.Version.Rel != "1.el9" {
		t.Errorf("rel = %q, want %q", p.Version.Rel, "1.el9")
	}
}

func TestEmptyPackageList(t *testing.T) {
	primaryXML := renderRPMXML(t, formatrpm.WritePrimaryXML, nil)

	var metadata struct {
		Packages int `xml:"packages,attr"`
	}
	if err := xml.Unmarshal(primaryXML, &metadata); err != nil {
		t.Fatalf("empty primary.xml parse error: %v", err)
	}

	if metadata.Packages != 0 {
		t.Errorf("packages = %d, want 0", metadata.Packages)
	}
}

func TestCompsXMLValidation(t *testing.T) {
	validComps := `<?xml version="1.0" encoding="UTF-8"?>
<comps>
  <group>
    <id>core</id>
    <name>Core</name>
    <description>Core packages</description>
  </group>
</comps>`
	if err := formatrpm.ValidateCompsXML([]byte(validComps)); err != nil {
		t.Errorf("ValidateCompsXML(valid) error: %v", err)
	}

	invalidComps := `<comps><group><id>broken`
	if err := formatrpm.ValidateCompsXML([]byte(invalidComps)); err == nil {
		t.Error("ValidateCompsXML(invalid) expected error, got nil")
	}
}

func TestFullPipelineFromBinary(t *testing.T) {
	rpmData := buildMinimalRPM()

	pkg, err := pkgrpm.ParseRPM(bytes.NewReader(rpmData))
	if err != nil {
		t.Fatalf("ParseRPM() error: %v", err)
	}

	if pkg.Name != "rpmtest" {
		t.Errorf("Name = %q, want %q", pkg.Name, "rpmtest")
	}

	packages := []pkgrpm.Package{*pkg}

	primaryXML := renderRPMXML(t, formatrpm.WritePrimaryXML, packages)

	if !strings.Contains(string(primaryXML), "rpmtest") {
		t.Error("primary.xml does not contain parsed package name")
	}

	compressRPMXML(t, primaryXML)
}

func buildMinimalRPM() []byte {
	return buildMinimalRPMWithLicense("")
}

// buildMinimalRPMWithLicense is buildMinimalRPM with the License tag (1014)
// set to license; an empty license leaves the tag out.
func buildMinimalRPMWithLicense(license string) []byte {
	var buf bytes.Buffer

	lead := make([]byte, 96)
	binary.BigEndian.PutUint32(lead[0:4], 0xEDABEEDB)
	lead[4] = 3 // major version (uint8)
	lead[5] = 0 // minor version (uint8)
	copy(lead[10:76], []byte("rpmtest-1.0-1.x86_64.rpm"))
	buf.Write(lead)

	sigData := []byte("\x00")
	sigEntries := []pkgrpm.TagEntry{
		{Tag: 1000, Type: 6, Offset: 0, Count: 1},
	}
	sigHeader := buildTestHeaderBytes(sigEntries, sigData)
	buf.Write(sigHeader)

	sigTotal := int64(16) + int64(len(sigEntries))*16 + int64(len(sigData))

	padBytes := pkgrpm.PadTo8(sigTotal)
	if padBytes > 0 {
		buf.Write(make([]byte, padBytes))
	}

	pkgName := []byte("rpmtest\x00")
	pkgVer := []byte("1.0\x00")
	pkgRel := []byte("1.el9\x00")
	pkgArch := []byte("x86_64\x00")
	pkgSummary := []byte("Test package\x00")
	pkgDesc := []byte("A test\x00")

	var pkgDataBuf bytes.Buffer

	offs := make(map[int32]int32)

	off := int32(0)
	offs[1000] = off

	pkgDataBuf.Write(pkgName)
	off += int32(len(pkgName))

	offs[1001] = off

	pkgDataBuf.Write(pkgVer)
	off += int32(len(pkgVer))

	offs[1002] = off

	pkgDataBuf.Write(pkgRel)
	off += int32(len(pkgRel))

	offs[1022] = off

	pkgDataBuf.Write(pkgArch)
	off += int32(len(pkgArch))

	offs[1004] = off

	pkgDataBuf.Write(pkgSummary)
	off += int32(len(pkgSummary))

	offs[1005] = off

	pkgDataBuf.Write(pkgDesc)
	off += int32(len(pkgDesc))

	pkgEntries := []pkgrpm.TagEntry{
		{Tag: 1000, Type: 6, Offset: offs[1000], Count: 1},
		{Tag: 1001, Type: 6, Offset: offs[1001], Count: 1},
		{Tag: 1002, Type: 6, Offset: offs[1002], Count: 1},
		{Tag: 1022, Type: 6, Offset: offs[1022], Count: 1},
		{Tag: 1004, Type: 6, Offset: offs[1004], Count: 1},
		{Tag: 1005, Type: 6, Offset: offs[1005], Count: 1},
	}

	if license != "" {
		pkgDataBuf.WriteString(license + "\x00")

		pkgEntries = append(pkgEntries, pkgrpm.TagEntry{Tag: 1014, Type: 6, Offset: off, Count: 1})
	}

	pkgHeader := buildTestHeaderBytes(pkgEntries, pkgDataBuf.Bytes())
	buf.Write(pkgHeader)

	return buf.Bytes()
}

func buildTestHeaderBytes(entries []pkgrpm.TagEntry, data []byte) []byte {
	var buf bytes.Buffer

	intro := make([]byte, 16)
	intro[0] = 0x8E
	intro[1] = 0xAD
	intro[2] = 0xE8
	intro[3] = 1
	binary.BigEndian.PutUint32(intro[8:12], uint32(len(entries)))
	binary.BigEndian.PutUint32(intro[12:16], uint32(len(data)))
	buf.Write(intro)

	for _, e := range entries {
		entryBuf := make([]byte, 16)
		binary.BigEndian.PutUint32(entryBuf[0:4], uint32(e.Tag))
		binary.BigEndian.PutUint32(entryBuf[4:8], uint32(e.Type))
		binary.BigEndian.PutUint32(entryBuf[8:12], uint32(e.Offset))
		binary.BigEndian.PutUint32(entryBuf[12:16], uint32(e.Count))
		buf.Write(entryBuf)
	}

	buf.Write(data)

	return buf.Bytes()
}
