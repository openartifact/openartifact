// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/pkg/logger"
)

// TestMain lets the test binary double as the CLI: contract tests re-exec it
// with OA_AS_CLI=1 so that os.Exit paths and flag parsing run for real.
func TestMain(m *testing.M) {
	if os.Getenv("OA_AS_CLI") == "1" {
		main()
		os.Exit(0)
	}

	os.Exit(m.Run())
}

// realRouter registers every REST handler the server mounts with nil
// dependencies. Handlers are never invoked: the router only decides whether a
// method+path pair exists, which is exactly what a CLI route bug breaks.
func realRouter() chi.Router {
	r := chi.NewRouter()
	const repoPrefix = "/api/v1/repositories/{name}"

	api.NewAuthHandler(nil, nil, nil, nil).RegisterPublic(r)
	api.NewAuthHandler(nil, nil, nil, nil).RegisterAuthed(r)
	sys := api.NewSystemInfoHandler("", "", "", nil, nil, "")
	sys.RegisterPublic(r)
	sys.RegisterAuthed(r)
	api.NewAdminUsersHandler(nil, nil, nil).Register(r)
	api.NewAdminGroupsHandler(nil, nil, nil).Register(r)
	api.NewAdminTokensHandler(nil, nil, nil).Register(r)
	api.NewSelfTokensHandler(nil, nil, nil).Register(r)
	api.NewAdminAuditHandler(nil, nil, nil).Register(r)
	api.NewAdminStorageHandler(nil, nil, nil).Register(r)
	api.NewAdminGPGHandler(nil, nil, nil).Register(r)
	api.NewProjectsHandler(nil, nil, nil, nil).RegisterRoutes(r)
	api.NewRepositoriesHandler(nil, nil, nil).RegisterAuthed(r)
	api.NewCleanupHandler(nil, nil, nil, nil).RegisterAuthed(r)
	api.NewSBOMHandler(nil, nil, nil, nil, nil).RegisterAuthed(r)
	api.NewVEXHandler(nil, nil, nil, nil).RegisterAuthed(r)
	api.NewWebhooksHandler(nil, nil, nil, nil).RegisterAuthed(r)
	api.NewBackupHandler(nil, nil, nil, nil).RegisterAuthed(r)
	api.NewLicenseHandler(nil, nil, nil, nil).RegisterRoutes(r)
	api.NewSecretsHandler(nil, nil, nil).RegisterRoutes(r, repoPrefix)
	api.NewVulnHandler(nil, nil, nil, nil).RegisterRoutes(r, repoPrefix)
	api.NewQuotasHandler(nil, nil, nil).RegisterRoutes(r, repoPrefix)
	api.NewPropertiesHandler(nil, nil).RegisterRoutes(r, repoPrefix)
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		panic(err)
	}

	api.NewDebUploadHandler(log, nil, nil, nil).Register(r)
	api.NewRPMUploadHandler(log, nil, nil, nil).Register(r)
	api.NewDeltaHandler(log, nil, nil).Register(r)
	api.NewPresignedUploadHandler(log, nil, nil, nil, 0, 0, nil).Register(r)

	registerTrashRoutes(r)

	// registered by internal/search
	r.Get("/api/v1/search", func(http.ResponseWriter, *http.Request) {})

	return r
}

type reply struct {
	status  int
	body    string
	headers map[string]string
}

func ok(body string) reply { return reply{status: http.StatusOK, body: body} }

type recorded struct {
	method  string
	pattern string
	query   url.Values
	body    []byte
}

type contractServer struct {
	t       *testing.T
	router  chi.Router
	replies map[string]reply

	mu   sync.Mutex
	reqs []recorded
	bad  []string
}

func newContractServer(t *testing.T, replies map[string]reply) (*contractServer, *httptest.Server) {
	t.Helper()

	cs := &contractServer{t: t, router: realRouter(), replies: replies}
	srv := httptest.NewServer(cs)
	t.Cleanup(srv.Close)

	return cs, srv
}

func (cs *contractServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	rctx := chi.NewRouteContext()
	if !cs.router.Match(rctx, r.Method, r.URL.Path) {
		cs.mu.Lock()
		cs.bad = append(cs.bad, r.Method+" "+r.URL.Path)
		cs.mu.Unlock()
		http.Error(w, `{"error":"no such route"}`, http.StatusNotFound)

		return
	}

	pattern := rctx.RoutePattern()

	cs.mu.Lock()
	cs.reqs = append(cs.reqs, recorded{r.Method, pattern, r.URL.Query(), body})
	cs.mu.Unlock()

	rep, found := cs.replies[r.Method+" "+pattern]
	if !found {
		cs.mu.Lock()
		cs.bad = append(cs.bad, "no canned reply for "+r.Method+" "+pattern)
		cs.mu.Unlock()
		http.Error(w, `{"error":"no canned reply"}`, http.StatusInternalServerError)

		return
	}

	for k, v := range rep.headers {
		w.Header().Set(k, v)
	}

	if rep.status == 0 {
		rep.status = http.StatusOK
	}

	if strings.HasPrefix(rep.body, "{") || strings.HasPrefix(rep.body, "[") {
		w.Header().Set("Content-Type", "application/json")
	}

	w.WriteHeader(rep.status)
	_, _ = w.Write([]byte(rep.body))
}

type result struct {
	stdout, stderr string
	code           int
}

// runOA runs the CLI (this test binary re-exec'd) against srv.
func runOA(t *testing.T, srv *httptest.Server, stdin string, args ...string) result {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	full := append([]string{"--server", srv.URL, "--token", "t"}, args...)
	cmd := exec.CommandContext(context.Background(), exe, full...)
	cmd.Env = append(os.Environ(), "OA_AS_CLI=1", "HOME="+t.TempDir(), "OA_SERVER=", "OA_TOKEN=")
	cmd.Stdin = strings.NewReader(stdin)

	var so, se bytes.Buffer

	cmd.Stdout, cmd.Stderr = &so, &se

	code := 0

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}

		code = ee.ExitCode()
	}

	return result{so.String(), se.String(), code}
}

// call is one expected request. body, when non-nil, must equal the decoded
// JSON body exactly (so a wrong or extra field name fails). query entries must
// be present with that value.
type call struct {
	method, pattern string
	body            map[string]any
	query           map[string]string
	bodyHas         string
}

type contractCase struct {
	name    string
	args    []string
	stdin   string
	replies map[string]reply
	calls   []call
	out     string // substring of stdout
	fail    string // non-empty: CLI must exit non-zero with this in stderr
}

func (c contractCase) run(t *testing.T) {
	t.Helper()

	cs, srv := newContractServer(t, c.replies)
	res := runOA(t, srv, c.stdin, c.args...)

	if len(cs.bad) > 0 {
		t.Errorf("CLI hit routes the server does not serve: %v", cs.bad)
	}

	if c.fail != "" {
		if res.code == 0 || !strings.Contains(res.stderr, c.fail) {
			t.Errorf("want failure containing %q, got code=%d stderr=%q", c.fail, res.code, res.stderr)
		}
	} else if res.code != 0 {
		t.Fatalf("exit %d, stderr=%q stdout=%q", res.code, res.stderr, res.stdout)
	}

	if !strings.Contains(res.stdout, c.out) {
		t.Errorf("stdout %q lacks %q", res.stdout, c.out)
	}

	if len(cs.reqs) != len(c.calls) {
		t.Fatalf("requests = %d, want %d: %+v", len(cs.reqs), len(c.calls), cs.reqs)
	}

	for i, want := range c.calls {
		got := cs.reqs[i]
		if got.method != want.method || got.pattern != want.pattern {
			t.Errorf("request %d = %s %s, want %s %s", i, got.method, got.pattern, want.method, want.pattern)
			continue
		}

		for k, v := range want.query {
			if got.query.Get(k) != v {
				t.Errorf("request %d query %s = %q, want %q", i, k, got.query.Get(k), v)
			}
		}

		if want.bodyHas != "" && !bytes.Contains(got.body, []byte(want.bodyHas)) {
			t.Errorf("request %d body lacks %q", i, want.bodyHas)
		}

		if want.body != nil {
			var have map[string]any

			if err := json.Unmarshal(got.body, &have); err != nil {
				t.Errorf("request %d body not JSON: %q", i, got.body)
				continue
			}

			if !reflect.DeepEqual(have, want.body) {
				t.Errorf("request %d body = %v, want %v", i, have, want.body)
			}
		}
	}
}

func runCases(t *testing.T, cases []contractCase) {
	t.Helper()

	for _, c := range cases {
		t.Run(c.name, c.run)
	}
}

type m = map[string]any

func TestContractRepo(t *testing.T) {
	const one = `{"name":"r","format":"npm"}`

	runCases(t, []contractCase{
		{
			name:    "list pages with limit",
			args:    []string{"repo", "list"},
			replies: map[string]reply{"GET /api/v1/repositories": ok(`{"repositories":[{"name":"r","format":"npm"}],"total":1}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories", query: map[string]string{"limit": "100", "page": "1"}}},
			out:     "r",
		},
		{
			name:    "create",
			args:    []string{"repo", "create", "r", "--format", "npm", "--description", "d", "--public"},
			replies: map[string]reply{"POST /api/v1/repositories": {status: 201, body: one}},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories", body: m{"name": "r", "format": "npm", "description": "d", "public": true}}},
		},
		{
			name:    "show uses singular path",
			args:    []string{"repo", "show", "r"},
			replies: map[string]reply{"GET /api/v1/repository/{name}": ok(one)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repository/{name}"}},
			out:     "npm",
		},
		{
			name:    "update sends only set flags",
			args:    []string{"repo", "update", "r", "--public=false"},
			replies: map[string]reply{"PATCH /api/v1/repository/{name}": ok(one)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/repository/{name}", body: m{"public": false}}},
		},
		{
			name: "update with nothing fails", args: []string{"repo", "update", "r"}, fail: "nothing to update",
		},
		{
			name:    "delete sends confirm",
			args:    []string{"repo", "delete", "r", "--confirm", "r"},
			replies: map[string]reply{"DELETE /api/v1/repository/{name}": {status: 204}},
			calls:   []call{{method: "DELETE", pattern: "/api/v1/repository/{name}", query: map[string]string{"confirm": "r"}}},
		},
		{
			name: "delete without confirm fails non-interactively", args: []string{"repo", "delete", "r"}, fail: "--confirm r",
		},
		{
			name: "delete with wrong confirm fails locally", args: []string{"repo", "delete", "r", "--confirm", "x"}, fail: "does not match",
		},
		{
			name:    "create with trash settings",
			args:    []string{"repo", "create", "r", "--format", "npm", "--trash-retention-days", "0", "--immutable-releases"},
			replies: map[string]reply{"POST /api/v1/repositories": {status: 201, body: one}},
			calls: []call{{method: "POST", pattern: "/api/v1/repositories", body: m{
				"name": "r", "format": "npm", "description": "", "public": false, "trash_retention_days": float64(0), "immutable_releases": true,
			}}},
		},
		{
			name:    "update trash settings",
			args:    []string{"repo", "update", "r", "--trash-retention-days", "14", "--immutable-releases=false"},
			replies: map[string]reply{"PATCH /api/v1/repository/{name}": ok(one)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/repository/{name}", body: m{"trash_retention_days": float64(14), "immutable_releases": false}}},
		},
		{
			name:    "update merge proxy versions",
			args:    []string{"repo", "update", "g", "--merge-proxy-versions"},
			replies: map[string]reply{"PATCH /api/v1/repository/{name}": ok(one)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/repository/{name}", body: m{"merge_proxy_versions": true}}},
		},
		{
			name:    "update retention back to default sends null",
			args:    []string{"repo", "update", "r", "--trash-retention-days", "default"},
			replies: map[string]reply{"PATCH /api/v1/repository/{name}": ok(one)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/repository/{name}", body: m{"trash_retention_days": nil}}},
		},
		{
			name: "update with bad retention fails", args: []string{"repo", "update", "r", "--trash-retention-days", "-1"}, fail: "non-negative",
		},
		{
			name:    "packages",
			args:    []string{"repo", "packages", "r"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/packages": ok(`{"packages":[{"id":"p1","name":"left-pad","version":"1.0.0"}],"total":1}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/packages"}},
			out:     "left-pad",
		},
		{
			name:    "members",
			args:    []string{"repo", "members", "g"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/members": ok(`{"members":[{"id":"1","name":"a","format":"npm","repo_type":"hosted","priority":1}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/members"}},
			out:     "hosted",
		},
	})
}

func TestContractArtifact(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "a.tgz")
	deb := filepath.Join(dir, "a.deb")

	for _, p := range []string{tgz, deb} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runCases(t, []contractCase{
		{
			name:    "upload package",
			args:    []string{"artifact", "upload", "r", tgz},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/packages": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/packages", bodyHas: `name="file"`}},
		},
		{
			name:    "upload deb",
			args:    []string{"artifact", "upload", "r", deb},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/debs": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/debs"}},
		},
		{
			name: "delete package",
			args: []string{"artifact", "delete", "r", "p1"},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}":                    ok(`{"format":"rpm"}`),
				"DELETE /api/v1/repositories/{name}/packages/{id}": {status: 204},
			},
			calls: []call{{method: "GET", pattern: "/api/v1/repository/{name}"}, {method: "DELETE", pattern: "/api/v1/repositories/{name}/packages/{id}"}},
		},
		{
			name: "delete deb",
			args: []string{"artifact", "delete", "r", "p1"},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}":                ok(`{"format":"deb"}`),
				"DELETE /api/v1/repositories/{name}/debs/{id}": {status: 204},
			},
			calls: []call{{method: "GET", pattern: "/api/v1/repository/{name}"}, {method: "DELETE", pattern: "/api/v1/repositories/{name}/debs/{id}"}},
		},
		{
			name:    "search",
			args:    []string{"search", "left"},
			replies: map[string]reply{"GET /api/v1/search": ok(`{"packages":[],"repositories":[]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/search", query: map[string]string{"q": "left"}}},
		},
	})
}

func TestContractAuthSession(t *testing.T) {
	login := reply{status: 200, body: `{"must_change_password":false}`, headers: map[string]string{"Set-Cookie": "oa_csrf=c1; Path=/"}}

	runCases(t, []contractCase{
		{
			name:    "token list",
			args:    []string{"auth", "token", "list"},
			replies: map[string]reply{"GET /api/v1/auth/tokens": ok(`{"data":[{"id":"t1","name":"n","kind":"access"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/auth/tokens"}},
			out:     "t1",
		},
		{
			name:  "token create signs in first",
			args:  []string{"auth", "token", "create", "--name", "ci", "--username", "admin", "--password-stdin"},
			stdin: "pw\n",
			replies: map[string]reply{
				"POST /api/v1/auth/login":  login,
				"POST /api/v1/auth/tokens": {status: 201, body: `{"token":"oa_x"}`},
			},
			calls: []call{
				{method: "POST", pattern: "/api/v1/auth/login", body: m{"username": "admin", "password": "pw"}},
				{method: "POST", pattern: "/api/v1/auth/tokens", body: m{"name": "ci", "kind": "access"}},
			},
			out: "oa_x",
		},
		{
			name:  "token revoke signs in first",
			args:  []string{"auth", "token", "revoke", "t1", "--username", "admin", "--password-stdin"},
			stdin: "pw\n",
			replies: map[string]reply{
				"POST /api/v1/auth/login":         login,
				"DELETE /api/v1/auth/tokens/{id}": {status: 204},
			},
			calls: []call{
				{method: "POST", pattern: "/api/v1/auth/login"},
				{method: "DELETE", pattern: "/api/v1/auth/tokens/{id}"},
			},
		},
		{
			name:  "passwd signs in first",
			args:  []string{"auth", "passwd", "--password-stdin"},
			stdin: "old\nnew-password-123\n",
			replies: map[string]reply{
				"GET /api/v1/auth/me":               ok(`{"username":"admin"}`),
				"POST /api/v1/auth/login":           login,
				"POST /api/v1/auth/change-password": {status: 204},
			},
			calls: []call{
				{method: "GET", pattern: "/api/v1/auth/me"},
				{method: "POST", pattern: "/api/v1/auth/login", body: m{"username": "admin", "password": "old"}},
				{method: "POST", pattern: "/api/v1/auth/change-password", body: m{"current_password": "old", "new_password": "new-password-123"}},
			},
		},
	})
}

func TestContractUserGroupOrgToken(t *testing.T) {
	runCases(t, []contractCase{
		{
			name:    "user list",
			args:    []string{"user", "list"},
			replies: map[string]reply{"GET /api/v1/users": ok(`{"data":[{"id":"u1","username":"bob","email":"b@x"}],"total":1}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/users", query: map[string]string{"per_page": "200"}}},
			out:     "bob",
		},
		{
			name:    "user create",
			args:    []string{"user", "create", "--username", "bob", "--email", "b@x", "--full-name", "Bob", "--password-stdin"},
			stdin:   "secret-pass-1\n",
			replies: map[string]reply{"POST /api/v1/users": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/users", body: m{"username": "bob", "email": "b@x", "full_name": "Bob", "password": "secret-pass-1"}}},
		},
		{
			name:    "user update",
			args:    []string{"user", "update", "u1", "--disabled=false", "--email", "n@x"},
			replies: map[string]reply{"PATCH /api/v1/users/{id}": ok(`{}`)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/users/{id}", body: m{"disabled": false, "email": "n@x"}}},
		},
		{
			name:    "user delete",
			args:    []string{"user", "delete", "u1"},
			replies: map[string]reply{"DELETE /api/v1/users/{id}": {status: 204}},
			calls:   []call{{method: "DELETE", pattern: "/api/v1/users/{id}"}},
		},
		{
			name:    "group create",
			args:    []string{"group", "create", "devs", "--description", "d"},
			replies: map[string]reply{"POST /api/v1/groups": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/groups", body: m{"name": "devs", "description": "d"}}},
		},
		{
			name:    "group list",
			args:    []string{"group", "list"},
			replies: map[string]reply{"GET /api/v1/groups": ok(`{"data":[{"id":"g1","name":"devs"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/groups"}},
			out:     "devs",
		},
		{
			name:    "group members",
			args:    []string{"group", "members", "g1"},
			replies: map[string]reply{"GET /api/v1/groups/{id}/members": ok(`{"data":[{"id":"u1","username":"bob"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/groups/{id}/members"}},
			out:     "bob",
		},
		{
			name:    "group add-member",
			args:    []string{"group", "add-member", "g1", "u1"},
			replies: map[string]reply{"POST /api/v1/groups/{id}/members/{userId}": {status: 204}},
			calls:   []call{{method: "POST", pattern: "/api/v1/groups/{id}/members/{userId}"}},
		},
		{
			name:    "project list",
			args:    []string{"project", "list"},
			replies: map[string]reply{"GET /api/v1/projects": ok(`{"projects":[{"name":"acme","display_name":"Acme","my_role":"maintainer","repository_count":3}],"can_create":true}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/projects"}},
			out:     "maintainer",
		},
		{
			name:    "project show prints my_role",
			args:    []string{"project", "show", "acme"},
			replies: map[string]reply{"GET /api/v1/projects/{project}": ok(`{"name":"acme","my_role":"owner"}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/projects/{project}"}},
			out:     "my_role",
		},
		{
			name:    "project update uses PATCH and sends only set flags",
			args:    []string{"project", "update", "acme", "--display-name", "Acme Corp"},
			replies: map[string]reply{"PATCH /api/v1/projects/{project}": ok(`{}`)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/projects/{project}", body: m{"display_name": "Acme Corp"}}},
		},
		{
			name:    "project update policy flags",
			args:    []string{"project", "update", "acme", "--allowed-formats", "npm,deb", "--allow-public-repos=false", "--max-repositories", "10"},
			replies: map[string]reply{"PATCH /api/v1/projects/{project}": ok(`{}`)},
			calls: []call{{method: "PATCH", pattern: "/api/v1/projects/{project}",
				body: m{"allowed_formats": []any{"npm", "deb"}, "allow_public_repos": false, "max_repositories": float64(10)}}},
		},
		{
			name: "project update with nothing fails", args: []string{"project", "update", "acme"}, fail: "nothing to update",
		},
		{
			name:    "project create with owner",
			args:    []string{"project", "create", "acme", "--owner", "bob"},
			replies: map[string]reply{"POST /api/v1/projects": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/projects", body: m{"name": "acme", "display_name": "", "description": "", "owner": "bob"}}},
		},
		{
			name:    "project create",
			args:    []string{"project", "create", "acme", "--display-name", "Acme"},
			replies: map[string]reply{"POST /api/v1/projects": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/projects", body: m{"name": "acme", "display_name": "Acme", "description": ""}}},
		},
		{
			name:    "project members",
			args:    []string{"project", "members", "acme"},
			replies: map[string]reply{"GET /api/v1/projects/{project}/members": ok(`{"members":[{"user_id":"u1","role":"owner"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/projects/{project}/members"}},
			out:     "owner",
		},
		{
			name:    "project add-member",
			args:    []string{"project", "add-member", "acme", "u1", "--role", "owner"},
			replies: map[string]reply{"POST /api/v1/projects/{project}/members": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/projects/{project}/members", body: m{"user_id": "u1", "role": "owner"}}},
		},
		{
			name:    "project add-member by username",
			args:    []string{"project", "add-member", "acme", "--username", "bob", "--role", "developer"},
			replies: map[string]reply{"POST /api/v1/projects/{project}/members": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/projects/{project}/members", body: m{"username": "bob", "role": "developer"}}},
		},
		{
			name: "project add-member needs an id or a username", args: []string{"project", "add-member", "acme"}, fail: "Usage",
		},
		{
			name:    "project set-role uses PATCH",
			args:    []string{"project", "set-role", "acme", "u1", "maintainer"},
			replies: map[string]reply{"PATCH /api/v1/projects/{project}/members/{user_id}": ok(`{}`)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/projects/{project}/members/{user_id}", body: m{"role": "maintainer"}}},
		},
		{
			name:    "project groups",
			args:    []string{"project", "groups", "acme"},
			replies: map[string]reply{"GET /api/v1/projects/{project}/groups": ok(`{"groups":[{"group_id":"g1","group_name":"devs","role":"developer"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/projects/{project}/groups"}},
			out:     "devs",
		},
		{
			name:    "project add-group",
			args:    []string{"project", "add-group", "acme", "g1", "--role", "developer"},
			replies: map[string]reply{"PUT /api/v1/projects/{project}/groups/{group}": ok(`{}`)},
			calls:   []call{{method: "PUT", pattern: "/api/v1/projects/{project}/groups/{group}", body: m{"role": "developer"}}},
		},
		{
			name:    "project add-group by name",
			args:    []string{"project", "add-group", "acme", "dev team"},
			replies: map[string]reply{"PUT /api/v1/projects/{project}/groups/{group}": ok(`{}`)},
			calls:   []call{{method: "PUT", pattern: "/api/v1/projects/{project}/groups/{group}", body: m{"role": "viewer"}}},
		},
		{
			name:    "project rm-group",
			args:    []string{"project", "rm-group", "acme", "g1"},
			replies: map[string]reply{"DELETE /api/v1/projects/{project}/groups/{group}": {status: 204}},
			calls:   []call{{method: "DELETE", pattern: "/api/v1/projects/{project}/groups/{group}"}},
		},
		{
			name:    "admin token list",
			args:    []string{"token", "list"},
			replies: map[string]reply{"GET /api/v1/admin/tokens": ok(`{"data":[{"id":"t9","name":"x","kind":"access","user_id":"u1"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/tokens", query: map[string]string{"per_page": "100"}}},
			out:     "t9",
		},
		{
			name:    "admin token revoke",
			args:    []string{"token", "revoke", "t9"},
			replies: map[string]reply{"DELETE /api/v1/admin/tokens/{tokenId}": {status: 204}},
			calls:   []call{{method: "DELETE", pattern: "/api/v1/admin/tokens/{tokenId}"}},
		},
	})
}

func TestContractStorage(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(cfgFile, []byte(`{"bucket":"b","secret_key":"s3cr3t"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	runCases(t, []contractCase{
		{
			name:    "list",
			args:    []string{"storage", "list"},
			replies: map[string]reply{"GET /api/v1/admin/storage": ok(`{"items":[{"name":"s","kind":"s3","is_default":true,"status":"ok"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/storage"}},
			out:     "s3",
		},
		{
			name:    "create with non-secret inline config",
			args:    []string{"storage", "create", "--name", "s", "--type", "s3", "--config", `{"bucket":"b","region":"eu"}`, "--default"},
			replies: map[string]reply{"POST /api/v1/admin/storage": {status: 201, body: `{}`}},
			calls: []call{{method: "POST", pattern: "/api/v1/admin/storage",
				body: m{"name": "s", "kind": "s3", "config": m{"bucket": "b", "region": "eu"}, "is_default": true}}},
		},
		{
			name:    "create with --config-file",
			args:    []string{"storage", "create", "--name", "s", "--type", "s3", "--config-file", cfgFile},
			replies: map[string]reply{"POST /api/v1/admin/storage": {status: 201, body: `{}`}},
			calls: []call{{method: "POST", pattern: "/api/v1/admin/storage",
				body: m{"name": "s", "kind": "s3", "config": m{"bucket": "b", "secret_key": "s3cr3t"}, "is_default": false}}},
		},
		{
			name:    "create with --config-file - reads stdin",
			args:    []string{"storage", "create", "--name", "s", "--type", "s3", "--config-file", "-"},
			stdin:   `{"bucket":"b","access_key":"AK","secret_key":"SK"}`,
			replies: map[string]reply{"POST /api/v1/admin/storage": {status: 201, body: `{}`}},
			calls: []call{{method: "POST", pattern: "/api/v1/admin/storage",
				body: m{"name": "s", "kind": "s3", "config": m{"bucket": "b", "access_key": "AK", "secret_key": "SK"}, "is_default": false}}},
		},
		{
			name: "inline secret rejected before any request",
			args: []string{"storage", "create", "--name", "s", "--type", "s3", "--config", `{"bucket":"b","secret_key":"x"}`},
			fail: "--config-file",
		},
		{
			name: "nested inline secret rejected",
			args: []string{"storage", "create", "--name", "s", "--config", `{"auth":{"Password":"x"}}`},
			fail: "--config-file",
		},
		{
			name: "both config flags rejected",
			args: []string{"storage", "create", "--name", "s", "--config", `{}`, "--config-file", cfgFile},
			fail: "not both",
		},
		{
			name:    "update sends only set flags",
			args:    []string{"storage", "update", "s", "--default=false"},
			replies: map[string]reply{"PATCH /api/v1/admin/storage/{name}": ok(`{}`)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/admin/storage/{name}", body: m{"is_default": false}}},
		},
		{
			name:    "test failure exits non-zero",
			args:    []string{"storage", "test", "s"},
			replies: map[string]reply{"POST /api/v1/admin/storage/{name}/test": ok(`{"ok":false,"error":"boom","message":"unreachable"}`)},
			calls:   []call{{method: "POST", pattern: "/api/v1/admin/storage/{name}/test"}},
			fail:    "unreachable",
		},
		{
			name:    "test success",
			args:    []string{"storage", "test", "s"},
			replies: map[string]reply{"POST /api/v1/admin/storage/{name}/test": ok(`{"ok":true}`)},
			calls:   []call{{method: "POST", pattern: "/api/v1/admin/storage/{name}/test"}},
			out:     "passed",
		},
	})
}

func TestContractBackupAuditSystem(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := os.WriteFile(archive, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	runCases(t, []contractCase{
		{
			name:    "backup list",
			args:    []string{"backup", "list"},
			replies: map[string]reply{"GET /api/v1/admin/backup": ok(`{"backups":[{"id":"b1","type":"full","status":"done","size_bytes":10}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/backup"}},
			out:     "b1",
		},
		{
			name:    "backup create",
			args:    []string{"backup", "create", "--type", "metadata-only"},
			replies: map[string]reply{"POST /api/v1/admin/backup": {status: 202, body: `{"id":"b2"}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/admin/backup", body: m{"type": "metadata-only"}}},
			out:     "b2",
		},
		{
			name:    "backup restore uploads the archive",
			args:    []string{"backup", "restore", archive},
			replies: map[string]reply{"POST /api/v1/admin/restore": {status: 202, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/admin/restore", bodyHas: `name="file"`}},
			out:     "Restore initiated",
		},
		{
			name:    "audit list uses actor and per_page",
			args:    []string{"audit", "list", "--limit", "5", "--actor", "admin", "--action", "repo.create"},
			replies: map[string]reply{"GET /api/v1/admin/audit": ok(`{"data":[{"timestamp":"2026-01-01T00:00:00Z","actor":"admin","action":"repo.create","target":"r"}],"has_more":false}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/audit", query: map[string]string{"per_page": "5", "page": "1", "actor": "admin", "action": "repo.create"}}},
			out:     "repo.create",
		},
		{
			name:    "audit export streams to stdout with filters",
			args:    []string{"audit", "export", "--format", "csv", "--since", "2026-01-01", "--until", "2026-02-01T00:00:00Z", "--actor", "admin", "--action", "auth"},
			replies: map[string]reply{"GET /api/v1/admin/audit/export": {status: 200, body: "id,timestamp\nx,y\n", headers: map[string]string{"Content-Type": "text/csv"}}},
			calls: []call{{method: "GET", pattern: "/api/v1/admin/audit/export", query: map[string]string{
				"format": "csv", "since": "2026-01-01", "until": "2026-02-01T00:00:00Z", "actor": "admin", "action": "auth",
			}}},
			out: "id,timestamp\nx,y\n",
		},
		{
			name:    "audit export defaults to jsonl and resolves a relative since",
			args:    []string{"audit", "export", "--since", "7d"},
			replies: map[string]reply{"GET /api/v1/admin/audit/export": ok("")},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/audit/export", query: map[string]string{"format": "jsonl"}}},
		},
		{
			name: "audit export rejects an unknown format before any request",
			args: []string{"audit", "export", "--format", "xml"},
			fail: "--format must be jsonl or csv",
		},
		{
			name: "audit export rejects a malformed time before any request",
			args: []string{"audit", "export", "--until", "yesterday-ish"},
			fail: "--until",
		},
		{
			name:    "audit export reports a refusal",
			args:    []string{"audit", "export"},
			replies: map[string]reply{"GET /api/v1/admin/audit/export": {status: 403, body: `{"error":"admin access required"}`}},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/audit/export"}},
			fail:    "admin access required",
		},
		{
			name:    "system info prefers the admin endpoint",
			args:    []string{"system", "info"},
			replies: map[string]reply{"GET /api/v1/admin/system/info": ok(`{"version":"1","uptime":"1h","db_status":"ok"}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/system/info"}},
			out:     "db_status",
		},
		{
			name: "system info falls back to the public endpoint",
			args: []string{"system", "info"},
			replies: map[string]reply{
				"GET /api/v1/admin/system/info": {status: 403, body: `{"error":"forbidden"}`},
				"GET /api/v1/system/info":       ok(`{"version":"1","status":"ok"}`),
			},
			calls: []call{{method: "GET", pattern: "/api/v1/admin/system/info"}, {method: "GET", pattern: "/api/v1/system/info"}},
			out:   "version",
		},
	})
}

func TestContractWebhook(t *testing.T) {
	runCases(t, []contractCase{
		{
			name:    "list",
			args:    []string{"webhook", "list"},
			replies: map[string]reply{"GET /api/v1/webhooks": ok(`{"webhooks":[{"id":"w1","url":"https://h","enabled":true}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/webhooks"}},
			out:     "w1",
		},
		{
			name:    "create sends enabled",
			args:    []string{"webhook", "create", "--name", "n", "--url", "https://h", "--events", "a,b", "--active=false"},
			replies: map[string]reply{"POST /api/v1/webhooks": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/webhooks", body: m{"name": "n", "url": "https://h", "events": []any{"a", "b"}, "enabled": false}}},
		},
		{
			name:    "update sends only set flags as enabled",
			args:    []string{"webhook", "update", "w1", "--active=false"},
			replies: map[string]reply{"PUT /api/v1/webhooks/{id}": ok(`{}`)},
			calls:   []call{{method: "PUT", pattern: "/api/v1/webhooks/{id}", body: m{"enabled": false}}},
		},
		{
			name:    "test is accepted",
			args:    []string{"webhook", "test", "w1"},
			replies: map[string]reply{"POST /api/v1/webhooks/{id}/test": {status: 202, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/webhooks/{id}/test"}},
		},
		{
			name:    "deliveries",
			args:    []string{"webhook", "deliveries", "w1"},
			replies: map[string]reply{"GET /api/v1/webhooks/{id}/deliveries": ok(`{"deliveries":[{"id":"d1","event_type":"x","status":"ok"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/webhooks/{id}/deliveries"}},
			out:     "d1",
		},
		{
			name:    "repo webhook create",
			args:    []string{"webhook", "repo", "create", "r", "--name", "n", "--url", "https://h", "--events", "artifact.uploaded"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/webhooks": {status: 201, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/webhooks", body: m{"name": "n", "url": "https://h", "events": []any{"artifact.uploaded"}, "enabled": true}}},
		},
	})
}

func TestContractScans(t *testing.T) {
	contentFile := filepath.Join(t.TempDir(), "c.txt")
	if err := os.WriteFile(contentFile, []byte("AKIA..."), 0o600); err != nil {
		t.Fatal(err)
	}

	runCases(t, []contractCase{
		{
			name:    "sbom generate",
			args:    []string{"sbom", "generate", "r", "--artifact-id", "a1", "--format", "spdx"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/sbom/generate": {status: 201, body: `{"id":"s1","component_count":3}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/sbom/generate", body: m{"artifact_id": "a1", "format": "spdx", "spec_version": "2.3"}}},
			out:     "s1",
		},
		{
			name:    "sbom generate spdx-json with spec version",
			args:    []string{"sbom", "generate", "r", "--artifact-id", "a1", "--format", "cyclonedx", "--spec-version", "1.6"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/sbom/generate": {status: 201, body: `{"id":"s2","component_count":0}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/sbom/generate", body: m{"artifact_id": "a1", "format": "cyclonedx", "spec_version": "1.6"}}},
			out:     "s2",
		},
		{
			name:    "vex set",
			args:    []string{"vex", "set", "r", "--artifact-id", "a1", "--vuln", "CVE-1", "--status", "not_affected", "--justification", "component_not_present"},
			replies: map[string]reply{"PUT /api/v1/repositories/{name}/vex/statements": ok(`{"id":"x1","status":"not_affected"}`)},
			calls: []call{{method: "PUT", pattern: "/api/v1/repositories/{name}/vex/statements",
				body: m{"artifact_id": "a1", "vuln_id": "CVE-1", "status": "not_affected", "justification": "component_not_present", "impact_statement": "", "action_statement": ""}}},
			out: "x1",
		},
		{
			name:    "vex list",
			args:    []string{"vex", "list", "r"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/vex/statements": ok(`{"statements":[{"id":"x1","vuln_id":"CVE-1","status":"affected"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/vex/statements"}},
			out:     "CVE-1",
		},
		{
			name:    "vex get",
			args:    []string{"vex", "get", "r", "--format", "openvex"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/vex": ok(`{"@context":"https://openvex.dev/ns/v0.2.0"}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/vex"}},
			out:     "openvex.dev",
		},
		{
			name:    "vex delete",
			args:    []string{"vex", "delete", "r", "x1"},
			replies: map[string]reply{"DELETE /api/v1/repositories/{name}/vex/statements/{id}": {status: 204}},
			calls:   []call{{method: "DELETE", pattern: "/api/v1/repositories/{name}/vex/statements/{id}"}},
			out:     "deleted",
		},
		{
			name:    "sbom list",
			args:    []string{"sbom", "list", "r"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/sbom": ok(`{"reports":[{"id":"s1","artifact_id":"a1","format":"spdx"}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/sbom"}},
			out:     "s1",
		},
		{
			name:    "vuln scan",
			args:    []string{"vuln", "scan", "r", "--artifact-id", "a1", "--package", "left-pad", "--version", "1.0.0", "--ecosystem", "npm"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/vuln/scan": ok(`{"id":"v1","scan_status":"completed","vuln_count":2}`)},
			calls: []call{{method: "POST", pattern: "/api/v1/repositories/{name}/vuln/scan",
				body: m{"artifact_id": "a1", "package_name": "left-pad", "package_version": "1.0.0", "ecosystem": "npm", "scanner": "osv"}}},
			out: "completed",
		},
		{
			name:    "vuln list reads scans",
			args:    []string{"vuln", "list", "r"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/vuln/scans": ok(`{"scans":[{"id":"v1","scan_status":"completed","critical_count":1}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/vuln/scans"}},
			out:     "completed",
		},
		{
			name:    "license scan",
			args:    []string{"license", "scan", "r", "--artifact-id", "a1"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/license/scan": ok(`{"id":"l1","scan_status":"completed"}`)},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/license/scan", body: m{"artifact_id": "a1"}}},
		},
		{
			name:    "secret scan with content file",
			args:    []string{"secret", "scan", "r", "--artifact-id", "a1", "--content-file", contentFile},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/secrets/scan": ok(`{"id":"x1","scan_status":"completed","finding_count":1}`)},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/secrets/scan", body: m{"artifact_id": "a1", "content": "AKIA..."}}},
		},
		{
			name:    "secret list reads scans",
			args:    []string{"secret", "list", "r"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/secrets/scans": ok(`{"scans":[{"id":"x1","scan_status":"completed","finding_count":1}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/secrets/scans"}},
			out:     "x1",
		},
		{
			name: "license delete is gone", args: []string{"license", "delete", "r", "l1"}, fail: "Unknown license subcommand",
		},
	})
}

func TestContractRepoFeatures(t *testing.T) {
	runCases(t, []contractCase{
		{
			name:    "cleanup create maps flags to server fields",
			args:    []string{"cleanup", "create", "r", "--name", "p", "--keep-count", "3", "--keep-days", "30", "--name-pattern", "^a", "--schedule", "0 3 * * *"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/cleanup-policies": {status: 201, body: `{}`}},
			calls: []call{{method: "POST", pattern: "/api/v1/repositories/{name}/cleanup-policies",
				body: m{"name": "p", "keep_last_n": float64(3), "keep_since_days": float64(30), "name_pattern": "^a", "schedule_cron": "0 3 * * *"}}},
		},
		{
			name: "cleanup create without criterion fails client-side", args: []string{"cleanup", "create", "r", "--name", "p"}, fail: "--keep-count or --keep-days",
		},
		{
			name:    "cleanup update sends only set flags",
			args:    []string{"cleanup", "update", "r", "p1", "--keep-count", "5", "--enabled=false"},
			replies: map[string]reply{"PUT /api/v1/repositories/{name}/cleanup-policies/{id}": ok(`{}`)},
			calls:   []call{{method: "PUT", pattern: "/api/v1/repositories/{name}/cleanup-policies/{id}", body: m{"keep_last_n": float64(5), "enabled": false}}},
		},
		{
			name:    "cleanup list reads policies",
			args:    []string{"cleanup", "list", "r"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/cleanup-policies": ok(`{"policies":[{"id":"p1","name":"p","enabled":true}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/cleanup-policies"}},
			out:     "p1",
		},
		{
			name:    "cleanup run is accepted",
			args:    []string{"cleanup", "run", "r", "p1"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/cleanup-policies/{id}/run": {status: 202, body: `{}`}},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/cleanup-policies/{id}/run"}},
		},
		{
			name:    "quota set maps to max_size_bytes",
			args:    []string{"quota", "set", "r", "--limit", "2", "--unit", "GB", "--max-artifacts", "10", "--warn-percent", "80"},
			replies: map[string]reply{"PUT /api/v1/repositories/{name}/quota": ok(`{}`)},
			calls: []call{{method: "PUT", pattern: "/api/v1/repositories/{name}/quota",
				body: m{"max_size_bytes": float64(2 << 30), "max_artifact_count": float64(10), "warn_threshold_percent": float64(80)}}},
		},
		{
			name: "quota set with no flags fails", args: []string{"quota", "set", "r"}, fail: "nothing to set",
		},
		{
			name:    "quota show",
			args:    []string{"quota", "show", "r"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/quota": ok(`{"max_size_bytes":1}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/quota"}},
			out:     "max_size_bytes",
		},
		{
			name:    "props set patches a flat map",
			args:    []string{"props", "set", "r", "a1", "env", "prod"},
			replies: map[string]reply{"PATCH /api/v1/repositories/{name}/artifacts/{artifact_id}/properties": ok(`{}`)},
			calls:   []call{{method: "PATCH", pattern: "/api/v1/repositories/{name}/artifacts/{artifact_id}/properties", body: m{"env": "prod"}}},
		},
		{
			name:    "props list",
			args:    []string{"props", "list", "r", "a1"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/artifacts/{artifact_id}/properties": ok(`{"env":"prod"}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/artifacts/{artifact_id}/properties"}},
			out:     "prod",
		},
		{
			name:    "props search prints artifact ids",
			args:    []string{"props", "search", "r", "env", "prod"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/artifacts/search": ok(`{"artifact_ids":["a1","a2"]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/artifacts/search", query: map[string]string{"key": "env", "value": "prod"}}},
			out:     "a2",
		},
		{
			name:    "delta list reads data",
			args:    []string{"delta", "list", "r", "p1"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/packages/{id}/deltas": ok(`{"success":true,"data":[{"from_sha256":"aa","to_sha256":"bb","delta_size":4}]}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/packages/{id}/deltas"}},
			out:     "aa",
		},
	})
}

func TestContractAuditExportToFile(t *testing.T) {
	const body = "{\"id\":\"1\"}\n{\"id\":\"2\"}\n"

	cs, srv := newContractServer(t, map[string]reply{"GET /api/v1/admin/audit/export": {status: 200, body: body}})
	dest := filepath.Join(t.TempDir(), "audit.jsonl")

	res := runOA(t, srv, "", "audit", "export", "--format", "jsonl", "-o", dest)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}

	if len(cs.bad) > 0 {
		t.Errorf("CLI hit routes the server does not serve: %v", cs.bad)
	}

	got, err := os.ReadFile(dest)
	if err != nil || string(got) != body {
		t.Errorf("file = %q (%v), want %q", got, err, body)
	}

	if res.stdout != "" {
		t.Errorf("stdout must stay clean when writing to a file, got %q", res.stdout)
	}

	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestAuditExportQuery(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	for name, tc := range map[string]struct {
		since, until string
		want         map[string]string
		err          string
	}{
		"relative days":    {since: "7d", want: map[string]string{"since": "2026-03-03T12:00:00Z"}},
		"relative hours":   {since: "36h", until: "1h", want: map[string]string{"since": "2026-03-09T00:00:00Z", "until": "2026-03-10T11:00:00Z"}},
		"date passes":      {since: "2026-01-01", want: map[string]string{"since": "2026-01-01"}},
		"rfc3339 passes":   {until: "2026-01-01T10:00:00+02:00", want: map[string]string{"until": "2026-01-01T10:00:00+02:00"}},
		"garbage":          {since: "soon", err: "--since"},
		"negative age":     {since: "-3h", err: "--since"},
		"day count needed": {until: "xd", err: "--until"},
	} {
		t.Run(name, func(t *testing.T) {
			q, err := auditExportQuery("jsonl", tc.since, tc.until, "", "", now)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			for k, v := range tc.want {
				if q.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, q.Get(k), v)
				}
			}
		})
	}
}
