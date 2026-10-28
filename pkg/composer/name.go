// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package composer holds what the Composer (PHP) repository formats share: the
// package naming rules, Composer's version normalization (VersionParser), the
// composer.json reader for dist archives and the Composer 2 metadata model
// (p2 documents with the "composer/2.0" minification). The hosted handler uses
// all of it; the proxy and group repositories reuse the metadata and version
// parts.
package composer

import (
	"regexp"
	"strings"
)

// MaxNameLen is the longest "vendor/package" name.
const MaxNameLen = 255

// nameRe is the package name grammar of Composer's schema: lower-case
// "vendor/package", each part made of letters and digits joined by single "_",
// "." or "-" separators (the package part also accepts "--").
var nameRe = regexp.MustCompile(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`)

// ValidName reports whether name is a valid Composer package name.
func ValidName(name string) bool {
	return len(name) <= MaxNameLen && nameRe.MatchString(name)
}

// MetadataName parses the file name of a p2 document ("package.json" or
// "package~dev.json") into the package part and whether it is the dev document.
// The last result is false when file is neither.
func MetadataName(file string) (string, bool, bool) {
	stem, found := strings.CutSuffix(file, ".json")
	if !found || stem == "" {
		return "", false, false
	}

	if s, isDev := strings.CutSuffix(stem, "~dev"); isDev {
		if s == "" {
			return "", false, false
		}

		return s, true, true
	}

	return stem, false, true
}
