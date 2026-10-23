// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// markersPath is the ownership-marker endpoint.
const markersPath = "/api/v1/admin/declarative"

// Options configure a plan or apply run.
type Options struct {
	Registry *Registry
	Client   *Client
	Load     LoadOptions
	// Kinds restricts manifests and pruning to these kinds (case-insensitive
	// names); empty means every kind.
	Kinds           []string
	Prune           bool
	PruneAll        bool
	ForceSecrets    bool
	ContinueOnError bool
	Resolver        SecretResolver
}

// Prepared is a loaded, validated and planned run.
type Prepared struct {
	Plan    *Plan
	Markers Markers
	Env     *Env
}

// ErrUnknownKind is wrapped when --kind names a kind that does not exist.
var ErrUnknownKind = errors.New("unknown kind")

// ErrServerTooOld is wrapped when the server lacks the declarative API.
var ErrServerTooOld = errors.New("server does not support declarative configuration")

// ErrNotAdmin is returned when the token lacks the manage permission.
var ErrNotAdmin = errors.New("oa apply needs an administrator token (manage permission)")

// KindFilter resolves --kind names to a set of canonical kind names. A nil set
// means no restriction.
func KindFilter(reg *Registry, names []string) (map[string]bool, error) {
	if len(names) == 0 {
		return nil, nil //nolint:nilnil // nil set = unrestricted
	}

	out := map[string]bool{}

	for _, n := range names {
		k, ok := reg.Lookup(n)
		if !ok {
			return nil, fmt.Errorf("%w %q (one of %s)", ErrUnknownKind, n, strings.Join(reg.Names(), ", "))
		}

		out[k.Name()] = true
	}

	return out, nil
}

// Preflight verifies the token can manage the server and returns the
// ownership markers.
func Preflight(ctx context.Context, c *Client) (Markers, error) {
	var resp struct {
		Items []Marker `json:"items"`
	}

	if err := c.Get(ctx, markersPath, &resp); err != nil {
		switch {
		case IsStatus(err, http.StatusUnauthorized):
			return nil, errors.New("not authenticated: the server rejected the token (set OA_TOKEN / --token or run `oa auth login`)") //nolint:err113 // user-facing message
		case IsStatus(err, http.StatusForbidden):
			return nil, fmt.Errorf("%w: the server answered 403 for %s", ErrNotAdmin, markersPath)
		case IsStatus(err, http.StatusNotFound):
			return nil, fmt.Errorf("%w: no %s endpoint; upgrade the server to a version that supports declarative configuration", ErrServerTooOld, markersPath)
		}

		return nil, fmt.Errorf("read ownership markers: %w", err)
	}

	m := make(Markers, len(resp.Items))
	for _, it := range resp.Items {
		m[Ident(it.Kind, it.Name)] = it
	}

	return m, nil
}

// Prepare loads the manifests, reads the live state and builds the plan.
func Prepare(ctx context.Context, opts Options) (*Prepared, error) {
	filter, err := KindFilter(opts.Registry, opts.Kinds)
	if err != nil {
		return nil, err
	}

	objs, err := Load(opts.Registry, opts.Load)
	if err != nil {
		return nil, err
	}

	if filter != nil {
		objs = filterObjects(objs, filter)
	}

	markers, err := Preflight(ctx, opts.Client)
	if err != nil {
		return nil, err
	}

	env := NewEnv(opts.Client)

	live, err := fetchLive(ctx, env, opts, objs, filter)
	if err != nil {
		return nil, err
	}

	plan := BuildPlan(PlanInput{
		Registry: opts.Registry, Desired: objs, Live: live, Markers: markers,
		Prune: opts.Prune || opts.PruneAll, PruneAll: opts.PruneAll, ForceSecrets: opts.ForceSecrets, Kinds: filter,
	})

	return &Prepared{Plan: plan, Markers: markers, Env: env}, nil
}

func filterObjects(objs []*Object, filter map[string]bool) []*Object {
	out := objs[:0:0]

	for _, o := range objs {
		if filter[o.Kind] {
			out = append(out, o)
		}
	}

	return out
}

// fetchLive lists the kinds that are needed: those with desired objects, those
// referenced by them, and every (filtered) kind when pruning.
func fetchLive(ctx context.Context, env *Env, opts Options, objs []*Object, filter map[string]bool) (map[string][]*Live, error) {
	need := map[string]bool{}

	for _, o := range objs {
		need[o.Kind] = true

		k, _ := opts.Registry.Get(o.Kind)
		for _, r := range k.Refs(o) {
			need[r.Kind] = true
		}
	}

	if opts.Prune || opts.PruneAll {
		for _, k := range opts.Registry.Ordered() {
			if filter == nil || filter[k.Name()] {
				need[k.Name()] = true
			}
		}
	}

	live := map[string][]*Live{}

	for _, k := range opts.Registry.Ordered() {
		if !need[k.Name()] {
			continue
		}

		list, err := k.List(ctx, env)
		if err != nil {
			return nil, fmt.Errorf("read %s state from the server: %w", k.Name(), err)
		}

		for _, l := range list {
			l.Kind = k.Name()
		}

		live[k.Name()] = list
	}

	return live, nil
}

// CheckSecrets resolves every secret the plan will send, before anything is
// changed, so a missing environment variable or file aborts the run early.
func CheckSecrets(plan *Plan, r SecretResolver) []error {
	var errs []error

	for _, it := range plan.Items {
		switch it.Action {
		case ActionCreate:
			if _, err := r.resolveObject(it.Desired); err != nil {
				errs = append(errs, err)
			}
		case ActionUpdate:
			if _, err := r.resolveChanges(Ident(it.Kind, it.Key), it.Changes); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errs
}

// Status is the outcome of one executed item.
type Status string

// Item outcomes.
const (
	StatusApplied Status = "applied"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Outcome is the result of executing one plan item.
type Outcome struct {
	Item   *Item
	Status Status
	Err    error
}

// Execute applies the plan. It stops at the first failure unless
// continueOnError is set; items not reached are reported as skipped.
func Execute(ctx context.Context, opts Options, p *Prepared) []Outcome {
	plan := p.Plan
	out := make([]Outcome, 0, len(plan.Items))
	failed := false

	var adopts []Marker

	for _, it := range plan.Items {
		if failed && !opts.ContinueOnError {
			out = append(out, Outcome{Item: it, Status: StatusSkipped})
			continue
		}

		if it.Action == ActionUnchanged {
			if it.Adopt && it.Live != nil {
				adopts = append(adopts, markerFor(it, it.Live.ID))
			}

			out = append(out, Outcome{Item: it, Status: StatusApplied})

			continue
		}

		if err := executeItem(ctx, opts, p, it); err != nil {
			failed = true

			out = append(out, Outcome{Item: it, Status: StatusFailed, Err: err})

			continue
		}

		out = append(out, Outcome{Item: it, Status: StatusApplied})
	}

	if len(adopts) > 0 {
		if err := putMarkers(ctx, opts.Client, adopts); err != nil {
			out = append(out, Outcome{
				Item:   &Item{Action: ActionUnchanged, Kind: "OwnershipMarkers", Key: "adopt"},
				Status: StatusFailed, Err: err,
			})
		}
	}

	return out
}

func markerFor(it *Item, id string) Marker {
	m := Marker{Kind: it.Kind, Name: it.Key, ObjectID: id}
	if it.Desired != nil {
		m.AppliedHash = SpecHash(it.Desired)
	}

	return m
}

func executeItem(ctx context.Context, opts Options, p *Prepared, it *Item) error {
	k, _ := opts.Registry.Get(it.Kind)
	ident := Ident(it.Kind, it.Key)

	switch it.Action {
	case ActionCreate:
		obj, err := opts.Resolver.resolveObject(it.Desired)
		if err != nil {
			return err
		}

		id, err := k.Create(ctx, p.Env, obj)
		if err != nil {
			return fmt.Errorf("create %s: %w", ident, err)
		}

		if err := putMarkers(ctx, opts.Client, []Marker{markerFor(it, id)}); err != nil {
			return fmt.Errorf("created %s but could not record ownership: %w", ident, err)
		}
	case ActionUpdate:
		changes, err := opts.Resolver.resolveChanges(ident, it.Changes)
		if err != nil {
			return err
		}

		id, err := k.Update(ctx, p.Env, it.Live, it.Desired, changes)
		if err != nil {
			return fmt.Errorf("update %s: %w", ident, err)
		}

		if id == "" {
			id = it.Live.ID
		}

		if err := putMarkers(ctx, opts.Client, []Marker{markerFor(it, id)}); err != nil {
			return fmt.Errorf("updated %s but could not record ownership: %w", ident, err)
		}
	case ActionDelete:
		if err := k.Delete(ctx, p.Env, it.Live); err != nil {
			return fmt.Errorf("delete %s: %w", ident, err)
		}

		if it.Managed {
			if err := deleteMarker(ctx, opts.Client, it.Kind, it.Key); err != nil {
				return fmt.Errorf("deleted %s but could not drop its ownership marker: %w", ident, err)
			}
		}
	case ActionUnchanged:
	}

	return nil
}

func putMarkers(ctx context.Context, c *Client, ms []Marker) error {
	items := make([]map[string]string, len(ms))

	for i, m := range ms {
		items[i] = map[string]string{
			"kind": m.Kind, "name": m.Name, "object_id": m.ObjectID, "applied_hash": m.AppliedHash,
		}
	}

	return c.Do(ctx, http.MethodPut, markersPath, map[string]any{"items": items}, nil)
}

func deleteMarker(ctx context.Context, c *Client, kind, name string) error {
	q := url.Values{"kind": {kind}, "name": {name}}

	err := c.Do(ctx, http.MethodDelete, markersPath+"?"+q.Encode(), nil, nil)
	if IsStatus(err, http.StatusNotFound) {
		return nil
	}

	return err
}

// Failed returns the failed outcomes.
func Failed(outcomes []Outcome) []Outcome {
	var f []Outcome

	for _, o := range outcomes {
		if o.Status == StatusFailed {
			f = append(f, o)
		}
	}

	return f
}

// SortedChangePaths is a small helper for stable report output.
func SortedChangePaths(changes []Change) []Change {
	out := append([]Change(nil), changes...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	return out
}
