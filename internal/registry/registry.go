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

// table is one immutable generation of the registry: Register builds a new one
// and publishes it, so a reader never sees a half-built table and the derived
// name list is computed once per registration, not once per call.
type table[V any] struct {
	m     map[string]*V
	names []string // sorted, capacity clipped so an append never writes into it
}

// Registry maps a format name to its hook value. The zero value is an empty
// registry ready for use. Lookups are lock-free; Register copies the table, so
// it suits the startup-time registration it is meant for.
type Registry[V any] struct {
	mu sync.Mutex
	t  atomic.Pointer[table[V]]
}

// Register adds v under name. It panics on an empty or a duplicate name: both
// are programming errors that must fail at startup, not serve a wrong format.
func (r *Registry[V]) Register(name string, v V) {
	if name == "" {
		panic("registry: empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	cur := r.t.Load()
	if cur == nil {
		cur = &table[V]{}
	}

	if _, dup := cur.m[name]; dup {
		panic(fmt.Sprintf("registry: %q registered twice", name))
	}

	next := &table[V]{m: make(map[string]*V, len(cur.m)+1)}
	maps.Copy(next.m, cur.m)
	next.m[name] = &v

	at, _ := slices.BinarySearch(cur.names, name)
	next.names = slices.Clip(slices.Insert(slices.Clone(cur.names), at, name))

	r.t.Store(next)
}

// Lookup returns a copy of the value registered under name.
func (r *Registry[V]) Lookup(name string) (V, bool) {
	var zero V

	v, ok := r.LookupRef(name)
	if !ok {
		return zero, false
	}

	return *v, true
}

// LookupRef returns the value registered under name without copying it: the
// pointer is to the registered value itself, shared by every caller, so it is
// read-only. Use it for a large V that is only inspected (Lookup copies it).
func (r *Registry[V]) LookupRef(name string) (*V, bool) {
	t := r.t.Load()
	if t == nil {
		return nil, false
	}

	v, ok := t.m[name]

	return v, ok
}

// Names returns the registered names, sorted. The slice is the registry's own,
// rebuilt by Register: it is shared by every caller and must be treated as
// read-only (slices.Clone it to modify).
func (r *Registry[V]) Names() []string {
	t := r.t.Load()
	if t == nil {
		return nil
	}

	return t.names
}
