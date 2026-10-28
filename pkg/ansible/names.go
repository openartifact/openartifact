// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ansible reads and validates Ansible collection artifacts, the
// ns-name-version.tar.gz files built by "ansible-galaxy collection build" and
// published to a Galaxy server, and holds the naming rules of collections.
package ansible

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	// MaxNameLen is the longest namespace or collection name.
	MaxNameLen = 64
	// MaxVersionLen is the longest collection version.
	MaxVersionLen = 128
	// MaxRequirementLen is the longest dependency requirement.
	MaxRequirementLen = 255
	// FileSuffix ends every collection artifact file name.
	FileSuffix = ".tar.gz"
)

// semverRe is the grammar of https://semver.org (no leading "v", no short
// forms): the versions "ansible-galaxy collection build" accepts.
var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// requirementRe is one clause of a version requirement: an optional operator
// and a version-like token ("*" is handled separately).
var requirementRe = regexp.MustCompile(`^(==|!=|>=|<=|>|<)?[0-9A-Za-z][0-9A-Za-z.+-]*$`)

// ValidName reports whether s is a valid namespace or collection name: 1 to
// MaxNameLen lower-case letters, digits and underscores, not starting with a
// digit. The shape keeps every name safe in a URL path, a file name and a
// dotted "namespace.name" (neither part holds "." or "-").
func ValidName(s string) bool {
	if s == "" || len(s) > MaxNameLen || s[0] >= '0' && s[0] <= '9' {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}

	return true
}

// ValidVersion reports whether v is a semantic version (semver.org, three
// numeric parts, optional pre-release and build metadata).
func ValidVersion(v string) bool {
	return len(v) <= MaxVersionLen && semverRe.MatchString(v)
}

// IsPrerelease reports whether the valid version v has a pre-release part.
func IsPrerelease(v string) bool { return semver.Prerelease("v"+v) != "" }

// CompareVersions orders valid versions by semantic-version precedence; build
// metadata does not count, so versions equal by precedence are ordered by their
// text to keep the order total.
func CompareVersions(a, b string) int {
	if c := semver.Compare("v"+a, "v"+b); c != 0 {
		return c
	}

	return strings.Compare(a, b)
}

// FQCN joins a namespace and a name into the "namespace.name" collection id.
func FQCN(namespace, name string) string { return namespace + "." + name }

// SplitFQCN splits "namespace.name" and reports whether both parts are valid.
func SplitFQCN(s string) (string, string, bool) {
	namespace, name, ok := strings.Cut(s, ".")
	if !ok || !ValidName(namespace) || !ValidName(name) {
		return "", "", false
	}

	return namespace, name, true
}

// FileName is the artifact file name of a collection version.
func FileName(namespace, name, version string) string {
	return namespace + "-" + name + "-" + version + FileSuffix
}

// ParseFileName splits "namespace-name-version.tar.gz" and reports whether all
// three parts are valid.
func ParseFileName(file string) (string, string, string, bool) {
	stem, found := strings.CutSuffix(file, FileSuffix)
	if !found {
		return "", "", "", false
	}

	namespace, rest, found := strings.Cut(stem, "-")
	if !found {
		return "", "", "", false
	}

	name, version, found := strings.Cut(rest, "-")
	if !found || !ValidName(namespace) || !ValidName(name) || !ValidVersion(version) {
		return "", "", "", false
	}

	return namespace, name, version, true
}

// ValidRequirement reports whether spec is a dependency version requirement:
// "*", a version, or comma-separated clauses with a comparison operator
// ("==", "!=", ">=", "<=", ">", "<"), as "ansible-galaxy" reads them.
func ValidRequirement(spec string) bool {
	if spec == "" || len(spec) > MaxRequirementLen {
		return false
	}

	if strings.TrimSpace(spec) == "*" {
		return true
	}

	for clause := range strings.SplitSeq(spec, ",") {
		if !requirementRe.MatchString(strings.ReplaceAll(clause, " ", "")) {
			return false
		}
	}

	return true
}
