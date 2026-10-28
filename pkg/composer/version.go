// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxVersionLen is the longest version string (and normalized version) stored.
const MaxVersionLen = 128

// ErrInvalidVersion marks a string that is not a Composer version.
var ErrInvalidVersion = errors.New("invalid version")

// modifierRe is VersionParser::$modifierRegex: the stability suffix, its
// number and the optional "-dev" marker.
const modifierRe = `[._-]?(?:(stable|beta|b|RC|alpha|a|patch|pl|p)((?:[.-]?\d+)*)?)?([.-]?dev)?`

var (
	aliasRe       = regexp.MustCompile(`^([^,\s]+) +as +([^,\s]+)$`)
	stabilityRe   = regexp.MustCompile(`(?i)@(?:stable|RC|beta|alpha|dev)$`)
	buildRe       = regexp.MustCompile(`^([^,\s+]+)\+[^\s]+$`)
	classicalRe   = regexp.MustCompile(`(?i)^v?(\d{1,5})(\.\d+)?(\.\d+)?(\.\d+)?` + modifierRe + `$`)
	datetimeRe    = regexp.MustCompile(`(?i)^v?(\d{4}(?:[.:-]?\d{2}){1,6}(?:[.:-]?\d{1,3}){0,2})` + modifierRe + `$`)
	devBranchRe   = regexp.MustCompile(`(?i)(.*?)[.-]?dev$`)
	numericBrchRe = regexp.MustCompile(`(?i)^v?(\d+)(\.(?:\d+|[xX*]))?(\.(?:\d+|[xX*]))?(\.(?:\d+|[xX*]))?$`)
	nonDigitRe    = regexp.MustCompile(`\D`)
	// publishedRe is what an uploaded version may be made of: it ends up in a
	// URL path, a JSON document and a file name.
	publishedRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/~#-]*$`)
)

// phpTrim is the character set of PHP's trim().
const phpTrim = " \t\n\r\x00\x0b"

// Normalize ports Composer's VersionParser::normalize: it turns a version as
// written in a tag or branch ("v1.2.3", "1.0.0-RC1", "dev-main", "1.x-dev")
// into the form Composer compares ("1.2.3.0", "1.0.0.0-RC1", "dev-main",
// "1.9999999.9999999.9999999-dev"). It returns ErrInvalidVersion for what
// Composer rejects.
func Normalize(version string) (string, error) {
	version = strings.Trim(version, phpTrim)
	orig := version

	if m := aliasRe.FindStringSubmatch(version); m != nil {
		version = m[1]
	}

	if loc := stabilityRe.FindStringIndex(version); loc != nil {
		version = version[:loc[0]]
	}

	switch version {
	case "master", "trunk", "default":
		version = "dev-" + version
	}

	if len(version) >= 4 && strings.EqualFold(version[:4], "dev-") {
		return "dev-" + version[4:], nil
	}

	if m := buildRe.FindStringSubmatch(version); m != nil {
		version = m[1]
	}

	if n, ok := normalizeNumeric(version); ok {
		return n, nil
	}

	if m := devBranchRe.FindStringSubmatch(version); m != nil {
		if n := normalizeBranch(m[1]); !strings.Contains(n, "dev-") {
			return n, nil
		}
	}

	return "", fmt.Errorf("%w: %q", ErrInvalidVersion, orig)
}

// normalizeNumeric handles the classical (1.2.3.4) and the date based
// (2010-01-02, 20100102-203040) forms with their stability modifiers.
func normalizeNumeric(version string) (string, bool) {
	var (
		out   string
		stab  []string
		found bool
	)

	if m := classicalRe.FindStringSubmatch(version); m != nil {
		out = m[1] + orDefault(m[2], ".0") + orDefault(m[3], ".0") + orDefault(m[4], ".0")
		stab, found = m[5:], true
	} else if m := datetimeRe.FindStringSubmatch(version); m != nil {
		out = nonDigitRe.ReplaceAllString(m[1], ".")
		stab, found = m[2:], true
	}

	if !found {
		return "", false
	}

	// stab is: stability keyword, its number, the "-dev" marker.
	if stab[0] != "" {
		if stab[0] == "stable" {
			return out, true
		}

		out += "-" + expandStability(stab[0]) + strings.TrimLeft(stab[1], ".-")
	}

	if stab[2] != "" {
		out += "-dev"
	}

	return out, true
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}

	return s
}

// expandStability is VersionParser::expandStability.
func expandStability(s string) string {
	switch strings.ToLower(s) {
	case "a":
		return "alpha"
	case "b":
		return "beta"
	case "p", "pl":
		return "patch"
	case "rc":
		return "RC"
	default:
		return strings.ToLower(s)
	}
}

// normalizeBranch is VersionParser::normalizeBranch: "1.x" becomes the
// 9999999-padded "-dev" version, any other name "dev-name".
func normalizeBranch(name string) string {
	name = strings.Trim(name, phpTrim)

	m := numericBrchRe.FindStringSubmatch(name)
	if m == nil {
		return "dev-" + name
	}

	var b strings.Builder

	for _, part := range m[1:] {
		if part == "" {
			part = ".x"
		}

		b.WriteString(strings.NewReplacer("*", "x", "X", "x").Replace(part))
	}

	return strings.ReplaceAll(b.String(), "x", "9999999") + "-dev"
}

// IsDev reports whether a normalized version is a development version: a
// branch ("dev-main") or a numeric branch ("1.9999999.9999999.9999999-dev").
// Composer repositories serve those in the "~dev" metadata document and every
// other version, pre-releases included, in the stable one.
func IsDev(normalized string) bool {
	return strings.HasPrefix(normalized, "dev-") || strings.HasSuffix(normalized, "-dev")
}

// NormalizePublished validates a version given for an upload and returns its
// normalized form. Besides what Composer's Normalize accepts it requires a
// plain version string: no surrounding space, aliases ("x as y"), stability
// flags ("@dev") or characters that do not belong in a URL path, and a
// non-empty branch name after "dev-".
func NormalizePublished(version string) (string, error) {
	if len(version) > MaxVersionLen || !publishedRe.MatchString(version) {
		return "", fmt.Errorf("%w: %q", ErrInvalidVersion, version)
	}

	n, err := Normalize(version)
	if err != nil {
		return "", err
	}

	if n == "dev-" || len(n) > MaxVersionLen {
		return "", fmt.Errorf("%w: %q", ErrInvalidVersion, version)
	}

	return n, nil
}
