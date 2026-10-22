// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"golang.org/x/mod/semver"
)

// incompatibleSuffix marks a v2+ version of a module without a go.mod, the
// only build metadata Go module versions may carry.
const incompatibleSuffix = "+incompatible"

// validVersion reports whether v is a canonical Go module version: a full
// vMAJOR.MINOR.PATCH[-prerelease], optionally with +incompatible. Shorthands
// such as "v1" or "v1.2" and arbitrary build metadata are not module versions
// the go command would ever request or accept from a proxy.
func validVersion(v string) bool {
	if !semver.IsValid(v) {
		return false
	}

	canonical := semver.Canonical(v)

	return v == canonical || v == canonical+incompatibleSuffix
}

// compareVersions orders two module versions by semantic-version precedence
// (not lexically: v1.10.0 > v1.9.0 and v1.0.0 > v1.0.0-rc1). Ties, which
// semver reports for versions differing only in build metadata, fall back to
// the string order so the result is deterministic.
func compareVersions(a, b string) int {
	if c := semver.Compare(a, b); c != 0 {
		return c
	}

	return strings.Compare(a, b)
}

// sortVersionsDesc orders modules from the highest to the lowest version.
func sortVersionsDesc(mods []*models.GoModule) {
	slices.SortStableFunc(mods, func(a, b *models.GoModule) int {
		return compareVersions(b.Version, a.Version)
	})
}

// latestVersion picks the module version GOPROXY @latest must report: the
// highest release, or the highest prerelease (pseudo-versions included) when
// the module has no release. It returns nil for an empty list.
func latestVersion(mods []*models.GoModule) *models.GoModule {
	var latest, latestPre *models.GoModule

	for _, m := range mods {
		best := &latest
		if semver.Prerelease(m.Version) != "" {
			best = &latestPre
		}

		if *best == nil || compareVersions(m.Version, (*best).Version) > 0 {
			*best = m
		}
	}

	if latest != nil {
		return latest
	}

	return latestPre
}
