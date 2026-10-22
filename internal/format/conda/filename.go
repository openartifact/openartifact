// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Column widths of conda_packages.
const (
	maxNameLen    = 255
	maxVersionLen = 100
	maxBuildLen   = 255
)

// packageFile is a conda package file name taken apart.
type packageFile struct {
	name, version, build, ext string
}

// parsePackageFilename splits <name>-<version>-<build>(.tar.bz2|.conda) from
// the right: a package name may contain hyphens (scikit-learn), but neither a
// version nor a build string can. ok is false for any other extension, a name
// with fewer than three parts, empty parts, path separators or control
// characters.
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
		len(f.name) > maxNameLen || len(f.version) > maxVersionLen || len(f.build) > maxBuildLen {
		return packageFile{}, false
	}

	for _, r := range stem {
		if r == '/' || r == '\\' || r < ' ' || r == 0x7f {
			return packageFile{}, false
		}
	}

	return f, true
}
