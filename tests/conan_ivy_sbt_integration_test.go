// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/conan"
	"github.com/openartifact/openartifact/internal/format/ivy"
	"github.com/openartifact/openartifact/internal/format/sbt"
	"github.com/openartifact/openartifact/internal/storage"
)

// formatServer mounts a format's repo routes under /repos/{name} over a real
// database and a filesystem blob store.
type formatServer struct {
	t      *testing.T
	router chi.Router
}

func newFormatServer(t *testing.T, register func(chi.Router)) *formatServer {
	t.Helper()

	router := chi.NewRouter()
	router.Route("/repos/{name}", register)

	return &formatServer{t: t, router: router}
}

func (s *formatServer) do(method, target, body string) *httptest.ResponseRecorder {
	s.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	return w
}

func (s *formatServer) must(method, target, body string, want int) *httptest.ResponseRecorder {
	s.t.Helper()

	w := s.do(method, target, body)
	if w.Code != want {
		s.t.Fatalf("%s %s = %d, want %d: %s", method, target, w.Code, want, w.Body.String())
	}

	return w
}

func newConanServer(t *testing.T, pg *database.PostgresDB) *formatServer {
	t.Helper()

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	return newFormatServer(t, conan.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost").RegisterRepoRoutes)
}

const (
	conanBase = "/repos/conan/v2/conans/zlib/1.3/_/_"
	conanRrev = "9b4de2c1f0e8a7d65c3b2a1908f7e6d5"
	conanPid  = "d4f0ab9e8c7b6a5948372615f0e1d2c3b4a59687"
	conanPrev = "5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
)

// CONAN-1: a conan client uploads four recipe files and three package files for
// one recipe revision; every one used to land on the same
// (name, version, user/channel, revision "0") key, so the second upload was a
// unique violation and a 500. All of them must now be stored, listed and
// downloadable under their own names.
func TestConanUploadsEveryFileOfARevision(t *testing.T) {
	pg := freshPostgres(t, 5)
	repoID := newRepo(t, pg, "conan", "conan")
	srv := newConanServer(t, pg)

	uploadConanRevision(srv)
	checkConanProtocol(t, srv)

	// Re-uploading a file replaces it in place.
	srv.must(http.MethodPut, conanRecipeFile("conanfile.py"), "recipe v2", http.StatusCreated)

	if got := srv.must(http.MethodGet, conanRecipeFile("conanfile.py"), "", http.StatusOK).Body.String(); got != "recipe v2" {
		t.Errorf("overwritten file = %q", got)
	}

	checkConanStorage(t, pg, repoID)

	// The conan client expects a plain 200 from delete; the files and blobs go.
	srv.must(http.MethodDelete, conanBase+"/revisions/"+conanRrev, "", http.StatusOK)
	srv.must(http.MethodGet, conanBase+"/latest", "", http.StatusNotFound)

	if rows, _ := pg.ListConanRecipeFiles(context.Background(), repoID, "zlib", "1.3", "_/_"); len(rows) != 0 {
		t.Errorf("rows after delete = %d, want 0", len(rows))
	}
}

func conanRecipeFiles() []string {
	return []string{"conan_export.tgz", "conan_sources.tgz", "conanfile.py", "conanmanifest.txt"}
}

func conanRecipeFile(name string) string {
	return conanBase + "/revisions/" + conanRrev + "/files/" + name
}

func conanPackageFile(name string) string {
	return conanBase + "/revisions/" + conanRrev + "/packages/" + conanPid + "/revisions/" + conanPrev + "/files/" + name
}

// uploadConanRevision uploads what `conan upload` sends for one recipe revision
// and one package: every file is a separate PUT.
func uploadConanRevision(srv *formatServer) {
	for _, f := range conanRecipeFiles() {
		srv.must(http.MethodPut, conanRecipeFile(f), "recipe:"+f, http.StatusCreated)
	}

	for _, f := range []string{"conan_package.tgz", "conaninfo.txt", "conanmanifest.txt"} {
		srv.must(http.MethodPut, conanPackageFile(f), "package:"+f, http.StatusCreated)
	}
}

// checkConanProtocol reads the revision back the way a conan client does.
func checkConanProtocol(t *testing.T, srv *formatServer) {
	t.Helper()

	var latest struct{ Revision string }
	if err := json.Unmarshal(
		srv.must(http.MethodGet, conanBase+"/latest", "", http.StatusOK).Body.Bytes(),
		&latest,
	); err != nil ||
		latest.Revision != conanRrev {
		t.Fatalf("latest = %+v, %v; want %s", latest, err, conanRrev)
	}

	var files struct {
		Files map[string]struct{ Size int64 } `json:"files"`
	}

	if err := json.Unmarshal(
		srv.must(http.MethodGet, conanBase+"/revisions/"+conanRrev+"/files", "", http.StatusOK).Body.Bytes(),
		&files,
	); err != nil {
		t.Fatal(err)
	}

	if len(files.Files) != len(conanRecipeFiles()) || files.Files["conanfile.py"].Size != int64(len("recipe:conanfile.py")) {
		t.Fatalf("recipe files = %+v, want %d files with their real sizes", files.Files, len(conanRecipeFiles()))
	}

	for _, f := range conanRecipeFiles() {
		if got := srv.must(http.MethodGet, conanRecipeFile(f), "", http.StatusOK).Body.String(); got != "recipe:"+f {
			t.Errorf("download %s = %q", f, got)
		}
	}

	if got := srv.must(
		http.MethodGet,
		conanPackageFile("conan_package.tgz"),
		"",
		http.StatusOK,
	).Body.String(); got != "package:conan_package.tgz" {
		t.Errorf("package download = %q", got)
	}
}

// checkConanStorage checks the rows behind the revision: one per file, one recipe
// and, in the component listing, one entry for the whole revision.
func checkConanStorage(t *testing.T, pg *database.PostgresDB, repoID string) {
	t.Helper()

	ctx := context.Background()

	rows, err := pg.ListConanRecipeFiles(ctx, repoID, "zlib", "1.3", "_/_")
	if err != nil || len(rows) != 7 {
		t.Fatalf("stored rows = %d, %v; want 7 (one per file)", len(rows), err)
	}

	recipes, err := pg.ListConanRecipes(ctx, repoID)
	if err != nil || len(recipes) != 1 || recipes[0] != (models.ConanRecipe{Name: "zlib", Version: "1.3", UserChannel: "_/_"}) {
		t.Fatalf("recipes = %+v, %v", recipes, err)
	}

	comps, total, err := pg.ListComponentsByRepo(ctx, repoID, "", 1, 50)
	if err != nil || total != 1 || len(comps) != 1 || comps[0].Name != "zlib" {
		t.Fatalf("components = %d (total %d), %v; want the single revision", len(comps), total, err)
	}
}

// Rows written before revisions were supported (empty file name, revision "0")
// stay out of protocol responses but can still be deleted.
func TestConanLegacyRowsAreHiddenButDeletable(t *testing.T) {
	pg := freshPostgres(t, 5)
	repoID := newRepo(t, pg, "conan", "conan")
	srv := newConanServer(t, pg)
	ctx := context.Background()

	legacy := &models.ConanPackage{
		RepositoryID: repoID, Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "0",
		BlobKey: strings.Repeat("a", 64), SizeBytes: 0,
	}
	if err := pg.CreateConanPackage(ctx, legacy); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	srv.must(http.MethodGet, conanBase+"/latest", "", http.StatusNotFound)

	var res struct{ Results []string }
	if err := json.Unmarshal(
		srv.must(http.MethodGet, "/repos/conan/v2/conans/search?q=*", "", http.StatusOK).Body.Bytes(),
		&res,
	); err != nil ||
		len(res.Results) != 0 {
		t.Fatalf("search = %+v, %v; want nothing", res, err)
	}

	srv.must(http.MethodDelete, conanBase, "", http.StatusOK)

	if rows, _ := pg.ListConanRecipeFiles(ctx, repoID, "zlib", "1.3", "_/_"); len(rows) != 0 {
		t.Errorf("legacy rows after delete = %d, want 0", len(rows))
	}
}

func newImmutableRepo(t *testing.T, pg *database.PostgresDB, name, format string) {
	t.Helper()

	systemOrg := "00000000-0000-0000-0000-000000000001"

	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &systemOrg, ImmutableReleases: true}
	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", name, err)
	}
}

// IVY-2 / SBT-1 / IVY-1 against the real unique indexes: publishing a file again
// overwrites it (it was a unique violation and a 500), a multi-segment group is
// one dotted group, and HEAD answers.
func TestIvyAndSbtRepublishAgainstPostgres(t *testing.T) {
	pg := freshPostgres(t, 5)
	newRepo(t, pg, "ivy", "ivy")
	newRepo(t, pg, "sbt", "sbt")

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	ivyH := ivy.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost")
	sbtH := sbt.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost")

	for _, tc := range []struct {
		name   string
		repo   string
		reg    func(chi.Router)
		layout string
	}{
		{"ivy", "ivy", ivyH.RegisterRepoRoutes, "com/example/lib/1.0/lib-1.0.jar"},
		{"sbt", "sbt", sbtH.RegisterRepoRoutes, "com/example/lib_2.13/1.0/lib_2.13-1.0.jar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFormatServer(t, tc.reg)
			url := "/repos/" + tc.repo + "/" + tc.layout

			srv.must(http.MethodHead, url, "", http.StatusNotFound)
			srv.must(http.MethodPut, url, "first", http.StatusCreated)

			head := srv.must(http.MethodHead, url, "", http.StatusOK)
			if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "5" {
				t.Errorf("HEAD body = %q, Content-Length = %q", head.Body.String(), head.Header().Get("Content-Length"))
			}

			// The same bytes again, then different bytes: both succeed.
			srv.must(http.MethodPut, url, "first", http.StatusCreated)
			srv.must(http.MethodPut, url, "second!", http.StatusCreated)

			if got := srv.must(http.MethodGet, url, "", http.StatusOK).Body.String(); got != "second!" {
				t.Errorf("downloaded %q after overwrite, want second!", got)
			}

			// The dotted group is the same coordinate.
			dotted := strings.Replace(tc.layout, "com/example/", "com.example/", 1)
			if got := srv.must(http.MethodGet, "/repos/"+tc.repo+"/"+dotted, "", http.StatusOK).Body.String(); got != "second!" {
				t.Errorf("GET via dotted group = %q", got)
			}

			srv.must(http.MethodDelete, url, "", http.StatusNoContent)
			srv.must(http.MethodGet, url, "", http.StatusNotFound)
		})
	}
}

func TestIvyImmutableReleaseRefusesOverwriteAgainstPostgres(t *testing.T) {
	pg := freshPostgres(t, 5)
	newImmutableRepo(t, pg, "ivy", "ivy")

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	srv := newFormatServer(t, ivy.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost").RegisterRepoRoutes)

	release := "/repos/ivy/com/example/lib/1.0/lib-1.0.jar"
	snapshot := "/repos/ivy/com/example/lib/1.1-SNAPSHOT/lib-1.1-SNAPSHOT.jar"

	srv.must(http.MethodPut, release, "first", http.StatusCreated)
	srv.must(http.MethodPut, snapshot, "first", http.StatusCreated)

	srv.must(http.MethodPut, release, "tampered", http.StatusConflict)
	srv.must(http.MethodDelete, release, "", http.StatusConflict)

	if body, _ := io.ReadAll(srv.must(http.MethodGet, release, "", http.StatusOK).Body); string(body) != "first" {
		t.Errorf("release = %q after refused overwrite", body)
	}

	srv.must(http.MethodPut, snapshot, "second", http.StatusCreated)
}
