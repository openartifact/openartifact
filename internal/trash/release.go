// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package trash

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/openartifact/openartifact/internal/repotype"
)

// pep440Marker matches the post/rev segments of a PEP 440 version, which do
// not make it a pre-release.
var pep440Marker = regexp.MustCompile(`(?i)[._-]?(post|rev|r)[._-]?\d*`)

// IsRelease reports whether version of a repository of the given format is a
// release: the kind of version immutable_releases protects. Snapshots and
// pre-releases stay mutable so CI keeps overwriting them. The rule follows the
// format's repotype.Versioning:
//
//	VersionMaven      (maven, ivy, sbt) not a *-SNAPSHOT version
//	VersionSemver     (npm, nuget, cargo, helm, go, terraform) no semver
//	                  pre-release part ("-rc.1"); Go pseudo-versions
//	                  (v0.0.0-2024...-abcdef) therefore count as pre-releases
//	VersionPEP440     (pypi) no PEP 440 a/b/rc/dev segment
//	VersionGem        (rubygems) no letter in the version (Gem::Version#prerelease?)
//	VersionOpaque     (raw, deb, rpm, pacman, conda, conan, docker, apk) has no
//	                  snapshot concept: every version is a release
func IsRelease(format, version string) bool {
	spec, _ := repotype.Lookup(strings.ToLower(format))

	switch spec.Versioning {
	case repotype.VersionMaven:
		return !strings.Contains(strings.ToUpper(version), "SNAPSHOT")
	case repotype.VersionSemver:
		return !semverPrerelease(version)
	case repotype.VersionPEP440:
		return !pep440Prerelease(version)
	case repotype.VersionGem:
		return !strings.ContainsFunc(version, unicode.IsLetter)
	default:
		return true
	}
}

// semverPrerelease reports whether v has a pre-release part. Build metadata
// ("+incompatible", "+build.5") is ignored.
func semverPrerelease(v string) bool {
	core, _, _ := strings.Cut(v, "+")

	return strings.Contains(core, "-")
}

// pep440Prerelease reports whether v carries an alpha/beta/rc/dev segment.
func pep440Prerelease(v string) bool {
	public, _, _ := strings.Cut(v, "+")
	public = pep440Marker.ReplaceAllString(public, "")

	return strings.ContainsFunc(public, unicode.IsLetter)
}
