// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ErrPrincipalNotFound is wrapped when a manifest names a user or group that does not exist.
var ErrPrincipalNotFound = errors.New("not found on the server")

// invalidf wraps sentinel with a formatted validation message.
func invalidf(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}

// Canon converts v into the canonical JSON-typed form the engine compares:
// all numbers become float64, maps become map[string]any, slices become []any.
// SecretRef values pass through unchanged.
func Canon(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}

		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = Canon(e)
		}

		return out
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = e
		}

		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = Canon(e)
		}

		return out
	}

	if f, ok := canonNumber(v); ok {
		return f
	}

	return v
}

// canonNumber converts every Go numeric type (and json.Number) to float64.
func canonNumber(v any) (any, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int8:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint8:
		return float64(t), true
	case uint16:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float32:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return t.String(), true
		}

		return f, true
	}

	return nil, false
}

// FromJSON decodes raw JSON into canonical form.
func FromJSON(raw []byte) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}

	return Canon(v), nil
}

// ToMap re-encodes any JSON-serializable value (typically an API response
// struct or a decoded object) into a canonical map[string]any.
func ToMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	out, err := FromJSON(raw)
	if err != nil {
		return nil, err
	}

	m, _ := out.(map[string]any)

	return m, nil
}

// PatchBody builds the request body of a PATCH/PUT from changes: dotted paths
// become nested objects, so {oidc_config.issuer, public} yields
// {"oidc_config": {"issuer": ...}, "public": ...}. Top-level fields not in
// changes are absent, which the REST PATCH endpoints treat as unchanged.
func PatchBody(changes []Change) map[string]any {
	body := map[string]any{}

	for _, c := range changes {
		parts := strings.Split(c.Path, ".")
		cur := body

		for _, p := range parts[:len(parts)-1] {
			next, ok := cur[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[p] = next
			}

			cur = next
		}

		cur[parts[len(parts)-1]] = c.New
	}

	return body
}

// ChangedTop returns the set of top-level field names touched by changes.
func ChangedTop(changes []Change) map[string]bool {
	out := make(map[string]bool, len(changes))

	for _, c := range changes {
		out[TopField(c.Path)] = true
	}

	return out
}

// TopField returns the top-level field of a change path ("members[bob].role"
// -> "members", "oidc_config.issuer" -> "oidc_config").
func TopField(path string) string {
	if i := strings.IndexAny(path, ".["); i >= 0 {
		return path[:i]
	}

	return path
}

// Pick returns a copy of spec restricted to keys.
func Pick(spec map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(keys))

	for _, k := range keys {
		if v, ok := spec[k]; ok {
			out[k] = v
		}
	}

	return out
}

// Omit returns a copy of spec without keys.
func Omit(spec map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(spec))
	maps.Copy(out, spec)

	for _, k := range keys {
		delete(out, k)
	}

	return out
}

// Str returns spec[key] as a string ("" when absent or not a string).
func Str(spec map[string]any, key string) string {
	s, _ := spec[key].(string)

	return s
}

// Bool returns spec[key] as a bool (false when absent or not a bool).
func Bool(spec map[string]any, key string) bool {
	b, _ := spec[key].(bool)

	return b
}

// StrList returns spec[key] as a string slice (nil when absent).
func StrList(spec map[string]any, key string) []string {
	l, _ := spec[key].([]any)
	out := make([]string, 0, len(l))

	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}

	return out
}

// SortedStrings returns a sorted copy of s.
func SortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)

	return out
}

// Idents is the user and group lookup shared by kinds that reference both.
type Idents struct {
	// Users maps username to user id; Groups maps group name to group id.
	Users  map[string]string
	Groups map[string]string
}

const identsCacheKey = "declarative.idents"

// ForgetIdents drops the cached user/group lookups; kinds that create users or
// groups call it so later lookups see the new objects.
func (e *Env) ForgetIdents() { delete(e.Cache, identsCacheKey) }

// UserID resolves a username to its id.
func (e *Env) UserID(ctx context.Context, username string) (string, error) {
	ids, err := e.idents(ctx)
	if err != nil {
		return "", err
	}

	id, ok := ids.Users[username]
	if !ok {
		return "", fmt.Errorf("%w: user %q", ErrPrincipalNotFound, username)
	}

	return id, nil
}

// GroupID resolves a group name to its id.
func (e *Env) GroupID(ctx context.Context, name string) (string, error) {
	ids, err := e.idents(ctx)
	if err != nil {
		return "", err
	}

	id, ok := ids.Groups[name]
	if !ok {
		return "", fmt.Errorf("%w: group %q", ErrPrincipalNotFound, name)
	}

	return id, nil
}

// UserNames returns id to username for every user.
func (e *Env) UserNames(ctx context.Context) (map[string]string, error) {
	ids, err := e.idents(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(ids.Users))
	for n, id := range ids.Users {
		out[id] = n
	}

	return out, nil
}

// GroupNames returns id to name for every group.
func (e *Env) GroupNames(ctx context.Context) (map[string]string, error) {
	ids, err := e.idents(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(ids.Groups))
	for n, id := range ids.Groups {
		out[id] = n
	}

	return out, nil
}

func (e *Env) idents(ctx context.Context) (*Idents, error) {
	if v, ok := e.Cache[identsCacheKey].(*Idents); ok {
		return v, nil
	}

	ids := &Idents{Users: map[string]string{}, Groups: map[string]string{}}

	const perPage = 200

	for page := 1; ; page++ {
		var resp struct {
			Data []struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"data"`
			Total int `json:"total"`
		}

		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
		if err := e.Client.Get(ctx, "/api/v1/users?"+q.Encode(), &resp); err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}

		for _, u := range resp.Data {
			ids.Users[u.Username] = u.ID
		}

		if len(resp.Data) < perPage || page*perPage >= resp.Total {
			break
		}
	}

	var groups struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}

	if err := e.Client.Get(ctx, "/api/v1/groups", &groups); err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}

	for _, g := range groups.Data {
		ids.Groups[g.Name] = g.ID
	}

	e.Cache[identsCacheKey] = ids

	return ids, nil
}
