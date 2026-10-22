// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"  //nolint:gosec // required by protocol specification
	"crypto/sha1" //nolint:gosec // required by protocol specification
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/xzutil"
)

// ReleaseFileGenerator generates Debian Release files.
type ReleaseFileGenerator struct {
	Origin        string
	Label         string
	Suite         string
	Version       string
	Codename      string
	Date          time.Time
	ValidUntil    time.Time
	Architectures []string
	Components    []string
	Description   string
}

// NewReleaseFileGenerator creates a new Release file generator.
func NewReleaseFileGenerator(origin, label, suite, codename string) *ReleaseFileGenerator {
	return &ReleaseFileGenerator{
		Origin:   origin,
		Label:    label,
		Suite:    suite,
		Codename: codename,
		Date:     time.Now().UTC(),
		// ValidUntil is deliberately left unset: metadata is only regenerated on
		// upload, so an expiry would make apt reject an idle repository.
		Architectures: make([]string, 0),
		Components:    make([]string, 0),
	}
}

// AddArchitecture adds a supported architecture.
func (rfg *ReleaseFileGenerator) AddArchitecture(arch string) {
	if slices.Contains(rfg.Architectures, arch) {
		return
	}

	rfg.Architectures = append(rfg.Architectures, arch)
}

// AddComponent adds a component.
func (rfg *ReleaseFileGenerator) AddComponent(comp string) {
	if slices.Contains(rfg.Components, comp) {
		return
	}

	rfg.Components = append(rfg.Components, comp)
}

// FileInfo represents information about a file in the Release file.
type FileInfo struct {
	Filename string
	Size     int64
	MD5sum   string
	SHA1     string
	SHA256   string
}

// GenerateRelease generates a Release file content.
func (rfg *ReleaseFileGenerator) GenerateRelease(files []FileInfo) string {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "Origin: %s\n", rfg.Origin)
	fmt.Fprintf(&buf, "Label: %s\n", rfg.Label)
	fmt.Fprintf(&buf, "Suite: %s\n", rfg.Suite)
	fmt.Fprintf(&buf, "Codename: %s\n", rfg.Codename)
	fmt.Fprintf(&buf, "Date: %s\n", rfg.Date.Format("Mon, 02 Jan 2006 15:04:05 MST"))

	if !rfg.ValidUntil.IsZero() {
		fmt.Fprintf(&buf, "Valid-Until: %s\n", rfg.ValidUntil.Format("Mon, 02 Jan 2006 15:04:05 MST"))
	}

	if rfg.Description != "" {
		fmt.Fprintf(&buf, "Description: %s\n", rfg.Description)
	}

	// Write architectures
	buf.WriteString("Architectures:")

	for _, arch := range rfg.Architectures {
		buf.WriteString(" " + arch)
	}

	buf.WriteString("\n")

	// Write components
	buf.WriteString("Components:")

	for _, comp := range rfg.Components {
		buf.WriteString(" " + comp)
	}

	buf.WriteString("\n")

	// Write file checksums
	if len(files) > 0 {
		buf.WriteString("MD5Sum:\n")

		for _, f := range files {
			fmt.Fprintf(&buf, " %s %10d %s\n", f.MD5sum, f.Size, f.Filename)
		}

		buf.WriteString("SHA1:\n")

		for _, f := range files {
			fmt.Fprintf(&buf, " %s %10d %s\n", f.SHA1, f.Size, f.Filename)
		}

		buf.WriteString("SHA256:\n")

		for _, f := range files {
			fmt.Fprintf(&buf, " %s %10d %s\n", f.SHA256, f.Size, f.Filename)
		}
	}

	return buf.String()
}

// PackagesFileGenerator generates Packages files.
type PackagesFileGenerator struct {
	entries []string
	// rendered caches Generate's result; AddEntry invalidates it so the
	// plain, gzip and xz variants share one render.
	rendered *string
}

// NewPackagesFileGenerator creates a new Packages file generator.
func NewPackagesFileGenerator() *PackagesFileGenerator {
	return &PackagesFileGenerator{
		entries: make([]string, 0),
	}
}

// AddEntry adds a package entry.
func (pfg *PackagesFileGenerator) AddEntry(entry string) {
	if entry != "" {
		pfg.entries = append(pfg.entries, entry)
		pfg.rendered = nil
	}
}

// Generate generates the Packages file content.
func (pfg *PackagesFileGenerator) Generate() string {
	if pfg.rendered != nil {
		return *pfg.rendered
	}

	var b strings.Builder

	n := 0
	for _, entry := range pfg.entries {
		n += len(entry)
	}

	b.Grow(n)

	for _, entry := range pfg.entries {
		b.WriteString(entry)
	}

	out := b.String()
	pfg.rendered = &out

	return out
}

// GenerateGzipped generates gzipped Packages file content.
func (pfg *PackagesFileGenerator) GenerateGzipped() ([]byte, error) {
	content := pfg.Generate()

	var buf bytes.Buffer

	gzWriter := gzip.NewWriter(&buf)
	if _, err := io.WriteString(gzWriter, content); err != nil {
		return nil, fmt.Errorf("failed to write gzipped content: %w", err)
	}

	if err := gzWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// GenerateXZipped generates xz-compressed Packages file content.
func (pfg *PackagesFileGenerator) GenerateXZipped() ([]byte, error) {
	content := pfg.Generate()

	var buf bytes.Buffer

	xzWriter, err := xzutil.NewWriter(&buf, len(content))
	if err != nil {
		return nil, fmt.Errorf("failed to create xz writer: %w", err)
	}

	if _, err := io.WriteString(xzWriter, content); err != nil {
		return nil, fmt.Errorf("failed to write xz content: %w", err)
	}

	if err := xzWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close xz writer: %w", err)
	}

	return buf.Bytes(), nil
}

// CalculateChecksums calculates MD5, SHA1, and SHA256 checksums.
func CalculateChecksums(data []byte) (string, string, string) {
	// MD5
	md5Hash := md5.Sum(data) //nolint:gosec // required by protocol specification
	md5sum := hex.EncodeToString(md5Hash[:])

	// SHA1
	sha1Hash := sha1.Sum(data) //nolint:gosec // required by protocol specification
	sha1sum := hex.EncodeToString(sha1Hash[:])

	// SHA256
	sha256Hash := sha256.Sum256(data)
	sha256sum := hex.EncodeToString(sha256Hash[:])

	return md5sum, sha1sum, sha256sum
}

// CalculateChecksumsFromReader calculates checksums from a reader.
func CalculateChecksumsFromReader(r io.Reader) (string, string, string, int64, error) {
	md5Hash := md5.New()   //nolint:gosec // required by protocol specification
	sha1Hash := sha1.New() //nolint:gosec // required by protocol specification
	sha256Hash := sha256.New()

	// Create a multi-writer to write to all hash functions simultaneously
	multiWriter := io.MultiWriter(md5Hash, sha1Hash, sha256Hash)

	// Copy data to all hash functions
	size, err := io.Copy(multiWriter, r)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("failed to calculate checksums: %w", err)
	}

	md5sum := hex.EncodeToString(md5Hash.Sum(nil))
	sha1sum := hex.EncodeToString(sha1Hash.Sum(nil))
	sha256sum := hex.EncodeToString(sha256Hash.Sum(nil))

	return md5sum, sha1sum, sha256sum, size, nil
}

// RepositoryIndexGenerator generates repository index files.
type RepositoryIndexGenerator struct {
	releaseGen   *ReleaseFileGenerator
	packagesGens map[string]*PackagesFileGenerator // key: "component/arch"
}

// NewRepositoryIndexGenerator creates a new repository index generator.
func NewRepositoryIndexGenerator(origin, label, suite, codename string) *RepositoryIndexGenerator {
	return &RepositoryIndexGenerator{
		releaseGen:   NewReleaseFileGenerator(origin, label, suite, codename),
		packagesGens: make(map[string]*PackagesFileGenerator),
	}
}

// GetPackagesGenerator gets or creates a Packages file generator for a component/arch.
func (rig *RepositoryIndexGenerator) GetPackagesGenerator(component, arch string) *PackagesFileGenerator {
	key := component + "/" + arch
	if gen, ok := rig.packagesGens[key]; ok {
		return gen
	}

	gen := NewPackagesFileGenerator()
	rig.packagesGens[key] = gen
	rig.releaseGen.AddComponent(component)
	rig.releaseGen.AddArchitecture(arch)

	return gen
}

// GenerateIndexFiles generates all index files.
func (rig *RepositoryIndexGenerator) GenerateIndexFiles() (map[string][]byte, error) {
	files := make(map[string][]byte)

	// Generate Packages files
	fileInfos := make([]FileInfo, 0)

	for key, gen := range rig.packagesGens {
		// Generate gzipped Packages file
		gzipped, err := gen.GenerateGzipped()
		if err != nil {
			return nil, fmt.Errorf("failed to generate packages file for %s: %w", key, err)
		}

		// Store gzipped version
		filename := fmt.Sprintf("dists/%s/Packages.gz", key)
		files[filename] = gzipped

		// Also store uncompressed version
		content := gen.Generate()
		filename = fmt.Sprintf("dists/%s/Packages", key)
		files[filename] = []byte(content)

		// Calculate checksums for Release file
		md5sum, sha1sum, sha256sum := CalculateChecksums(gzipped)
		fileInfos = append(fileInfos, FileInfo{
			Filename: fmt.Sprintf("dists/%s/Packages.gz", key),
			Size:     int64(len(gzipped)),
			MD5sum:   md5sum,
			SHA1:     sha1sum,
			SHA256:   sha256sum,
		})
	}

	// Generate Release file
	releaseContent := rig.releaseGen.GenerateRelease(fileInfos)
	files["dists/Release"] = []byte(releaseContent)

	return files, nil
}
