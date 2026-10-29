// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strings"

	"github.com/openartifact/openartifact/internal/registry"
)

// Hooks is what SBOM generation needs to know about one format beyond its
// repotype.Spec (which gives the Package URL type). Both fields are optional.
type Hooks struct {
	// PURLName rewrites the package name into the name part of the Package
	// URL (which may carry namespace segments joined with "/"). namespace is
	// the caller's explicit namespace: the distribution of a deb, the vendor
	// of an rpm; most formats ignore it. nil means the name as given.
	PURLName func(namespace, name string) string

	// RootType is the CycloneDX component type of the SBOM root ("container",
	// "file"). "" means "library".
	RootType string
}

// hooks holds the Hooks of every registered format. A format absent from it
// (every other format) keeps its package name as given and is a library.
var hooks registry.Registry[Hooks] //nolint:gochecknoglobals // the format hook table, filled by Register at startup

// Register adds the SBOM hooks of a format. It panics when the format is
// registered twice.
func Register(format string, h Hooks) {
	hooks.Register(format, h)
}

// The per-format hooks. MavenHooks is shared by every format whose Package URL
// type is maven (maven, ivy, sbt).

// MavenHooks: "group:artifact" coordinates become "group/artifact".
var MavenHooks = Hooks{PURLName: mavenPURLName} //nolint:gochecknoglobals // immutable format hook value
// PyPIHooks: names are normalized (lower case, "_" as "-").
var PyPIHooks = Hooks{PURLName: pypiPURLName} //nolint:gochecknoglobals // immutable format hook value
// PacmanHooks: pkg:alpm/arch/NAME, Arch Linux is the only pacman distro.
var PacmanHooks = Hooks{PURLName: pacmanPURLName} //nolint:gochecknoglobals // immutable format hook value
// DebHooks: the distribution is the namespace.
var DebHooks = Hooks{PURLName: namespacedPURLName} //nolint:gochecknoglobals // immutable format hook value
// RPMHooks: the vendor is the namespace.
var RPMHooks = Hooks{PURLName: namespacedPURLName} //nolint:gochecknoglobals // immutable format hook value
// DockerHooks: an image is a container.
var DockerHooks = Hooks{RootType: "container"} //nolint:gochecknoglobals // immutable format hook value
// RawHooks: a raw artifact is a bare file.
var RawHooks = Hooks{RootType: "file"} //nolint:gochecknoglobals // immutable format hook value

func mavenPURLName(_, name string) string {
	if g, a, ok := strings.Cut(name, ":"); ok {
		return g + "/" + a
	}

	return name
}

func pypiPURLName(_, name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

func pacmanPURLName(_, name string) string { return "arch/" + name }

func namespacedPURLName(namespace, name string) string {
	if namespace != "" {
		return namespace + "/" + name
	}

	return name
}

// rootType is the CycloneDX component type of the root of an SBOM of a format:
// every format that is neither an image nor a bare file is a library.
func rootType(format string) string {
	if h, ok := hooks.Lookup(strings.ToLower(format)); ok && h.RootType != "" {
		return h.RootType
	}

	return "library"
}
