// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// deployPublishPaths is, per opted-in format, the request the format's client
// publishes with (as a path below the repository) and the path it reads the
// published file back with.
var deployPublishPaths = map[string]struct{ method, publish, read string }{ //nolint:gochecknoglobals // test table
	"ansible":   {http.MethodPost, "api/v3/artifacts/collections/", "api/v3/artifacts/collections/acme-tools-1.0.0.tar.gz"},
	"cargo":     {http.MethodPut, "api/v1/crates/new", "api/v1/crates/crate/1.0.0/download"},
	"composer":  {http.MethodPut, "packages/upload/acme/tool/1.0.0", "dists/acme/tool/1.0.0/abc.zip"},
	"conan":     {http.MethodPut, "v2/conans/pkg/1.0/_/_/revisions/r1/files/conanfile.py", "files/conanfile.py"},
	"conda":     {http.MethodPut, "noarch/pkg-1.0-0.conda", "files/pkg-1.0-0.conda"},
	"go":        {http.MethodPut, "example.com/m/@v/v1.0.0.zip", "files/v1.0.0.zip"},
	"helm":      {http.MethodPost, "api/charts", "charts/chart-1.0.0.tgz"},
	"ivy":       {http.MethodPut, "org/mod/1.0/ivy-1.0.xml", "files/ivy-1.0.xml"},
	"maven":     {http.MethodPut, "com/acme/lib/1.0/lib-1.0.jar", "files/lib-1.0.jar"},
	"nix":       {http.MethodPut, "nar/x.nar.xz", "files/x.nar.xz"},
	"npm":       {http.MethodPut, "pkg", "pkg/-/pkg-1.0.0.tgz"},
	"nuget":     {http.MethodPut, "v3/push", "files/pkg.1.0.0.nupkg"},
	"pypi":      {http.MethodPost, "", "files/pkg-1.0.tar.gz"},
	"raw":       {http.MethodPut, "dir/file.txt", "files/file.txt"},
	"rubygems":  {http.MethodPost, "api/v1/gems", "files/pkg-1.0.gem"},
	"sbt":       {http.MethodPut, "org/mod/1.0/mod-1.0.jar", "files/mod-1.0.jar"},
	"terraform": {http.MethodPost, "v1/modules/acme/vpc/aws/1.0.0", "v1/modules/acme/vpc/aws/1.0.0/download"},
}

// deployEnv serves one hosted repository "h" and a group "g" of the format
// that has it as deploy target ("withTarget"), or as a plain member. The
// hosted router stands for the format handler: a write is stored under the
// repository it is addressed to and a read answers from the repositories.
type deployEnv struct {
	router http.Handler
	mu     sync.Mutex
	stored map[string]string // "<repo>:<path>" -> answer of the write
	writes int
}

func newDeployEnv(t *testing.T, format string) *deployEnv {
	t.Helper()

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: format, RepoType: typ}
	}

	target := repo("1", "h", "hosted")
	other := repo("2", "other", "hosted")
	group := repo("3", "g", "group")

	db := &fakeDB{
		repos: map[string]*models.Repository{"h": target, "other": other, "g": group},
		links: map[string][]*models.RepositoryGroupMember{"3": {{MemberRepoID: "1", Priority: 0}, {MemberRepoID: "2", Priority: 1}}},
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	e := &deployEnv{stored: map[string]string{}}

	write := chi.NewRouter()
	write.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		repoName, path := chi.URLParam(r, "name"), chi.URLParam(r, "*")
		answer := "published to " + repoName

		e.mu.Lock()
		e.stored[repoName+":"+strings.TrimPrefix(path, "files/")] = answer
		e.writes++
		e.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, answer)
	})

	read := chi.NewRouter()
	read.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()

		if _, ok := e.stored[chi.URLParam(r, "name")+":"+strings.TrimPrefix(chi.URLParam(r, "*"), "files/")]; ok {
			_, _ = io.WriteString(w, "content of "+chi.URLParam(r, "name"))
			return
		}

		http.NotFound(w, r)
	})

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{format: read}).
		WithDeployRouters(map[string]http.Handler{format: write})

	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "format-handler", http.StatusTeapot) })
	})
	e.router = router

	return e
}

// do sends a request as RBAC would have left it: authorizedTarget is the
// deploy target RBAC authorized the write against ("" when it set none).
func (e *deployEnv) do(method, path, authorizedTarget string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader("payload"))
	ctx := middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true})

	if authorizedTarget != "" {
		ctx = middleware.WithGroupDeploy(ctx, "g", authorizedTarget)
	}

	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req.WithContext(ctx))

	return w
}

func TestGroupDeployPublishesToTargetAndReadsBackThroughGroup(t *testing.T) {
	formats := strings.Split(repotype.GroupDeployFormats(), ", ")
	if got, want := len(deployPublishPaths), len(formats); got != want {
		t.Fatalf("table covers %d formats, the registry opts in %d: %v", got, want, formats)
	}

	for _, format := range formats {
		c, ok := deployPublishPaths[format]
		if !ok {
			t.Fatalf("no publish request for opted-in format %s", format)
		}

		t.Run(format, func(t *testing.T) {
			e := newDeployEnv(t, format)

			w := e.do(c.method, "/repos/g/"+c.publish, "h")
			if w.Code != http.StatusCreated || w.Body.String() != "published to h" {
				t.Fatalf("publish through the group = %d %q, want 201 naming the target", w.Code, w.Body.String())
			}

			// the file the target's handler stored is read through the group.
			e.mu.Lock()
			e.stored["h:"+strings.TrimPrefix(c.read, "files/")] = "x"
			e.mu.Unlock()

			if r := e.do(http.MethodGet, "/repos/g/"+c.read, ""); r.Code != http.StatusOK || r.Body.String() != "content of h" {
				t.Errorf("read back through the group = %d %q, want the target's content", r.Code, r.Body.String())
			}
		})
	}
}

func TestGroupDeployRefusesWhatRBACDidNotAuthorize(t *testing.T) {
	e := newDeployEnv(t, "raw")

	cases := []struct{ name, method, target string }{
		{"no deploy target authorized", http.MethodPut, ""},
		{"a delete is not a deploy write", http.MethodDelete, ""},
		{"authorized target is not hosted in this group's format", http.MethodPut, "g"},
		{"authorized target does not exist", http.MethodPut, "missing"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := e.do(c.method, "/repos/g/dir/file.txt", c.target); w.Code != http.StatusMethodNotAllowed {
				t.Errorf("= %d, want 405", w.Code)
			}
		})
	}

	if e.writes != 0 {
		t.Errorf("%d writes reached a hosted handler, want none", e.writes)
	}
}

func TestGroupDeployWritesGoToTheAuthorizedMemberOnly(t *testing.T) {
	e := newDeployEnv(t, "raw")

	if w := e.do(
		http.MethodPut,
		"/repos/g/dir/file.txt",
		"other",
	); w.Code != http.StatusCreated ||
		w.Body.String() != "published to other" {
		t.Fatalf("= %d %q, want the authorized member to serve it", w.Code, w.Body.String())
	}

	if _, ok := e.stored["h:dir/file.txt"]; ok {
		t.Error("the write also landed in another member")
	}
}
