// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package repotype defines the repository types and which formats can be
// served as proxy or group repositories. It is the single policy shared by
// repository creation (reject what cannot be served) and request dispatch
// (serve what was accepted).
package repotype

const (
	Hosted = "hosted"
	Proxy  = "proxy"
	Group  = "group"
)

// DockerHubUpstream is the upstream of a docker proxy repository created
// without an explicit upstream_url.
const DockerHubUpstream = "https://registry-1.docker.io"

// ProxySupported reports whether proxy repositories of format can be served:
// the formats whose upstream files are mirrored by path, and docker, which is
// pulled through the OCI distribution protocol.
func ProxySupported(format string) bool {
	switch format {
	case "maven", "deb", "docker", "rpm", "raw", "pypi", "npm":
		return true
	}

	return false
}

// GroupSupported reports whether group repositories of format can be served.
// Groups resolve members by path (and merge Maven metadata, npm packuments and
// PyPI project pages) or, for docker, by OCI manifest/blob; deb/rpm groups
// would need Packages/Release and repodata merged across members and are
// rejected instead.
func GroupSupported(format string) bool {
	switch format {
	case "maven", "docker", "raw", "pypi", "npm":
		return true
	}

	return false
}

// DefaultUpstreamURL is the upstream a proxy repository of format uses when
// none is given, "" for formats without a well-known public upstream.
func DefaultUpstreamURL(format string) string {
	switch format {
	case "docker":
		return DockerHubUpstream
	case "pypi":
		return "https://pypi.org/simple"
	}

	return ""
}

// ProxyFormats lists the proxy-capable formats for error messages.
func ProxyFormats() string { return "deb, docker, maven, npm, pypi, raw, rpm" }

// GroupFormats lists the group-capable formats for error messages.
func GroupFormats() string { return "docker, maven, npm, pypi, raw" }
