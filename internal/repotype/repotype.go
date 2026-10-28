// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package repotype is the registry of package formats and the policy derived
// from it: which formats exist, which can be served as proxy, group or
// federated repositories, and the per-format facts that more than one package
// needs (default upstream, signing, URL namespace, Package URL type, vuln
// ecosystem, version rules). It is the single policy shared by repository
// creation (reject what cannot be served) and request dispatch (serve what was
// accepted).
//
// Adding a format starts here: add one Spec to the registry, then follow the
// checklist in docs/development/README.md. The exhaustiveness tests in this
// package and next to every per-format switch fail, naming the place, until the
// new format is handled everywhere.
package repotype

import (
	"slices"
	"strings"
)

// The web UI consumes the format lists as web/src/lib/formats.generated.ts.
//go:generate go run ../../cmd/genformats -root ../..

const (
	Hosted = "hosted"
	Proxy  = "proxy"
	Group  = "group"
)

// DockerHubUpstream is the upstream of a docker proxy repository created
// without an explicit upstream_url.
const DockerHubUpstream = "https://registry-1.docker.io"

// Signing is how a format's repository metadata is signed.
type Signing string

const (
	// SigningNone is a format whose metadata is not signed.
	SigningNone Signing = ""
	// SigningGPG is a format signed with an OpenPGP key (admin GPG keys can be
	// assigned to its hosted repositories).
	SigningGPG Signing = "gpg"
	// SigningRSA is a format signed with the RSA key of its own admin endpoint
	// (the APKINDEX key of apk).
	SigningRSA Signing = "rsa"
	// SigningEd25519 is a format signed with an ed25519 key of its own admin
	// endpoint (the narinfo key of nix).
	SigningEd25519 Signing = "ed25519"
)

// Versioning is the version scheme of a format; it decides what counts as a
// release and how versions compare.
type Versioning string

const (
	// VersionOpaque is a format without a snapshot or pre-release concept:
	// every version is a release (raw, deb, rpm, pacman, conda, conan, docker,
	// apk).
	VersionOpaque Versioning = ""
	// VersionMaven is the Maven scheme: "-SNAPSHOT" versions are not releases,
	// and versions compare with the Maven comparator (maven, ivy, sbt).
	VersionMaven Versioning = "maven"
	// VersionSemver is semantic versioning: a "-" pre-release part is not a
	// release (npm, nuget, cargo, helm, go, terraform).
	VersionSemver Versioning = "semver"
	// VersionPEP440 is the Python scheme: a/b/rc/dev segments are not releases.
	VersionPEP440 Versioning = "pep440"
	// VersionGem is the RubyGems scheme: a letter in the version makes it a
	// pre-release.
	VersionGem Versioning = "gem"
)

// Spec describes one package format. Name is the repository format id: the
// value of repositories.format and of the API's format field.
type Spec struct {
	Name string
	// Proxy and Group report whether proxy or group repositories of the format
	// can be served.
	Proxy bool
	Group bool
	// Federation reports whether hosted repositories can be federated
	// (docs/design/federation.md): the formats whose units the federation
	// codecs replicate.
	Federation bool
	// GeneratedGroupMetadata is set for formats whose groups serve metadata the
	// group worker generates from their members (deb, rpm, apk, pacman) rather
	// than resolving requests member by member.
	GeneratedGroupMetadata bool
	// DefaultUpstream is the upstream a proxy repository uses when none is
	// given, "" for formats without a well-known public upstream.
	DefaultUpstream string
	// Signing is how the format's metadata is signed.
	Signing Signing
	// Isolated formats are served under /repos/<format>/{repo}/..., so the
	// format name can never be a repository name. Every other format is served
	// under /repos/{repo}/....
	Isolated bool
	// Versioning is the version scheme.
	Versioning Versioning
	// PURLType is the Package URL type of the format's packages, "" for
	// formats without one (their packages use the "generic" type).
	PURLType string
	// OSVEcosystem is the OSV ecosystem name the OSV scanner queries, "" for
	// formats OSV has no ecosystem for.
	OSVEcosystem string
	// GrypeScan reports whether grype can scan the format's packages through
	// their Package URL.
	GrypeScan bool
	// PackageTables are the tables holding the format's package rows. The
	// exhaustiveness tests check that every query that unions the package
	// tables (component listing, counts, catalog detail, usage, security,
	// quota, tiering, trash, recovery) names them all.
	PackageTables []string
	// DistroScoped formats (deb, rpm) are only meaningful inside a distribution
	// release: scanners need it to match advisories.
	DistroScoped bool
}

// specs is the registry, in display order (the order of the format pickers).
// Treat it as read-only.
//
//nolint:gochecknoglobals // immutable registry
var specs = [...]Spec{
	{
		Name: "docker", PackageTables: []string{"docker_manifests", "docker_tags"},
		Proxy: true, Group: true, Federation: true,
		DefaultUpstream: DockerHubUpstream, PURLType: "docker",
	},
	{
		Name: "npm", PackageTables: []string{"npm_packages", "npm_versions"},
		Proxy: true, Group: true, Federation: true,
		Versioning: VersionSemver, PURLType: "npm", OSVEcosystem: "npm", GrypeScan: true,
	},
	{
		Name: "pypi", PackageTables: []string{"pypi_packages"},
		Proxy: true, Group: true, Federation: true, DefaultUpstream: "https://pypi.org/simple",
		Versioning: VersionPEP440, PURLType: "pypi", OSVEcosystem: "PyPI", GrypeScan: true,
	},
	{
		Name: "maven", PackageTables: []string{"maven_artifacts"},
		Proxy: true, Group: true, Federation: true, Isolated: true,
		Versioning: VersionMaven, PURLType: "maven", OSVEcosystem: "Maven", GrypeScan: true,
	},
	{
		Name: "helm", PackageTables: []string{"helm_charts"},
		Proxy: true, Group: true, Federation: true,
		Versioning: VersionSemver, PURLType: "helm", OSVEcosystem: "Helm",
	},
	{
		Name: "cargo", PackageTables: []string{"cargo_crates"},
		Proxy: true, Group: true, Federation: true, DefaultUpstream: "https://index.crates.io",
		Versioning: VersionSemver, PURLType: "cargo", OSVEcosystem: "crates.io", GrypeScan: true,
	},
	{
		Name: "rubygems", PackageTables: []string{"rubygems_gems"},
		Proxy: true, Group: true, DefaultUpstream: "https://rubygems.org",
		Versioning: VersionGem, PURLType: "gem", OSVEcosystem: "RubyGems", GrypeScan: true,
	},
	{
		Name: "deb", PackageTables: []string{"deb_packages"},
		Proxy: true, Group: true, GeneratedGroupMetadata: true, Signing: SigningGPG,
		PURLType: "deb", OSVEcosystem: "Debian", GrypeScan: true, DistroScoped: true,
	},
	{
		Name: "rpm", PackageTables: []string{"rpm_packages"},
		Proxy: true, Group: true, GeneratedGroupMetadata: true, Signing: SigningGPG,
		PURLType: "rpm", OSVEcosystem: "Red Hat", GrypeScan: true, DistroScoped: true,
	},
	{
		Name: "nuget", PackageTables: []string{"nuget_packages"},
		Proxy: true, Group: true, Federation: true, DefaultUpstream: "https://api.nuget.org/v3/index.json",
		Versioning: VersionSemver, PURLType: "nuget", OSVEcosystem: "NuGet", GrypeScan: true,
	},
	{
		Name: "go", PackageTables: []string{"gomod_modules"},
		Proxy: true, Group: true, Federation: true, DefaultUpstream: "https://proxy.golang.org",
		Versioning: VersionSemver, PURLType: "golang", OSVEcosystem: "Go", GrypeScan: true,
	},
	{
		Name: "raw", PackageTables: []string{"raw_artifacts"},
		Proxy: true, Group: true, Federation: true, Isolated: true,
	},
	{
		Name: "conda", PackageTables: []string{"conda_packages"},
		Proxy: true, Group: true, PURLType: "conda",
	},
	{
		Name: "conan", PackageTables: []string{"conan_packages"},
		Proxy: true, Group: true, DefaultUpstream: "https://center2.conan.io", PURLType: "conan",
	},
	{
		Name: "ivy", PackageTables: []string{"ivy_artifacts"},
		Proxy: true, Group: true, Isolated: true, Versioning: VersionMaven, PURLType: "maven",
	},
	{
		Name: "sbt", PackageTables: []string{"sbt_artifacts"},
		Proxy: true, Group: true, Isolated: true, Versioning: VersionMaven, PURLType: "maven",
	},
	{
		Name: "terraform", PackageTables: []string{"terraform_modules", "terraform_providers"},
		Proxy: true, Group: true, DefaultUpstream: "https://registry.terraform.io",
		Signing: SigningGPG, Versioning: VersionSemver,
	},
	{
		Name: "apk", PackageTables: []string{"apk_packages"},
		Proxy: true, Group: true, GeneratedGroupMetadata: true, Signing: SigningRSA, PURLType: "apk",
	},
	{
		Name: "pacman", PackageTables: []string{"pacman_packages"},
		Proxy: true, Group: true, GeneratedGroupMetadata: true, DefaultUpstream: "https://geo.mirror.pkgbuild.com",
		Signing: SigningGPG, PURLType: "alpm",
	},
	{
		Name: "nix", PackageTables: []string{"nix_paths"}, Signing: SigningEd25519,
	},
}

//nolint:gochecknoglobals // immutable index over specs
var byName = func() map[string]int {
	m := make(map[string]int, len(specs))
	for i := range specs {
		m[specs[i].Name] = i
	}

	return m
}()

// Lookup returns the Spec of the format and whether it is registered. The name
// is matched exactly (formats are lower case).
func Lookup(name string) (Spec, bool) {
	i, ok := byName[name]
	if !ok {
		return Spec{}, false
	}

	return specs[i], true
}

// Valid reports whether name is a registered format: the values the
// chk_repo_format constraint of the repositories table accepts.
func Valid(name string) bool {
	_, ok := byName[name]

	return ok
}

// Names returns the registered format ids in display order.
func Names() []string {
	names := make([]string, len(specs))
	for i := range specs {
		names[i] = specs[i].Name
	}

	return names
}

// SortedNames returns the registered format ids in alphabetical order, the
// order of error messages and API enums.
func SortedNames() []string {
	names := Names()
	slices.Sort(names)

	return names
}

// ProxySupported reports whether proxy repositories of format can be served:
// the formats whose upstream files are mirrored by path, and docker, which is
// pulled through the OCI distribution protocol.
func ProxySupported(format string) bool {
	s, _ := Lookup(format)

	return s.Proxy
}

// GroupSupported reports whether group repositories of format can be served.
// Groups resolve members by path (and merge Maven metadata, npm packuments,
// NuGet registrations, PyPI project pages, Go version lists, Conan revision
// lists and searches and the RubyGems compact index; ivy, sbt and raw groups
// answer from the first member that has the file), for docker by OCI
// manifest/blob, and for deb, rpm, apk and pacman by generating the
// Release/Packages indexes, repodata or databases of the group from all
// members (internal/worker's GroupMetadataWorker) and serving each package
// from the member that owns it.
func GroupSupported(format string) bool {
	s, _ := Lookup(format)

	return s.Group
}

// GeneratedGroupMetadata reports whether the groups of format serve metadata
// the group worker generates from their members (deb, rpm, apk and pacman)
// rather than resolving requests member by member.
func GeneratedGroupMetadata(format string) bool {
	s, _ := Lookup(format)

	return s.GeneratedGroupMetadata
}

// FederationSupported reports whether hosted repositories of format can be
// federated (docs/design/federation.md): the formats whose units the
// federation codecs (internal/federation) replicate.
func FederationSupported(format string) bool {
	s, _ := Lookup(format)

	return s.Federation
}

// DefaultUpstreamURL is the upstream a proxy repository of format uses when
// none is given, "" for formats without a well-known public upstream.
func DefaultUpstreamURL(format string) string {
	s, _ := Lookup(format)

	return s.DefaultUpstream
}

// Isolated reports whether format is served under /repos/<format>/{repo}/...
// (the isolated namespaces) instead of /repos/{repo}/....
func Isolated(format string) bool {
	s, _ := Lookup(format)

	return s.Isolated
}

// ReservedName reports names that can never be repository names: they collide
// with Vue router pages (/repos/new, /repos/settings, /repos/trash) or with the
// isolated /repos/<format>/{name} namespaces, which would make the repository's
// own pages and native URLs unreachable. Every path that creates a repository
// (API, declarative manifests, backup restore) applies it.
func ReservedName(name string) bool {
	switch name = strings.ToLower(name); name {
	case "new", "settings", "trash":
		return true
	}

	return Isolated(name)
}

// GPGSigned reports whether hosted repositories of format are signed with an
// OpenPGP key an admin can assign.
func GPGSigned(format string) bool {
	s, _ := Lookup(format)

	return s.Signing == SigningGPG
}

// GPGFormats lists the formats signed with an OpenPGP key for error messages.
func GPGFormats() string { return list(func(s Spec) bool { return s.Signing == SigningGPG }) }

// ProxyFormats lists the proxy-capable formats for error messages.
func ProxyFormats() string { return list(func(s Spec) bool { return s.Proxy }) }

// GroupFormats lists the group-capable formats for error messages.
func GroupFormats() string { return list(func(s Spec) bool { return s.Group }) }

// FederationFormats lists the federation-capable formats for error messages.
func FederationFormats() string { return list(func(s Spec) bool { return s.Federation }) }

// list joins the names of the formats selected by keep, sorted.
func list(keep func(Spec) bool) string {
	var names []string

	for i := range specs {
		if keep(specs[i]) {
			names = append(names, specs[i].Name)
		}
	}

	slices.Sort(names)

	return strings.Join(names, ", ")
}
