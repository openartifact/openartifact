// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import "github.com/openartifact/openartifact/internal/registry"

var formats registry.Registry[Storage] //nolint:gochecknoglobals // the format registry, filled at startup by Register

// Register installs the storage descriptor of format. It panics when format is
// empty or already registered.
func Register(format string, s Storage) { formats.Register(format, s) }

// Lookup returns the descriptor registered for format.
func Lookup(format string) (Storage, bool) { return formats.Lookup(format) }

// Names returns the registered format names, sorted.
func Names() []string { return formats.Names() }

// Reset empties the registry; tests that register descriptors of their own use it.
func Reset() { formats.Reset() }
