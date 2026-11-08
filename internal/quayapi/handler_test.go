// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeAuthz allows the global manage action to the user "admin" only, like the
// real evaluator does for a token with global manage; it refuses anything that
// is not the global manage check.
type fakeAuthz struct{}

func (fakeAuthz) Evaluate(_ context.Context, p auth.Principal, repo, path, action string) (bool, error) {
	return repo == "" && path == "" && action == "manage" && p.Username == "admin", nil
}

type fakePublisher struct {
	events []string
}

func (p *fakePublisher) Dispatch(_ context.Context, repoID, eventType string, _ map[string]any) {
	p.events = append(p.events, repoID+" "+eventType)
}

type harness struct {
	t     *testing.T
	store *fakeStore
	pub   *fakePublisher
	h     http.Handler
}

func newHarness(t *testing.T, orgs ...string) *harness {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	store, pub := newFakeStore(), &fakePublisher{}

	return &harness{t: t, store: store, pub: pub, h: New(store, fakeAuthz{}, log, orgs).WithPublisher(pub)}
}

type response struct {
	status int
	body   map[string]any
}

func (r response) str(key string) string {
	s, _ := r.body[key].(string)

	return s
}

// do sends a request as the global-manage principal "admin".
func (h *harness) do(method, path string, body any) response {
	h.t.Helper()

	return h.as(&auth.Principal{Username: "admin"}, method, path, body)
}

func (h *harness) as(p *auth.Principal, method, path string, body any) response {
	h.t.Helper()

	var reader bytes.Buffer

	switch b := body.(type) {
	case nil:
	case string:
		reader.WriteString(b)
	default:
		if err := json.NewEncoder(&reader).Encode(b); err != nil {
			h.t.Fatalf("encode body: %v", err)
		}
	}

	req := httptest.NewRequestWithContext(context.Background(), method, Prefix+path, &reader)
	if p != nil {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), p))
	}

	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)

	out := response{status: rec.Code}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.body); err != nil {
			h.t.Fatalf("%s %s: response is not a JSON object: %v: %s", method, path, err, rec.Body.String())
		}
	}

	return out
}

type step struct {
	name   string
	method string
	path   string
	body   any
	status int
	check  func(t *testing.T, r response)
}

func runSteps(t *testing.T, h *harness, steps []step) {
	t.Helper()

	for _, s := range steps {
		got := h.do(s.method, s.path, s.body)
		if got.status != s.status {
			t.Fatalf("%s: %s %s = %d (%v), want %d", s.name, s.method, s.path, got.status, got.body, s.status)
		}

		if s.check != nil {
			s.check(t, got)
		}
	}
}

func wantField(key, want string) func(*testing.T, response) {
	return func(t *testing.T, r response) {
		t.Helper()

		if got := r.str(key); got != want {
			t.Errorf("%s = %q, want %q (body %v)", key, got, want, r.body)
		}
	}
}

func errorContains(want string) func(*testing.T, response) {
	return func(t *testing.T, r response) {
		t.Helper()

		for _, key := range []string{"error_message", "message"} {
			if !strings.Contains(r.str(key), want) {
				t.Errorf("%s = %q, want it to contain %q", key, r.str(key), want)
			}
		}
	}
}

// TestImageControllerSequence drives the calls the Konflux image-controller makes to
// onboard, reconcile and offboard a component.
func TestImageControllerSequence(t *testing.T) {
	h := newHarness(t)

	var token1, token2 string

	repo := map[string]any{"namespace": "konflux", "visibility": "private", "repository": "tenant/comp", "description": "d"}

	runSteps(t, h, []step{
		{name: "repository absent", method: http.MethodGet, path: "/repository/konflux/tenant/comp", status: 404},
		{
			name:   "create repository",
			method: http.MethodPost,
			path:   "/repository",
			body:   repo,
			status: 201,
			check: func(t *testing.T, r response) {
				t.Helper()

				wantField("name", "tenant/comp")(t, r)
				wantField("namespace", "konflux")(t, r)

				if r.body["is_public"] != false {
					t.Errorf("is_public = %v, want false", r.body["is_public"])
				}
			},
		},
		{
			name:   "create again",
			method: http.MethodPost,
			path:   "/repository",
			body:   repo,
			status: 400,
			check: func(t *testing.T, r response) {
				t.Helper()

				if r.str("error_message") != "Repository already exists" {
					t.Errorf("error_message = %q: the image-controller matches this text exactly", r.str("error_message"))
				}
			},
		},
		{
			name:   "repository exists",
			method: http.MethodGet,
			path:   "/repository/konflux/tenant/comp",
			status: 200,
			check:  wantField("name", "tenant/comp"),
		},
		{
			name:   "list repositories",
			method: http.MethodGet,
			path:   "/repository?namespace=konflux&last_modified=true&next_page=",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				if repos, _ := r.body["repositories"].([]any); len(repos) != 1 || r.str("next_page") != "" {
					t.Errorf("list = %v, want one repository and an empty next_page", r.body)
				}
			},
		},
		{
			name:   "robot absent",
			method: http.MethodGet,
			path:   "/organization/konflux/robots/tenant_comp",
			status: 400,
			check:  errorContains("Could not find robot with specified username"),
		},
		{
			name:   "create robot",
			method: http.MethodPut,
			path:   "/organization/konflux/robots/tenant_comp",
			body:   map[string]any{"description": "pull"},
			status: 201,
			check: func(t *testing.T, r response) {
				t.Helper()

				wantField("name", "konflux+tenant_comp")(t, r)

				if token1 = r.str("token"); !strings.HasPrefix(token1, "oa_") {
					t.Errorf("token = %q, want an OpenArtifact token", token1)
				}
			},
		},
		{
			name:   "create robot again",
			method: http.MethodPut,
			path:   "/organization/konflux/robots/tenant_comp",
			status: 400,
			check:  errorContains("Existing robot with name"),
		},
		{name: "get robot returns the same token", method: http.MethodGet, path: "/organization/konflux/robots/tenant_comp", status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				if r.str("token") != token1 {
					t.Errorf("token = %q, want the one returned at creation (%q)", r.str("token"), token1)
				}
			}},
		{name: "get robot by full name", method: http.MethodGet, path: "/organization/konflux/robots/konflux+tenant_comp", status: 200},
		{
			name:   "list robots hides tokens",
			method: http.MethodGet,
			path:   "/organization/konflux/robots",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				robots, _ := r.body["robots"].([]any)
				if len(robots) != 1 {
					t.Fatalf("robots = %v, want one", r.body)
				}

				if _, leaked := robots[0].(map[string]any)["token"]; leaked {
					t.Errorf("robot list leaks a token: %v", robots[0])
				}
			},
		},
		{
			name:   "grant read",
			method: http.MethodPut,
			path:   "/repository/konflux/tenant/comp/permissions/user/konflux+tenant_comp",
			body:   map[string]string{"role": "read"},
			status: 200,
			check:  wantField("role", "read"),
		},
		{
			name:   "grant admin is refused",
			method: http.MethodPut,
			path:   "/repository/konflux/tenant/comp/permissions/user/konflux+tenant_comp",
			body:   map[string]string{"role": "admin"},
			status: 400,
		},
		{name: "grant to unknown robot", method: http.MethodPut, path: "/repository/konflux/tenant/comp/permissions/user/konflux+ghost",
			body: map[string]string{"role": "read"}, status: 400, check: errorContains("Could not find robot")},
		{name: "upgrade to write", method: http.MethodPut, path: "/repository/konflux/tenant/comp/permissions/user/konflux+tenant_comp",
			body: map[string]string{"role": "write"}, status: 200, check: wantField("role", "write")},
		{
			name:   "list permissions",
			method: http.MethodGet,
			path:   "/repository/konflux/tenant/comp/permissions/user",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				perms, _ := r.body["permissions"].(map[string]any)

				entry, _ := perms["konflux+tenant_comp"].(map[string]any)
				if entry["role"] != "write" || entry["is_robot"] != true {
					t.Errorf("permissions = %v, want konflux+tenant_comp as a write robot (the upgrade replaces the read grant)", perms)
				}
			},
		},
		{
			name:   "regenerate",
			method: http.MethodPost,
			path:   "/organization/konflux/robots/tenant_comp/regenerate",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				if token2 = r.str("token"); token2 == "" || token2 == token1 {
					t.Errorf("regenerated token = %q, want a new one (old %q)", token2, token1)
				}
			},
		},
		{
			name:   "regenerate unknown",
			method: http.MethodPost,
			path:   "/organization/konflux/robots/ghost/regenerate",
			status: 400,
			check:  errorContains("Could not find robot"),
		},
		{name: "get robot returns the new token", method: http.MethodGet, path: "/organization/konflux/robots/tenant_comp", status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				if r.str("token") != token2 {
					t.Errorf("token = %q, want the regenerated %q", r.str("token"), token2)
				}
			}},
		{
			name:   "revoke",
			method: http.MethodDelete,
			path:   "/repository/konflux/tenant/comp/permissions/user/konflux+tenant_comp",
			status: 204,
		},
		{
			name:   "revoke again",
			method: http.MethodDelete,
			path:   "/repository/konflux/tenant/comp/permissions/user/konflux+tenant_comp",
			status: 400,
			check:  errorContains("User does not have permission for repo"),
		},
		{
			name:   "notifications empty",
			method: http.MethodGet,
			path:   "/repository/konflux/tenant/comp/notification/",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				if list, ok := r.body["notifications"].([]any); !ok || len(list) != 0 {
					t.Errorf("notifications = %v, want an empty array", r.body["notifications"])
				}
			},
		},
		{name: "delete robot", method: http.MethodDelete, path: "/organization/konflux/robots/tenant_comp", status: 204},
		{name: "delete robot again", method: http.MethodDelete, path: "/organization/konflux/robots/tenant_comp", status: 404},
		{
			name:   "robot gone",
			method: http.MethodGet,
			path:   "/organization/konflux/robots/tenant_comp",
			status: 400,
			check:  errorContains("Could not find robot"),
		},
		{name: "delete repository", method: http.MethodDelete, path: "/repository/konflux/tenant/comp", status: 204},
		{name: "delete repository again", method: http.MethodDelete, path: "/repository/konflux/tenant/comp", status: 404},
	})

	if len(h.store.targets) != 0 {
		t.Errorf("permission targets left after the repository delete: %v", h.store.targets)
	}

	if len(h.store.revokedTokens) != 1 {
		t.Errorf("revoked tokens = %v, want the one replaced by the regenerate", h.store.revokedTokens)
	}
}

func TestRepositoryTargetsScopeOneImage(t *testing.T) {
	h := newHarness(t)

	got := h.do(http.MethodPost, "/repository", map[string]string{"namespace": "konflux", "visibility": "private", "repository": "a.b/c-d"})
	if got.status != http.StatusCreated {
		t.Fatalf("create = %d (%v)", got.status, got.body)
	}

	byName := map[string]models.PermissionTarget{}
	for _, tg := range h.store.targets {
		byName[tg.Name[strings.LastIndex(tg.Name, "-")+1:]] = tg
	}

	read, write := byName["read"], byName["write"]
	if read.RepositoryFilter != "konflux" || read.PathPattern != `a\.b/c-d/.*` {
		t.Errorf("read target = %+v, want it scoped to konflux and the image path", read)
	}

	if strings.Join(read.Actions, ",") != "read" || strings.Join(write.Actions, ",") != "read,write" {
		t.Errorf("actions: read=%v write=%v, want read / read,write: a robot must never get delete", read.Actions, write.Actions)
	}
}

func TestRepositoryCreationRules(t *testing.T) {
	tests := []struct {
		name   string
		orgs   []string
		body   map[string]string
		status int
		want   string
	}{
		{name: "ok nested", body: map[string]string{"namespace": "konflux", "visibility": "private", "repository": "a/b/c"}, status: 201},
		{
			name:   "unknown organization",
			body:   map[string]string{"namespace": "nope", "visibility": "private", "repository": "a"},
			status: 404,
		},
		{
			name:   "not a docker repository",
			body:   map[string]string{"namespace": "npm-repo", "visibility": "private", "repository": "a"},
			status: 404,
		},
		{name: "proxy repository", body: map[string]string{"namespace": "proxy", "visibility": "private", "repository": "a"}, status: 404},
		{
			name:   "organization not allowed",
			orgs:   []string{"open"},
			body:   map[string]string{"namespace": "konflux", "visibility": "private", "repository": "a"},
			status: 404,
		},
		{
			name:   "organization allowed",
			orgs:   []string{"konflux"},
			body:   map[string]string{"namespace": "konflux", "visibility": "private", "repository": "a"},
			status: 201,
		},
		{
			name:   "public request on private organization",
			body:   map[string]string{"namespace": "konflux", "visibility": "public", "repository": "a"},
			status: 400,
			want:   "per organization",
		},
		{
			name:   "private request on public organization",
			body:   map[string]string{"namespace": "open", "visibility": "private", "repository": "a"},
			status: 400,
			want:   "per organization",
		},
		{
			name:   "public on public organization",
			body:   map[string]string{"namespace": "open", "visibility": "public", "repository": "a"},
			status: 201,
		},
		{name: "bad visibility", body: map[string]string{"namespace": "konflux", "visibility": "x", "repository": "a"}, status: 400},
		{name: "uppercase name", body: map[string]string{"namespace": "konflux", "visibility": "private", "repository": "A"}, status: 400},
		{
			name:   "reserved segment",
			body:   map[string]string{"namespace": "konflux", "visibility": "private", "repository": "a/tag/b"},
			status: 400,
			want:   "path segment",
		},
		{name: "empty name", body: map[string]string{"namespace": "konflux", "visibility": "private", "repository": ""}, status: 400},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.orgs...)

			got := h.do(http.MethodPost, "/repository", tc.body)
			if got.status != tc.status {
				t.Fatalf("status = %d (%v), want %d", got.status, got.body, tc.status)
			}

			if tc.want != "" && !strings.Contains(got.str("error_message"), tc.want) {
				t.Errorf("error_message = %q, want it to contain %q", got.str("error_message"), tc.want)
			}
		})
	}
}

func TestChangeVisibility(t *testing.T) {
	h := newHarness(t)

	runSteps(t, h, []step{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/repository",
			body:   map[string]string{"namespace": "konflux", "visibility": "private", "repository": "a"},
			status: 201,
		},
		{
			name:   "same visibility",
			method: http.MethodPost,
			path:   "/repository/konflux/a/changevisibility",
			body:   map[string]string{"visibility": "private"},
			status: 200,
		},
		{
			name:   "other visibility",
			method: http.MethodPost,
			path:   "/repository/konflux/a/changevisibility",
			body:   map[string]string{"visibility": "public"},
			status: 400,
		},
		{
			name:   "invalid visibility",
			method: http.MethodPost,
			path:   "/repository/konflux/a/changevisibility",
			body:   map[string]string{"visibility": "x"},
			status: 400,
		},
	})
}

func TestAuthorization(t *testing.T) {
	tests := []struct {
		name      string
		principal *auth.Principal
		status    int
	}{
		{name: "no principal", principal: nil, status: http.StatusUnauthorized},
		{name: "anonymous", principal: &auth.Principal{IsAnonymous: true}, status: http.StatusUnauthorized},
		{name: "user without global manage", principal: &auth.Principal{Username: "alice"}, status: http.StatusForbidden},
		{
			name:      "robot deploy token",
			principal: &auth.Principal{Username: "quay-robot-1", TokenKind: auth.TokenKindDeploy},
			status:    http.StatusForbidden,
		},
		{name: "global manage", principal: &auth.Principal{Username: "admin"}, status: http.StatusOK},
	}

	paths := []struct{ method, path string }{
		{http.MethodGet, "/repository?namespace=konflux"},
		{http.MethodPost, "/repository"},
		{http.MethodGet, "/organization/konflux/robots"},
		{http.MethodPut, "/organization/konflux/robots/x"},
		{http.MethodGet, "/repository/konflux/a/b"},
		{http.MethodDelete, "/organization/konflux/team/t"},
	}

	for _, tc := range tests {
		for _, p := range paths {
			t.Run(tc.name+" "+p.method+" "+p.path, func(t *testing.T) {
				h := newHarness(t)

				got := h.as(tc.principal, p.method, p.path, nil)
				if tc.status != http.StatusOK {
					if got.status != tc.status {
						t.Fatalf("status = %d, want %d", got.status, tc.status)
					}

					if got.str("error_message") == "" {
						t.Errorf("body %v has no error_message", got.body)
					}

					return
				}

				if got.status == http.StatusUnauthorized || got.status == http.StatusForbidden {
					t.Fatalf("a global manage principal was refused: %d", got.status)
				}
			})
		}
	}
}

func TestRoutingErrors(t *testing.T) {
	tests := []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "", 404},
		{http.MethodGet, "/", 404},
		{http.MethodGet, "/unknown", 404},
		{http.MethodDelete, "/repository", 405},
		{http.MethodPatch, "/repository/konflux/a", 404},
		{http.MethodPut, "/organization/konflux/robots", 405},
		{http.MethodGet, "/organization/konflux/robots/a/b", 404},
		{http.MethodGet, "/repository/konflux", 404},
		{http.MethodGet, "/repository/konflux/a/permissions", 404},
		{http.MethodGet, "/organization/konflux/team/t", 405},
		{http.MethodGet, "/organization/konflux/other", 404},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			h := newHarness(t)

			if got := h.do(tc.method, tc.path, nil); got.status != tc.status {
				t.Errorf("status = %d (%v), want %d", got.status, got.body, tc.status)
			}
		})
	}
}

func TestSplitRepositoryPath(t *testing.T) {
	tests := []struct {
		in       string
		wantName string
		wantSub  string
	}{
		{"a", "a", ""},
		{"ns/comp", "ns/comp", ""},
		{"ns/comp/tag", "ns/comp", "tag"},
		{"ns/comp/tag/v1", "ns/comp", "tag/v1"},
		{"ns/comp/permissions/user/x", "ns/comp", "permissions/user/x"},
		{"ns/comp/notification/id", "ns/comp", "notification/id"},
		{"ns/comp/changevisibility", "ns/comp", "changevisibility"},
		{"tag/comp", "tag/comp", ""},
		{"tag", "tag", ""},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			name, sub := splitRepositoryPath(strings.Split(tc.in, "/"))
			if name != tc.wantName || strings.Join(sub, "/") != tc.wantSub {
				t.Errorf("split(%q) = %q, %q; want %q, %q", tc.in, name, strings.Join(sub, "/"), tc.wantName, tc.wantSub)
			}
		})
	}
}

func TestValidateRepositoryName(t *testing.T) {
	tests := []struct {
		name    string
		invalid bool
	}{
		{"a", false}, {"ns/comp", false}, {"a.b_c-d/e", false}, {"a/b/c/d", false},
		{"", true}, {"A", true}, {"a//b", true}, {"/a", true}, {"a/", true}, {"a b", true}, {"-a", true}, {"a/permissions", true},
		{strings.Repeat("a", 256), true},
	}

	for _, tc := range tests {
		if got := validateRepositoryName(tc.name) != ""; got != tc.invalid {
			t.Errorf("validateRepositoryName(%q) rejected = %v, want %v", tc.name, got, tc.invalid)
		}
	}
}

func TestTags(t *testing.T) {
	h := newHarness(t)
	now := time.Unix(1_700_000_000, 0)

	h.do(http.MethodPost, "/repository", map[string]string{"namespace": "konflux", "visibility": "private", "repository": "ns/comp"})
	h.store.tags["org-1/ns/comp"] = []*models.DockerTag{
		{ID: "t1", Name: "v1", ManifestDigest: "sha256:aaa", UpdatedAt: now},
		{ID: "t2", Name: "v2", ManifestDigest: "sha256:bbb", UpdatedAt: now},
		{ID: "t3", Name: "v3", ManifestDigest: "sha256:ccc", UpdatedAt: now},
	}

	runSteps(t, h, []step{
		{
			name:   "page 1",
			method: http.MethodGet,
			path:   "/repository/konflux/ns/comp/tag/?page=1&limit=2",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				tags, _ := r.body["tags"].([]any)
				if len(tags) != 2 || r.body["has_additional"] != true || r.body["page"] != float64(1) {
					t.Errorf("page 1 = %v, want two tags and has_additional", r.body)
				}

				first, _ := tags[0].(map[string]any)
				if first["name"] != "v1" || first["manifest_digest"] != "sha256:aaa" || first["start_ts"] != float64(now.Unix()) {
					t.Errorf("first tag = %v", first)
				}

				if _, ok := first["trust_enabled"]; ok {
					t.Error("trust_enabled must not be emitted: the image-controller decodes it as a string")
				}
			},
		},
		{
			name:   "page 2",
			method: http.MethodGet,
			path:   "/repository/konflux/ns/comp/tag/?page=2&limit=2",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				if tags, _ := r.body["tags"].([]any); len(tags) != 1 || r.body["has_additional"] != false {
					t.Errorf("page 2 = %v, want one tag and no more", r.body)
				}
			},
		},
		{name: "default paging without a trailing slash", method: http.MethodGet, path: "/repository/konflux/ns/comp/tag", status: 200},
		{name: "delete", method: http.MethodDelete, path: "/repository/konflux/ns/comp/tag/v2", status: 204},
		{name: "delete again", method: http.MethodDelete, path: "/repository/konflux/ns/comp/tag/v2", status: 404},
	})

	if len(h.store.deletedTags) != 1 || h.store.deletedTags[0] != "v2" {
		t.Errorf("deleted tags = %v, want [v2]", h.store.deletedTags)
	}

	if len(h.pub.events) != 1 || h.pub.events[0] != "org-1 artifact.deleted" {
		t.Errorf("webhook events = %v, want one artifact.deleted for the organization", h.pub.events)
	}
}

func TestTagDeleteHonoursImmutableReleases(t *testing.T) {
	h := newHarness(t)

	h.do(http.MethodPost, "/repository", map[string]string{"namespace": "locked", "visibility": "private", "repository": "ns/comp"})
	h.store.tags["org-5/ns/comp"] = []*models.DockerTag{{ID: "t1", Name: "v1", ManifestDigest: "sha256:aaa"}}

	got := h.do(http.MethodDelete, "/repository/locked/ns/comp/tag/v1", nil)
	if got.status != http.StatusConflict {
		t.Fatalf("status = %d (%v), want 409", got.status, got.body)
	}

	if len(h.store.deletedTags) != 0 || len(h.pub.events) != 0 {
		t.Errorf("a refused delete removed the tag or emitted an event: %v %v", h.store.deletedTags, h.pub.events)
	}
}

func TestTeams(t *testing.T) {
	h := newHarness(t)

	runSteps(t, h, []step{
		{
			name:   "create repository",
			method: http.MethodPost,
			path:   "/repository",
			body:   map[string]string{"namespace": "konflux", "visibility": "private", "repository": "ns/comp"},
			status: 201,
		},
		{name: "members of absent team", method: http.MethodGet, path: "/organization/konflux/team/devs/members", status: 404},
		{
			name:   "team permission for absent team",
			method: http.MethodPut,
			path:   "/repository/konflux/ns/comp/permissions/team/devs",
			body:   map[string]string{"role": "read"},
			status: 404,
		},
		{
			name:   "create team",
			method: http.MethodPut,
			path:   "/organization/konflux/team/devs",
			body:   map[string]string{"role": "member"},
			status: 200,
			check:  wantField("name", "devs"),
		},
		{
			name:   "create team again",
			method: http.MethodPut,
			path:   "/organization/konflux/team/devs",
			body:   map[string]string{"role": "member"},
			status: 200,
		},
		{name: "invalid team name", method: http.MethodPut, path: "/organization/konflux/team/Bad-Name", status: 400},
		{name: "add unknown user", method: http.MethodPut, path: "/organization/konflux/team/devs/members/ghost", status: 400},
		{name: "add member", method: http.MethodPut, path: "/organization/konflux/team/devs/members/alice", status: 200},
		{name: "add member again", method: http.MethodPut, path: "/organization/konflux/team/devs/members/alice", status: 200},
		{
			name:   "list members",
			method: http.MethodGet,
			path:   "/organization/konflux/team/devs/members",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				members, _ := r.body["members"].([]any)
				if len(members) != 1 || members[0].(map[string]any)["name"] != "alice" {
					t.Errorf("members = %v, want alice once", r.body["members"])
				}
			},
		},
		{
			name:   "grant team write",
			method: http.MethodPut,
			path:   "/repository/konflux/ns/comp/permissions/team/devs",
			body:   map[string]string{"role": "write"},
			status: 200,
			check:  wantField("role", "write"),
		},
		{
			name:   "team permissions",
			method: http.MethodGet,
			path:   "/organization/konflux/team/devs/permissions",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				perms, _ := r.body["permissions"].([]any)
				if len(perms) != 1 || perms[0].(map[string]any)["role"] != "write" {
					t.Errorf("permissions = %v, want one write grant", r.body["permissions"])
				}
			},
		},
		{name: "remove member", method: http.MethodDelete, path: "/organization/konflux/team/devs/members/alice", status: 204},
		{
			name:   "remove member of absent team",
			method: http.MethodDelete,
			path:   "/organization/konflux/team/none/members/alice",
			status: 404,
		},
		{
			name:   "grant user permission",
			method: http.MethodPut,
			path:   "/repository/konflux/ns/comp/permissions/user/alice",
			body:   map[string]string{"role": "read"},
			status: 200,
			check:  wantField("name", "alice"),
		},
		{name: "delete team", method: http.MethodDelete, path: "/organization/konflux/team/devs", status: 204},
		{name: "delete team again", method: http.MethodDelete, path: "/organization/konflux/team/devs", status: 404},
	})
}

func TestNotifications(t *testing.T) {
	h := newHarness(t)

	var id string

	body := map[string]any{
		"title": "push", "event": "repo_push", "method": "webhook",
		"config": map[string]string{"url": "https://example.test/hook"}, "eventConfig": map[string]string{},
	}

	runSteps(t, h, []step{
		{
			name:   "create repository",
			method: http.MethodPost,
			path:   "/repository",
			body:   map[string]string{"namespace": "konflux", "visibility": "private", "repository": "ns/comp"},
			status: 201,
		},
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/repository/konflux/ns/comp/notification/",
			body:   body,
			status: 201,
			check: func(t *testing.T, r response) {
				t.Helper()

				if id = r.str("uuid"); id == "" {
					t.Errorf("no uuid in %v", r.body)
				}
			},
		},
		{
			name:   "missing event",
			method: http.MethodPost,
			path:   "/repository/konflux/ns/comp/notification/",
			body:   map[string]any{"title": "x", "method": "webhook"},
			status: 400,
		},
		{name: "config not an object", method: http.MethodPost, path: "/repository/konflux/ns/comp/notification/",
			body: `{"event":"repo_push","method":"webhook","config":"x"}`, status: 400},
		{name: "invalid JSON", method: http.MethodPost, path: "/repository/konflux/ns/comp/notification/", body: `{`, status: 400},
		{
			name:   "list",
			method: http.MethodGet,
			path:   "/repository/konflux/ns/comp/notification/",
			status: 200,
			check: func(t *testing.T, r response) {
				t.Helper()

				list, _ := r.body["notifications"].([]any)
				if len(list) != 1 {
					t.Fatalf("notifications = %v, want one", r.body)
				}

				n := list[0].(map[string]any)
				if n["uuid"] != id || n["event"] != "repo_push" || n["config"].(map[string]any)["url"] != "https://example.test/hook" {
					t.Errorf("notification = %v", n)
				}
			},
		},
	})

	runSteps(t, h, []step{
		{
			name:   "get",
			method: http.MethodGet,
			path:   "/repository/konflux/ns/comp/notification/" + id,
			status: 200,
			check:  wantField("title", "push"),
		},
		{
			name:   "update",
			method: http.MethodPut,
			path:   "/repository/konflux/ns/comp/notification/" + id,
			status: 200,
			body: map[string]any{
				"title":        "renamed",
				"event":        "repo_push",
				"method":       "webhook",
				"event_config": map[string]string{"k": "v"},
			},
			check: wantField("title", "renamed"),
		},
		{name: "update absent", method: http.MethodPut, path: "/repository/konflux/ns/comp/notification/nope", status: 404,
			body: map[string]any{"title": "x", "event": "repo_push", "method": "webhook"}},
		{name: "get absent", method: http.MethodGet, path: "/repository/konflux/ns/comp/notification/nope", status: 404},
		{name: "delete", method: http.MethodDelete, path: "/repository/konflux/ns/comp/notification/" + id, status: 204},
		{name: "delete again", method: http.MethodDelete, path: "/repository/konflux/ns/comp/notification/" + id, status: 404},
	})
}

func TestUserPermissionRules(t *testing.T) {
	h := newHarness(t)

	runSteps(t, h, []step{
		{
			name:   "create repository",
			method: http.MethodPost,
			path:   "/repository",
			body:   map[string]string{"namespace": "konflux", "visibility": "private", "repository": "ns/comp"},
			status: 201,
		},
		{
			name:   "robot of another organization",
			method: http.MethodPut,
			path:   "/repository/konflux/ns/comp/permissions/user/open+r",
			body:   map[string]string{"role": "read"},
			status: 400,
		},
		{
			name:   "unknown user",
			method: http.MethodPut,
			path:   "/repository/konflux/ns/comp/permissions/user/ghost",
			body:   map[string]string{"role": "read"},
			status: 400,
		},
		{name: "invalid body", method: http.MethodPut, path: "/repository/konflux/ns/comp/permissions/user/alice", body: `{`, status: 400},
		{name: "revoke unknown user", method: http.MethodDelete, path: "/repository/konflux/ns/comp/permissions/user/ghost", status: 404},
		{name: "revoke without grant", method: http.MethodDelete, path: "/repository/konflux/ns/comp/permissions/user/alice", status: 400},
		{
			name:   "organization robot name for another organization",
			method: http.MethodPut,
			path:   "/organization/konflux/robots/open+r",
			status: 400,
		},
		{name: "invalid robot name", method: http.MethodPut, path: "/organization/konflux/robots/Bad_Name", status: 400},
	})
}

func TestRobotBackingAccountNameFitsUsernameLimit(t *testing.T) {
	h := newHarness(t)

	long := strings.Repeat("a", 200)
	if got := h.do(http.MethodPut, "/organization/konflux/robots/"+long, nil); got.status != http.StatusCreated {
		t.Fatalf("create = %d (%v)", got.status, got.body)
	}

	for username := range h.store.users {
		if strings.HasPrefix(username, robotUserPrefix) && len(username) > 64 {
			t.Errorf("service account username %q exceeds the 64 character limit", username)
		}
	}
}
