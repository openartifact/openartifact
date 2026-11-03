// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/repodispatch"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
	xmod "golang.org/x/mod/module"
	xzip "golang.org/x/mod/zip"
)

const systemOrg = "00000000-0000-0000-0000-000000000001"

// formatServer is the real route table of the server (every format mounted as
// in production, the repository dispatcher in front) on a Postgres database
// and a filesystem store, behind an httptest server.
type formatServer struct {
	t    *testing.T
	pg   *database.PostgresDB
	url  string
	http *http.Client
}

func newFormatServer(t *testing.T) *formatServer {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	name := "oa_http_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: name, SSLMode: "disable", MaxOpenConns: 8, MaxIdleConns: 8,
	}, log)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	fsStore, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	a := &app{cfg: &config.Config{}, log: log, reg: metrics.NewRegistryOn(prometheus.NewRegistry()), db: pg}
	a.web.authed = chi.NewRouter()
	a.store.blobs = storage.NewMetricsBlobStore(fsStore, a.reg)
	a.store.served = presign.Wrap(a.store.blobs, pg, presign.SettingsFromConfig(a.cfg.Storage), a.reg, log)

	// Stand-in for the RBAC middleware: every request is an authenticated admin.
	a.web.authed.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := &auth.Principal{Username: "admin", IsLegacy: true, Credential: auth.CredentialLegacy}
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	})

	if err := a.newFormatHandlers(); err != nil {
		t.Fatal(err)
	}

	if err := a.mountFormats(repodispatch.New(log, pg, nil, nil, a.hostedRouters())); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(a.web.authed)
	t.Cleanup(srv.Close)

	return &formatServer{t: t, pg: pg, url: srv.URL, http: srv.Client()}
}

func (s *formatServer) repo(name, format string) {
	s.t.Helper()

	org := systemOrg

	r := &models.Repository{Name: name, Format: format, RepoType: "hosted", Public: true, ProjectID: &org}
	if err := s.pg.CreateRepository(s.t.Context(), r); err != nil {
		s.t.Fatalf("create %s repository: %v", format, err)
	}
}

func (s *formatServer) do(method, path string, body []byte, headers ...string) (int, http.Header, []byte) {
	s.t.Helper()

	req, err := http.NewRequestWithContext(s.t.Context(), method, s.url+path, bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	// A caller that sets Accept-Encoding itself gets the raw body back.
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}

	return resp.StatusCode, resp.Header, out
}

func (s *formatServer) mustDo(want int, method, path string, body []byte, headers ...string) (http.Header, []byte) {
	s.t.Helper()

	code, h, out := s.do(method, path, body, headers...)
	if code != want {
		s.t.Fatalf("%s %s = %d, want %d; body=%q", method, path, code, want, out)
	}

	return h, out
}

// moduleZip builds the zip of module path at version, the one `go` would
// publish, with a go.mod and one source file.
func moduleZip(t *testing.T, path, version string) []byte {
	t.Helper()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+path+"\n\ngo 1.20\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pkg := path[strings.LastIndexByte(path, '/')+1:]
	pkg = strings.NewReplacer(".", "", "-", "").Replace(pkg)

	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package "+pkg+"\n\nconst V = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := xzip.CreateFromDir(&buf, xmod.Version{Path: path, Version: version}, dir); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func esc(t *testing.T, p string) string {
	t.Helper()

	e, err := xmod.EscapePath(p)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

// TestGoModulePathsOfAnyDepth drives the real go client against a hosted Go
// repository for module paths of one, two and three segments, whose GOPROXY
// URLs have as many segments as terraform's and npm's routes: they used to be
// answered by those ("repository not found"), and a terraform repository on the
// same mount must still answer its own routes.
func TestGoModulePathsOfAnyDepth(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go binary")
	}

	s := newFormatServer(t)
	s.repo("gor", "go")
	s.repo("tf", "terraform")

	modules := []string{"example.com", "example.com/mod", "example.com/org/mod"}
	for _, m := range modules {
		s.mustDo(http.StatusCreated, http.MethodPut, "/repos/gor/"+esc(t, m)+"/@v/v1.2.3", moduleZip(t, m, "v1.2.3"))
	}

	// Plain protocol: every endpoint of every depth.
	for _, m := range modules {
		base := "/repos/gor/" + esc(t, m)

		_, body := s.mustDo(http.StatusOK, http.MethodGet, base+"/@latest", nil)

		var info struct{ Version string }
		if err := json.Unmarshal(body, &info); err != nil || info.Version != "v1.2.3" {
			t.Errorf("%s @latest = %q (%v)", m, body, err)
		}

		_, body = s.mustDo(http.StatusOK, http.MethodGet, base+"/@v/list", nil)
		if strings.TrimSpace(string(body)) != "v1.2.3" {
			t.Errorf("%s list = %q", m, body)
		}

		s.mustDo(http.StatusOK, http.MethodGet, base+"/@v/v1.2.3.info", nil)
		s.mustDo(http.StatusOK, http.MethodGet, base+"/@v/v1.2.3.mod", nil)
		s.mustDo(http.StatusOK, http.MethodGet, base+"/@v/v1.2.3.zip", nil)
	}

	// A repository of another format is not served as a Go proxy.
	s.mustDo(http.StatusNotFound, http.MethodGet, "/repos/tf/example.com/mod/@latest", nil)

	// The real client.
	env := append(os.Environ(),
		"GOPROXY="+s.url+"/repos/gor", "GOSUMDB=off", "GOFLAGS=-mod=mod -modcacherw", "GOTOOLCHAIN=local",
		"GOMODCACHE="+t.TempDir(), "GOCACHE="+t.TempDir(), "GOPATH="+t.TempDir(), "GONOSUMCHECK=1", "GOWORK=off",
	)

	consumer := t.TempDir()
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte("module consumer\n\ngo 1.20\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), goBin, args...)
		cmd.Dir, cmd.Env = consumer, env

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}

		return string(out)
	}

	for _, m := range modules {
		if out := run("list", "-m", m+"@latest"); strings.TrimSpace(out) != m+" v1.2.3" {
			t.Errorf("go list -m %s@latest = %q", m, out)
		}

		if out := run("list", "-m", "-versions", m); strings.TrimSpace(out) != m+" v1.2.3" {
			t.Errorf("go list -m -versions %s = %q", m, out)
		}

		run("get", m+"@latest")
	}

	gomod, err := os.ReadFile(filepath.Join(consumer, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range modules {
		if !strings.Contains(string(gomod), m+" v1.2.3") {
			t.Errorf("go get did not require %s:\n%s", m, gomod)
		}
	}

	// Terraform on the same mount still answers its registry routes.
	s.mustDo(http.StatusCreated, http.MethodPut, "/repos/tf/_api/modules/vpc/aws/1.0.0", tfModule(t))

	_, body := s.mustDo(http.StatusOK, http.MethodGet, "/repos/tf/vpc/aws/versions", nil)
	if !strings.Contains(string(body), `"1.0.0"`) {
		t.Errorf("terraform versions = %s", body)
	}

	_, body = s.mustDo(http.StatusOK, http.MethodGet, "/repos/tf/vpc/aws/1.0.0", nil)
	if !strings.Contains(string(body), `"version":"1.0.0"`) {
		t.Errorf("terraform version = %s", body)
	}

	_, body = s.mustDo(http.StatusOK, http.MethodGet, "/repos/tf/vpc/aws", nil)
	if !strings.Contains(string(body), `"1.0.0"`) {
		t.Errorf("terraform latest = %s", body)
	}
}

// tfModule is a Terraform module archive: one root .tf file.
func tfModule(t *testing.T) []byte {
	t.Helper()

	var raw bytes.Buffer

	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	content := []byte("variable \"x\" {}\n")

	if err := tw.WriteHeader(&tar.Header{Name: "main.tf", Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}

	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return raw.Bytes()
}

// TestNPMVersionDocuments: GET /{pkg}/{version} and /@scope/pkg/{version} of a
// hosted npm repository return the version document, "latest" and any other
// dist-tag resolve, and an unknown version is a 404. Conda's two-segment and
// terraform's three-segment routes used to answer these URLs.
func TestNPMVersionDocuments(t *testing.T) {
	s := newFormatServer(t)
	s.repo("np", "npm")

	publish := func(name, version string) {
		t.Helper()

		tarball := []byte("tarball of " + name + version)
		sum := sha256.Sum256(tarball)
		file := name[strings.LastIndexByte(name, '/')+1:] + "-" + version + ".tgz"
		doc := map[string]any{
			"name":      name,
			"dist-tags": map[string]string{"latest": version, "next": version},
			"versions": map[string]any{version: map[string]any{
				"name": name, "version": version, "description": "doc of " + version,
				"dist": map[string]string{"tarball": "http://evil.example/x.tgz", "shasum": hex.EncodeToString(sum[:20])},
			}},
			"_attachments": map[string]any{file: map[string]any{
				"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tarball), "length": len(tarball),
			}},
		}

		body, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}

		s.mustDo(http.StatusCreated, http.MethodPut, "/repos/np/"+strings.ReplaceAll(name, "/", "%2f"), body,
			"Content-Type", "application/json")
	}

	publish("left-pad", "1.0.0")
	publish("@acme/lib", "2.1.0")

	for _, tc := range []struct{ path, name, version string }{
		{"/repos/np/left-pad/1.0.0", "left-pad", "1.0.0"},
		{"/repos/np/left-pad/latest", "left-pad", "1.0.0"},
		{"/repos/np/left-pad/next", "left-pad", "1.0.0"},
		{"/repos/np/@acme/lib/2.1.0", "@acme/lib", "2.1.0"},
		{"/repos/np/@acme%2flib/2.1.0", "@acme/lib", "2.1.0"},
		{"/repos/np/@acme/lib/latest", "@acme/lib", "2.1.0"},
	} {
		_, body := s.mustDo(http.StatusOK, http.MethodGet, tc.path, nil)

		var doc struct {
			Name, Version string
			Dist          struct{ Tarball string }
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: %v: %s", tc.path, err, body)
		}

		if doc.Name != tc.name || doc.Version != tc.version {
			t.Errorf("%s = %s@%s, want %s@%s", tc.path, doc.Name, doc.Version, tc.name, tc.version)
		}

		if strings.Contains(doc.Dist.Tarball, "evil.example") || !strings.HasPrefix(doc.Dist.Tarball, s.url+"/repos/np/") {
			t.Errorf("%s: tarball %q is not this server's", tc.path, doc.Dist.Tarball)
		}
	}

	for _, p := range []string{"/repos/np/left-pad/9.9.9", "/repos/np/@acme/lib/9.9.9", "/repos/np/nope/1.0.0"} {
		s.mustDo(http.StatusNotFound, http.MethodGet, p, nil)
	}

	// The packument and the tarball are unaffected.
	s.mustDo(http.StatusOK, http.MethodGet, "/repos/np/left-pad", nil)
	s.mustDo(http.StatusOK, http.MethodGet, "/repos/np/left-pad/-/left-pad-1.0.0.tgz", nil)
	s.mustDo(http.StatusOK, http.MethodGet, "/repos/np/@acme/lib/-/lib-2.1.0.tgz", nil)

	// A repository of another format does not answer npm documents.
	s.repo("rawr", "helm")
	s.mustDo(http.StatusNotFound, http.MethodGet, "/repos/rawr/left-pad/1.0.0", nil)
}

// TestDockerBlobRange: a blob GET honours a single Range (206 with
// Content-Range, 416 past the end), a plain GET and HEAD stay as they were.
func TestDockerBlobRange(t *testing.T) {
	s := newFormatServer(t)
	s.repo("reg", "docker")

	blob := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	sum := sha256.Sum256(blob)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	blobURL := "/v2/reg/app/blobs/" + digest

	h, _ := s.mustDo(http.StatusAccepted, http.MethodPost, "/v2/reg/app/blobs/uploads/", nil)
	s.mustDo(http.StatusCreated, http.MethodPut, h.Get("Location")+"?digest="+digest, blob)

	h, body := s.mustDo(http.StatusOK, http.MethodGet, blobURL, nil)
	if !bytes.Equal(body, blob) || h.Get("Docker-Content-Digest") != digest ||
		h.Get("Content-Length") != strconv.Itoa(len(blob)) || h.Get("Accept-Ranges") != "bytes" {
		t.Errorf("plain GET: %d bytes, headers %v", len(body), h)
	}

	for _, tc := range []struct {
		rng        string
		start, end int // inclusive
	}{
		{"bytes=0-99", 0, 99},
		{"bytes=100-199", 100, 199},
		{"bytes=65000-", 65000, len(blob) - 1},
		{"bytes=-10", len(blob) - 10, len(blob) - 1},
		{"bytes=65530-70000", 65530, len(blob) - 1}, // an end past the blob is clamped
	} {
		h, body := s.mustDo(http.StatusPartialContent, http.MethodGet, blobURL, nil, "Range", tc.rng)

		want := blob[tc.start : tc.end+1]
		if !bytes.Equal(body, want) {
			t.Errorf("%s: got %d bytes, want the %d of %d-%d", tc.rng, len(body), len(want), tc.start, tc.end)
		}

		wantCR := "bytes " + strconv.Itoa(tc.start) + "-" + strconv.Itoa(tc.end) + "/" + strconv.Itoa(len(blob))
		if h.Get("Content-Range") != wantCR || h.Get("Content-Length") != strconv.Itoa(len(want)) ||
			h.Get("Docker-Content-Digest") != digest {
			t.Errorf("%s: Content-Range %q (want %q), Content-Length %q, digest %q",
				tc.rng, h.Get("Content-Range"), wantCR, h.Get("Content-Length"), h.Get("Docker-Content-Digest"))
		}
	}

	h, _ = s.mustDo(http.StatusRequestedRangeNotSatisfiable, http.MethodGet, blobURL, nil, "Range", "bytes=65536-")
	if h.Get("Content-Range") != "bytes */"+strconv.Itoa(len(blob)) {
		t.Errorf("416 Content-Range = %q", h.Get("Content-Range"))
	}

	// HEAD is unaffected: no body, size and digest of the whole blob.
	h, body = s.mustDo(http.StatusOK, http.MethodHead, blobURL, nil)
	if len(body) != 0 || h.Get("Content-Length") != strconv.Itoa(len(blob)) || h.Get("Docker-Content-Digest") != digest {
		t.Errorf("HEAD: %d bytes, headers %v", len(body), h)
	}

	s.mustDo(http.StatusNotFound, http.MethodGet, "/v2/reg/app/blobs/sha256:"+strings.Repeat("0", 64), nil, "Range", "bytes=0-9")
}

// TestRawDownloadValidatorsAndRange: a hosted raw file has a strong ETag (its
// sha256), Last-Modified and byte ranges; an unchanged file revalidates to a
// 304 and a replaced one does not.
func TestRawDownloadValidatorsAndRange(t *testing.T) {
	s := newFormatServer(t)
	s.repo("r", "raw")

	content := bytes.Repeat([]byte("raw content line\n"), 5000)
	sum := sha256.Sum256(content)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`

	s.mustDo(http.StatusCreated, http.MethodPut, "/repos/raw/r/dir/file.bin", content, "Content-Type", "application/octet-stream")

	h, body := s.mustDo(http.StatusOK, http.MethodGet, "/repos/raw/r/dir/file.bin", nil)
	if !bytes.Equal(body, content) || h.Get("ETag") != etag || h.Get("Accept-Ranges") != "bytes" ||
		h.Get("Last-Modified") == "" || h.Get("Content-Length") != strconv.Itoa(len(content)) ||
		h.Get("X-Checksum-Sha256") != hex.EncodeToString(sum[:]) {
		t.Errorf("GET: %d bytes, headers %v", len(body), h)
	}

	// HEAD describes the same representation.
	hh, hbody := s.mustDo(http.StatusOK, http.MethodHead, "/repos/raw/r/dir/file.bin", nil)
	if len(hbody) != 0 || hh.Get("ETag") != etag || hh.Get("Accept-Ranges") != "bytes" ||
		hh.Get("Last-Modified") != h.Get("Last-Modified") {
		t.Errorf("HEAD: headers %v", hh)
	}

	// Revalidation.
	nm, nbody := s.mustDo(http.StatusNotModified, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "If-None-Match", etag)
	if len(nbody) != 0 || nm.Get("ETag") != etag {
		t.Errorf("304: %d bytes, headers %v", len(nbody), nm)
	}

	s.mustDo(http.StatusNotModified, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "If-None-Match", `"other", `+etag)
	s.mustDo(http.StatusNotModified, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "If-Modified-Since", h.Get("Last-Modified"))
	s.mustDo(http.StatusOK, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "If-None-Match", `"stale"`)

	// Ranges.
	rh, rbody := s.mustDo(http.StatusPartialContent, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "Range", "bytes=17-33")
	if !bytes.Equal(rbody, content[17:34]) || rh.Get("Content-Range") != "bytes 17-33/"+strconv.Itoa(len(content)) {
		t.Errorf("range: %q, %q", rbody, rh.Get("Content-Range"))
	}

	_, rbody = s.mustDo(http.StatusPartialContent, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "Range", "bytes=-5")
	if !bytes.Equal(rbody, content[len(content)-5:]) {
		t.Errorf("suffix range: %q", rbody)
	}

	// If-Range with the current validator keeps the range, a stale one returns the whole file.
	s.mustDo(http.StatusPartialContent, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "Range", "bytes=0-9", "If-Range", etag)

	_, rbody = s.mustDo(http.StatusOK, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "Range", "bytes=0-9", "If-Range", `"stale"`)
	if len(rbody) != len(content) {
		t.Errorf("stale If-Range: %d bytes", len(rbody))
	}

	s.mustDo(http.StatusRequestedRangeNotSatisfiable, http.MethodGet, "/repos/raw/r/dir/file.bin", nil,
		"Range", "bytes="+strconv.Itoa(len(content))+"-")

	// Replacing the file changes the validator: the old one no longer matches.
	other := []byte("replacement")
	s.mustDo(http.StatusCreated, http.MethodPut, "/repos/raw/r/dir/file.bin", other, "Content-Type", "application/octet-stream")

	h2, body2 := s.mustDo(http.StatusOK, http.MethodGet, "/repos/raw/r/dir/file.bin", nil, "If-None-Match", etag)
	if !bytes.Equal(body2, other) || h2.Get("ETag") == etag {
		t.Errorf("replaced file: %q, ETag %q", body2, h2.Get("ETag"))
	}

	// An empty file and an empty range request are served.
	s.mustDo(http.StatusCreated, http.MethodPut, "/repos/raw/r/empty", nil, "Content-Type", "text/plain")
	s.mustDo(http.StatusOK, http.MethodGet, "/repos/raw/r/empty", nil)
}
