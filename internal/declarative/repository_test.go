// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// repoFakeReq is one request the fake REST API received.
type repoFakeReq struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
}

// repoFake is a recording fake REST API; answer decides every response.
type repoFake struct {
	mu     sync.Mutex
	reqs   []repoFakeReq
	answer func(repoFakeReq) (int, any)
}

func newRepoFake(t *testing.T, answer func(repoFakeReq) (int, any)) (*Env, *repoFake) {
	t.Helper()

	fake := &repoFake{answer: answer}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := repoFakeReq{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query()}

		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req.Body); err != nil {
				t.Errorf("decode request body of %s %s: %v", r.Method, r.URL.Path, err)
			}
		}

		fake.mu.Lock()
		fake.reqs = append(fake.reqs, req)
		fake.mu.Unlock()

		status, resp := answer(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		if resp != nil {
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	t.Cleanup(srv.Close)

	return NewEnv(NewClient(srv.URL, "tok", srv.Client())), fake
}

func (f *repoFake) requests() []repoFakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]repoFakeReq(nil), f.reqs...)
}

// mutations returns the requests that change state (everything but GET), as
// "METHOD path" plus the body.
func (f *repoFake) mutations() []repoFakeReq {
	var out []repoFakeReq

	for _, r := range f.requests() {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}

	return out
}

func repoQuotaJSON(size, count, warn int) map[string]any {
	return map[string]any{
		"id": "q", "repo_id": "r", "max_size_bytes": size, "max_artifact_count": count,
		"warn_threshold_percent": warn, "current_size_bytes": 12345, "current_artifact_count": 7,
	}
}

// repoSubAnswer answers the quota and cleanup-policy reads of any repository
// with "no quota, no policies".
func repoSubAnswer(req repoFakeReq) (int, any, bool) {
	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/quota"):
		return http.StatusOK, repoQuotaJSON(0, 0, 80), true
	case req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/cleanup-policies"):
		return http.StatusOK, map[string]any{"policies": []any{}}, true
	}

	return 0, nil, false
}

func repoNotFound(req repoFakeReq) (int, any) {
	return http.StatusNotFound, map[string]any{"error": "no route " + req.Method + " " + req.Path}
}

func repoListFixture() map[string]any {
	return map[string]any{"total": 3, "page": 1, "limit": 100, "repositories": []any{
		map[string]any{
			"id": "id-rpm", "name": "rpm-hosted", "format": "rpm", "description": "Internal", "public": true,
			"repo_type": "hosted", "project": "platform", "presign_downloads": true, "trash_retention_days": 7,
			"immutable_releases": true, "merge_proxy_versions": false, "sign_packages": true,
			"created_at": "2026-01-01T00:00:00Z",
		},
		map[string]any{
			"id": "id-px", "name": "npm-proxy", "format": "npm", "description": nil, "public": false,
			"repo_type": "proxy", "project": "platform", "presign_downloads": nil, "trash_retention_days": nil,
			"upstream_url": "https://registry.npmjs.org", "upstream_username": "bot",
			"cache_ttl_seconds": 3600, "negative_cache_ttl_seconds": 60, "max_stale_seconds": 0,
		},
		map[string]any{
			"id": "id-grp", "name": "npm-all", "format": "npm", "description": "", "public": false,
			"repo_type": "group", "project": "platform", "merge_proxy_versions": true,
			"cache_ttl_seconds": 3600,
		},
	}}
}

func repoListAnswer(req repoFakeReq) (int, any) {
	switch {
	case req.Method == http.MethodGet && req.Path == "/api/v1/repositories":
		return http.StatusOK, repoListFixture()
	case req.Method == http.MethodGet && req.Path == "/api/v1/repositories/npm-all/members":
		return http.StatusOK, map[string]any{"members": []any{
			map[string]any{"name": "npm-proxy", "priority": 2},
			map[string]any{"name": "npm-local", "priority": 1},
		}}
	case req.Method == http.MethodGet && req.Path == "/api/v1/repositories/rpm-hosted/quota":
		return http.StatusOK, repoQuotaJSON(1000000, 50, 90)
	case req.Method == http.MethodGet && req.Path == "/api/v1/repositories/rpm-hosted/cleanup-policies":
		return http.StatusOK, map[string]any{"policies": []any{
			map[string]any{
				"id": "p1", "name": "keep-10", "enabled": true, "keep_last_n": 10, "keep_since_days": nil,
				"keep_downloaded_since_days": nil, "name_pattern": "", "schedule_cron": "0 2 * * *",
			},
			map[string]any{
				"id": "p2", "name": "recent", "enabled": false, "keep_last_n": nil, "keep_since_days": 30,
				"name_pattern": "^snap", "schedule_cron": "0 3 * * 0",
			},
		}}
	}

	if status, resp, ok := repoSubAnswer(req); ok {
		return status, resp
	}

	if req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/members") {
		return http.StatusOK, map[string]any{"members": []any{}}
	}

	return repoNotFound(req)
}

func TestRepositoryList(t *testing.T) {
	t.Parallel()

	env, _ := newRepoFake(t, repoListAnswer)

	lives, err := NewRepositoryKind().List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	zeroQuota := map[string]any{"max_size_bytes": 0.0, "max_artifact_count": 0.0, "warn_threshold_percent": 80.0}

	tests := []struct {
		key  string
		id   string
		want map[string]any
	}{
		{
			key: "rpm-hosted", id: "id-rpm",
			want: map[string]any{
				"format": "rpm", "repo_type": "hosted", "description": "Internal", "public": true,
				"project": "platform", "presign_downloads": true, "trash_retention_days": 7.0,
				"immutable_releases": true, "sign_packages": true,
				"quota": map[string]any{"max_size_bytes": 1000000.0, "max_artifact_count": 50.0, "warn_threshold_percent": 90.0},
				"cleanup_policies": []any{
					map[string]any{
						"name": "keep-10", "enabled": true, "keep_last_n": 10.0,
						"name_pattern": "", "schedule_cron": "0 2 * * *",
					},
					map[string]any{
						"name": "recent", "enabled": false, "keep_since_days": 30.0,
						"name_pattern": "^snap", "schedule_cron": "0 3 * * 0",
					},
				},
			},
		},
		{
			key: "npm-proxy", id: "id-px",
			want: map[string]any{
				"format": "npm", "repo_type": "proxy", "description": "", "public": false,
				"project": "platform", "presign_downloads": nil, "trash_retention_days": nil,
				"immutable_releases": false,
				"upstream_url":       "https://registry.npmjs.org", "upstream_username": "bot",
				"cache_ttl_seconds": 3600.0, "negative_cache_ttl_seconds": 60.0, "max_stale_seconds": 0.0,
				"quota": zeroQuota, "cleanup_policies": []any{},
			},
		},
		{
			key: "npm-all", id: "id-grp",
			want: map[string]any{
				"format": "npm", "repo_type": "group", "description": "", "public": false,
				"project": "platform", "presign_downloads": nil, "trash_retention_days": nil,
				"immutable_releases": false, "merge_proxy_versions": true,
				"member_repos": []any{"npm-local", "npm-proxy"},
				"quota":        zeroQuota, "cleanup_policies": []any{},
			},
		},
	}

	if len(lives) != len(tests) {
		t.Fatalf("got %d repositories, want %d", len(lives), len(tests))
	}

	for i, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()

			live := lives[i]
			if live.Kind != KindRepository || live.Key != tc.key || live.ID != tc.id || live.Seeded {
				t.Errorf("identity = %s %q %q seeded=%v", live.Kind, live.Key, live.ID, live.Seeded)
			}

			if !reflect.DeepEqual(live.Spec, tc.want) {
				t.Errorf("spec =\n%#v\nwant\n%#v", live.Spec, tc.want)
			}

			if len(live.Secrets) != 0 {
				t.Errorf("secrets = %v, want none (upstream password presence is not exposed)", live.Secrets)
			}
		})
	}
}

// TestRepositoryLiveIsValidManifest checks the export -> apply loop: every
// live spec only uses schema fields and passes the kind's own validation.
func TestRepositoryLiveIsValidManifest(t *testing.T) {
	t.Parallel()

	env, _ := newRepoFake(t, repoListAnswer)
	kind := NewRepositoryKind()

	lives, err := kind.List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	validator, ok := kind.(Validator)
	if !ok {
		t.Fatal("Repository kind does not implement Validator")
	}

	schema := kind.Schema()

	for _, live := range lives {
		for k := range live.Spec {
			if _, known := schema.Fields[k]; !known {
				t.Errorf("%s: live field %q is not in the schema", live.Key, k)
			}
		}

		o := &Object{Kind: KindRepository, Name: live.Key, Spec: live.Spec}
		if errs := validator.Validate(o); len(errs) != 0 {
			t.Errorf("%s: live spec fails validation: %v", live.Key, errs)
		}

		if res := diffSpec(schema, live.Spec, live.Spec, live.Secrets, false); len(res.Changes) != 0 {
			t.Errorf("%s: live spec differs from itself: %v", live.Key, res.Changes)
		}
	}
}

func TestRepositoryListPagesThroughEveryRepository(t *testing.T) {
	t.Parallel()

	const total = repoPageSize + 5

	env, fake := newRepoFake(t, func(req repoFakeReq) (int, any) {
		if req.Path == "/api/v1/repositories" {
			page := 1

			if _, err := fmt.Sscan(req.Query.Get("page"), &page); err != nil {
				return http.StatusBadRequest, map[string]any{"error": "bad page"}
			}

			var items []any

			for i := (page - 1) * repoPageSize; i < min(page*repoPageSize, total); i++ {
				items = append(items, map[string]any{
					"id": fmt.Sprintf("id-%d", i), "name": fmt.Sprintf("repo-%03d", i), "format": "raw", "repo_type": "hosted",
				})
			}

			return http.StatusOK, map[string]any{"total": total, "repositories": items}
		}

		if status, resp, ok := repoSubAnswer(req); ok {
			return status, resp
		}

		return repoNotFound(req)
	})

	lives, err := NewRepositoryKind().List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	if len(lives) != total {
		t.Fatalf("got %d repositories, want %d", len(lives), total)
	}

	for i, l := range lives {
		if want := fmt.Sprintf("repo-%03d", i); l.Key != want {
			t.Fatalf("lives[%d].Key = %q, want %q", i, l.Key, want)
		}
	}

	pages := 0

	for _, r := range fake.requests() {
		if r.Path == "/api/v1/repositories" {
			pages++
		}
	}

	if pages != 2 {
		t.Errorf("fetched %d pages, want 2", pages)
	}
}

func TestRepositoryCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		spec map[string]any
		// want are the mutating requests in order.
		want []repoFakeReq
	}{
		{
			name: "hosted rpm with quota and policy",
			key:  "rpm-hosted",
			spec: map[string]any{
				"format": "rpm", "project": "platform", "public": true, "sign_packages": true,
				"quota":            map[string]any{"max_size_bytes": 1e9, "warn_threshold_percent": 90.0},
				"cleanup_policies": []any{map[string]any{"name": "keep", "enabled": true, "keep_last_n": 5.0}},
			},
			want: []repoFakeReq{
				{Method: http.MethodPost, Path: "/api/v1/repositories", Body: map[string]any{
					"name": "rpm-hosted", "format": "rpm", "project": "platform", "public": true, "sign_packages": true,
				}},
				{Method: http.MethodPut, Path: "/api/v1/repositories/rpm-hosted/quota", Body: map[string]any{
					"max_size_bytes": 1e9, "warn_threshold_percent": 90.0,
				}},
				{Method: http.MethodPost, Path: "/api/v1/repositories/rpm-hosted/cleanup-policies", Body: map[string]any{
					"name": "keep", "enabled": true, "keep_last_n": 5.0,
				}},
			},
		},
		{
			name: "proxy sends the resolved password in the create body",
			key:  "npm-proxy",
			spec: map[string]any{
				"format": "npm", "repo_type": "proxy", "upstream_url": "https://registry.npmjs.org",
				"upstream_username": "bot", "upstream_password": "s3cr3t", "presign_downloads": nil,
			},
			want: []repoFakeReq{
				{Method: http.MethodPost, Path: "/api/v1/repositories", Body: map[string]any{
					"name": "npm-proxy", "format": "npm", "repo_type": "proxy", "upstream_url": "https://registry.npmjs.org",
					"upstream_username": "bot", "upstream_password": "s3cr3t", "presign_downloads": nil,
				}},
			},
		},
		{
			name: "group sends member_repos in priority order",
			key:  "npm-all",
			spec: map[string]any{"format": "npm", "repo_type": "group", "member_repos": []any{"b", "a"}},
			want: []repoFakeReq{
				{Method: http.MethodPost, Path: "/api/v1/repositories", Body: map[string]any{
					"name": "npm-all", "format": "npm", "repo_type": "group", "member_repos": []any{"b", "a"},
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newRepoFake(t, func(req repoFakeReq) (int, any) {
				switch {
				case req.Method == http.MethodPost && req.Path == "/api/v1/repositories":
					return http.StatusCreated, map[string]any{"id": "new-id", "name": tc.key}
				case req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/cleanup-policies"):
					return http.StatusOK, map[string]any{"policies": []any{}}
				case req.Method != http.MethodGet:
					return http.StatusOK, map[string]any{}
				}

				return repoNotFound(req)
			})

			id, err := NewRepositoryKind().Create(context.Background(), env, &Object{Kind: KindRepository, Name: tc.key, Spec: tc.spec})
			if err != nil {
				t.Fatal(err)
			}

			if id != "new-id" {
				t.Errorf("id = %q, want new-id", id)
			}

			assertRepoMutations(t, fake, tc.want)
		})
	}
}

func assertRepoMutations(t *testing.T, fake *repoFake, want []repoFakeReq) {
	t.Helper()

	got := fake.mutations()
	if len(got) != len(want) {
		t.Fatalf("got %d mutating requests %+v, want %d %+v", len(got), got, len(want), want)
	}

	for i := range want {
		if got[i].Method != want[i].Method || got[i].Path != want[i].Path {
			t.Errorf("request %d = %s %s, want %s %s", i, got[i].Method, got[i].Path, want[i].Method, want[i].Path)
		}

		if !reflect.DeepEqual(got[i].Body, want[i].Body) {
			t.Errorf("request %d (%s %s) body = %#v, want %#v", i, got[i].Method, got[i].Path, got[i].Body, want[i].Body)
		}
	}
}

func TestRepositoryCreateFollowUpFailureKeepsIDAndAPIError(t *testing.T) {
	t.Parallel()

	env, _ := newRepoFake(t, func(req repoFakeReq) (int, any) {
		switch req.Method {
		case http.MethodPost:
			return http.StatusCreated, map[string]any{"id": "new-id"}
		case http.MethodPut:
			return http.StatusBadRequest, map[string]any{"error": "max_size_bytes must not exceed the project limit"}
		}

		return repoNotFound(req)
	})

	o := &Object{Kind: KindRepository, Name: "r", Spec: map[string]any{
		"format": "raw", "quota": map[string]any{"max_size_bytes": 5.0},
	}}

	id, err := NewRepositoryKind().Create(context.Background(), env, o)
	if id != "new-id" {
		t.Errorf("id = %q, want new-id so the ownership marker is written", id)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want *APIError 400", err)
	}
}

func repoPoliciesFixture() map[string]any {
	return map[string]any{"policies": []any{
		map[string]any{
			"id": "p1", "name": "keep-10", "enabled": true, "keep_last_n": 10, "keep_since_days": nil,
			"name_pattern": "", "schedule_cron": "0 2 * * *",
		},
		map[string]any{
			"id": "p2", "name": "old", "enabled": true, "keep_last_n": 3, "name_pattern": "", "schedule_cron": "0 2 * * *",
		},
	}}
}

func TestRepositoryUpdateSendsOnlyChanges(t *testing.T) {
	t.Parallel()

	const base = "/api/v1/repositories/my-repo"

	tests := []struct {
		name    string
		spec    map[string]any
		changes []Change
		want    []repoFakeReq
	}{
		{
			name:    "scalars",
			changes: []Change{{Path: "description", Old: "", New: "x"}, {Path: "public", Old: false, New: true}},
			want: []repoFakeReq{{Method: http.MethodPatch, Path: "/api/v1/repository/my-repo", Body: map[string]any{
				"description": "x", "public": true,
			}}},
		},
		{
			name:    "nullable fields reset to null",
			changes: []Change{{Path: "presign_downloads", Old: true, New: nil}, {Path: "trash_retention_days", Old: 7.0, New: nil}},
			want: []repoFakeReq{{Method: http.MethodPatch, Path: "/api/v1/repository/my-repo", Body: map[string]any{
				"presign_downloads": nil, "trash_retention_days": nil,
			}}},
		},
		{
			name:    "member list and project move",
			changes: []Change{{Path: "member_repos", New: []any{"b", "a"}}, {Path: "project", Old: "x", New: "platform"}},
			want: []repoFakeReq{{Method: http.MethodPatch, Path: "/api/v1/repository/my-repo", Body: map[string]any{
				"member_repos": []any{"b", "a"}, "project": "platform",
			}}},
		},
		{
			name:    "secret change carries the resolved value",
			changes: []Change{{Path: "upstream_password", New: "s3cr3t", Secret: true}},
			want: []repoFakeReq{{Method: http.MethodPatch, Path: "/api/v1/repository/my-repo", Body: map[string]any{
				"upstream_password": "s3cr3t",
			}}},
		},
		{
			name:    "quota only touches the quota endpoint with the changed field",
			changes: []Change{{Path: "quota.max_size_bytes", Old: 0.0, New: 5.0}},
			want: []repoFakeReq{{Method: http.MethodPut, Path: base + "/quota", Body: map[string]any{
				"max_size_bytes": 5.0,
			}}},
		},
		{
			name: "cleanup policies reconcile by name",
			spec: map[string]any{"cleanup_policies": []any{
				map[string]any{"name": "keep-10", "enabled": true, "keep_last_n": 20.0},
				map[string]any{"name": "new", "enabled": true, "keep_since_days": 30.0},
			}},
			changes: []Change{
				{Path: "cleanup_policies[keep-10].keep_last_n", Old: 10.0, New: 20.0},
				{Path: "cleanup_policies[new]", New: map[string]any{"name": "new"}},
				{Path: "cleanup_policies[old]", Old: map[string]any{"name": "old"}},
			},
			want: []repoFakeReq{
				{Method: http.MethodPut, Path: base + "/cleanup-policies/p1", Body: map[string]any{"keep_last_n": 20.0}},
				{Method: http.MethodPost, Path: base + "/cleanup-policies", Body: map[string]any{
					"name": "new", "enabled": true, "keep_since_days": 30.0,
				}},
				{Method: http.MethodDelete, Path: base + "/cleanup-policies/p2"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newRepoFake(t, func(req repoFakeReq) (int, any) {
				if req.Method == http.MethodGet && req.Path == base+"/cleanup-policies" {
					return http.StatusOK, repoPoliciesFixture()
				}

				if req.Method != http.MethodGet {
					return http.StatusOK, map[string]any{}
				}

				return repoNotFound(req)
			})

			live := &Live{Kind: KindRepository, Key: "my-repo", ID: "id-1", Spec: map[string]any{}}
			o := &Object{Kind: KindRepository, Name: "my-repo", Spec: tc.spec}

			id, err := NewRepositoryKind().Update(context.Background(), env, live, o, tc.changes)
			if err != nil {
				t.Fatal(err)
			}

			if id != "id-1" {
				t.Errorf("id = %q, want id-1", id)
			}

			assertRepoMutations(t, fake, tc.want)
		})
	}
}

func TestRepositoryUpdateKeepsAPIError(t *testing.T) {
	t.Parallel()

	env, _ := newRepoFake(t, func(repoFakeReq) (int, any) {
		return http.StatusConflict, map[string]any{"error": "conflict"}
	})

	live := &Live{Kind: KindRepository, Key: "r", ID: "id", Spec: map[string]any{}}

	_, err := NewRepositoryKind().Update(context.Background(), env, live, &Object{Spec: map[string]any{}},
		[]Change{{Path: "public", New: true}})

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("err = %v, want *APIError 409", err)
	}
}

// TestRepositoryUpdateGuardsUpstreamPassword covers the server rule that moving
// a proxy to another origin or username drops the stored password.
func TestRepositoryUpdateGuardsUpstreamPassword(t *testing.T) {
	t.Parallel()

	secret := SecretRef{Env: "PW"}

	tests := []struct {
		name    string
		spec    map[string]any
		changes []Change
		wantErr bool
	}{
		{
			name:    "other origin with managed password",
			spec:    map[string]any{"upstream_password": secret},
			changes: []Change{{Path: "upstream_url", Old: "https://a.example.com", New: "https://b.example.com"}},
			wantErr: true,
		},
		{
			name:    "other username with managed password",
			spec:    map[string]any{"upstream_password": secret},
			changes: []Change{{Path: "upstream_username", Old: "a", New: "b"}},
			wantErr: true,
		},
		{
			name: "password re-sent in the same update",
			spec: map[string]any{"upstream_password": secret},
			changes: []Change{
				{Path: "upstream_url", Old: "https://a.example.com", New: "https://b.example.com"},
				{Path: "upstream_password", New: "pw", Secret: true},
			},
		},
		{
			name:    "same origin keeps the password",
			spec:    map[string]any{"upstream_password": secret},
			changes: []Change{{Path: "upstream_url", Old: "https://a.example.com", New: "https://a.example.com:443/mirror"}},
		},
		{
			name:    "password not managed by the manifest",
			spec:    map[string]any{},
			changes: []Change{{Path: "upstream_url", Old: "https://a.example.com", New: "https://b.example.com"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newRepoFake(t, func(req repoFakeReq) (int, any) { return http.StatusOK, map[string]any{} })
			live := &Live{Kind: KindRepository, Key: "px", ID: "id", Spec: map[string]any{"upstream_url": "https://a.example.com"}}

			_, err := NewRepositoryKind().Update(context.Background(), env, live, &Object{Spec: tc.spec}, tc.changes)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}

			if tc.wantErr && len(fake.requests()) != 0 {
				t.Errorf("a refused update must not reach the server: %+v", fake.requests())
			}
		})
	}
}

func TestRepositoryDeleteConfirmsByName(t *testing.T) {
	t.Parallel()

	env, fake := newRepoFake(t, func(repoFakeReq) (int, any) { return http.StatusNoContent, nil })

	err := NewRepositoryKind().Delete(context.Background(), env, &Live{Key: "my repo", ID: "id"})
	if err != nil {
		t.Fatal(err)
	}

	reqs := fake.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodDelete || reqs[0].Path != "/api/v1/repository/my%20repo" {
		t.Fatalf("requests = %+v", reqs)
	}

	if got := reqs[0].Query.Get("confirm"); got != "my repo" {
		t.Errorf("confirm = %q, want the repository name", got)
	}
}

func TestRepositoryKeyOrderRefs(t *testing.T) {
	t.Parallel()

	kind := NewRepositoryKind()

	if kind.Name() != KindRepository || kind.Rank() != 40 || len(kind.MetaKeys()) != 0 {
		t.Errorf("name/rank/meta = %s %d %v", kind.Name(), kind.Rank(), kind.MetaKeys())
	}

	tests := []struct {
		name      string
		obj       *Object
		wantOrder int
		wantRefs  []Ref
	}{
		{
			name:      "hosted defaults",
			obj:       &Object{Name: "a", Spec: map[string]any{"format": "raw"}},
			wantOrder: 0,
		},
		{
			name:      "proxy in a project",
			obj:       &Object{Name: "b", Spec: map[string]any{"repo_type": "proxy", "project": "p"}},
			wantOrder: 0,
			wantRefs:  []Ref{{Kind: KindProject, Key: "p", Field: "project"}},
		},
		{
			name: "group after its members",
			obj: &Object{Name: "g", Spec: map[string]any{
				"repo_type": "group", "member_repos": []any{"a", "b"},
			}},
			wantOrder: 1,
			wantRefs: []Ref{
				{Kind: KindRepository, Key: "a", Field: "member_repos[0]"},
				{Kind: KindRepository, Key: "b", Field: "member_repos[1]"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := kind.Key(tc.obj); got != tc.obj.Name {
				t.Errorf("Key = %q, want %q", got, tc.obj.Name)
			}

			if got := kind.Order(tc.obj); got != tc.wantOrder {
				t.Errorf("Order = %d, want %d", got, tc.wantOrder)
			}

			if got := kind.Refs(tc.obj); !reflect.DeepEqual(got, tc.wantRefs) {
				t.Errorf("Refs = %+v, want %+v", got, tc.wantRefs)
			}
		})
	}
}

func TestRepositoryValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		obj     *Object
		wantErr string
	}{
		{name: "minimal hosted", obj: &Object{Name: "ok_1", Spec: map[string]any{"format": "raw"}}},
		{name: "bad name", obj: &Object{Name: "a/b", Spec: map[string]any{"format": "raw"}}, wantErr: "metadata.name"},
		{name: "reserved name", obj: &Object{Name: "Maven", Spec: map[string]any{"format": "raw"}}, wantErr: "reserved"},
		{
			name:    "upstream field on hosted",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "raw", "upstream_url": "https://x.example.com"}},
			wantErr: "upstream_url is only valid for proxy",
		},
		{
			name:    "cache ttl on group",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "raw", "repo_type": "group", "cache_ttl_seconds": 5.0}},
			wantErr: "cache_ttl_seconds is only valid for proxy",
		},
		{
			name:    "members on hosted",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "raw", "member_repos": []any{"a"}}},
			wantErr: "member_repos is only valid for group",
		},
		{
			name:    "merge_proxy_versions on proxy",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "raw", "repo_type": "proxy", "merge_proxy_versions": true}},
			wantErr: "merge_proxy_versions is only valid for group",
		},
		{
			name:    "sign_packages on hosted deb",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "deb", "sign_packages": true}},
			wantErr: "sign_packages is only valid for hosted rpm",
		},
		{name: "sign_packages on hosted rpm", obj: &Object{Name: "r", Spec: map[string]any{"format": "rpm", "sign_packages": true}}},
		{
			name:    "proxy of an unsupported format",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "conan", "repo_type": "proxy"}},
			wantErr: "proxy repositories are not supported",
		},
		{
			name:    "group of an unsupported format",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "sbt", "repo_type": "group"}},
			wantErr: "group repositories are not supported",
		},
		{
			name:    "upstream url with credentials",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "npm", "repo_type": "proxy", "upstream_url": "https://u:p@x.example.com"}},
			wantErr: "plain http(s) base URL",
		},
		{
			name:    "ttl above the clamp",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "npm", "repo_type": "proxy", "cache_ttl_seconds": 99999999.0}},
			wantErr: "cache_ttl_seconds must be between",
		},
		{
			name:    "trash retention out of range",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "raw", "trash_retention_days": 40000.0}},
			wantErr: "trash_retention_days must be between",
		},
		{
			name:    "warn threshold above 100",
			obj:     &Object{Name: "r", Spec: map[string]any{"format": "raw", "quota": map[string]any{"warn_threshold_percent": 101.0}}},
			wantErr: "warn_threshold_percent",
		},
		{
			name:    "group containing itself",
			obj:     &Object{Name: "g", Spec: map[string]any{"format": "raw", "repo_type": "group", "member_repos": []any{"g"}}},
			wantErr: "cannot contain itself",
		},
		{
			name:    "duplicate member",
			obj:     &Object{Name: "g", Spec: map[string]any{"format": "raw", "repo_type": "group", "member_repos": []any{"a", "a"}}},
			wantErr: "duplicate member",
		},
		{
			name: "blank policy name and bad pattern",
			obj: &Object{Name: "r", Spec: map[string]any{"format": "raw", "cleanup_policies": []any{
				map[string]any{"name": " ", "name_pattern": "("},
			}}},
			wantErr: "name must not be blank",
		},
	}

	kind, ok := NewRepositoryKind().(Validator)
	if !ok {
		t.Fatal("Repository kind does not implement Validator")
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			errs := kind.Validate(tc.obj)
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}

				return
			}

			if joined := errors.Join(errs...); joined == nil || !strings.Contains(joined.Error(), tc.wantErr) {
				t.Fatalf("errors = %v, want one containing %q", joined, tc.wantErr)
			}
		})
	}
}
