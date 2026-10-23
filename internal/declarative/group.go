// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"golang.org/x/sync/errgroup"
)

const (
	// groupRank orders groups after the users they contain and before the
	// projects that attach them.
	groupRank = 15

	groupsPath = "/api/v1/groups"

	// identConcurrency bounds the parallel sub-resource fetches of List.
	identConcurrency = 8
)

// groupKind manages user groups. members (usernames) is authoritative when
// present in a manifest; it is left out of `oa export` by default because SSO
// group sync usually fills it.
type groupKind struct{}

// NewGroupKind returns the Group kind.
func NewGroupKind() Kind { return groupKind{} }

func (groupKind) Name() string { return KindGroup }

func (groupKind) Rank() int { return groupRank }

func (groupKind) MetaKeys() []string { return nil }

func (groupKind) Key(o *Object) string { return o.Name }

func (groupKind) Order(*Object) int { return 0 }

// ExportOmit implements ExportOmitter.
func (groupKind) ExportOmit() []string { return []string{"members"} }

func (groupKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"description": {Type: TString},
		"members":     {Type: TSet, Elem: &Field{Type: TString}},
	}}
}

func (groupKind) Refs(o *Object) []Ref {
	members := StrList(o.Spec, "members")
	refs := make([]Ref, 0, len(members))

	for i, m := range members {
		refs = append(refs, Ref{Kind: KindUser, Key: m, Field: fmt.Sprintf("members[%d]", i)})
	}

	return refs
}

// groupResponse is one group of GET /api/v1/groups.
type groupResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func (groupKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	var resp struct {
		Data []groupResponse `json:"data"`
	}

	if err := env.Client.Get(ctx, groupsPath, &resp); err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}

	out := make([]*Live, len(resp.Data))

	err := identForEach(ctx, len(resp.Data), func(ctx context.Context, i int) error {
		g := resp.Data[i]

		members, err := groupMembers(ctx, env, g.ID)
		if err != nil {
			return fmt.Errorf("list members of group %q: %w", g.Name, err)
		}

		out[i] = &Live{
			Kind: KindGroup,
			Key:  g.Name,
			ID:   g.ID,
			Spec: map[string]any{"description": nullStr(g.Description), "members": members},
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// groupMembers returns the sorted usernames of a group as a canonical list.
func groupMembers(ctx context.Context, env *Env, id string) ([]any, error) {
	var resp struct {
		Data []struct {
			Username string `json:"username"`
		} `json:"data"`
	}

	if err := env.Client.Get(ctx, groupsPath+"/"+PathEscape(id)+"/members", &resp); err != nil {
		return nil, err //nolint:wrapcheck // callers add the group context
	}

	names := make([]string, 0, len(resp.Data))
	for _, u := range resp.Data {
		names = append(names, u.Username)
	}

	sort.Strings(names)

	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}

	return out, nil
}

// identForEach runs fn for every index in [0, n) with at most
// identConcurrency calls in flight and returns the first error.
func identForEach(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(identConcurrency)

	for i := range n {
		g.Go(func() error { return fn(gctx, i) })
	}

	return g.Wait() //nolint:wrapcheck // fn errors carry their own context
}

func (groupKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	body := map[string]any{"name": o.Name}
	if d, ok := o.Spec["description"]; ok {
		body["description"] = d
	}

	var created groupResponse
	if err := env.Client.Do(ctx, http.MethodPost, groupsPath, body, &created); err != nil {
		return "", fmt.Errorf("create group %q: %w", o.Name, err)
	}

	env.ForgetIdents()

	if members := StrList(o.Spec, "members"); len(members) > 0 {
		if err := setGroupMembers(ctx, env, created.ID, members); err != nil {
			return created.ID, fmt.Errorf("set members of group %q: %w", o.Name, err)
		}
	}

	return created.ID, nil
}

func (groupKind) Update(ctx context.Context, env *Env, live *Live, o *Object, changes []Change) (string, error) {
	top := ChangedTop(changes)
	id := PathEscape(live.ID)

	if top["description"] {
		body := PatchBody(changes)
		if err := env.Client.Do(ctx, http.MethodPatch, groupsPath+"/"+id, Pick(body, "description"), nil); err != nil {
			return "", fmt.Errorf("update group %q: %w", o.Name, err)
		}
	}

	if top["members"] {
		if err := setGroupMembers(ctx, env, live.ID, StrList(o.Spec, "members")); err != nil {
			return "", fmt.Errorf("set members of group %q: %w", o.Name, err)
		}
	}

	return live.ID, nil
}

// setGroupMembers replaces the membership of a group with the named users.
func setGroupMembers(ctx context.Context, env *Env, groupID string, usernames []string) error {
	ids := make([]string, 0, len(usernames))

	for _, name := range usernames {
		id, err := env.UserID(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // callers add the group context
		}

		ids = append(ids, id)
	}

	path := groupsPath + "/" + PathEscape(groupID) + "/members"

	return env.Client.Do(ctx, http.MethodPut, path, map[string]any{"members": ids}, nil) //nolint:wrapcheck // callers add the group context
}

func (groupKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.Do(ctx, http.MethodDelete, groupsPath+"/"+PathEscape(live.ID), nil, nil); err != nil {
		return fmt.Errorf("delete group %q: %w", live.Key, err)
	}

	env.ForgetIdents()

	return nil
}
