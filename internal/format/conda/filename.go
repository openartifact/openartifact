// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"strings"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Column widths of conda_packages.
const (
	maxNameLen     = 255
	maxVersionLen  = 100
	maxBuildLen    = 255
	maxPlatformLen = 50
)

// plainSegment reports whether s is valid UTF-8 without path separators or
// control characters: text a database column holds and a URL segment names.
func plainSegment(s string) bool {
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

// validPlatform reports whether platform, the subdirectory of a repository
// packages are published under (linux-64, noarch), fits conda_packages.platform.
// Any other value is a database error, not a package.
func validPlatform(platform string) bool {
	return platform != "" && len(platform) <= maxPlatformLen && plainSegment(platform)
}

// packageFile is a conda package file name taken apart.
type packageFile struct {
	name, version, build, ext string
}

// parsePackageFilename splits <name>-<version>-<build>(.tar.bz2|.conda) from
// the right: a package name may contain hyphens (scikit-learn), but neither a
// version nor a build string can. ok is false for any other extension, a name
// with fewer than three parts, empty parts, invalid UTF-8, path separators or
// control characters.
func parsePackageFilename(filename string) (packageFile, bool) {
	var f packageFile

	switch {
	case strings.HasSuffix(filename, models.CondaExtTarBz2):
		f.ext = models.CondaExtTarBz2
	case strings.HasSuffix(filename, models.CondaExtConda):
		f.ext = models.CondaExtConda
	default:
		return f, false
	}

	stem := strings.TrimSuffix(filename, f.ext)

	rest, build, ok := strings.CutLast(stem, "-")
	if !ok {
		return f, false
	}

	name, version, ok := strings.CutLast(rest, "-")
	if !ok {
		return f, false
	}

	f.name, f.version, f.build = name, version, build

	if f.name == "" || f.version == "" || f.build == "" ||
		len(f.name) > maxNameLen || len(f.version) > maxVersionLen || len(f.build) > maxBuildLen ||
		!plainSegment(stem) {
		return packageFile{}, false
	}

	return f, true
}
