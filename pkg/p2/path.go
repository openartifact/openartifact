// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package p2 is the grammar of Eclipse p2 repositories as an update site proxy
// needs it: the layout of a repository (metadata documents, bundles, features,
// binaries), the composite repositories that point at other repositories, and
// the rewriting of the documents that would otherwise send a client around the
// proxy (the mirror and statistics URLs of artifacts.xml, absolute child
// locations of a composite repository).
//
// A p2 repository is a directory of static files:
//
//	p2.index                                     which metadata files exist
//	content.(jar|xml|xml.xz)                     installable units
//	artifacts.(jar|xml|xml.xz)                   artifact descriptors and download rules
//	compositeContent.(jar|xml|xml.xz)            composite: child repositories
//	compositeArtifacts.(jar|xml|xml.xz)
//	plugins/<id>_<version>.jar                   OSGi bundles (also .pack.gz, legacy)
//	features/<id>_<version>.jar                  features
//	binary/<id>_<version>                        other artifacts
//
// Nothing here talks to the network.
package p2

import (
	"path"
	"regexp"
	"strings"
)

// HostPrefix is the first segment of the keys that address the upstream host
// outside the proxied base URL. A composite repository may name children in a
// sibling directory ("../../technology/epp/packages/2024-12/"); the proxy
// serves them under "_host/<path on the upstream host>" so that the client
// stays inside the proxy.
const HostPrefix = "_host"

// Codec is how a metadata document is stored.
type Codec int

const (
	// CodecXML is the plain "<name>.xml" file.
	CodecXML Codec = iota
	// CodecJar is "<name>.jar": a zip archive that holds "<name>.xml".
	CodecJar
	// CodecXZ is "<name>.xml.xz".
	CodecXZ
)

// Doc is a metadata document the proxy rewrites before it caches it.
type Doc struct {
	// Name is the base name of the document without extension ("artifacts",
	// "compositeContent", ...); Name + ".xml" is the entry of a jar.
	Name  string
	Codec Codec
	// Composite is set for the composite repositories, whose children point at
	// other repositories.
	Composite bool
}

// Entry is the name of the XML file inside a jar.
func (d Doc) Entry() string { return d.Name + ".xml" }

// rewritten are the metadata documents that carry what a client must not see
// as the upstream wrote it. content.* is not among them: it lists units, never
// where to download.
var rewritten = map[string]bool{ //nolint:gochecknoglobals // constant table
	"artifacts":          false,
	"compositeArtifacts": true,
	"compositeContent":   true,
}

// ParseDoc recognises the rewritten metadata documents by the last segment of
// a key: artifacts, compositeArtifacts and compositeContent in the three
// encodings.
func ParseDoc(key string) (Doc, bool) {
	file := path.Base(key)

	for _, e := range codecExts {
		name, ok := strings.CutSuffix(file, e.ext)
		if !ok {
			continue
		}

		composite, known := rewritten[name]
		if !known {
			return Doc{}, false
		}

		return Doc{Name: name, Codec: e.codec, Composite: composite}, true
	}

	return Doc{}, false
}

// codecExts are the file extensions of the three encodings of a document.
var codecExts = [...]struct { //nolint:gochecknoglobals // constant table
	ext   string
	codec Codec
}{{".xml.xz", CodecXZ}, {".jar", CodecJar}, {".xml", CodecXML}}

// metadataNames are the base names of the files a repository serves besides its
// artifacts.
var metadataNames = map[string]bool{ //nolint:gochecknoglobals // constant table
	"p2.index":           true,
	"content":            true,
	"artifacts":          true,
	"compositeContent":   true,
	"compositeArtifacts": true,
}

// IsMetadata reports whether key names a repository metadata file (p2.index,
// content, artifacts and composite documents, with their checksum files).
func IsMetadata(key string) bool {
	file := path.Base(key)
	if metadataNames[file] {
		return true
	}

	for _, e := range codecExts {
		if name, ok := strings.CutSuffix(file, e.ext); ok && metadataNames[name] {
			return true
		}
	}

	// Checksum files next to a metadata file: content.xml.xz.sha256.
	for _, ext := range []string{".sha256", ".sha1", ".md5", ".asc"} {
		if owner, ok := strings.CutSuffix(file, ext); ok && IsMetadata(path.Join(path.Dir(key), owner)) {
			return true
		}
	}

	return false
}

// artifactDirs are the directories the artifacts of a repository live in.
var artifactDirs = [...]string{"plugins", "features", "binary"} //nolint:gochecknoglobals // constant table

// artifactDir returns the artifact directory a key lies in and the file name,
// at the root of the repository or deeper (a repository may sit below a path
// prefix or under the host namespace).
func artifactDir(key string) (string, string, bool) {
	d, f := path.Split(key)
	if f == "" {
		return "", "", false
	}

	d = strings.TrimSuffix(d, "/")
	for _, a := range artifactDirs {
		if d == a || strings.HasSuffix(d, "/"+a) {
			return a, f, true
		}
	}

	return "", "", false
}

// IsArtifact reports whether key is an artifact: a file of a plugins, features
// or binary directory. Artifacts are named by id and version, so they never
// change once published.
func IsArtifact(key string) bool {
	_, _, ok := artifactDir(key)

	return ok
}

// artifactExt is the extension of a bundle or feature file: a jar, or its
// legacy pack200 form.
var artifactExt = regexp.MustCompile(`\.jar(?:\.pack\.gz)?$`)

// bundleName splits "<id>_<version>" at the first underscore that starts an
// OSGi version (digits, then dots or the end). Identifiers may contain
// underscores, and the qualifier of a version may too, so the first underscore
// followed by a number is the separator.
var bundleName = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*?)_(\d+(?:\.\d+){0,2}(?:\.[A-Za-z0-9_-]+)?)$`)

// Coordinates recovers the id and version of a bundle or feature from the key of
// its file ("plugins/org.eclipse.core.runtime_3.31.0.v20240101.jar"); ok is
// false for anything else, binaries included (their names carry no extension,
// and an id there is a classifier path, not a bundle).
func Coordinates(key string) (string, string, bool) {
	dir, file, found := artifactDir(key)
	if !found || dir == "binary" {
		return "", "", false
	}

	stem := artifactExt.ReplaceAllString(file, "")
	if stem == file {
		return "", "", false
	}

	m := bundleName.FindStringSubmatch(stem)
	if m == nil {
		return "", "", false
	}

	return m[1], m[2], true
}
