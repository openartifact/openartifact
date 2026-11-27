// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import "strings"

// Kind says what a request path of a pub repository asks for.
type Kind uint8

// The kinds of ParsePath.
const (
	// KindPackage is api/packages/<name>: the version list of a package.
	KindPackage Kind = iota + 1
	// KindVersion is api/packages/<name>/versions/<version>.
	KindVersion
	// KindArchive is api/archives/<name>-<version>.tar.gz.
	KindArchive
	// KindNew is api/packages/versions/new: the start of a publish.
	KindNew
	// KindUpload is api/packages/versions/newUpload: the archive upload.
	KindUpload
	// KindFinish is api/packages/versions/newUploadFinish: the end of a publish.
	KindFinish
)

// Path route segments shared by the handlers, the proxy and the group.
const (
	// PackagesDir is the directory of the package documents.
	PackagesDir = "api/packages/"
	// ArchivesDir is the directory of the package archives.
	ArchivesDir = "api/archives/"
	// NewPath, UploadPath and FinishPath are the three publish endpoints.
	NewPath    = "api/packages/versions/new"
	UploadPath = "api/packages/versions/newUpload"
	FinishPath = "api/packages/versions/newUploadFinish"
)

// Path is a parsed request path.
type Path struct {
	Kind    Kind
	Name    string
	Version string
}

// PackagePath is the path of a package document.
func PackagePath(name string) string { return PackagesDir + name }

// ArchivePath is the path of a package archive.
func ArchivePath(name, version string) string { return ArchivesDir + ArchiveName(name, version) }

// ParsePath parses a request path relative to the repository root, without a
// leading or trailing slash. ok is false for a path that is no pub endpoint or
// that holds an invalid name or version.
func ParsePath(rel string) (Path, bool) {
	switch rel {
	case NewPath:
		return Path{Kind: KindNew}, true
	case UploadPath:
		return Path{Kind: KindUpload}, true
	case FinishPath:
		return Path{Kind: KindFinish}, true
	}

	if file, found := strings.CutPrefix(rel, ArchivesDir); found {
		name, version, ok := ParseArchiveName(file)
		if !ok {
			return Path{}, false
		}

		return Path{Kind: KindArchive, Name: name, Version: version}, true
	}

	rest, found := strings.CutPrefix(rel, PackagesDir)
	if !found {
		return Path{}, false
	}

	name, tail, hasTail := strings.Cut(rest, "/")
	if !ValidName(name) {
		return Path{}, false
	}

	if !hasTail {
		return Path{Kind: KindPackage, Name: name}, true
	}

	version, found := strings.CutPrefix(tail, "versions/")
	if !found || !ValidVersion(version) {
		return Path{}, false
	}

	return Path{Kind: KindVersion, Name: name, Version: version}, true
}
