// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/pathseg"
)

// MetadataSuffix is appended to a file URL to address its PEP 658 core
// metadata.
const MetadataSuffix = ".metadata"

const maxFilenameLen = 255

// SafeFilename reports whether a file name from an index may be exposed as the
// last segment of an OpenArtifact file URL (and used in a cache key): a plain
// name, no separators, dot segments or control characters, and not itself a
// ".metadata" name (those address the core metadata of another file).
func SafeFilename(name string) bool {
	if len(name) > maxFilenameLen || !pathseg.Name(name) || strings.HasSuffix(name, MetadataSuffix) {
		return false
	}

	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}

	return true
}

// PathKind is what a request path under a proxy or group repository asks for.
type PathKind int

const (
	// PathOther is anything that is not a simple API or file path.
	PathOther PathKind = iota
	// PathIndex is the project index, "simple/".
	PathIndex
	// PathProject is a project page, "simple/<project>/".
	PathProject
	// PathFile is a distribution file or its PEP 658 metadata,
	// "files/<project>/<filename>".
	PathFile
)

// ParsePath classifies a repository-relative request path. For a project page
// the PEP 503 normalization is applied to the name; names that are not safe
// path segments yield PathOther. A file path must carry the normalized
// project name, as emitted by the links of FileURL.
func ParsePath(relPath string) (PathKind, string, string) {
	p := strings.TrimPrefix(relPath, "/")

	switch {
	case strings.TrimSuffix(p, "/") == "simple":
		return PathIndex, "", ""
	case strings.HasPrefix(p, "simple/"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "simple/"), "/")

		norm := NormalizeName(name)
		if strings.Contains(name, "/") || !ValidName(norm) {
			return PathOther, "", ""
		}

		return PathProject, norm, ""
	case strings.HasPrefix(p, "files/"):
		project, filename, ok := strings.Cut(strings.TrimPrefix(p, "files/"), "/")
		if !ok || !ValidName(project) || NormalizeName(project) != project ||
			!SafeFilename(strings.TrimSuffix(filename, MetadataSuffix)) {
			return PathOther, "", ""
		}

		return PathFile, project, filename
	}

	return PathOther, "", ""
}

// ServableFiles returns the files whose names can be addressed through an
// OpenArtifact file URL.
func ServableFiles(files []File) []File {
	out := make([]File, 0, len(files))

	for i := range files {
		if SafeFilename(files[i].Filename) {
			out = append(out, files[i])
		}
	}

	return out
}

// ProjectURL is the reference to a project's simple page, relative to the
// simple index page ("simple/", whose URL ends in a slash).
func ProjectURL(project string) string {
	return segment(project) + "/"
}

// FileURL is the reference to a project's file, relative to the project page
// "simple/<project>/" (whose URL ends in a slash): proxy and group
// repositories serve it from files/<project>/<filename>.
func FileURL(project, filename string) string {
	return "../../files/" + segment(project) + "/" + segment(filename)
}

// segment escapes name as one path segment of a relative reference.
// url.PathEscape keeps ':', which in a reference's first segment would be read
// as a scheme ("javascript:x/"), so it is escaped too.
func segment(name string) string {
	return strings.ReplaceAll(url.PathEscape(name), ":", "%3A")
}
