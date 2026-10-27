// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	"github.com/openartifact/openartifact/internal/format/formattest"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/apk/apktest"
)

// apkStack is a hosted apk repository on a real database: the HTTP handler,
// the signing key service and the metadata worker wired as in cmd/server, with
// regeneration driven synchronously by the test.
type apkStack struct {
	t      *testing.T
	pg     *database.PostgresDB
	store  *formattest.BlobStore
	worker *ApkMetadataWorker
	router chi.Router
	repo   *models.Repository
}

func newApkStack(t *testing.T, name string) *apkStack {
	t.Helper()

	if err := crypto.Init(strings.Repeat("s", 32)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	pg := newTrashPurgeTestDB(t)
	store := formattest.NewBlobStore()
	log := testLogger(t)
	keys := apkkeys.New(pg, log)
	w := NewApkMetadataWorker(pg, store, keys, log)
	t.Cleanup(w.Stop)

	org := trashTestOrgID
	repo := &models.Repository{Name: name, Format: "apk", RepoType: "hosted", ProjectID: &org}

	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	h := formatapk.NewHTTPHandler(log, pg, store, "http://oa.test").WithKeys(keys).WithRegenerator(w)
	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)
	h.RegisterAPIRoutes(router)

	return &apkStack{t: t, pg: pg, store: store, worker: w, router: router, repo: repo}
}

func (s *apkStack) do(method, path string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	s.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	return w
}

func (s *apkStack) multipart(path string, fields map[string]string, pkg []byte) *httptest.ResponseRecorder {
	s.t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			s.t.Fatal(err)
		}
	}

	part, err := mw.CreateFormFile("file", "pkg.apk")
	if err != nil {
		s.t.Fatal(err)
	}

	if _, err := part.Write(pkg); err != nil {
		s.t.Fatal(err)
	}

	if err := mw.Close(); err != nil {
		s.t.Fatal(err)
	}

	return s.do(http.MethodPost, path, body.Bytes(), "Content-Type", mw.FormDataContentType())
}

func (s *apkStack) put(dir, file string, data []byte) *httptest.ResponseRecorder {
	s.t.Helper()

	return s.do(http.MethodPut, "/repos/"+s.repo.Name+"/"+dir+"/"+file, data)
}

func (s *apkStack) regenerate() {
	s.t.Helper()

	if err := s.worker.regenerate(context.Background(), s.repo.ID); err != nil {
		s.t.Fatalf("regenerate: %v", err)
	}
}

// publicKey fetches /public.key the way a client installs it.
func (s *apkStack) publicKey() (string, *rsa.PublicKey) {
	s.t.Helper()

	w := s.do(http.MethodGet, "/repos/"+s.repo.Name+"/public.key", nil)
	if w.Code != http.StatusOK {
		s.t.Fatalf("public.key: status %d: %s", w.Code, w.Body.String())
	}

	block, _ := pem.Decode(w.Body.Bytes())
	if block == nil {
		s.t.Fatalf("public.key is not PEM: %q", w.Body.String())
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		s.t.Fatal(err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		s.t.Fatalf("public key is %T", pub)
	}

	name := w.Header().Get("Content-Disposition")
	if !strings.Contains(name, "openartifact-"+s.repo.Name+".rsa.pub") {
		s.t.Errorf("Content-Disposition = %q, want the key name", name)
	}

	return "openartifact-" + s.repo.Name + ".rsa.pub", rsaPub
}

// index returns the verified entries of one directory's index.
func (s *apkStack) index(dir string) []pkgapk.Package {
	s.t.Helper()

	keyName, pub := s.publicKey()

	w := s.do(http.MethodGet, "/repos/"+s.repo.Name+"/"+dir+"/APKINDEX.tar.gz", nil)
	if w.Code != http.StatusOK {
		s.t.Fatalf("GET %s index: status %d: %s", dir, w.Code, w.Body.String())
	}

	signedBy, err := pkgapk.VerifyIndex(w.Body.Bytes(), map[string]*rsa.PublicKey{keyName: pub})
	if err != nil || signedBy != keyName {
		s.t.Fatalf("index of %s does not verify against the served key: %q, %v", dir, signedBy, err)
	}

	pkgs, err := pkgapk.ParseIndex(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		s.t.Fatalf("ParseIndex(%s): %v", dir, err)
	}

	return pkgs
}

func names(pkgs []pkgapk.Package) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.Name+"-"+p.Version)
	}

	return out
}

// requireRevalidation checks that path carries an ETag and answers 304 to it.
func (s *apkStack) requireRevalidation(path string) {
	s.t.Helper()

	etag := s.do(http.MethodGet, path, nil).Header().Get("ETag")
	if etag == "" {
		s.t.Fatalf("%s carries no ETag", path)
	}

	if w := s.do(http.MethodGet, path, nil, "If-None-Match", etag); w.Code != http.StatusNotModified {
		s.t.Errorf("conditional GET %s: status %d, want 304", path, w.Code)
	}
}

func TestApkUploadValidation(t *testing.T) {
	s := newApkStack(t, "apk-valid")

	hello := apktest.Build(t, apktest.Options{Name: "hello", Version: "1.0-r0", Arch: "x86_64"})

	for _, tc := range []struct {
		name, dir, file string
		body            []byte
		want            int
	}{
		{"stored", "v3.20/main/x86_64", "hello-1.0-r0.apk", hello, http.StatusCreated},
		{"duplicate", "v3.20/main/x86_64", "hello-1.0-r0.apk", hello, http.StatusConflict},
		{"wrong arch directory", "v3.20/main/aarch64", "hello-1.0-r0.apk", hello, http.StatusBadRequest},
		{"another file name", "v3.20/main/x86_64", "other-9.9-r0.apk", hello, http.StatusBadRequest},
		{"junk", "v3.20/main/x86_64", "junk-1.0-r0.apk", []byte("not an apk"), http.StatusBadRequest},
		{"the index name", "v3.20/main/x86_64", "APKINDEX.tar.gz", hello, http.StatusNotFound},
	} {
		if w := s.put(tc.dir, tc.file, tc.body); w.Code != tc.want {
			t.Errorf("%s: PUT status %d, want %d: %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestApkHostedLifecycle(t *testing.T) {
	s := newApkStack(t, "apk-life")
	ctx := context.Background()

	hello := apktest.Build(
		t,
		apktest.Options{Name: "hello", Version: "1.0-r0", Arch: "x86_64", Depends: []string{"so:libc.musl-x86_64.so.1"}},
	)
	data := apktest.Build(t, apktest.Options{Name: "hello-doc", Version: "1.0-r0", Arch: "noarch"})

	for file, body := range map[string][]byte{"hello-1.0-r0.apk": hello, "hello-doc-1.0-r0.apk": data} {
		if w := s.put("v3.20/main/x86_64", file, body); w.Code != http.StatusCreated {
			t.Fatalf("PUT %s: status %d: %s", file, w.Code, w.Body.String())
		}
	}

	s.regenerate()

	pkgs := s.index("v3.20/main/x86_64")
	if got := strings.Join(names(pkgs), ","); got != "hello-1.0-r0,hello-doc-1.0-r0" {
		t.Fatalf("index entries = %s", got)
	}

	parsed, err := pkgapk.ParsePackage(bytes.NewReader(hello), int64(len(hello)))
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range pkgs {
		if p.Name == "hello" && (p.Checksum != parsed.Checksum || p.Size != int64(len(hello))) {
			t.Errorf("index entry %+v does not identify the uploaded package (%s)", p, parsed.Checksum)
		}
	}

	// A directory that never held a package has no index.
	if w := s.do(http.MethodGet, "/repos/apk-life/v3.20/main/aarch64/APKINDEX.tar.gz", nil); w.Code != http.StatusNotFound {
		t.Errorf("index of an unused directory: status %d, want 404", w.Code)
	}

	w := s.do(http.MethodGet, "/repos/apk-life/v3.20/main/x86_64/hello-1.0-r0.apk", nil)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), hello) {
		t.Fatalf("GET package: status %d, %d bytes", w.Code, w.Body.Len())
	}

	s.requireRevalidation("/repos/apk-life/v3.20/main/x86_64/APKINDEX.tar.gz")

	// Deleting goes to the trash and drops the entry from the regenerated index.
	if w := s.do(http.MethodDelete, "/repos/apk-life/v3.20/main/x86_64/hello-1.0-r0.apk", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d: %s", w.Code, w.Body.String())
	}

	if w := s.do(http.MethodGet, "/repos/apk-life/v3.20/main/x86_64/hello-1.0-r0.apk", nil); w.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE: status %d, want 404", w.Code)
	}

	s.regenerate()

	if got := strings.Join(names(s.index("v3.20/main/x86_64")), ","); got != "hello-doc-1.0-r0" {
		t.Errorf("index after delete = %q", got)
	}

	rows, err := s.pg.ListApkPackages(ctx, s.repo.ID)
	if err != nil || len(rows) != 1 {
		t.Errorf("live rows = %d, %v; want 1", len(rows), err)
	}

	// A directory that lost its last package keeps serving an empty signed index.
	if w := s.do(http.MethodDelete, "/repos/apk-life/v3.20/main/x86_64/hello-doc-1.0-r0.apk", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE last package: status %d", w.Code)
	}

	s.regenerate()

	if got := s.index("v3.20/main/x86_64"); len(got) != 0 {
		t.Errorf("index of the emptied directory = %v", names(got))
	}
}

func TestApkIndexIsSignedByTheRotatedKey(t *testing.T) {
	s := newApkStack(t, "apk-rot")
	pkg := apktest.Build(t, apktest.Options{Name: "tool", Arch: "aarch64"})

	if w := s.put("edge/community/aarch64", "tool-1.0-r0.apk", pkg); w.Code != http.StatusCreated {
		t.Fatalf("PUT: status %d: %s", w.Code, w.Body.String())
	}

	s.regenerate()
	_, before := s.publicKey()

	keys := apkkeys.New(s.pg, testLogger(t))
	keys.SetRefresher(s.worker)

	if _, err := keys.Generate(context.Background(), s.repo, "test"); err != nil {
		t.Fatal(err)
	}

	s.regenerate()

	_, after := s.publicKey()
	if before.Equal(after) {
		t.Fatal("the key did not change")
	}

	// index() verifies against the key served now: the old key must not do.
	if got := names(s.index("edge/community/aarch64")); len(got) != 1 {
		t.Errorf("index entries = %v", got)
	}
}

func TestApkRestUploadDefaultsAndNoarch(t *testing.T) {
	s := newApkStack(t, "apk-rest")
	noarch := apktest.Build(t, apktest.Options{Name: "docs", Arch: "noarch"})

	upload := func(fields map[string]string, pkg []byte) *httptest.ResponseRecorder {
		return s.multipart("/api/v1/repositories/apk-rest/apks", fields, pkg)
	}

	if w := upload(nil, noarch); w.Code != http.StatusBadRequest {
		t.Errorf("noarch without arch: status %d, want 400: %s", w.Code, w.Body.String())
	}

	if w := upload(map[string]string{"arch": "x86_64"}, noarch); w.Code != http.StatusOK {
		t.Fatalf("noarch with arch: status %d: %s", w.Code, w.Body.String())
	}

	s.regenerate()

	if got := names(s.index("edge/main/x86_64")); len(got) != 1 || got[0] != "docs-1.0-r0" {
		t.Errorf("default branch/repo index = %v", got)
	}
}
