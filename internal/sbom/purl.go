// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strings"
)

// purlType maps an OpenArtifact repository format to its Package URL type.
func purlType(format string) string {
	switch strings.ToLower(format) {
	case "npm":
		return "npm"
	case "maven", "ivy", "sbt":
		return "maven"
	case "pypi":
		return "pypi"
	case "cargo":
		return "cargo"
	case "rubygems":
		return "gem"
	case "helm":
		return "helm"
	case "nuget":
		return "nuget"
	case "docker":
		return "docker"
	case "deb":
		return "deb"
	case "rpm":
		return "rpm"
	case "go":
		return "golang"
	case "conan":
		return "conan"
	case "conda":
		return "conda"
	default:
		return "generic"
	}
}

// purlEncode percent-encodes one purl component: everything but the
// unreserved characters (and ':' in versions) per the purl specification.
func purlEncode(s string) string {
	const hexdigits = "0123456789ABCDEF"

	var b strings.Builder

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '~', c == ':':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexdigits[c>>4])
			b.WriteByte(hexdigits[c&15])
		}
	}

	return b.String()
}

// purlPath encodes each '/'-separated segment of a purl namespace/name.
func purlPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = purlEncode(s)
	}

	return strings.Join(segs, "/")
}

// generatePURL builds the Package URL of an artifact. Maven coordinates may be
// written "group:artifact" or "group/artifact"; pypi names are normalized.
// version "" and "unknown" yield a version-less purl.
func generatePURL(format, name, version string) string {
	typ := purlType(format)

	switch typ {
	case "maven":
		if g, a, ok := strings.Cut(name, ":"); ok {
			name = g + "/" + a
		}
	case "pypi":
		name = strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	}

	purl := "pkg:" + typ + "/" + purlPath(name)
	if version != "" && version != "unknown" {
		purl += "@" + purlEncode(version)
	}

	return purl
}
