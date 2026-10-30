// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
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
