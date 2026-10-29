// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"sync"

	formatstorage "github.com/openartifact/openartifact/internal/format/storage"
)

// fixtureFederated are the formats the production plugin layer registers a
// Federation descriptor for; the fixtures below mirror its values.
var fixtureFederated = []string{"cargo", "go", "helm", "maven", "npm", "nuget", "pypi", "raw"} //nolint:gochecknoglobals // test fixture

var fixtureMu sync.Mutex //nolint:gochecknoglobals // guards the shared descriptor registry in tests

// registerFixtureFormats registers the federation descriptors of the
// federated formats, once, for the parallel tests of the package.
func registerFixtureFormats() {
	fixtureMu.Lock()
	defer fixtureMu.Unlock()

	fixtures := map[string]formatstorage.Storage{
		"cargo": {Federation: &formatstorage.Federation{}},
		"helm":  {Federation: &formatstorage.Federation{}},
		"nuget": {Federation: &formatstorage.Federation{}},
		"go":    {Federation: &formatstorage.Federation{}},
		"raw":   {Federation: &formatstorage.Federation{Unit: formatstorage.UnitFile}},
		"pypi":  {Federation: &formatstorage.Federation{Unit: formatstorage.UnitFile}},
		"maven": {
			Federation: &formatstorage.Federation{Unit: formatstorage.UnitFile},
			SideTables: []formatstorage.SideTable{{Name: "maven_files"}},
		},
		"npm": {Federation: &formatstorage.Federation{
			Unit: formatstorage.UnitVersion, Tags: true, TouchPackage: true, StaleAttr: "packument_stale",
			LocalPackageAttrs: []string{"latest_version", "packument_stale"},
		}},
	}

	for name, st := range fixtures {
		if _, ok := formatstorage.Lookup(name); !ok {
			formatstorage.Register(name, st)
		}
	}
}
