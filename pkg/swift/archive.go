// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// MaxManifestBytes is the largest manifest read from a source archive.
	MaxManifestBytes = 1 << 20
	// MaxArchiveEntries is the most entries a source archive may hold.
	MaxArchiveEntries = 200_000
	// MaxManifests is the most manifests (Package.swift and the
	// version-specific ones) read from an archive.
	MaxManifests = 64
)

// ErrInvalidArchive is wrapped by every error of ReadArchive; its text says
// what is wrong with the archive.
var ErrInvalidArchive = errors.New("invalid source archive")

// manifestRe matches the version-specific manifests, Package@swift-4.2.swift.
var manifestRe = regexp.MustCompile(`^Package@swift-(\d+(?:\.\d+){0,2})\.swift$`)

// toolsVersionRe matches the "swift-tools-version" comment of a manifest.
var toolsVersionRe = regexp.MustCompile(`^//\s*swift-tools-version\s*:\s*(\d+(?:\.\d+){0,2})`)

// Manifest is a manifest of a release.
type Manifest struct {
	// Filename is Package.swift or Package@swift-<version>.swift.
	Filename string `json:"filename"`
	// SwiftVersion is the version in the file name, "" for Package.swift.
	SwiftVersion string `json:"swiftVersion,omitempty"` //nolint:tagliatelle // same names as the registry documents
	// ToolsVersion is the swift-tools-version the manifest declares, "" when it
	// declares none.
	ToolsVersion string `json:"toolsVersion,omitempty"` //nolint:tagliatelle // same names as the registry documents
	// Content is the manifest source.
	Content string `json:"content"`
}

// ParseManifestName returns the Swift version in the name of a
// version-specific manifest; ok is false for any other name (Package.swift
// included).
func ParseManifestName(name string) (string, bool) {
	m := manifestRe.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}

	return m[1], true
}

// ManifestName is the file name of the manifest for a Swift version, Package.swift
// for "".
func ManifestName(swiftVersion string) string {
	if swiftVersion == "" {
		return ManifestFile
	}

	return "Package@swift-" + swiftVersion + ".swift"
}

// ToolsVersion returns the swift-tools-version declared on the first line of a
// manifest, "" when there is none.
func ToolsVersion(src string) string {
	first, _, _ := strings.Cut(strings.TrimPrefix(src, "\ufeff"), "\n")

	if m := toolsVersionRe.FindStringSubmatch(first); m != nil {
		return m[1]
	}

	return ""
}

// ReadArchive reads the manifests of a source archive: the Package.swift at its
// root or in the one top-level directory every entry sits in (as "swift package
// archive-source" writes it), and the Package@swift-*.swift next to it. Package.swift
// is the first manifest. It reads nothing else of the archive. An archive that
// is not a zip, has an entry that escapes the archive, has no manifest, or whose
// manifest is too large or not UTF-8 text is an ErrInvalidArchive.
func ReadArchive(ra io.ReaderAt, size int64) ([]Manifest, error) {
	zr, err := zip.NewReader(ra, size)
	if errors.Is(err, zip.ErrInsecurePath) {
		return nil, fmt.Errorf("%w: an entry is outside the archive", ErrInvalidArchive)
	}

	if err != nil {
		return nil, fmt.Errorf("%w: not a zip file", ErrInvalidArchive)
	}

	if len(zr.File) > MaxArchiveEntries {
		return nil, fmt.Errorf("%w: more than %d entries", ErrInvalidArchive, MaxArchiveEntries)
	}

	files, prefix, err := manifestFiles(zr)
	if err != nil {
		return nil, err
	}

	var out []Manifest

	for _, f := range files {
		name := strings.TrimPrefix(f.Name, prefix)

		m, err := readManifest(f, name)
		if err != nil {
			return nil, err
		}

		if name == ManifestFile {
			out = append([]Manifest{m}, out...)
		} else {
			out = append(out, m)
		}
	}

	return out, nil
}

// archivePrefix checks every entry name and returns the directory the package
// sits under ("" when Package.swift is at the root, "top/" for the single
// top-level directory of a git archive).
func archivePrefix(zr *zip.Reader) (string, error) {
	tops := map[string]bool{}
	rootManifest := false

	for _, f := range zr.File {
		name := f.Name
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || slices.Contains(strings.Split(name, "/"), "..") {
			return "", fmt.Errorf("%w: entry %q is outside the archive", ErrInvalidArchive, name)
		}

		if strings.HasPrefix(name, "__MACOSX/") {
			continue
		}

		top, rest, nested := strings.Cut(name, "/")
		if !nested {
			if name == ManifestFile {
				rootManifest = true
			}

			tops[""] = true

			continue
		}

		if rest != "" {
			tops[top] = true
		}
	}

	prefix := ""

	if !rootManifest {
		if len(tops) != 1 {
			return "", errMissingManifest
		}

		for top := range tops {
			prefix = top + "/"
		}
	}

	return prefix, nil
}

// manifestFiles finds the manifest entries and the directory prefix they sit
// under, and checks every entry name on the way.
func manifestFiles(zr *zip.Reader) ([]*zip.File, string, error) {
	prefix, err := archivePrefix(zr)
	if err != nil {
		return nil, "", err
	}

	var files []*zip.File

	seen := map[string]bool{}

	for _, f := range zr.File {
		name, ok := strings.CutPrefix(f.Name, prefix)
		if !ok || strings.Contains(name, "/") || f.FileInfo().IsDir() {
			continue
		}

		if _, versioned := ParseManifestName(name); name != ManifestFile && !versioned {
			continue
		}

		if seen[name] {
			return nil, "", fmt.Errorf("%w: %s appears twice", ErrInvalidArchive, name)
		}

		seen[name] = true
		files = append(files, f)
	}

	if !seen[ManifestFile] {
		return nil, "", errMissingManifest
	}

	if len(files) > MaxManifests {
		return nil, "", fmt.Errorf("%w: more than %d manifests", ErrInvalidArchive, MaxManifests)
	}

	return files, prefix, nil
}

var errMissingManifest = fmt.Errorf("%w: package doesn't contain a valid manifest (%s) file", ErrInvalidArchive, ManifestFile)

// readManifest reads one manifest entry, at most MaxManifestBytes of it.
func readManifest(f *zip.File, name string) (Manifest, error) {
	rc, err := f.Open()
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: cannot read %s", ErrInvalidArchive, name)
	}

	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: cannot read %s", ErrInvalidArchive, name)
	}

	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: %s is larger than %d bytes", ErrInvalidArchive, path.Base(name), MaxManifestBytes)
	}

	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return Manifest{}, fmt.Errorf("%w: %s is not UTF-8 text", ErrInvalidArchive, name)
	}

	swiftVersion, _ := ParseManifestName(name)
	content := string(data)

	return Manifest{Filename: name, SwiftVersion: swiftVersion, ToolsVersion: ToolsVersion(content), Content: content}, nil
}
