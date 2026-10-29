// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package plugin is the one place a package format is plugged into the server.
// A format is a Format value: its name, the constructor of its hosted
// protocol handler, the analysis hooks (dependency parsing, licences,
// vulnerability scanning, SBOM), its client setup snippets, its proxy and group
// behaviour and its metadata lifecycle. Register records the value in every
// library registry (internal/proxy, internal/group, internal/pkgdeps, ...) and
// in this package's own, which the server iterates to mount routes and wire the
// workers. internal/formats holds one registration per format.
//
// The static, data-only policy of a format (which repositories it supports,
// where it is mounted, how it is signed, its package URL type) stays in
// internal/repotype.Spec; the plugin holds the behaviour. A test in
// internal/formats checks that the two agree.
//
// Libraries never import this package: it imports them and pushes hooks into
// their registries, so there is no cycle and no init-time registration. Nothing
// is registered until internal/formats.Register runs.
package plugin

import (
	"fmt"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/registry"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// Format is one package format. Every method is required: a format states what
// it does not have with an explicit zero value (NoProxy, NoGroup, NoAnalysis,
// lifecycle.None), never with nil, so a missing behaviour is a decision visible
// in its registration and not an omission.
type Format interface {
	// Name is the format's name: the repotype.Spec name, the repository
	// format column and the key of every registry.
	Name() string

	// Hosted builds the protocol handler of the format's hosted repositories
	// from the server's services.
	Hosted(*Deps) (Hosted, error)

	// Analysis is what the format contributes to the supply-chain analysis.
	Analysis() Analysis

	// ClientSetup is the format's snippet builder (the "Use this repository"
	// commands of the UI).
	ClientSetup() clientsetup.Builder

	// Proxy is the format's proxy-repository behaviour; NoProxy when the
	// format has no proxy repositories.
	Proxy() Proxy

	// Group is the format's group-repository behaviour; NoGroup when the
	// format has no group repositories.
	Group() Group

	// Lifecycle is what happens to the format's generated metadata when rows
	// change outside the handler.
	Lifecycle() lifecycle.Hooks

	// Storage() storage.Storage will be added here when the generic package
	// engines (trash, cleanup, federation, promotion, backup, recovery) read
	// the format's rows through internal/format/storage. Register will then
	// record it in that registry as it does the hooks above.
}

// Analysis lists the supply-chain hooks of a format; a nil field is a stage the
// format has no support for (its packages are libraries with no parsed
// dependencies, no licence lookup, and so on).
type Analysis struct {
	// Manifest says which files of a package archive hold its manifest.
	Manifest *manifest.Rule
	// Deps parses a package's dependencies from those files.
	Deps *pkgdeps.Hooks
	// License looks a package's licence up in its manifest.
	License *licensemeta.Hooks
	// Vuln is the Package URL and the lockfile the vulnerability scanner uses.
	Vuln *vuln.Hooks
	// SBOM describes how the package is named in a generated SBOM.
	SBOM *sbom.Hooks
}

// NoAnalysis is the analysis of a format that has none.
var NoAnalysis = Analysis{} //nolint:gochecknoglobals // immutable zero value, named so a registration states the choice

// Proxy is a format's proxy-repository behaviour.
type Proxy struct {
	// Enabled is true when the format has proxy repositories; it must equal
	// repotype.Spec.Proxy.
	Enabled bool
	// Hooks are the proxy pipeline hooks of the format.
	Hooks proxy.Hooks
}

// NoProxy is the proxy behaviour of a format that has no proxy repositories.
var NoProxy = Proxy{} //nolint:gochecknoglobals // immutable zero value, named so a registration states the choice

// ProxyOf is the proxy behaviour with the given hooks.
func ProxyOf(h proxy.Hooks) Proxy { return Proxy{Enabled: true, Hooks: h} }

// Group is a format's group-repository behaviour.
type Group struct {
	// Enabled is true when the format has group repositories; it must equal
	// repotype.Spec.Group.
	Enabled bool
	// Hooks are the format's merge hooks; nil is a plain first-hit group.
	Hooks *group.Hooks
}

// NoGroup is the group behaviour of a format that has no group repositories.
var NoGroup = Group{} //nolint:gochecknoglobals // immutable zero value, named so a registration states the choice

// FirstHitGroup is a group of the format that answers with the first member
// that has the file and merges nothing (docker, raw).
var FirstHitGroup = Group{Enabled: true} //nolint:gochecknoglobals // immutable value, named so a registration states the choice

// GroupOf is the group behaviour with the given merge hooks.
func GroupOf(h *group.Hooks) Group { return Group{Enabled: true, Hooks: h} }

var formats registry.Registry[Format] //nolint:gochecknoglobals // the format registry, filled by Register at startup

// Register records f in every registry. It panics when f is invalid or its name
// is already registered: both are programming errors that must fail at startup.
func Register(f Format) {
	name := f.Name()
	if name == "" {
		panic("plugin: format with no name")
	}

	if _, ok := repotype.Lookup(name); !ok {
		panic(fmt.Sprintf("plugin: format %q has no repotype.Spec", name))
	}

	if f.ClientSetup() == nil {
		panic(fmt.Sprintf("plugin: format %q has no client setup builder", name))
	}

	formats.Register(name, f)
	registerLibraries(name, f)
}

// registerLibraries pushes f's hooks into the library registries.
func registerLibraries(name string, f Format) {
	if p := f.Proxy(); p.Enabled {
		// Register also records the presign classifier of the hooks.
		proxy.Register(name, p.Hooks)
	}

	if g := f.Group(); g.Hooks != nil {
		group.Register(name, *g.Hooks)
	}

	a := f.Analysis()
	if a.Manifest != nil {
		manifest.Register(name, *a.Manifest)
	}

	if a.Deps != nil {
		pkgdeps.Register(name, *a.Deps)
	}

	if a.License != nil {
		licensemeta.Register(name, *a.License)
	}

	if a.Vuln != nil {
		vuln.Register(name, *a.Vuln)
	}

	if a.SBOM != nil {
		sbom.Register(name, *a.SBOM)
	}

	clientsetup.Register(name, f.ClientSetup())
	lifecycle.Register(name, f.Lifecycle())
}

// All returns the registered formats, sorted by name.
func All() []Format {
	names := formats.Names()
	out := make([]Format, 0, len(names))

	for _, name := range names {
		if f, ok := formats.Lookup(name); ok {
			out = append(out, f)
		}
	}

	return out
}

// Lookup returns the format registered under name.
func Lookup(name string) (Format, bool) { return formats.Lookup(name) }
