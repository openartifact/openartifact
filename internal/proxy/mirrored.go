// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"

	"github.com/openartifact/openartifact/internal/presign"
)

// The formats below mirror an upstream by path: no Serve hook, so the generic
// resolve and serveResolved pipeline answers them (proxy.go).

// mavenSidecarExts are the checksum and signature files published next to a
// Maven file; each is immutable exactly when the file it belongs to is.
var mavenSidecarExts = []string{".md5", ".sha1", ".sha256", ".sha512", ".asc"} //nolint:gochecknoglobals // read-only table

// mavenImmutable reports whether key is a released, never-rewritten Maven file.
func mavenImmutable(key string) bool {
	for _, ext := range mavenSidecarExts {
		if owner, ok := strings.CutSuffix(key, ext); ok {
			return mavenImmutable(owner)
		}
	}

	name := keyName(key)

	return name != "" &&
		!strings.HasPrefix(name, "maven-metadata") &&
		name != "archetype-catalog.xml" &&
		!strings.Contains(key, "-SNAPSHOT")
}

// inDir reports whether key lies below a directory named dir, at the root of
// the upstream or deeper (an upstream URL may already include a path prefix).
func inDir(key, dir string) bool {
	return strings.HasPrefix(key, dir+"/") || strings.Contains(key, "/"+dir+"/")
}

// DebHooks is the proxy hooks of the deb format.
var DebHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	// .deb/.udeb below a pool/ directory (the Debian archive layout, where a
	// file name is never reused); the same names in a flat repository, where a
	// publisher may overwrite a package, use the repository TTL.
	Immutable: func(key string) bool {
		name := keyName(key)

		return inDir(key, "pool") && (strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".udeb"))
	},
	IsMetadata:  func(key string) bool { return inDir(key, "dists") },
	PackageExts: []string{".deb", ".udeb"},
	BlobPath:    presign.DebBlobPath,
	Coordinates: debCoordinates,
}

// MavenHooks is the proxy hooks of the maven format.
var MavenHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Immutable:   mavenImmutable,
	IsMetadata:  func(key string) bool { return strings.HasPrefix(keyName(key), "maven-metadata") },
	PackageExts: []string{".jar", ".pom", ".zip", ".war", ".ear", ".aar", ".module", ".gz"},
	BlobPath:    presign.MavenBlobPath,
	Coordinates: mavenDownloadCoordinates,
}

// RawHooks is the proxy hooks of the raw format: every file is a download,
// there are no index files and nothing is immutable.
var RawHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	BlobPath: presign.RawBlobPath,
}

// RPMHooks is the proxy hooks of the rpm format.
var RPMHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	// .rpm below a Packages/ directory (the layout of Fedora, CentOS, RHEL and
	// most vendor repositories, whose NEVRA-named files are not rebuilt in
	// place); flat or other layouts use the repository TTL. No cheap integrity
	// signal (the repodata checksums are not checked by this proxy) exists to
	// pin the rest.
	Immutable:   func(key string) bool { return inDir(key, "Packages") && strings.HasSuffix(keyName(key), ".rpm") },
	IsMetadata:  func(key string) bool { return inDir(key, "repodata") },
	PackageExts: []string{".rpm"},
	BlobPath:    presign.RPMBlobPath,
	Coordinates: rpmCoordinates,
}
