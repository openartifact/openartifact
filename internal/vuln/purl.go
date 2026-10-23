// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"errors"
	"strings"
)

var errNoPURL = errors.New("package cannot be expressed as a package URL")

// purlTypes maps our ecosystem names to Package URL types. An ecosystem that is
// not here is not scannable with grype: that includes deb and rpm, whose
// packages only mean something within a distribution that a name and a version
// do not give (grype warns and reports no match).
//
//nolint:gochecknoglobals // immutable ecosystem mapping table
var purlTypes = map[string]string{
	"npm":      "npm",
	"pypi":     "pypi",
	"maven":    "maven",
	"cargo":    "cargo",
	"rubygems": "gem",
	"nuget":    "nuget",
	"go":       "golang",
}

// buildPURL returns the Package URL (https://github.com/package-url/purl-spec)
// of the package, the form grype takes as a scan target. Every component is
// percent-encoded, so no input can add a component, a qualifier or a command
// line option (the result always starts with "pkg:").
func buildPURL(ecosystem, name, version string) (string, error) {
	eco := strings.ToLower(ecosystem)

	purlType, ok := purlTypes[eco]
	if !ok {
		return "", errUnsupportedEcosystem
	}

	var namespace []string

	pkg := name

	switch eco {
	case "npm":
		// "@scope/name": the scope is the namespace, "@" encoded.
		pkg = strings.ToLower(name)
		if scope, rest, found := strings.Cut(pkg, "/"); found {
			namespace, pkg = []string{scope}, rest
		}
	case "pypi":
		pkg = strings.ReplaceAll(strings.ToLower(name), "_", "-")
	case "maven":
		// "group:artifact" (the form the maven scanner manifest uses); a bare
		// name is its own group, as in the trivy manifest.
		group, artifact, found := strings.Cut(name, ":")
		if !found {
			group, artifact = name, name
		}

		namespace, pkg = []string{group}, artifact
	case "go":
		// The module path "github.com/org/mod": everything before the last
		// element is the namespace.
		if mod, last, found := strings.CutLast(name, "/"); found {
			namespace, pkg = strings.Split(mod, "/"), last
		}
	}

	if pkg == "" || version == "" {
		return "", errNoPURL
	}

	var b strings.Builder

	b.WriteString("pkg:" + purlType + "/")

	for _, ns := range namespace {
		if ns == "" {
			return "", errNoPURL
		}

		b.WriteString(purlEscape(ns) + "/")
	}

	b.WriteString(purlEscape(pkg) + "@" + purlEscape(version))

	return b.String(), nil
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
// ecosystem with no registered Package URL type (deb, rpm, helm) or a name that
// cannot be expressed gets a generic one (pkg:generic/name@version) that names
// the ecosystem as a qualifier.
func PackageURL(ecosystem, name, version string) string {
	if p, err := buildPURL(ecosystem, name, version); err == nil {
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
