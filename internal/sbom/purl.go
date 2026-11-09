// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/repotype"
)

// purlType maps an OpenArtifact repository format to its Package URL type;
// formats without one (raw, terraform) and unknown formats are "generic".
func purlType(format string) string {
	if s, _ := repotype.Lookup(strings.ToLower(format)); s.PURLType != "" {
		return s.PURLType
	}

	return "generic"
}

// purlPath encodes each '/'-separated segment of a purl namespace/name.
func purlPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = repotype.PURLEscape(s)
	}

	return strings.Join(segs, "/")
}

// generatePURL builds the Package URL of an artifact. Maven coordinates may be
// written "group:artifact" or "group/artifact"; pypi names are normalized.
// version "" and "unknown" yield a version-less purl.
func generatePURL(format, name, version string) string {
	return generatePURLNS(format, "", name, version)
}

// generatePURLNS is generatePURL with an explicit namespace for the formats that
// require one (deb: the distribution, rpm: the vendor); other formats ignore it.
// Without one the version tells it where it can (a deb with an Ubuntu suffix, an
// rpm release ".el9"), so a package never gets a namespace-less purl that no
// vulnerability report matches.
func generatePURLNS(format, namespace, name, version string) string {
	typ := purlType(format)

	if h, ok := hooks.Lookup(strings.ToLower(format)); ok {
		if namespace == "" && h.Namespace != nil {
			namespace = h.Namespace(version)
		}

		if h.PURLName != nil {
			name = h.PURLName(namespace, name)
		}
	}

	purl := "pkg:" + typ + "/" + purlPath(name)
	if version != "" && version != "unknown" {
		purl += "@" + repotype.PURLEscape(version)
	}

	return purl
}

// ArtifactPURL returns the Package URL OpenArtifact gives an artifact of a
// repository format: the purl of its SBOM and VEX documents, and the identity
// imported VEX statements are matched on (after NormalizePURL).
func ArtifactPURL(format, artifactID string) string {
	name, version := splitArtifactID(artifactID)

	return generatePURL(format, name, version)
}

// NormalizePURL returns the canonical form of a versioned Package URL for
// equality matching: "pkg:<type>/<namespace>/<name>@<version>" with the type
// lower-cased, every segment unescaped then escaped like OpenArtifact's own
// purls, qualifiers and subpath dropped and pypi names normalized. ok is false
// when p is not a purl or names no version: a versionless statement would
// speak about every version of a package.
func NormalizePURL(p string) (string, bool) {
	scheme, rest, found := strings.Cut(strings.TrimSpace(p), ":")
	if !found || !strings.EqualFold(scheme, "pkg") {
		return "", false
	}

	rest, _, _ = strings.Cut(rest, "#")
	rest, _, _ = strings.Cut(rest, "?")
	rest = strings.Trim(rest, "/")

	typ, rest, found := strings.Cut(rest, "/")
	if typ = strings.ToLower(typ); !found || !validPURLType(typ) {
		return "", false
	}

	// The version follows the last '@', unless that '@' opens a segment (an
	// unescaped npm scope of a versionless purl).
	i := strings.LastIndex(rest, "@")
	if i <= 0 || rest[i-1] == '/' {
		return "", false
	}

	version, err := url.PathUnescape(rest[i+1:])
	if err != nil || version == "" {
		return "", false
	}

	segs := strings.Split(strings.Trim(rest[:i], "/"), "/")
	for j, s := range segs {
		if s, err = url.PathUnescape(s); err != nil || s == "" {
			return "", false
		}

		segs[j] = s
	}

	if typ == "pypi" {
		segs[len(segs)-1] = pypiPURLName("", segs[len(segs)-1])
	}

	for j, s := range segs {
		segs[j] = repotype.PURLEscape(s)
	}

	return "pkg:" + typ + "/" + strings.Join(segs, "/") + "@" + repotype.PURLEscape(version), true
}

// validPURLType reports whether typ is a lower-cased purl type: ASCII letters,
// digits, '.', '+' and '-', not starting with a digit.
func validPURLType(typ string) bool {
	if typ == "" || (typ[0] >= '0' && typ[0] <= '9') {
		return false
	}

	for _, c := range typ {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '+' && c != '-' {
			return false
		}
	}

	return true
}
