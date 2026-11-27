// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package swift implements the parts of the Swift Package Registry protocol
// (SE-0292, https://github.com/swiftlang/swift-package-manager/blob/main/Documentation/PackageRegistry/Registry.md)
// that do not depend on a server: the naming rules of scopes, names and
// versions, the grammar of registry paths, the Accept negotiation, Link
// headers, the JSON documents a registry serves and the reading of the source
// archive a release is published as.
package swift

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	// MaxScopeLen is the longest scope.
	MaxScopeLen = 39
	// MaxNameLen is the longest package name.
	MaxNameLen = 100
	// MaxVersionLen is the longest version.
	MaxVersionLen = 128
	// ContentVersion is the value of the Content-Version header: the version of
	// the registry API a response conforms to.
	ContentVersion = "1"
)

// semverRe is the grammar of https://semver.org: no leading "v", no short forms.
var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// ValidScope reports whether s is a package scope: 1 to MaxScopeLen letters,
// digits and hyphens, starting and ending with a letter or digit, no two
// hyphens in a row.
func ValidScope(s string) bool {
	return validIdent(s, MaxScopeLen, false)
}

// ValidName reports whether s is a package name: 1 to MaxNameLen letters,
// digits, hyphens and underscores, starting and ending with a letter or digit,
// no two separators in a row.
func ValidName(s string) bool {
	return validIdent(s, MaxNameLen, true)
}

// validIdent checks the grammar the specification writes as the lookahead
// regular expressions \A[a-zA-Z0-9](?:[a-zA-Z0-9]|-(?=[a-zA-Z0-9])){0,38}\z
// (and [-_] for names): a separator must be followed by a letter or digit.
func validIdent(s string, maxLen int, underscore bool) bool {
	if s == "" || len(s) > maxLen {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if alnum(c) {
			continue
		}

		sep := c == '-' || (underscore && c == '_')
		if !sep || i == 0 || i == len(s)-1 || !alnum(s[i+1]) {
			return false
		}
	}

	return true
}

func alnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// ValidVersion reports whether v is a semantic version (three numeric parts,
// optional pre-release and build metadata).
func ValidVersion(v string) bool {
	return len(v) <= MaxVersionLen && semverRe.MatchString(v)
}

// ValidPublishVersion reports whether a release may be published as v: a valid
// version that does not end in ".zip" or ".json", which would make the URL of
// the release ambiguous with its source archive and its ".json" form.
func ValidPublishVersion(v string) bool {
	return ValidVersion(v) && !strings.HasSuffix(v, ".zip") && !strings.HasSuffix(v, ".json")
}

// ID is the package identity "scope.name" as the registry documents spell it.
func ID(scope, name string) string { return scope + "." + name }

// Key is the case-folded identity packages are looked up by: scopes and names
// are case-insensitive.
func Key(scope, name string) string { return strings.ToLower(scope + "." + name) }

// SplitID splits "scope.name". Neither part may hold a dot, so the first one is
// the separator.
func SplitID(id string) (string, string, bool) {
	scope, name, ok := strings.Cut(id, ".")

	return scope, name, ok && ValidScope(scope) && ValidName(name)
}

// Compare orders two valid versions by semantic-version precedence and breaks
// ties (versions that differ only in build metadata) by their text, so that the
// order is total.
func Compare(a, b string) int {
	if c := semver.Compare("v"+a, "v"+b); c != 0 {
		return c
	}

	return strings.Compare(a, b)
}
