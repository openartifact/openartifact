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
	"regexp"
	"sort"
	"strings"
)

const (
	permTargetRank = 50

	permTargetsPath = "/api/v1/permission-targets"

	// permTargetMaxName is the longest permission target name the API accepts.
	permTargetMaxName = 128

	principalUser  = "user"
	principalGroup = "group"
)

var (
	errPermTargetName    = errors.New("metadata.name: must be 1-128 characters without leading or trailing whitespace")
	errPermTargetActions = errors.New("actions: must contain at least one action")

	errPermTargetEmptyPattern = errors.New("must not be empty (use '*' to match everything)")
)

// permTargetSeeded names the targets the migrations create.
var permTargetSeeded = map[string]bool{"admin-all": true, "anonymous-read": true} //nolint:gochecknoglobals // immutable lookup table

// permTargetKind manages permission targets: a repository filter and a path
// pattern (regular expressions matching the whole value, "*" matches
// everything), the actions they grant and the users and groups they are
// assigned to. assignments is authoritative when present in a manifest.
type permTargetKind struct{}

// NewPermissionTargetKind returns the PermissionTarget kind.
func NewPermissionTargetKind() Kind { return permTargetKind{} }

func (permTargetKind) Name() string { return KindPermissionTarget }

func (permTargetKind) Rank() int { return permTargetRank }

func (permTargetKind) MetaKeys() []string { return nil }

func (permTargetKind) Key(o *Object) string { return o.Name }

func (permTargetKind) Order(*Object) int { return 0 }

func (permTargetKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"repository_filter": {Type: TString, Required: true},
		"path_pattern":      {Type: TString, Required: true},
		"actions": {Type: TSet, Required: true, Elem: &Field{
			Type: TString, Enum: []string{"read", "write", "delete", "manage", "annotate"},
		}},
		"assignments": {Type: TObject, Fields: map[string]*Field{
			"users":  {Type: TSet, Elem: &Field{Type: TString}},
			"groups": {Type: TSet, Elem: &Field{Type: TString}},
		}},
	}}
}

// Validate implements Validator. The server compiles the patterns exactly the
// way the evaluator matches them; this does the same offline.
func (permTargetKind) Validate(o *Object) []error {
	var errs []error

	if o.Name != strings.TrimSpace(o.Name) || o.Name == "" || len(o.Name) > permTargetMaxName {
		errs = append(errs, errPermTargetName)
	}

	for _, field := range []string{"repository_filter", "path_pattern"} {
		if err := permTargetCheckPattern(field, Str(o.Spec, field)); err != nil {
			errs = append(errs, err)
		}
	}

	if actions, ok := o.Spec["actions"].([]any); ok && len(actions) == 0 {
		errs = append(errs, errPermTargetActions)
	}

	return errs
}

func permTargetCheckPattern(field, pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("%s: %w", field, errPermTargetEmptyPattern)
	}

	if pattern == "*" {
		pattern = ".*"
	}

	if _, err := regexp.Compile(`^(?:` + pattern + `)$`); err != nil {
		return fmt.Errorf("%s: invalid regular expression: %w", field, err)
	}

	return nil
}

func (permTargetKind) Refs(o *Object) []Ref {
	var refs []Ref

	users, _ := permTargetAssigned(o.Spec, "users")
	for i, u := range users {
		refs = append(refs, Ref{Kind: KindUser, Key: u, Field: fmt.Sprintf("assignments.users[%d]", i)})
	}

	groups, _ := permTargetAssigned(o.Spec, "groups")
	for i, g := range groups {
		refs = append(refs, Ref{Kind: KindGroup, Key: g, Field: fmt.Sprintf("assignments.groups[%d]", i)})
	}

	return refs
}

// permTargetAssigned returns spec.assignments.<key> and whether it is present.
func permTargetAssigned(spec map[string]any, key string) ([]string, bool) {
	assignments, _ := spec["assignments"].(map[string]any)
	if _, ok := assignments[key].([]any); !ok {
		return nil, false
	}

	return StrList(assignments, key), true
}

// permTargetResponse is one target of GET /api/v1/permission-targets.
type permTargetResponse struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	RepositoryFilter string   `json:"repository_filter"`
	PathPattern      string   `json:"path_pattern"`
	Actions          []string `json:"actions"`
}

func (permTargetKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	var resp struct {
		Data []permTargetResponse `json:"data"`
	}

	if err := env.Client.Get(ctx, permTargetsPath, &resp); err != nil {
		return nil, fmt.Errorf("list permission targets: %w", err)
	}

	out := make([]*Live, len(resp.Data))

	err := identForEach(ctx, len(resp.Data), func(ctx context.Context, i int) error {
		t := &resp.Data[i]

		users, groups, err := permTargetAssignments(ctx, env, t.ID)
		if err != nil {
			return fmt.Errorf("list assignments of permission target %q: %w", t.Name, err)
		}

		out[i] = &Live{
			Kind: KindPermissionTarget,
			Key:  t.Name,
			ID:   t.ID,
			Spec: map[string]any{
				"repository_filter": t.RepositoryFilter,
				"path_pattern":      t.PathPattern,
				"actions":           Canon(SortedStrings(t.Actions)),
				"assignments":       map[string]any{"users": users, "groups": groups},
			},
			Seeded: permTargetSeeded[t.Name],
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// permTargetAssignments returns the sorted usernames and group names a target
// is assigned to.
func permTargetAssignments(ctx context.Context, env *Env, id string) ([]any, []any, error) {
	var resp struct {
		Data []struct {
			PrincipalType string `json:"principal_type"`
			PrincipalName string `json:"principal_name"`
		} `json:"data"`
	}

	if err := env.Client.Get(ctx, permTargetsPath+"/"+PathEscape(id)+"/assignments", &resp); err != nil {
		return nil, nil, err //nolint:wrapcheck // callers add the target context
	}

	var userNames, groupNames []string

	for _, a := range resp.Data {
		switch a.PrincipalType {
		case principalUser:
			userNames = append(userNames, a.PrincipalName)
		case principalGroup:
			groupNames = append(groupNames, a.PrincipalName)
		}
	}

	return permTargetNames(userNames), permTargetNames(groupNames), nil
}

// permTargetNames returns names sorted as a canonical list (never nil).
func permTargetNames(names []string) []any {
	sorted := SortedStrings(names)
	out := make([]any, len(sorted))

	for i, n := range sorted {
		out[i] = n
	}

	return out
}

func (permTargetKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	body := Pick(o.Spec, "repository_filter", "path_pattern", "actions")
	body["name"] = o.Name

	var created permTargetResponse
	if err := env.Client.Do(ctx, http.MethodPost, permTargetsPath, body, &created); err != nil {
		return "", fmt.Errorf("create permission target %q: %w", o.Name, err)
	}

	empty := &Live{ID: created.ID, Spec: map[string]any{}}
	if err := permTargetReconcile(ctx, env, empty, o); err != nil {
		return created.ID, err
	}

	return created.ID, nil
}

func (permTargetKind) Update(ctx context.Context, env *Env, live *Live, o *Object, changes []Change) (string, error) {
	var fields []Change

	for _, c := range changes {
		if !ChangedTop([]Change{c})["assignments"] {
			fields = append(fields, c)
		}
	}

	if len(fields) > 0 {
		path := permTargetsPath + "/" + PathEscape(live.ID)
		if err := env.Client.Do(ctx, http.MethodPatch, path, PatchBody(fields), nil); err != nil {
			return "", fmt.Errorf("update permission target %q: %w", o.Name, err)
		}
	}

	if err := permTargetReconcile(ctx, env, live, o); err != nil {
		return live.ID, err
	}

	return live.ID, nil
}

// permTargetReconcile makes the assignments of the target equal to what o asks
// for (users and groups independently, each only when present).
func permTargetReconcile(ctx context.Context, env *Env, live *Live, o *Object) error {
	for _, p := range []struct {
		key, principal string
		resolve        func(context.Context, string) (string, error)
	}{
		{"users", principalUser, env.UserID},
		{"groups", principalGroup, env.GroupID},
	} {
		desired, ok := permTargetAssigned(o.Spec, p.key)
		if !ok {
			continue
		}

		current, _ := permTargetAssigned(live.Spec, p.key)
		add, remove := permTargetDiff(current, desired)
		base := permTargetsPath + "/" + PathEscape(live.ID) + "/assignments"

		for _, name := range add {
			id, err := p.resolve(ctx, name)
			if err != nil {
				return fmt.Errorf("permission target %q assignment: %w", o.Name, err)
			}

			body := map[string]any{"principal_type": p.principal, "principal_id": id}
			if err := env.Client.Do(ctx, http.MethodPost, base, body, nil); err != nil {
				return fmt.Errorf("assign permission target %q to %s %q: %w", o.Name, p.principal, name, err)
			}
		}

		for _, name := range remove {
			id, err := p.resolve(ctx, name)
			if err != nil {
				return fmt.Errorf("permission target %q assignment: %w", o.Name, err)
			}

			q := url.Values{"principal_type": {p.principal}, "principal_id": {id}}
			if err := env.Client.Do(ctx, http.MethodDelete, base+"?"+q.Encode(), nil, nil); err != nil {
				return fmt.Errorf("unassign permission target %q from %s %q: %w", o.Name, p.principal, name, err)
			}
		}
	}

	return nil
}

// permTargetDiff returns the names in desired missing from current and the
// names in current missing from desired, both sorted.
func permTargetDiff(current, desired []string) ([]string, []string) {
	var add, remove []string

	have := make(map[string]bool, len(current))
	for _, n := range current {
		have[n] = true
	}

	want := make(map[string]bool, len(desired))

	for _, n := range desired {
		want[n] = true

		if !have[n] {
			add = append(add, n)
		}
	}

	for _, n := range current {
		if !want[n] {
			remove = append(remove, n)
		}
	}

	sort.Strings(add)
	sort.Strings(remove)

	return add, remove
}

func (permTargetKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.Do(ctx, http.MethodDelete, permTargetsPath+"/"+PathEscape(live.ID), nil, nil); err != nil {
		return fmt.Errorf("delete permission target %q: %w", live.Key, err)
	}

	return nil
}
