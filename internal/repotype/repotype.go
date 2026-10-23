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
	case "maven", "deb", "docker", "rpm", "raw", "pypi", "npm", "cargo", "go", "helm", "nuget":
		return true
	}

	return false
}

// GroupSupported reports whether group repositories of format can be served.
// Groups resolve members by path (and merge Maven metadata, npm packuments,
// NuGet registrations, PyPI project pages and Go version lists), for docker by
// OCI manifest/blob, and for deb and rpm by generating the Release/Packages
// indexes or repodata of the group from all members (internal/worker's
// GroupMetadataWorker) and serving each package from the member that owns it.
func GroupSupported(format string) bool {
	switch format {
	case "maven", "deb", "docker", "raw", "pypi", "npm", "cargo", "go", "helm", "nuget", "rpm":
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
	case "cargo":
		return "https://index.crates.io"
	case "go":
		return "https://proxy.golang.org"
	case "nuget":
		return "https://api.nuget.org/v3/index.json"
	}

	return ""
}

// ProxyFormats lists the proxy-capable formats for error messages.
func ProxyFormats() string { return "cargo, deb, docker, go, helm, maven, npm, nuget, pypi, raw, rpm" }

// GroupFormats lists the group-capable formats for error messages.
func GroupFormats() string { return "cargo, deb, docker, go, helm, maven, npm, nuget, pypi, raw, rpm" }

// FederationSupported reports whether hosted repositories of format can be
// federated (docs/design/federation.md): the formats whose units the
// federation codecs (internal/federation) replicate.
func FederationSupported(format string) bool {
	switch format {
	case "raw", "maven", "npm", "docker", "helm", "pypi", "cargo", "go", "nuget":
		return true
	}

	return false
}

// FederationFormats lists the federation-capable formats for error messages.
func FederationFormats() string { return "cargo, docker, go, helm, maven, npm, nuget, pypi, raw" }
