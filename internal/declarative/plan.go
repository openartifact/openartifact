// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Plan-time problems; each makes the whole plan unapplicable.
var (
	errAmbiguous  = errors.New("ambiguous")
	errImmutable  = errors.New("immutable field(s)")
	errMissingRef = errors.New("missing reference")
)

// Action is what the plan does to one object.
type Action string

// Plan actions.
const (
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionUnchanged Action = "unchanged"
	ActionDelete    Action = "delete"
)

// Marker is the server-side ownership record of an object created or adopted
// by `oa apply`.
type Marker struct {
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	ObjectID    string    `json:"object_id"`
	AppliedHash string    `json:"applied_hash"`
	AppliedAt   time.Time `json:"applied_at"`
}

// Markers indexes markers by Ident(kind, name).
type Markers map[string]Marker

// Item is one planned operation.
type Item struct {
	Action  Action
	Kind    string
	Key     string
	Desired *Object
	Live    *Live
	Changes []Change
	// Adopt marks an existing object without a valid ownership marker that
	// apply will take over (writing the marker) even when nothing changes.
	Adopt bool
	// Managed is set on delete items: the object carries a valid marker.
	Managed bool
	// Err is a plan-time problem with this item; any Err makes the whole plan
	// unapplicable.
	Err error
}

// Plan is the ordered list of operations: creates/updates/unchanged in
// dependency order, then deletes in reverse dependency order.
type Plan struct {
	Items []*Item
}

// Counts returns the number of items per action.
func (p *Plan) Counts() map[Action]int {
	c := map[Action]int{}
	for _, it := range p.Items {
		c[it.Action]++
	}

	return c
}

// HasChanges reports whether applying the plan would modify the server
// (adoption alone only writes ownership markers and is not a change).
func (p *Plan) HasChanges() bool {
	for _, it := range p.Items {
		if it.Action != ActionUnchanged {
			return true
		}
	}

	return false
}

// Errors returns the plan-time errors.
func (p *Plan) Errors() []error {
	var errs []error

	for _, it := range p.Items {
		if it.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", Ident(it.Kind, it.Key), it.Err))
		}
	}

	return errs
}

// PlanInput is everything BuildPlan needs; it performs no I/O.
type PlanInput struct {
	Registry *Registry
	Desired  []*Object
	// Live holds the objects found on the server per kind. A kind without an
	// entry was not fetched: references to it are not checked and it is never
	// pruned.
	Live    map[string][]*Live
	Markers Markers
	// Prune deletes managed objects absent from Desired; PruneAll also deletes
	// unmanaged ones (never seeded objects, never Users unless the kind filter
	// names User).
	Prune        bool
	PruneAll     bool
	ForceSecrets bool
	// Kinds restricts pruning to these kinds (nil = every fetched kind).
	Kinds map[string]bool
}

// BuildPlan diffs the desired objects against the live state.
func BuildPlan(in PlanInput) *Plan {
	liveBy := indexLive(in.Live)
	desired := append([]*Object(nil), in.Desired...)
	SortObjects(in.Registry, desired)

	plan := &Plan{}
	desiredKeys := map[string]bool{}

	for _, o := range desired {
		k, _ := in.Registry.Get(o.Kind)
		key := k.Key(o)
		desiredKeys[Ident(o.Kind, key)] = true

		plan.Items = append(plan.Items, planObject(in, k, o, key, liveBy[Ident(o.Kind, key)]))
	}

	var deletes []*Item
	if in.Prune || in.PruneAll {
		deletes = planPrune(in, desiredKeys)
	}

	checkRefs(in, plan, desiredKeys, deletes)

	plan.Items = append(plan.Items, deletes...)

	return plan
}

func indexLive(live map[string][]*Live) map[string][]*Live {
	out := map[string][]*Live{}

	for kind, list := range live {
		for _, l := range list {
			id := Ident(kind, l.Key)
			out[id] = append(out[id], l)
		}
	}

	return out
}

func planObject(in PlanInput, k Kind, o *Object, key string, live []*Live) *Item {
	it := &Item{Kind: o.Kind, Key: key, Desired: o}

	switch len(live) {
	case 0:
		it.Action = ActionCreate
		it.Changes = createChanges(o)

		return it
	case 1:
	default:
		it.Action = ActionUnchanged
		it.Err = fmt.Errorf("%w: %d objects on the server share this name; rename or delete the duplicates", errAmbiguous, len(live))

		return it
	}

	l := live[0]
	it.Live = l

	res := diffSpec(k.Schema(), o.Spec, l.Spec, l.Secrets, in.ForceSecrets)
	it.Changes = res.Changes

	if len(res.Immutable) > 0 {
		it.Err = fmt.Errorf("%w %s cannot be changed on an existing object; revert the manifest, or delete the object (oa apply --prune after removing it, or via the API) and apply again to recreate it",
			errImmutable, strings.Join(res.Immutable, ", "))
	}

	it.Action = ActionUnchanged
	if len(res.Changes) > 0 {
		it.Action = ActionUpdate
	}

	m, ok := in.Markers[Ident(o.Kind, key)]
	it.Adopt = !ok || m.ObjectID != l.ID

	return it
}

// createChanges lists every desired field as an addition (for reports).
func createChanges(o *Object) []Change {
	flat := map[string]Change{}
	flatten("", o.Spec, flat)

	paths := sortedKeys(flat)
	out := make([]Change, 0, len(paths))

	for _, p := range paths {
		out = append(out, flat[p])
	}

	return out
}

func flatten(prefix string, v any, out map[string]Change) {
	if m, ok := v.(map[string]any); ok && prefix != "" && len(m) > 0 {
		for k, e := range m {
			flatten(joinPath(prefix, k), e, out)
		}

		return
	}

	if m, ok := v.(map[string]any); ok && prefix == "" {
		for k, e := range m {
			flatten(k, e, out)
		}

		return
	}

	_, secret := v.(SecretRef)
	out[prefix] = Change{Path: prefix, New: v, Secret: secret}
}

// planPrune returns delete items for server objects absent from the manifests.
func planPrune(in PlanInput, desiredKeys map[string]bool) []*Item {
	var items []*Item

	for kind, list := range in.Live {
		if in.Kinds != nil && !in.Kinds[kind] {
			continue
		}

		prunable := func(*Live) bool { return true }
		if p, ok := in.Registry.prunable(kind); ok {
			prunable = p.Prunable
		}

		counts := map[string]int{}
		for _, l := range list {
			counts[l.Key]++
		}

		for _, l := range list {
			if desiredKeys[Ident(kind, l.Key)] || l.Seeded || counts[l.Key] > 1 || !prunable(l) {
				continue
			}

			m, ok := in.Markers[Ident(kind, l.Key)]
			managed := ok && m.ObjectID == l.ID

			if !managed && (!in.PruneAll || !pruneAllCovers(in, kind)) {
				continue
			}

			items = append(items, &Item{Action: ActionDelete, Kind: kind, Key: l.Key, Live: l, Managed: managed})
		}
	}

	sortDeletes(in.Registry, items)

	return items
}

// pruneAllCovers keeps --prune-all away from human users: User objects are
// only deleted without a marker when the kind filter names User explicitly.
func pruneAllCovers(in PlanInput, kind string) bool {
	if kind != KindUser {
		return true
	}

	return in.Kinds != nil && in.Kinds[KindUser]
}

// sortDeletes orders deletions by reverse dependency: highest rank first.
func sortDeletes(reg *Registry, items []*Item) {
	sort.SliceStable(items, func(i, j int) bool {
		ki, _ := reg.Get(items[i].Kind)
		kj, _ := reg.Get(items[j].Kind)

		if ki.Rank() != kj.Rank() {
			return ki.Rank() > kj.Rank()
		}

		// Group repositories (higher Order) before their members.
		if oi, oj := orderOfLive(ki, items[i]), orderOfLive(kj, items[j]); oi != oj {
			return oi > oj
		}

		return items[i].Key < items[j].Key
	})
}

func orderOfLive(k Kind, it *Item) int {
	if it.Live == nil {
		return 0
	}

	return k.Order(&Object{Kind: it.Kind, Name: it.Key, Spec: it.Live.Spec, Meta: map[string]string{}})
}

// checkRefs flags objects referencing something that is neither desired nor on
// the server (after the planned deletions).
func checkRefs(in PlanInput, plan *Plan, desiredKeys map[string]bool, deletes []*Item) {
	deleted := map[string]bool{}
	for _, d := range deletes {
		deleted[Ident(d.Kind, d.Key)] = true
	}

	liveKeys := map[string]bool{}

	for kind, list := range in.Live {
		for _, l := range list {
			liveKeys[Ident(kind, l.Key)] = true
		}
	}

	for _, it := range plan.Items {
		if it.Desired == nil || it.Err != nil {
			continue
		}

		k, _ := in.Registry.Get(it.Kind)

		for _, r := range k.Refs(it.Desired) {
			id := Ident(r.Kind, r.Key)

			if _, fetched := in.Live[r.Kind]; !fetched && !desiredKeys[id] {
				continue
			}

			if desiredKeys[id] || (liveKeys[id] && !deleted[id]) {
				continue
			}

			it.Err = fmt.Errorf("%w: %s references %s %q, which is in neither the manifests nor on the server", errMissingRef, r.Field, r.Kind, r.Key)

			break
		}
	}
}

// SpecHash is the digest recorded in the ownership marker: sha256 of the
// canonical desired spec, secrets replaced by their reference (never values).
func SpecHash(o *Object) string {
	raw, err := json.Marshal(hashable(o.Spec))
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}

func hashable(v any) any {
	switch t := v.(type) {
	case SecretRef:
		return "secretRef:" + t.String()
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = hashable(e)
		}

		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = hashable(e)
		}

		return out
	}

	return v
}
