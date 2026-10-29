// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package registry is the small name-keyed registry the format hook tables are
// built on. A package that needs a per-format decision (the proxy pipeline, the
// group merger, the dependency parsers) owns one Registry of its own hook type;
// internal/format/plugin fills them all from a format's single registration at
// startup. There is no init-time registration: nothing is registered until
// plugin.Register runs (cmd/server and the test helpers call it), so a binary
// or a test serves exactly the formats it registered.
package registry

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
)

// Registry maps a format name to its hook value. The zero value is an empty
// registry ready for use. Lookups are lock-free; Register copies the table, so
// it suits the startup-time registration it is meant for.
type Registry[V any] struct {
	mu sync.Mutex
	m  atomic.Pointer[map[string]V]
}

// Register adds v under name. It panics on an empty or a duplicate name: both
// are programming errors that must fail at startup, not serve a wrong format.
func (r *Registry[V]) Register(name string, v V) {
	if name == "" {
		panic("registry: empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var cur map[string]V
	if p := r.m.Load(); p != nil {
		cur = *p
	}

	if _, dup := cur[name]; dup {
		panic(fmt.Sprintf("registry: %q registered twice", name))
	}

	next := make(map[string]V, len(cur)+1)
	maps.Copy(next, cur)
	next[name] = v
	r.m.Store(&next)
}

// Lookup returns the value registered under name.
func (r *Registry[V]) Lookup(name string) (V, bool) {
	var zero V

	p := r.m.Load()
	if p == nil {
		return zero, false
	}

	v, ok := (*p)[name]
	if !ok {
		return zero, false
	}

	return v, true
}

// Names returns the registered names, sorted.
func (r *Registry[V]) Names() []string {
	p := r.m.Load()
	if p == nil {
		return nil
	}

	return slices.Sorted(maps.Keys(*p))
}
