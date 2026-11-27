// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import "strings"

// Kind says which registry resource a path names.
type Kind int

// The resources of the registry protocol.
const (
	// KindList is /{scope}/{name}: the releases of a package.
	KindList Kind = iota + 1
	// KindRelease is /{scope}/{name}/{version}: the metadata of a release.
	KindRelease
	// KindManifest is /{scope}/{name}/{version}/Package.swift.
	KindManifest
	// KindArchive is /{scope}/{name}/{version}.zip: the source archive.
	KindArchive
	// KindIdentifiers is /identifiers: the lookup by repository URL.
	KindIdentifiers
	// KindLogin is /login.
	KindLogin
)

const (
	extJSON = ".json"
	extZip  = ".zip"

	// ManifestFile is the name of the package manifest.
	ManifestFile = "Package.swift"
)

// Path is a parsed registry path.
type Path struct {
	Kind    Kind
	Scope   string
	Name    string
	Version string
}

// ParsePath classifies a registry path relative to the repository root, with
// or without a leading slash and with percent-escapes already decoded. The
// ".json" extension of the list and release resources is dropped. A path that
// is not one of the resources, or whose scope, name or version is invalid, is
// not ok.
func ParsePath(rel string) (Path, bool) {
	segs := strings.Split(strings.TrimPrefix(rel, "/"), "/")

	switch {
	case len(segs) == 1 && segs[0] == "identifiers":
		return Path{Kind: KindIdentifiers}, true
	case len(segs) == 1 && segs[0] == "login":
		return Path{Kind: KindLogin}, true
	case len(segs) < 2 || len(segs) > 4:
		return Path{}, false
	}

	p := Path{Scope: segs[0], Name: segs[1]}
	if !ValidScope(p.Scope) {
		return Path{}, false
	}

	if len(segs) == 2 {
		p.Kind = KindList
		p.Name = strings.TrimSuffix(p.Name, extJSON)

		return p, ValidName(p.Name)
	}

	if !ValidName(p.Name) {
		return Path{}, false
	}

	version := segs[2]

	switch {
	case len(segs) == 4:
		p.Kind, p.Version = KindManifest, version

		return p, segs[3] == ManifestFile && ValidVersion(version)
	case strings.HasSuffix(version, extZip):
		p.Kind, p.Version = KindArchive, strings.TrimSuffix(version, extZip)
	case !ValidVersion(version) && strings.HasSuffix(version, extJSON):
		p.Kind, p.Version = KindRelease, strings.TrimSuffix(version, extJSON)
	default:
		p.Kind, p.Version = KindRelease, version
	}

	return p, ValidVersion(p.Version)
}
