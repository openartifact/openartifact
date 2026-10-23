// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// identCall is one request the fake API received.
type identCall struct {
	Method string
	URI    string
	Body   map[string]any
}

// identFake records the requests of an httptest fake REST API.
type identFake struct {
	mu    sync.Mutex
	calls []identCall
}

// identRoute answers one "METHOD /path" of the fake API.
type identRoute func(r *http.Request) (int, any)

// identJSON is a route answering a fixed status and JSON body.
func identJSON(status int, body any) identRoute {
	return func(*http.Request) (int, any) { return status, body }
}

// newIdentEnv starts a fake API serving routes (keyed "METHOD /path", query
// strings excluded) and returns an Env talking to it.
func newIdentEnv(t *testing.T, routes map[string]identRoute) (*Env, *identFake) {
	t.Helper()

	fake := &identFake{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any

		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}

		fake.mu.Lock()
		fake.calls = append(fake.calls, identCall{Method: r.Method, URI: r.URL.RequestURI(), Body: body})
		fake.mu.Unlock()

		route, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such route"}`))

			return
		}

		status, resp := route(r)
		if resp != nil {
			w.Header().Set("Content-Type", "application/json")
		}

		w.WriteHeader(status)

		if resp != nil {
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	t.Cleanup(srv.Close)

	return NewEnv(NewClient(srv.URL, "tok", srv.Client())), fake
}

// writes returns the non-GET requests in arrival order.
func (f *identFake) writes() []identCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []identCall

	for _, c := range f.calls {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}

	return out
}

// identAssertCalls fails the test unless the write requests equal want.
func identAssertCalls(t *testing.T, got, want []identCall) {
	t.Helper()

	if reflect.DeepEqual(got, want) || (len(got) == 0 && len(want) == 0) {
		return
	}

	t.Errorf("requests mismatch\n got: %s\nwant: %s", identFormat(got), identFormat(want))
}

func identFormat(calls []identCall) string {
	var out strings.Builder

	for _, c := range calls {
		b, _ := json.Marshal(c.Body)
		fmt.Fprintf(&out, "\n  %s %s %s", c.Method, c.URI, b)
	}

	return out.String()
}

func identUsersRoutes(users ...map[string]any) map[string]identRoute {
	return map[string]identRoute{
		"GET /api/v1/users": identJSON(http.StatusOK, map[string]any{"data": users, "total": len(users)}),
	}
}

func TestUserKindList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		users []map[string]any
		want  map[string]*Live
	}{
		{
			name: "maps fields, nulls to empty strings and flags the bootstrap admin as seeded",
			users: []map[string]any{
				{"id": "u-admin", "username": "admin", "email": nil, "full_name": nil, "disabled": false, "must_change_password": true},
				{"id": "u-bob", "username": "bob", "email": "bob@example.org", "full_name": "Bob B", "disabled": true, "must_change_password": false},
			},
			want: map[string]*Live{
				"admin": {Kind: KindUser, Key: "admin", ID: "u-admin", Seeded: true, Spec: map[string]any{
					"email": "", "full_name": "", "disabled": false, "must_change_password": true,
				}},
				"bob": {Kind: KindUser, Key: "bob", ID: "u-bob", Spec: map[string]any{
					"email": "bob@example.org", "full_name": "Bob B", "disabled": true, "must_change_password": false,
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, _ := newIdentEnv(t, identUsersRoutes(tc.users...))

			got, err := NewUserKind().List(context.Background(), env)
			if err != nil {
				t.Fatalf("List: %v", err)
			}

			if len(got) != len(tc.want) {
				t.Fatalf("List returned %d users, want %d", len(got), len(tc.want))
			}

			for _, l := range got {
				if !reflect.DeepEqual(l, tc.want[l.Key]) {
					t.Errorf("user %q = %+v, want %+v", l.Key, l, tc.want[l.Key])
				}
			}
		})
	}
}

func TestUserKindListPagesThroughEveryUser(t *testing.T) {
	t.Parallel()

	const total = usersPerPage + 1

	var pages []string

	var mu sync.Mutex

	env, _ := newIdentEnv(t, map[string]identRoute{
		"GET /api/v1/users": func(r *http.Request) (int, any) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))

			mu.Lock()
			pages = append(pages, r.URL.Query().Get("page")+"/"+r.URL.Query().Get("per_page"))
			mu.Unlock()

			var data []map[string]any

			for i := (page - 1) * usersPerPage; i < min(page*usersPerPage, total); i++ {
				data = append(data, map[string]any{"id": "id-" + strconv.Itoa(i), "username": "user" + strconv.Itoa(i)})
			}

			return http.StatusOK, map[string]any{"data": data, "total": total}
		},
	})

	got, err := NewUserKind().List(context.Background(), env)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(got) != total {
		t.Errorf("List returned %d users, want %d", len(got), total)
	}

	if want := []string{"1/200", "2/200"}; !reflect.DeepEqual(pages, want) {
		t.Errorf("requested pages = %v, want %v", pages, want)
	}
}

func TestUserKindCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec map[string]any
		want map[string]any
	}{
		{
			name: "sends only the given fields and never a password",
			spec: map[string]any{"email": "ci@example.org", "full_name": "CI", "disabled": true, "must_change_password": false},
			want: map[string]any{"username": "ci", "email": "ci@example.org", "full_name": "CI", "disabled": true, "must_change_password": false},
		},
		{
			name: "leaves an empty email out so it stays NULL",
			spec: map[string]any{"email": "", "full_name": "CI"},
			want: map[string]any{"username": "ci", "full_name": "CI"},
		},
		{
			name: "creates from the name alone",
			spec: map[string]any{},
			want: map[string]any{"username": "ci"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newIdentEnv(t, map[string]identRoute{
				"POST /api/v1/users": identJSON(http.StatusCreated, map[string]any{"id": "u-ci", "username": "ci"}),
			})
			env.Cache[identsCacheKey] = &Idents{}

			id, err := NewUserKind().Create(context.Background(), env, &Object{Kind: KindUser, Name: "ci", Spec: tc.spec})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if id != "u-ci" {
				t.Errorf("id = %q, want u-ci", id)
			}

			identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodPost, URI: "/api/v1/users", Body: tc.want}})

			if _, cached := env.Cache[identsCacheKey]; cached {
				t.Error("Create did not drop the cached user lookup")
			}
		})
	}
}

func TestUserKindUpdateSendsOnlyChangedFields(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"PATCH /api/v1/users/u-bob": identJSON(http.StatusOK, map[string]any{}),
	})

	live := &Live{Kind: KindUser, Key: "bob", ID: "u-bob"}
	changes := []Change{
		{Path: "full_name", Old: "Bob", New: "Robert"},
		{Path: "disabled", Old: false, New: true},
	}

	id, err := NewUserKind().Update(context.Background(), env, live, &Object{Name: "bob"}, changes)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if id != "u-bob" {
		t.Errorf("id = %q, want u-bob", id)
	}

	identAssertCalls(t, fake.writes(), []identCall{{
		Method: http.MethodPatch, URI: "/api/v1/users/u-bob",
		Body: map[string]any{"full_name": "Robert", "disabled": true},
	}})
}

func TestUserKindDelete(t *testing.T) {
	t.Parallel()

	env, fake := newIdentEnv(t, map[string]identRoute{
		"DELETE /api/v1/users/u-bob": identJSON(http.StatusNoContent, nil),
	})

	if err := NewUserKind().Delete(context.Background(), env, &Live{Key: "bob", ID: "u-bob"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	identAssertCalls(t, fake.writes(), []identCall{{Method: http.MethodDelete, URI: "/api/v1/users/u-bob"}})
}

func TestUserKindErrorsKeepAPIError(t *testing.T) {
	t.Parallel()

	env, _ := newIdentEnv(t, map[string]identRoute{
		"POST /api/v1/users": identJSON(http.StatusConflict, map[string]any{"error": "conflict"}),
	})

	_, err := NewUserKind().Create(context.Background(), env, &Object{Name: "ci", Spec: map[string]any{}})
	if !IsStatus(err, http.StatusConflict) {
		t.Errorf("Create error = %v, want an *APIError with status 409", err)
	}
}

func TestUserKindSchemaHasNoPassword(t *testing.T) {
	t.Parallel()

	k := NewUserKind()
	fields := k.Schema().Fields

	if _, ok := fields["password"]; ok {
		t.Error("User schema must not accept a password")
	}

	for _, want := range []string{"email", "full_name", "disabled", "must_change_password"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("User schema is missing %q", want)
		}
	}

	if k.Rank() != 10 {
		t.Errorf("Rank = %d, want 10", k.Rank())
	}
}
