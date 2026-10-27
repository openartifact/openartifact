// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package conda holds the parts of the conda channel protocol that hosted,
// proxy and group repositories share: package file names, the request paths of
// a channel, and a streaming reader and merger for the very large
// repodata.json documents (the conda-forge linux-64 one is close to half a
// gigabyte) that never holds a whole document in memory.
package conda

import (
	"strings"
	"unicode/utf8"
)

// Package file extensions: .tar.bz2 files are listed under "packages" in a
// repodata.json, .conda files under "packages.conda".
const (
	ExtTarBz2 = ".tar.bz2"
	ExtConda  = ".conda"
)

// Column widths of the hosted format's conda_packages table, also the limits
// a package file name must respect.
const (
	MaxNameLen     = 255
	MaxVersionLen  = 100
	MaxBuildLen    = 255
	MaxPlatformLen = 50
)

// PlainSegment reports whether s is valid UTF-8 without path separators or
// control characters: text a database column holds and a URL segment names.
func PlainSegment(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	for _, r := range s {
		if r == '/' || r == '\\' || r < ' ' || r == 0x7f {
			return false
		}
	}

	return true
}

// ValidSubdir reports whether subdir, the directory of a channel packages and
// repodata live under (linux-64, noarch), is usable as a repository path
// segment and fits the platform column of the hosted format. Any other value
// is a database error or a path escape, not a subdirectory.
func ValidSubdir(subdir string) bool {
	return subdir != "" && subdir != "." && subdir != ".." && len(subdir) <= MaxPlatformLen && PlainSegment(subdir)
}

// File is a conda package file name taken apart.
type File struct {
	Name, Version, Build, Ext string
}

// ParseFilename splits <name>-<version>-<build>(.tar.bz2|.conda) from the
// right: a package name may contain hyphens (scikit-learn), but neither a
// version nor a build string can. ok is false for any other extension, a name
// with fewer than three parts, empty parts, invalid UTF-8, path separators or
// control characters.
func ParseFilename(filename string) (File, bool) {
	var f File

	switch {
	case strings.HasSuffix(filename, ExtTarBz2):
		f.Ext = ExtTarBz2
	case strings.HasSuffix(filename, ExtConda):
		f.Ext = ExtConda
	default:
		return f, false
	}

	stem := strings.TrimSuffix(filename, f.Ext)

	rest, build, ok := strings.CutLast(stem, "-")
	if !ok {
		return f, false
	}

	name, version, ok := strings.CutLast(rest, "-")
	if !ok {
		return f, false
	}

	f.Name, f.Version, f.Build = name, version, build

	if f.Name == "" || f.Version == "" || f.Build == "" ||
		len(f.Name) > MaxNameLen || len(f.Version) > MaxVersionLen || len(f.Build) > MaxBuildLen ||
		!PlainSegment(stem) {
		return File{}, false
	}

	return f, true
}
