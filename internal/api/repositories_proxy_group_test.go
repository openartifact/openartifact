// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
)

type proxyGroupMockDB struct {
	repoCreateMockDB

	existing map[string]*models.Repository
}

func (db *proxyGroupMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return db.existing[name], nil
}

func createRepo(t *testing.T, db *proxyGroupMockDB, body string) *httptest.ResponseRecorder {
	t.Helper()

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(&db.repoCreateMockDB, http.MethodPost, "/api/v1/repositories", body))

	return rw
}

func newProxyGroupDB() *proxyGroupMockDB {
	db := &proxyGroupMockDB{
		existing: map[string]*models.Repository{
			"hosted-maven": {ID: "1", Name: "hosted-maven", Format: "maven", RepoType: "hosted"},
			"proxy-maven":  {ID: "2", Name: "proxy-maven", Format: "maven", RepoType: "proxy"},
			"group-maven":  {ID: "3", Name: "group-maven", Format: "maven", RepoType: "group"},
		},
	}
	db.targets = adminTargets()

	return db
}

// Every real format supports proxy and group repositories, so the guard is
// exercised with a name no handler serves.
func TestUnsupportedRepoTypeMessage(t *testing.T) {
	for _, repoType := range []string{"proxy", "group"} {
		if msg := unsupportedRepoTypeMessage(repoType, "bogus"); !strings.Contains(msg, "not supported for format") {
			t.Errorf("%s of bogus: message %q", repoType, msg)
		}

		for _, format := range repotype.Names() {
			if repoType == "group" && format == "pacman" {
				continue // pacman groups arrive in a later change
			}

			if format == "composer" {
				continue // hosted only: composer proxy and group arrive in a later change
			}

			if msg := unsupportedRepoTypeMessage(repoType, format); msg != "" {
				t.Errorf("%s of %s: unexpected message %q", repoType, format, msg)
			}
		}
	}

	if msg := unsupportedRepoTypeMessage("hosted", "bogus"); msg != "" {
		t.Errorf("hosted: message %q", msg)
	}
}

func TestCreateProxyRejectsUnsafeUpstreams(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "")

	for name, url := range map[string]string{
		"loopback":       "http://127.0.0.1:8080/",
		"metadata":       "http://169.254.169.254/latest/",
		"private":        "http://10.0.0.5/",
		"userinfo":       "https://user:pw@93.184.216.34/",
		"query":          "https://93.184.216.34/?token=x",
		"unsupported":    "ftp://93.184.216.34/",
		"missing scheme": "93.184.216.34/maven2",
	} {
		db := newProxyGroupDB()
		rw := createRepo(t, db, `{"name":"p","format":"maven","repo_type":"proxy","upstream_url":"`+url+`"}`)

		if rw.Code != http.StatusBadRequest || db.created != nil {
			t.Errorf("%s: status %d created %v body %s", name, rw.Code, db.created != nil, rw.Body.String())
		}
	}
}

func TestCreateProxyAcceptsSupportedFormats(t *testing.T) {
	for _, format := range []string{"cargo", "conan", "conda", "deb", "docker", "go", "helm", "ivy", "maven", "npm", "nuget", "pypi", "raw", "rpm", "rubygems", "sbt", "terraform"} {
		db := newProxyGroupDB()
		rw := createRepo(
			t,
			db,
			`{"name":"p","format":"`+format+`","repo_type":"proxy","upstream_url":"https://93.184.216.34/repo/","upstream_username":"u","upstream_password":"p"}`,
		)

		if rw.Code != http.StatusCreated || db.created == nil || db.created.RepoType != "proxy" {
			t.Errorf("%s: status %d body %s", format, rw.Code, rw.Body.String())
			continue
		}

		if strings.Contains(rw.Body.String(), `"upstream_password"`) || strings.Contains(rw.Body.String(), "upstream_password_enc") {
			t.Errorf("%s: response exposes the upstream password: %s", format, rw.Body.String())
		}

		if !strings.Contains(rw.Body.String(), `"upstream_url":"https://93.184.216.34/repo/"`) {
			t.Errorf("%s: response lacks upstream_url: %s", format, rw.Body.String())
		}
	}
}

func TestCreateProxyDefaultUpstream(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "")

	for format, want := range map[string]string{
		"pypi":     "https://pypi.org/simple",
		"cargo":    "https://index.crates.io",
		"go":       "https://proxy.golang.org",
		"nuget":    "https://api.nuget.org/v3/index.json",
		"rubygems": "https://rubygems.org",
		"conan":    "https://center2.conan.io",

		"terraform": "https://registry.terraform.io",
	} {
		db := newProxyGroupDB()
		if rw := createRepo(t, db, `{"name":"p","format":"`+format+`","repo_type":"proxy"}`); rw.Code != http.StatusCreated ||
			db.created == nil || db.created.UpstreamURL != want {
			t.Errorf("%s proxy without upstream_url: status %d created %+v body %s", format, rw.Code, db.created, rw.Body.String())
		}
	}

	// Formats without a well-known public upstream still require one.
	db := newProxyGroupDB()
	if rw := createRepo(t, db, `{"name":"p","format":"maven","repo_type":"proxy"}`); rw.Code != http.StatusBadRequest || db.created != nil {
		t.Errorf("maven proxy without upstream_url: status %d body %s", rw.Code, rw.Body.String())
	}
}

func TestCreateDockerProxyDefaultsToDockerHub(t *testing.T) {
	db := newProxyGroupDB()

	rw := createRepo(t, db, `{"name":"hub","format":"docker","repo_type":"proxy"}`)
	if rw.Code != http.StatusCreated || db.created == nil || db.created.UpstreamURL != "https://registry-1.docker.io" {
		t.Fatalf("status %d body %s", rw.Code, rw.Body.String())
	}

	// Other formats still require an explicit upstream.
	db = newProxyGroupDB()
	if rw := createRepo(t, db, `{"name":"m","format":"maven","repo_type":"proxy"}`); rw.Code != http.StatusBadRequest || db.created != nil {
		t.Errorf("maven proxy without upstream_url: status %d", rw.Code)
	}
}

func TestCreateDockerGroupIsAccepted(t *testing.T) {
	db := newProxyGroupDB()

	rw := createRepo(t, db, `{"name":"g","format":"docker","repo_type":"group"}`)
	if rw.Code != http.StatusCreated || db.created == nil || db.created.RepoType != "group" {
		t.Errorf("status %d body %s", rw.Code, rw.Body.String())
	}
}

func TestCreateGroupMemberValidation(t *testing.T) {
	cases := []struct {
		name, members string
		want          int
	}{
		{"hosted and proxy", `["hosted-maven","proxy-maven"]`, http.StatusCreated},
		{"nested group", `["group-maven"]`, http.StatusBadRequest},
		{"unknown member", `["nope"]`, http.StatusBadRequest},
		{"duplicate", `["hosted-maven","hosted-maven"]`, http.StatusBadRequest},
	}

	for _, c := range cases {
		db := newProxyGroupDB()
		rw := createRepo(t, db, `{"name":"g","format":"maven","repo_type":"group","member_repos":`+c.members+`}`)

		if rw.Code != c.want {
			t.Errorf("%s: status %d, want %d; body %s", c.name, rw.Code, c.want, rw.Body.String())
		}
	}
}

func TestCreateGroupRejectsMemberOfOtherFormat(t *testing.T) {
	db := newProxyGroupDB()
	db.existing["raw-repo"] = &models.Repository{ID: "9", Name: "raw-repo", Format: "raw", RepoType: "hosted"}

	rw := createRepo(t, db, `{"name":"g","format":"maven","repo_type":"group","member_repos":["raw-repo"]}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400; body %s", rw.Code, rw.Body.String())
	}
}

func TestUploadsAndDeletesToProxyAndGroupAreRefused(t *testing.T) {
	log := newFixTestLogger(t)
	db := newProxyGroupDB()
	db.existing["proxy-deb"] = &models.Repository{ID: "4", Name: "proxy-deb", Format: "deb", RepoType: "proxy"}
	db.existing["proxy-rpm"] = &models.Repository{ID: "5", Name: "proxy-rpm", Format: "rpm", RepoType: "proxy"}

	router := chi.NewRouter()
	NewMavenUploadHandler(log, db, nil, nil).Register(router)
	NewRPMUploadHandler(log, db, nil, nil).Register(router)
	NewDebUploadHandler(log, db, nil, nil).Register(router)

	cases := []struct{ method, path string }{
		{http.MethodPut, "/repos/maven/proxy-maven/g/a/1.0/a-1.0.jar"},
		{http.MethodDelete, "/repos/maven/proxy-maven/g/a/1.0/a-1.0.jar"},
		{http.MethodPut, "/repos/maven/group-maven/g/a/1.0/a-1.0.jar"},
		{http.MethodPost, "/api/v1/repositories/proxy-rpm/packages"},
		{http.MethodDelete, "/api/v1/repositories/proxy-rpm/packages/abc"},
		{http.MethodPost, "/api/v1/repositories/proxy-deb/packages"},
		{http.MethodDelete, "/api/v1/repositories/proxy-deb/packages/abc"},
	}

	for _, c := range cases {
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), c.method, c.path, strings.NewReader("x")))

		if rw.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405; body %s", c.method, c.path, rw.Code, rw.Body.String())
		}
	}

	var form bytes.Buffer

	mw := multipart.NewWriter(&form)
	fw, _ := mw.CreateFormFile("file", "a.deb")
	_, _ = fw.Write([]byte("x"))
	_ = mw.Close()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories/proxy-deb/debs", &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusMethodNotAllowed {
		t.Errorf("deb upload = %d, want 405; body %s", rw.Code, rw.Body.String())
	}
}

func TestCreateRejectsReservedRepoNames(t *testing.T) {
	for _, name := range []string{"new", "settings", "trash", "ivy", "maven", "raw", "sbt", "New"} {
		db := newProxyGroupDB()
		rw := createRepo(t, db, `{"name":"`+name+`","format":"npm"}`)

		if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "reserved") {
			t.Errorf("%s: status %d body %s", name, rw.Code, rw.Body.String())
		}

		if db.created != nil {
			t.Errorf("%s: repository was created", name)
		}
	}
}
