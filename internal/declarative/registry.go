// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"fmt"
	"sort"
	"strings"
)

// Registry is the set of kinds the engine manages, keyed by manifest kind.
type Registry struct {
	kinds map[string]Kind
}

// NewRegistry returns a registry of the given kinds.
func NewRegistry(kinds ...Kind) *Registry {
	r := &Registry{kinds: make(map[string]Kind, len(kinds))}
	for _, k := range kinds {
		r.kinds[k.Name()] = k
	}

	return r
}

// Get returns the kind named name (exact, case-sensitive manifest spelling).
func (r *Registry) Get(name string) (Kind, bool) {
	k, ok := r.kinds[name]

	return k, ok
}

// Lookup is Get with a case-insensitive name, for command-line flags.
func (r *Registry) Lookup(name string) (Kind, bool) {
	for n, k := range r.kinds {
		if strings.EqualFold(n, name) {
			return k, true
		}
	}

	return nil, false
}

// Names returns the kind names in apply order.
func (r *Registry) Names() []string {
	ks := r.Ordered()
	out := make([]string, len(ks))

	for i, k := range ks {
		out[i] = k.Name()
	}

	return out
}

// Ordered returns the kinds in apply order (ascending rank, then name).
func (r *Registry) Ordered() []Kind {
	out := make([]Kind, 0, len(r.kinds))
	for _, k := range r.kinds {
		out = append(out, k)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank() != out[j].Rank() {
			return out[i].Rank() < out[j].Rank()
		}

		return out[i].Name() < out[j].Name()
	})

	return out
}

// Ident formats "Kind/key" for messages.
func Ident(kind, key string) string { return fmt.Sprintf("%s/%s", kind, key) }

// prunable returns the Prunable implementation of the named kind, if any.
func (r *Registry) prunable(name string) (Prunable, bool) {
	k, ok := r.Get(name)
	if !ok {
		return nil, false
	}

	p, ok := k.(Prunable)

	return p, ok
}
