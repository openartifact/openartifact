// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	conanRRev  = "cac0f6daea041b0ccf42934163defb20"
	conanPID   = "d0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3"
	conanPRev  = "0123456789abcdef0123456789abcdef"
	conanBase  = "v2/conans/zlib/1.3.1/_/_"
	conanRRoot = conanBase + "/revisions/" + conanRRev
	conanPRoot = conanRRoot + "/packages/" + conanPID + "/revisions/" + conanPRev

	conanLatestDoc = `{"revision":"` + conanRRev + `","time":"2025-12-09T12:51:39.337+0000"}`
	conanRevsDoc   = `{"reference":"zlib/1.3.1","revisions":[{"revision":"` + conanRRev + `","time":"2025-12-09T12:51:39.337+0000"}]}`
	conanFilesDoc  = `{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`
)

// fakeConan is an upstream Conan 2 remote serving zlib/1.3.1.
type fakeConan struct {
	mu sync.Mutex

	paths  []string
	auths  []string
	token  string // when set, requests need "Bearer <token>"; users authenticate to it
	user   string
	pass   string
	noAuth bool // the authenticate endpoint does not exist (404)
	down   atomic.Bool

	archive  []byte
	filesDoc string
	revsDoc  string
	logins   atomic.Int32
	searches []string
	v1       bool // serves the Conan 1 search and snapshot endpoints
}

func newFakeConan() *fakeConan {
	var gz bytes.Buffer

	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("package contents"))
	_ = zw.Close()

	return &fakeConan{archive: gz.Bytes(), filesDoc: conanFilesDoc, revsDoc: conanRevsDoc}
}

func (c *fakeConan) seen(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0

	for _, p := range c.paths {
		if p == "/"+path {
			n++
		}
	}

	return n
}

func (c *fakeConan) handler(w http.ResponseWriter, r *http.Request) {
	if c.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.auths = append(c.auths, r.Header.Get("Authorization"))
	token, user, pass, noAuth, files, revs := c.token, c.user, c.pass, c.noAuth, c.filesDoc, c.revsDoc
	c.mu.Unlock()

	if r.URL.Path == "/v2/users/authenticate" {
		c.authenticate(w, r, user, pass, token, noAuth)
		return
	}

	if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, "/")

	switch rel {
	case conanBase + "/latest":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, conanLatestDoc)
	case conanBase + "/revisions":
		_, _ = io.WriteString(w, revs)
	case conanRRoot + "/files", conanPRoot + "/files":
		_, _ = io.WriteString(w, files)
	case conanRRoot + "/files/conanfile.py":
		_, _ = io.WriteString(w, "from conan import ConanFile\n")
	case conanPRoot + "/files/conan_package.tgz":
		_, _ = w.Write(c.archive)
	case conanRRoot + "/files/conan_sources.tgz":
		_, _ = io.WriteString(w, "<html>captive portal</html>")
	case "v2/conans/search":
		c.mu.Lock()
		c.searches = append(c.searches, r.URL.RawQuery)
		c.mu.Unlock()
		_, _ = io.WriteString(w, `{"results":["zlib/1.3.1@_/_"]}`)
	case conanRRoot + "/search":
		_, _ = io.WriteString(w, `{"`+conanPID+`":{"settings":{"os":"Linux"}}}`)
	case conanRRoot + "/packages/" + conanPID + "/latest":
		_, _ = io.WriteString(w, `{"revision":"`+conanPRev+`","time":"2025-12-09T12:51:39.337+0000"}`)
	case conanPRoot + "/files/conanmanifest.txt":
		_, _ = io.WriteString(w, "1\n")
	case "v1/conans/search":
		_, _ = io.WriteString(w, `{"results":["zlib/1.3.1@_/_"]}`)
	case "v1/conans/zlib/1.3.1/_/_/search":
		_, _ = io.WriteString(w, `{"`+conanPID+`":{"settings":{"os":"Linux"}}}`)
	case "v1/conans/zlib/1.3.1/_/_":
		_, _ = io.WriteString(w, `{"conanfile.py":"d41d8cd98f00b204e9800998ecf8427e"}`)
	case "v1/conans/zlib/1.3.1/_/_/packages/" + conanPID:
		_, _ = io.WriteString(w, `{"conaninfo.txt":"d41d8cd98f00b204e9800998ecf8427e"}`)
	default:
		http.NotFound(w, r)
	}
}

func (c *fakeConan) authenticate(w http.ResponseWriter, r *http.Request, user, pass, token string, noAuth bool) {
	if noAuth {
		http.NotFound(w, r)
		return
	}

	c.logins.Add(1)

	u, p, ok := r.BasicAuth()
	if !ok || u != user || p != pass {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}

	_, _ = io.WriteString(w, token)
}

func conanFixture(t *testing.T) (*fixture, *fakeConan) {
	t.Helper()

	c := newFakeConan()
	f := newFixture(t, c.handler)
	f.repo.Format = "conan"
	f.repo.Name = "conan-proxy"
	f.repo.UpstreamURL = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")

	return f, c
}

func TestConanProxy_RevisionDocumentsAreCachedAndServedAsJSON(t *testing.T) {
	f, c := conanFixture(t)

	for _, tc := range []struct{ path, want string }{
		{conanBase + "/latest", conanLatestDoc},
		{conanBase + "/revisions", conanRevsDoc},
	} {
		for range 2 {
			w := f.do(http.MethodGet, tc.path)
			if w.Code != http.StatusOK || w.Body.String() != tc.want {
				t.Fatalf("GET %s = %d %q", tc.path, w.Code, w.Body.String())
			}

			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("GET %s Content-Type = %q, want application/json", tc.path, ct)
			}
		}

		if n := c.seen(tc.path); n != 1 {
			t.Errorf("upstream saw %s %d times, want 1", tc.path, n)
		}
	}

	// Documents that move are cached for the TTL, not for ever.
	if e := f.db.entry(
		"repo-1",
		conanBase+"/latest",
	); e == nil ||
		time.Until(e.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("latest entry = %+v, want an entry that expires within the TTL", e)
	}
}

func TestConanProxy_StaleRevisionListServedWhileUpstreamIsDown(t *testing.T) {
	f, c := conanFixture(t)

	if w := f.do(http.MethodGet, conanBase+"/revisions"); w.Code != http.StatusOK {
		t.Fatalf("first GET = %d", w.Code)
	}

	f.db.expire("repo-1", conanBase+"/revisions")
	c.down.Store(true)

	w := f.do(http.MethodGet, conanBase+"/revisions")
	if w.Code != http.StatusOK || w.Body.String() != conanRevsDoc || w.Header().Get("Warning") != staleValue {
		t.Fatalf("stale GET = %d %q warning %q", w.Code, w.Body.String(), w.Header().Get("Warning"))
	}
}

func TestConanProxy_FilesAreImmutableOnceTheRevisionIsPublished(t *testing.T) {
	f, c := conanFixture(t)

	for _, path := range []string{conanRRoot + "/files", conanRRoot + "/files/conanfile.py", conanPRoot + "/files/conan_package.tgz"} {
		w := f.do(http.MethodGet, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}

		e := f.db.entry("repo-1", path)
		if e == nil || time.Until(e.ExpiresAt) < 50*365*24*time.Hour {
			t.Errorf("%s entry = %+v, want one that never expires", path, e)
		}

		if f.do(http.MethodGet, path).Code != http.StatusOK || c.seen(path) != 1 {
			t.Errorf("%s fetched %d times, want 1", path, c.seen(path))
		}
	}

	if body := f.do(http.MethodGet, conanPRoot+"/files/conan_package.tgz").Body.Bytes(); !bytes.Equal(body, c.archive) {
		t.Errorf("archive served = %d bytes, want the upstream's %d", len(body), len(c.archive))
	}
}

func TestConanProxy_FileListWithoutManifestIsNotPinned(t *testing.T) {
	f, c := conanFixture(t)
	c.filesDoc = `{"files":{"conanfile.py":{}}}`

	if w := f.do(http.MethodGet, conanRRoot+"/files"); w.Code != http.StatusOK {
		t.Fatalf("GET = %d", w.Code)
	}

	if e := f.db.entry(
		"repo-1",
		conanRRoot+"/files",
	); e == nil ||
		time.Until(e.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("entry of a revision still being uploaded = %+v, want one that expires within the TTL", e)
	}
}

func TestConanProxy_RefusesWhatTheUpstreamAPIDoesNotSend(t *testing.T) {
	f, c := conanFixture(t)
	c.revsDoc = `<html>captive portal</html>`

	for _, path := range []string{
		conanBase + "/revisions",                // not JSON
		conanRRoot + "/files/conan_sources.tgz", // HTML where an archive is expected
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Errorf("GET %s = %d, want 502", path, w.Code)
		}

		if e := f.db.entry("repo-1", path); e != nil {
			t.Errorf("%s was cached: %+v", path, e)
		}
	}
}

func TestConanProxy_RefusesPathsOutsideTheReadAPIWithoutUpstreamRequest(t *testing.T) {
	f, _ := conanFixture(t)

	for _, path := range []string{
		"v2/conans/zlib/1.3.1/_/_", "v2/conans/zlib/1.3.1/_/_/revisions/../files", "v2/conans/../etc/passwd",
		conanRRoot + "/files/../../x", conanRRoot + "/files/a%2fb", "v2/ping", "v2/users/authenticate",
		"v2/conans/zlib/1.3.1/_/_/revisions/" + conanRRev + "/files//x", "v1/conans/zlib/1.3.1/_/_/upload_urls",
		"v1/conans/zlib/1.3.1/_/_/remove_files", "v1/files/zlib/1.3.1/_/_/x/export/../y", "api/v1/crates/x",
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}

	if f.hits.Load() != 0 {
		t.Errorf("upstream requests = %d, want 0", f.hits.Load())
	}
}

func TestConanProxy_SearchIsPassedThroughUncachedWithKnownParametersOnly(t *testing.T) {
	f, c := conanFixture(t)

	do := func(query string) int {
		w := f.doQuery("v2/conans/search", query)

		if w.Code == http.StatusOK && !strings.Contains(w.Body.String(), "zlib/1.3.1@_/_") {
			t.Errorf("search body = %q", w.Body.String())
		}

		return w.Code
	}

	if code := do("q=zlib*&ignorecase=False&evil=1"); code != http.StatusOK {
		t.Fatalf("search = %d", code)
	}

	do("q=zlib*")

	c.mu.Lock()
	searches := append([]string(nil), c.searches...)
	c.mu.Unlock()

	want := []string{"ignorecase=False&q=zlib%2A", "q=zlib%2A"}
	if strings.Join(searches, "|") != strings.Join(want, "|") {
		t.Errorf("upstream searches = %q, want %q", searches, want)
	}

	if f.db.entry("repo-1", "v2/conans/search") != nil {
		t.Errorf("a search was cached")
	}

	if code := do("q=" + strings.Repeat("a", maxConanQueryLen+1)); code != http.StatusBadRequest {
		t.Errorf("oversized query = %d, want 400", code)
	}

	w := f.doQuery(conanRRoot+"/search", "")

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), conanPID) {
		t.Errorf("package search = %d %q", w.Code, w.Body.String())
	}
}

func TestConanProxy_UpstreamFailuresAreNotLeaked(t *testing.T) {
	f, c := conanFixture(t)
	c.down.Store(true)

	w := f.do(http.MethodGet, conanBase+"/latest")
	if w.Code != http.StatusBadGateway && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}

	if strings.Contains(w.Body.String(), "down") && strings.Contains(w.Body.String(), f.upstream.URL) {
		t.Errorf("body leaks the upstream: %q", w.Body.String())
	}
}

func TestConanProxy_LogsInWithTheRepositoryCredentialsAndRenewsTheToken(t *testing.T) {
	f, c := conanFixture(t)
	c.user, c.pass, c.token = "ci", "s3cret", "tok-1"
	f.repo.UpstreamUsername = "ci"
	f.repo.UpstreamPasswordEnc = "s3cret" // legacy plaintext values decrypt to themselves

	for _, path := range []string{conanBase + "/latest", conanBase + "/revisions"} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}
	}

	if n := c.logins.Load(); n != 1 {
		t.Errorf("logins = %d, want the token to be reused", n)
	}

	// The upstream rotates its tokens: the cached one is refused once, the proxy
	// logs in again and the request succeeds.
	c.mu.Lock()
	c.token = "tok-2"
	c.mu.Unlock()

	if w := f.do(http.MethodGet, conanRRoot+"/files"); w.Code != http.StatusOK {
		t.Fatalf("GET after rotation = %d", w.Code)
	}

	if n := c.logins.Load(); n != 2 {
		t.Errorf("logins = %d, want 2", n)
	}

	c.mu.Lock()
	last := c.auths[len(c.auths)-1]
	c.mu.Unlock()

	if last != "Bearer tok-2" {
		t.Errorf("last Authorization = %q", last)
	}
}

func TestConanProxy_RefusedCredentialsAreAnUpstreamErrorNeverForwarded(t *testing.T) {
	f, c := conanFixture(t)
	c.user, c.pass, c.token = "ci", "right", "tok"
	f.repo.UpstreamUsername = "ci"
	f.repo.UpstreamPasswordEnc = "wrong"

	w := f.do(http.MethodGet, conanBase+"/latest")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}

	if strings.Contains(w.Body.String(), "wrong") || strings.Contains(w.Body.String(), "unauthorized") {
		t.Errorf("body leaks upstream detail: %q", w.Body.String())
	}
}

func TestConanProxy_UpstreamWithoutLoginEndpointGetsBasicCredentials(t *testing.T) {
	f, c := conanFixture(t)
	c.noAuth = true
	f.repo.UpstreamUsername = "ci"
	f.repo.UpstreamPasswordEnc = "s3cret"

	if w := f.do(http.MethodGet, conanBase+"/latest"); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	c.mu.Lock()
	last := c.auths[len(c.auths)-1]
	c.mu.Unlock()

	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ci:s3cret")); last != want {
		t.Errorf("Authorization = %q, want %q", last, want)
	}
}

func TestConanProxy_PasswordWithoutUsernameIsABearerToken(t *testing.T) {
	f, c := conanFixture(t)
	c.token = "static-token"
	f.repo.UpstreamPasswordEnc = "static-token"

	if w := f.do(http.MethodGet, conanBase+"/latest"); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	if c.logins.Load() != 0 {
		t.Errorf("logged in although the password is the token")
	}
}

func TestConanImmutableAndMetadataKeys(t *testing.T) {
	for key, immutable := range map[string]bool{
		conanRRoot + "/files":                            true,
		conanRRoot + "/files/conanfile.py":               true,
		conanPRoot + "/files/conan_package.tgz":          true,
		conanBase + "/latest":                            false,
		conanBase + "/revisions":                         false,
		conanRRoot + "/packages/" + conanPID + "/latest": false,
		"v2/conans/search":                               false,
	} {
		if got := Immutable("conan", key); got != immutable {
			t.Errorf("Immutable(%q) = %v, want %v", key, got, immutable)
		}

		if got := isMetadataKey("conan", key); got != !strings.Contains(key, "/files/") {
			t.Errorf("isMetadataKey(%q) = %v", key, got)
		}
	}

	if !headerSafeToken("abc.DEF-123_") || headerSafeToken("") || headerSafeToken("a b") || headerSafeToken("a\nb") ||
		headerSafeToken("é") {
		t.Errorf("headerSafeToken misjudges a token")
	}
}

// doQuery is do with a query string, which the search endpoints read.
func (f *fixture) doQuery(path, query string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?"+query, nil)
	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	return w
}
