// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"sync"

	"github.com/openartifact/openartifact/internal/format/storage"
)

// registerFixtureDescriptors registers, once per test binary, the storage
// descriptors the tests promote by: the formats that exercise every Promotion
// knob (tags, whole versions, shared versions, collisions, side tables,
// regeneration). The plugin layer registers the real ones.
func registerFixtureDescriptors() {
	registerOnce.Do(func() {
		storage.Register("maven", storage.Storage{
			SideTables: []storage.SideTable{{Name: "maven_files"}},
			Promotion:  storage.Promotion{SharedVersion: true},
		})
		storage.Register("pypi", storage.Storage{
			Promotion: storage.Promotion{SharedVersion: true, Licenses: "ARRAY[sv.metadata->>'license']"},
		})
		storage.Register("rpm", storage.Storage{
			Promotion: storage.Promotion{
				Regen:   storage.RegenRPM,
				Collide: "d.filename = s.filename",
			},
		})
	})
}

//nolint:gochecknoglobals // one registration per test binary
var registerOnce sync.Once
