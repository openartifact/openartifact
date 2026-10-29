// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"errors"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/repotype"
)

var (
	errNoPURL = errors.New("package cannot be expressed as a package URL")

	// errDistroRequired marks a deb or rpm whose distribution is not known:
	// grype matches it against the advisories of one distribution release and
	// finds nothing when it does not know which.
	errDistroRequired = errors.New("package needs a distribution release to be scanned")
)

// buildPURL returns the Package URL (https://github.com/package-url/purl-spec)
// of the package, the form grype takes as a scan target. Every component is
// percent-encoded, so no input can add a component, a qualifier or a command
// line option (the result always starts with "pkg:").
//
// A deb or rpm needs its distribution release as the distro qualifier
// (pkg:deb/debian/openssl@3.0.11-1~deb12u2?distro=debian-12); without one it is
// refused with errDistroRequired.
func buildPURL(p Package) (string, error) {
	eco := strings.ToLower(p.Ecosystem)

	spec, ok := repotype.Lookup(eco)
	if !ok || !spec.GrypeScan {
		return "", errUnsupportedEcosystem
	}

	parts := PURLParts{Name: p.Name, Version: p.Version}

	if h, ok := hooks.Lookup(eco); ok && h.PURL != nil {
		var err error

		if parts, err = h.PURL(p); err != nil {
			return "", err
		}
	}

	if parts.Name == "" || parts.Version == "" {
		return "", errNoPURL
	}

	var b strings.Builder

	b.WriteString("pkg:" + spec.PURLType + "/")

	for _, ns := range parts.Namespace {
		if ns == "" {
			return "", errNoPURL
		}

		b.WriteString(purlEscape(ns) + "/")
	}

	b.WriteString(purlEscape(parts.Name) + "@" + purlEscape(parts.Version) + parts.Qualifiers)

	return b.String(), nil
}

// composerVersion drops the "v" of a "v1.2.3" tag.
func composerVersion(v string) string {
	if len(v) > 1 && v[0] == 'v' && v[1] >= '0' && v[1] <= '9' {
		return v[1:]
	}

	return v
}

// rpmEVR returns the epoch and the "version-release" an rpm is scanned as. The
// epoch comes from the Epoch field or, when that is zero, from an "E:" prefix of
// the version (the form "rpm -q" prints); the release is appended unless the
// version already carries one (an rpm version cannot contain a hyphen).
func rpmEVR(p Package) (int, string) {
	epoch, version := p.Epoch, p.Version

	if e, rest, found := strings.Cut(version, ":"); found {
		if n, err := strconv.Atoi(e); err == nil && n >= 0 {
			version = rest
			if epoch == 0 {
				epoch = n
			}
		}
	}

	if p.Release != "" && !strings.Contains(version, "-") {
		version += "-" + p.Release
	}

	return epoch, version
}

// purlEscape percent-encodes everything but the unreserved characters
// (letters, digits, "." "-" "_" "~"): the strictest encoding, which every
// Package URL parser decodes.
func purlEscape(s string) string {
	const hex = "0123456789ABCDEF"

	var b strings.Builder

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}

	return b.String()
}

// PackageURL returns the Package URL of a scanned package for reports. An
// ecosystem with no registered Package URL type (helm), a deb or rpm (a stored
// scan does not record its distribution) or a name that cannot be expressed
// gets a generic one (pkg:generic/name@version) that names the ecosystem as a
// qualifier.
func PackageURL(ecosystem, name, version string) string {
	if p, err := buildPURL(Package{Name: name, Version: version, Ecosystem: ecosystem}); err == nil {
		return p
	}

	p := "pkg:generic/" + purlEscape(name)
	if version != "" {
		p += "@" + purlEscape(version)
	}

	if ecosystem != "" {
		p += "?ecosystem=" + purlEscape(strings.ToLower(ecosystem))
	}

	return p
}
