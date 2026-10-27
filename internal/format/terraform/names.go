// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"strings"

	"golang.org/x/mod/semver"
)

const (
	// maxNameLen is the width of the name, system and type columns.
	maxNameLen = 64
	// maxVersionLen is the width of the version column.
	maxVersionLen = 100
	// maxPlatformLen is the width of the os and arch columns.
	maxPlatformLen = 32
)

// reservedNames cannot be a module name or a provider type: the web UI owns
// /repos/{repo}/packages/..., so a registry URL starting with it never reaches
// this handler.
func reservedName(s string) bool { return strings.EqualFold(s, "packages") }

// isNameByte reports whether c may appear inside a module name, module system
// or provider type.
func isNameByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_'
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// validName reports whether s is a registry name: 1-64 characters of
// [0-9A-Za-z_-] that start and end with a letter or digit. The shape is what
// the Terraform registry protocol accepts and keeps every name safe to embed in
// a URL path segment, a file name and a ticket.
func validName(s string) bool {
	if s == "" || len(s) > maxNameLen || !isAlnum(s[0]) || !isAlnum(s[len(s)-1]) {
		return false
	}

	for i := range len(s) {
		if !isNameByte(s[i]) {
			return false
		}
	}

	return true
}

// validUploadName is validName without the reserved names.
func validUploadName(s string) bool {
	return validName(s) && !reservedName(s)
}

// validModuleSystem is validUploadName that also refuses "versions": a
// module's "latest" URL /{name}/{system} would collide with the provider
// versions URL /{type}/versions.
func validModuleSystem(s string) bool {
	return validUploadName(s) && !strings.EqualFold(s, "versions")
}

// validPlatformPart reports whether s is an os or arch: 1-32 characters of
// [a-z0-9] (no underscore, because "os_arch" is the platform spelling).
func validPlatformPart(s string) bool {
	if s == "" || len(s) > maxPlatformLen {
		return false
	}

	for i := range len(s) {
		if c := s[i]; c < '0' || c > '9' && c < 'a' || c > 'z' {
			return false
		}
	}

	return true
}

// validVersion reports whether v is a strict semantic version without a "v"
// prefix and without build metadata: MAJOR.MINOR.PATCH with an optional
// prerelease. Build metadata is refused because versions are unique by
// precedence in Terraform, and a "+" is awkward in URLs and file names.
func validVersion(v string) bool {
	if v == "" || len(v) > maxVersionLen || strings.ContainsAny(v, "+") || v[0] == 'v' {
		return false
	}

	// Canonical fills in a shorthand ("1.2") and strips build metadata, so a
	// version is strict exactly when it is its own canonical form.
	return semver.Canonical("v"+v) == "v"+v
}

// isPrerelease reports whether a valid version has a prerelease part.
func isPrerelease(v string) bool {
	return semver.Prerelease("v"+v) != ""
}

// compareVersions orders two valid versions by semantic-version precedence.
func compareVersions(a, b string) int {
	return semver.Compare("v"+a, "v"+b)
}

// latestVersion picks the version a client gets when it asks for "the latest":
// the highest stable version, or the highest of all when every version is a
// prerelease. It returns "" for an empty list.
func latestVersion(versions []string) string {
	best, bestStable := "", ""

	for _, v := range versions {
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}

		if !isPrerelease(v) && (bestStable == "" || compareVersions(v, bestStable) > 0) {
			bestStable = v
		}
	}

	if bestStable != "" {
		return bestStable
	}

	return best
}

// platformName is the "os_arch" spelling used in file names and tickets.
func platformName(osName, arch string) string {
	return osName + "_" + arch
}

// providerZipName is the file name of one provider platform archive.
func providerZipName(typ, version, osName, arch string) string {
	return "terraform-provider-" + typ + "_" + version + "_" + platformName(osName, arch) + ".zip"
}

// providerSumsName is the file name of a provider version's checksum file.
func providerSumsName(typ, version string) string {
	return "terraform-provider-" + typ + "_" + version + "_SHA256SUMS"
}

// moduleFileName is the file name a module archive is downloaded as; its
// extension tells the client how to unpack it.
func moduleFileName(name, system, version, ext string) string {
	return name + "-" + system + "-" + version + "." + ext
}
