// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
)

const (
	// projectRank orders projects after the users and groups they reference
	// and before the repositories they contain.
	projectRank = 20

	projectsPath = "/api/v1/projects"

	projectRoleOwner      = "owner"
	projectRoleMaintainer = "maintainer"
	projectRoleDeveloper  = "developer"
	projectRoleViewer     = "viewer"
)

var (
	errProjectFormatsEmpty = errors.New("allowed_formats: an empty list is ambiguous; use null to allow every format")
	errProjectTypesEmpty   = errors.New("allowed_repo_types: must not be empty")
	errProjectNoOwner      = errors.New("members: at least one member must have the role owner")
)

// projectKind manages projects: policy fields, direct members and attached
// groups. Projects are addressed on the API by name. members and groups are
// authoritative when present in a manifest.
//
// Server-side defaults of a new project (allowed_repo_types hosted,
// repo_name_prefix "<slug>-", max_repositories 50, token_max_ttl_days 365)
// apply to every field a manifest leaves out. The project's original owner
// cannot be removed or demoted by the server.
type projectKind struct{}

// NewProjectKind returns the Project kind.
func NewProjectKind() Kind { return projectKind{} }

func (projectKind) Name() string { return KindProject }

func (projectKind) Rank() int { return projectRank }

func (projectKind) MetaKeys() []string { return nil }

func (projectKind) Key(o *Object) string { return o.Name }

func (projectKind) Order(*Object) int { return 0 }

func (projectKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"display_name": {Type: TString},
		"description":  {Type: TString},
		// null allows every format; the server stores an empty list as null.
		"allowed_formats": {Type: TSet, Nullable: true, Elem: &Field{Type: TString}},
		"allowed_repo_types": {Type: TSet, Elem: &Field{
			Type: TString, Enum: []string{"hosted", "proxy", "group"},
		}},
		"repo_name_prefix":   {Type: TString},
		"allow_public_repos": {Type: TBool},
		"max_repositories":   {Type: TInt, HasMin: true, Min: 0},
		"token_max_ttl_days": {Type: TInt, HasMin: true, Min: 0},
		"members": {Type: TSet, KeyBy: "username", Elem: &Field{Type: TObject, Fields: map[string]*Field{
			"username": {Type: TString, Required: true},
			"role": {
				Type: TString, Required: true,
				Enum: []string{projectRoleOwner, projectRoleMaintainer, projectRoleDeveloper, projectRoleViewer},
			},
		}}},
		"groups": {Type: TSet, KeyBy: "group", Elem: &Field{Type: TObject, Fields: map[string]*Field{
			"group": {Type: TString, Required: true},
			"role": {
				Type: TString, Required: true,
				Enum: []string{projectRoleMaintainer, projectRoleDeveloper, projectRoleViewer},
			},
		}}},
	}}
}

// Validate implements Validator.
func (projectKind) Validate(o *Object) []error {
	var errs []error

	if formats, ok := o.Spec["allowed_formats"].([]any); ok && len(formats) == 0 {
		errs = append(errs, errProjectFormatsEmpty)
	}

	if types, ok := o.Spec["allowed_repo_types"].([]any); ok && len(types) == 0 {
		errs = append(errs, errProjectTypesEmpty)
	}

	if _, has := o.Spec["members"].([]any); has && projectFirstOwner(o.Spec) == "" {
		errs = append(errs, errProjectNoOwner)
	}

	return errs
}

func (projectKind) Refs(o *Object) []Ref {
	var refs []Ref

	for i, m := range projectEntries(o.Spec, "members") {
		refs = append(refs, Ref{Kind: KindUser, Key: Str(m, "username"), Field: fmt.Sprintf("members[%d].username", i)})
	}

	for i, g := range projectEntries(o.Spec, "groups") {
		refs = append(refs, Ref{Kind: KindGroup, Key: Str(g, "group"), Field: fmt.Sprintf("groups[%d].group", i)})
	}

	return refs
}

// projectEntries returns the object elements of spec[key] in manifest order.
func projectEntries(spec map[string]any, key string) []map[string]any {
	list, _ := spec[key].([]any)
	out := make([]map[string]any, 0, len(list))

	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}

	return out
}

// projectRoles maps the identity field of spec[key]'s elements to their role.
func projectRoles(spec map[string]any, key, identity string) map[string]string {
	entries := projectEntries(spec, key)
	out := make(map[string]string, len(entries))

	for _, e := range entries {
		out[Str(e, identity)] = Str(e, "role")
	}

	return out
}

// projectFirstOwner returns the first member with the owner role, "" if none.
func projectFirstOwner(spec map[string]any) string {
	for _, m := range projectEntries(spec, "members") {
		if Str(m, "role") == projectRoleOwner {
			return Str(m, "username")
		}
	}

	return ""
}

// projectResponse is one project of GET /api/v1/projects.
type projectResponse struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	DisplayName      *string  `json:"display_name"`
	Description      *string  `json:"description"`
	AllowedFormats   []string `json:"allowed_formats"`
	AllowedRepoTypes []string `json:"allowed_repo_types"`
	RepoNamePrefix   string   `json:"repo_name_prefix"`
	AllowPublicRepos bool     `json:"allow_public_repos"`
	MaxRepositories  int      `json:"max_repositories"`
	TokenMaxTTLDays  int      `json:"token_max_ttl_days"`
	IsSystem         bool     `json:"is_system"`
}

func (p *projectResponse) spec() map[string]any {
	var formats any // null: every format

	if len(p.AllowedFormats) > 0 {
		formats = Canon(SortedStrings(p.AllowedFormats))
	}

	return map[string]any{
		"display_name":       nullStr(p.DisplayName),
		"description":        nullStr(p.Description),
		"allowed_formats":    formats,
		"allowed_repo_types": Canon(SortedStrings(p.AllowedRepoTypes)),
		"repo_name_prefix":   p.RepoNamePrefix,
		"allow_public_repos": p.AllowPublicRepos,
		"max_repositories":   float64(p.MaxRepositories),
		"token_max_ttl_days": float64(p.TokenMaxTTLDays),
	}
}

func projectPath(name string) string { return projectsPath + "/" + PathEscape(name) }

func (projectKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	var resp struct {
		Projects []projectResponse `json:"projects"`
	}

	if err := env.Client.Get(ctx, projectsPath+"?all=true", &resp); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	out := make([]*Live, len(resp.Projects))

	err := identForEach(ctx, len(resp.Projects), func(ctx context.Context, i int) error {
		p := &resp.Projects[i]
		spec := p.spec()

		var err error

		if spec["members"], err = projectMembers(ctx, env, p.Name); err != nil {
			return err
		}

		if spec["groups"], err = projectGroups(ctx, env, p.Name); err != nil {
			return err
		}

		out[i] = &Live{Kind: KindProject, Key: p.Name, ID: p.ID, Spec: spec, Seeded: p.IsSystem}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func projectMembers(ctx context.Context, env *Env, name string) ([]any, error) {
	var resp struct {
		Members []struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"members"`
	}

	if err := env.Client.Get(ctx, projectPath(name)+"/members", &resp); err != nil {
		return nil, fmt.Errorf("list members of project %q: %w", name, err)
	}

	sort.Slice(resp.Members, func(i, j int) bool { return resp.Members[i].Username < resp.Members[j].Username })

	out := make([]any, len(resp.Members))
	for i, m := range resp.Members {
		out[i] = map[string]any{"username": m.Username, "role": m.Role}
	}

	return out, nil
}

func projectGroups(ctx context.Context, env *Env, name string) ([]any, error) {
	var resp struct {
		Groups []struct {
			GroupName string `json:"group_name"`
			Role      string `json:"role"`
		} `json:"groups"`
	}

	if err := env.Client.Get(ctx, projectPath(name)+"/groups", &resp); err != nil {
		return nil, fmt.Errorf("list groups of project %q: %w", name, err)
	}

	sort.Slice(resp.Groups, func(i, j int) bool { return resp.Groups[i].GroupName < resp.Groups[j].GroupName })

	out := make([]any, len(resp.Groups))
	for i, g := range resp.Groups {
		out[i] = map[string]any{"group": g.GroupName, "role": g.Role}
	}

	return out, nil
}

var projectBodyFields = []string{ //nolint:gochecknoglobals // immutable field list
	"display_name", "description", "allowed_formats", "allowed_repo_types",
	"repo_name_prefix", "allow_public_repos", "max_repositories", "token_max_ttl_days",
}

func (projectKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	body := Pick(o.Spec, projectBodyFields...)
	body["name"] = o.Name

	// A fresh project allows every format; null needs no field.
	if v, ok := body["allowed_formats"]; ok && v == nil {
		delete(body, "allowed_formats")
	}

	// The server adds the owner as an owner member itself. Without members the
	// calling token's user stays the owner.
	created := &Live{Spec: map[string]any{}}

	owner := projectFirstOwner(o.Spec)
	if owner != "" {
		body["owner"] = owner
		created.Spec["members"] = []any{map[string]any{"username": owner, "role": projectRoleOwner}}
	}

	var resp projectResponse
	if err := env.Client.Do(ctx, http.MethodPost, projectsPath, body, &resp); err != nil {
		return "", fmt.Errorf("create project %q: %w", o.Name, err)
	}

	if err := projectReconcile(ctx, env, o, created); err != nil {
		return resp.ID, err
	}

	return resp.ID, nil
}

func (projectKind) Update(ctx context.Context, env *Env, live *Live, o *Object, changes []Change) (string, error) {
	var policy []Change

	for _, c := range changes {
		if top := ChangedTop([]Change{c}); !top["members"] && !top["groups"] {
			policy = append(policy, c)
		}
	}

	if len(policy) > 0 {
		body := PatchBody(policy)
		if v, ok := body["allowed_formats"]; ok && v == nil {
			body["allowed_formats"] = []any{}
		}

		if err := env.Client.Do(ctx, http.MethodPatch, projectPath(o.Name), body, nil); err != nil {
			return "", fmt.Errorf("update project %q: %w", o.Name, err)
		}
	}

	if err := projectReconcile(ctx, env, o, live); err != nil {
		return live.ID, err
	}

	return live.ID, nil
}

// projectReconcile makes the members and groups of the project equal to what o
// asks for (when it asks for them), relative to the current state in live.
func projectReconcile(ctx context.Context, env *Env, o *Object, live *Live) error {
	if _, ok := o.Spec["members"].([]any); ok {
		if err := projectReconcileMembers(ctx, env, o, live); err != nil {
			return err
		}
	}

	if _, ok := o.Spec["groups"].([]any); ok {
		if err := projectReconcileGroups(ctx, env, o, live); err != nil {
			return err
		}
	}

	return nil
}

// projectOrder sorts names with owner-bound changes first, so that a new owner
// exists before the previous one is demoted or removed.
func projectOrder(names []string, roles map[string]string) {
	sort.Slice(names, func(i, j int) bool {
		oi, oj := roles[names[i]] == projectRoleOwner, roles[names[j]] == projectRoleOwner
		if oi != oj {
			return oi
		}

		return names[i] < names[j]
	})
}

// projectDiffRoles splits desired against current into names to add, names
// whose role changes and names to remove, each in apply order.
func projectDiffRoles(current, desired map[string]string) ([]string, []string, []string) {
	var add, change, remove []string

	for n, role := range desired {
		have, ok := current[n]

		switch {
		case !ok:
			add = append(add, n)
		case have != role:
			change = append(change, n)
		}
	}

	for n := range current {
		if _, ok := desired[n]; !ok {
			remove = append(remove, n)
		}
	}

	projectOrder(add, desired)
	projectOrder(change, desired)
	sort.Strings(remove)

	return add, change, remove
}

func projectReconcileMembers(ctx context.Context, env *Env, o *Object, live *Live) error {
	desired := projectRoles(o.Spec, "members", "username")
	current := projectRoles(live.Spec, "members", "username")

	add, change, remove := projectDiffRoles(current, desired)
	base := projectPath(o.Name) + "/members"

	for _, n := range add {
		body := map[string]any{"username": n, "role": desired[n]}
		if err := env.Client.Do(ctx, http.MethodPost, base, body, nil); err != nil {
			return fmt.Errorf("add member %q to project %q: %w", n, o.Name, err)
		}
	}

	for _, n := range change {
		id, err := env.UserID(ctx, n)
		if err != nil {
			return fmt.Errorf("project %q member: %w", o.Name, err)
		}

		if err := env.Client.Do(ctx, http.MethodPatch, base+"/"+PathEscape(id), map[string]any{"role": desired[n]}, nil); err != nil {
			return fmt.Errorf("change role of member %q in project %q: %w", n, o.Name, err)
		}
	}

	for _, n := range remove {
		id, err := env.UserID(ctx, n)
		if err != nil {
			return fmt.Errorf("project %q member: %w", o.Name, err)
		}

		if err := env.Client.Do(ctx, http.MethodDelete, base+"/"+PathEscape(id), nil, nil); err != nil {
			return fmt.Errorf("remove member %q from project %q: %w", n, o.Name, err)
		}
	}

	return nil
}

func projectReconcileGroups(ctx context.Context, env *Env, o *Object, live *Live) error {
	desired := projectRoles(o.Spec, "groups", "group")
	current := projectRoles(live.Spec, "groups", "group")
	add, change, remove := projectDiffRoles(current, desired)
	base := projectPath(o.Name) + "/groups"

	for _, n := range slices.Concat(add, change) {
		id, err := env.GroupID(ctx, n)
		if err != nil {
			return fmt.Errorf("project %q group: %w", o.Name, err)
		}

		if err := env.Client.Do(ctx, http.MethodPut, base+"/"+PathEscape(id), map[string]any{"role": desired[n]}, nil); err != nil {
			return fmt.Errorf("attach group %q to project %q: %w", n, o.Name, err)
		}
	}

	for _, n := range remove {
		id, err := env.GroupID(ctx, n)
		if err != nil {
			return fmt.Errorf("project %q group: %w", o.Name, err)
		}

		if err := env.Client.Do(ctx, http.MethodDelete, base+"/"+PathEscape(id), nil, nil); err != nil {
			return fmt.Errorf("detach group %q from project %q: %w", n, o.Name, err)
		}
	}

	return nil
}

func (projectKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.Do(ctx, http.MethodDelete, projectPath(live.Key), nil, nil); err != nil {
		return fmt.Errorf("delete project %q: %w", live.Key, err)
	}

	return nil
}
