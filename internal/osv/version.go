// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"strings"

	"golang.org/x/mod/semver"

	"github.com/openartifact/openartifact/pkg/gemversion"
	"github.com/openartifact/openartifact/pkg/maven"
	"github.com/openartifact/openartifact/pkg/nuget"
	"github.com/openartifact/openartifact/pkg/pyversion"
)

// canonicalVersion maps a version to the spelling OSV uses for its ecosystem:
// Go modules are published without the "v" prefix.
func canonicalVersion(ecosystem, v string) string {
	v = strings.TrimSpace(v)
	if ecosystem == "Go" {
		return strings.TrimPrefix(v, "v")
	}

	return v
}

// compareFunc orders two versions; ok is false when either is not a valid
// version of the scheme (such a pair never matches a range).
type compareFunc func(a, b string) (c int, ok bool)

// comparator returns the version ordering of an OSV range of the given type for
// an ecosystem, nil when the range cannot be evaluated: SEMVER ranges use
// semantic versioning everywhere, ECOSYSTEM ranges the ecosystem's own scheme,
// GIT ranges are commit based and never match a package version.
func comparator(rangeType, ecosystem string) compareFunc {
	switch rangeType {
	case "SEMVER":
		return compareSemver
	case "ECOSYSTEM":
		return ecosystemComparator(ecosystem)
	default:
		return nil
	}
}

func ecosystemComparator(ecosystem string) compareFunc {
	switch ecosystem {
	case "npm", "crates.io", "Go":
		return compareSemver
	case "Maven":
		return func(a, b string) (int, bool) { return maven.CompareVersions(a, b), true }
	case "NuGet":
		return func(a, b string) (int, bool) {
			if _, ok := nuget.Normalize(a); !ok {
				return 0, false
			}

			if _, ok := nuget.Normalize(b); !ok {
				return 0, false
			}

			return nuget.CompareStrings(a, b), true
		}
	case "RubyGems":
		return func(a, b string) (int, bool) { return gemversion.Compare(a, b), true }
	case "PyPI":
		return func(a, b string) (int, bool) {
			if !pyversion.Valid(a) || !pyversion.Valid(b) {
				return 0, false
			}

			return pyversion.Compare(a, b), true
		}
	default:
		return nil
	}
}

func compareSemver(a, b string) (int, bool) {
	a, b = semverForm(a), semverForm(b)
	if !semver.IsValid(a) || !semver.IsValid(b) {
		return 0, false
	}

	return semver.Compare(a, b), true
}

// semverForm adds the "v" prefix golang.org/x/mod/semver requires.
func semverForm(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "="), "v")

	return "v" + v
}
