// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package osv imports the OSV vulnerability database (https://osv.dev, data
// under CC-BY 4.0) into the local mirror and decides whether a package version
// is affected by an advisory, following the OSV schema's evaluation rules.
package osv

import (
	"regexp"
	"strings"
)

// ecosystem pairs the OSV ecosystem name with the OpenArtifact repository
// format whose packages it covers.
type ecosystem struct {
	name   string
	format string
}

// supported are the ecosystems the mirror matches locally: the ones whose
// version ordering is implemented and whose package names are not scoped to a
// distribution release. The other formats (apk, deb, helm, rpm, ...) keep using
// the online OSV API.
var supported = []ecosystem{ //nolint:gochecknoglobals // constant table
	{"npm", "npm"},
	{"PyPI", "pypi"},
	{"Maven", "maven"},
	{"crates.io", "cargo"},
	{"RubyGems", "rubygems"},
	{"NuGet", "nuget"},
	{"Go", "go"},
}

// Ecosystems returns the OSV names of the supported ecosystems.
func Ecosystems() []string {
	names := make([]string, len(supported))
	for i, e := range supported {
		names[i] = e.name
	}

	return names
}

// Supported reports whether the mirror can match an OSV ecosystem locally.
func Supported(name string) bool {
	_, ok := FormatOf(name)

	return ok
}

// FormatOf returns the repository format of an OSV ecosystem.
func FormatOf(name string) (string, bool) {
	for _, e := range supported {
		if e.name == name {
			return e.format, true
		}
	}

	return "", false
}

var pypiSeparators = regexp.MustCompile(`[-_.]+`)

// NormalizeName returns the form of a package name used to match stored
// packages with advisories: PEP 503 for PyPI, lower case for the case-insensitive
// registries (crates.io, RubyGems, NuGet) and the name itself otherwise.
func NormalizeName(ecosystem, name string) string {
	switch ecosystem {
	case "PyPI":
		return pypiSeparators.ReplaceAllString(strings.ToLower(name), "-")
	case "crates.io", "RubyGems", "NuGet":
		return strings.ToLower(name)
	default:
		return name
	}
}
