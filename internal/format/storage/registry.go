// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"sync"
	"sync/atomic"

	"github.com/openartifact/openartifact/internal/registry"
)

var formats registry.Registry[Storage] //nolint:gochecknoglobals // the format registry, filled at startup by Register

// registerMu serializes Register with the rebuild of formatsByKind, so the
// index published last always covers every descriptor registered so far.
var registerMu sync.Mutex //nolint:gochecknoglobals // guards the registry and its derived trash-kind index

// formatsByKind is the trash-kind view of the registry: kind to the registered
// formats whose trash items carry it, in registry (sorted) order. Register
// rebuilds it, so the audit and trash paths read it without scanning the
// descriptors. Nil until the first Register.
var formatsByKind atomic.Pointer[map[string][]string] //nolint:gochecknoglobals // derived from the format registry, rebuilt by Register

// Register installs the storage descriptor of format. It panics when format is
// empty or already registered.
func Register(format string, s Storage) {
	registerMu.Lock()
	defer registerMu.Unlock()

	formats.Register(format, s)

	byKind := make(map[string][]string)

	for _, name := range formats.Names() {
		if d, ok := formats.LookupRef(name); ok && d.Trash.Kind != "" {
			byKind[d.Trash.Kind] = append(byKind[d.Trash.Kind], name)
		}
	}

	formatsByKind.Store(&byKind)
}

// Lookup returns a copy of the descriptor registered for format.
func Lookup(format string) (Storage, bool) { return formats.Lookup(format) }

// Names returns the registered format names, sorted. The slice is shared and
// must not be modified.
func Names() []string { return formats.Names() }
