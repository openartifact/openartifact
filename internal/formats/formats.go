// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package formats holds the plugin registration of every package format: one
// file per format, each a plugin.Format value assembling the hosted handler
// constructor, the analysis, client setup, proxy and group hooks and the
// metadata lifecycle of that format. Register records them all.
//
// Adding a format means adding its Spec to internal/repotype, a file here and a
// line to registrations; TestRegistrationsMatchSpecs fails until all three
// agree.
//
// This package must not import internal/api, internal/worker or
// internal/middleware: their tests call Register from TestMain. What a format
// needs from them (the REST upload handlers, the upload counter) comes in
// through plugin.Deps.
package formats

import (
	"sync"

	"github.com/openartifact/openartifact/internal/format/plugin"
)

// registrations lists the format registrations; a new format adds its file's
// constructor here.
func registrations() []plugin.Format {
	return []plugin.Format{
		ansibleFormat(), apkFormat(), cargoFormat(), composerFormat(), conanFormat(), condaFormat(),
		debFormat(), dockerFormat(), goFormat(), helmFormat(), ivyFormat(), mavenFormat(), nixFormat(),
		npmFormat(), nugetFormat(), pacmanFormat(), pypiFormat(), rawFormat(), rpmFormat(),
		rubygemsFormat(), sbtFormat(), terraformFormat(),
	}
}

var registerOnce sync.Once //nolint:gochecknoglobals // makes Register idempotent

// Register records every format in the plugin registry and, through it, in the
// library registries. It is idempotent: the server, genformats and the test
// binaries that need the hooks all call it.
func Register() {
	registerOnce.Do(func() {
		for _, f := range registrations() {
			plugin.Register(f)
		}
	})
}
