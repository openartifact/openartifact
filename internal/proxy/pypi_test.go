// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/pypisimple"
)

const (
	wheelName   = "requests-2.32.0-py3-none-any.whl"
	wheelBody   = "PK\x03\x04 pretend wheel bytes"
	metaBody    = "Metadata-Version: 2.1\nName: requests\n"
	htmlContent = "text/html; charset=utf-8"
)

// pypiUpstream is a fake pypi simple index plus file host.
type pypiUpstream struct {
	t *testing.T
	// page is the body served for /simple/requests/, with %FILES% replaced by
	// the upstream's own URL.
	page        string
	pageType    string
	wheelSHA    string
	down        atomic.Bool
	lastProject atomic.Value // last project requested from /simple/
	lastAuth    atomic.Value // Authorization header of the last file request
}

func newPyPIFixture(t *testing.T, up *pypiUpstream) *fixture {
	t.Helper()

	up.t = t
	if up.wheelSHA == "" {
		up.wheelSHA = sum(wheelBody)
	}

	if up.pageType == "" {
		up.pageType = htmlContent
	}

	var f *fixture

	f = newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if up.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}

		switch {
		case r.URL.Path == "/simple/":
			w.Header().Set("Content-Type", htmlContent)
			_, _ = io.WriteString(
				w,
				`<html><body><a href="/simple/Requests/">Requests</a><a href="/simple/zope.interface/">zope.interface</a><a href="/simple/bad%2Fname/">bad/name</a></body></html>`,
			)
		case strings.HasPrefix(r.URL.Path, "/simple/"):
			up.lastProject.Store(strings.Trim(strings.TrimPrefix(r.URL.Path, "/simple/"), "/"))

			if strings.Trim(strings.TrimPrefix(r.URL.Path, "/simple/"), "/") != "requests" {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", up.pageType)
			_, _ = io.WriteString(w, strings.ReplaceAll(up.page, "%FILES%", f.upstreamBase()))
		case r.URL.Path == "/files/"+wheelName:
			up.lastAuth.Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, wheelBody)
		case r.URL.Path == "/files/"+wheelName+".metadata":
			up.lastAuth.Store(r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, metaBody)
		default:
			http.NotFound(w, r)
		}
	})
	f.repo.Format = "pypi"
	f.repo.Name = "pypi-proxy"
	f.repo.UpstreamURL = f.upstreamBase() + "/simple/"

	return f
}

// upstreamBase is the scheme://host of the fixture's upstream.
func (f *fixture) upstreamBase() string {
	return strings.TrimSuffix(strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/"), "/simple/")
}

func (f *fixture) doAccept(method, path, accept string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	return w
}

func htmlPage(sha string) string {
	return `<!DOCTYPE html><html><body>
<a href="%FILES%/files/` + wheelName + `#sha256=` + sha + `" data-requires-python="&gt;=3.8" data-dist-info-metadata="sha256=` + sum(metaBody) + `">` + wheelName + `</a>
<a href="%FILES%/files/requests-1.0.tar.gz#sha256=` + strings.Repeat("ab", 32) + `" data-yanked="broken release">requests-1.0.tar.gz</a>
</body></html>`
}

func jsonPage(sha string) string {
	return `{"meta":{"api-version":"1.0"},"name":"requests","files":[
{"filename":"` + wheelName + `","url":"%FILES%/files/` + wheelName + `","hashes":{"sha256":"` + sha + `"},"requires-python":">=3.8","core-metadata":{"sha256":"` + sum(metaBody) + `"},"size":22,"upload-time":"2024-05-20T00:00:00Z"},
{"filename":"requests-1.0.tar.gz","url":"%FILES%/files/requests-1.0.tar.gz","hashes":{"sha256":"` + strings.Repeat("ab", 32) + `"},"yanked":"broken release"}]}`
}

func TestPyPIProxy_ProjectPageRewritesLinksHTML(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	w := f.doAccept(http.MethodGet, "simple/requests/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if ct := w.Header().Get("Content-Type"); ct != htmlContent {
		t.Errorf("content type = %q", ct)
	}

	body := w.Body.String()
	for _, want := range []string{
		`href="/repos/pypi-proxy/files/requests/` + wheelName + `#sha256=` + sum(wheelBody) + `"`,
		`data-requires-python="&gt;=3.8"`,
		`data-core-metadata="sha256=` + sum(metaBody) + `"`,
		`data-dist-info-metadata="sha256=` + sum(metaBody) + `"`,
		`data-yanked="broken release"`,
		`>` + wheelName + `</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s\n%s", want, body)
		}
	}

	if strings.Contains(body, f.upstreamBase()) {
		t.Errorf("upstream URL leaked into the page:\n%s", body)
	}
}

func TestPyPIProxy_Negotiation(t *testing.T) {
	tests := []struct {
		name     string
		upstream *pypiUpstream
		accept   string
		wantType string
		wantJSON bool
	}{
		{
			"json upstream, no accept -> html",
			&pypiUpstream{page: jsonPage(sum(wheelBody)), pageType: pypisimple.ContentTypeJSON},
			"",
			htmlContent,
			false,
		},
		{
			"json upstream, pip accept -> json",
			&pypiUpstream{page: jsonPage(sum(wheelBody)), pageType: pypisimple.ContentTypeJSON},
			"application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html; q=0.1, text/html; q=0.01",
			pypisimple.ContentTypeJSON,
			true,
		},
		{
			"html upstream, json accept -> json",
			&pypiUpstream{page: htmlPage(sum(wheelBody))},
			pypisimple.ContentTypeJSON,
			pypisimple.ContentTypeJSON,
			true,
		},
		{
			"html upstream, explicit html",
			&pypiUpstream{page: htmlPage(sum(wheelBody))},
			pypisimple.ContentTypeHTMLv1,
			pypisimple.ContentTypeHTMLv1,
			false,
		},
		{
			"json upstream, wildcard -> html",
			&pypiUpstream{page: jsonPage(sum(wheelBody)), pageType: pypisimple.ContentTypeJSON},
			"*/*",
			htmlContent,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPyPIFixture(t, tt.upstream)

			w := f.doAccept(http.MethodGet, "simple/requests/", tt.accept)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}

			if ct := w.Header().Get("Content-Type"); ct != tt.wantType {
				t.Errorf("content type = %q, want %q", ct, tt.wantType)
			}

			if !strings.Contains(w.Header().Get("Vary"), "Accept") {
				t.Errorf("Vary = %q, want Accept", w.Header().Get("Vary"))
			}

			files, err := pypisimple.ParseProject(w.Body.Bytes(), w.Header().Get("Content-Type"))
			if err != nil {
				t.Fatalf("response does not parse as %s: %v\n%s", tt.wantType, err, w.Body.String())
			}

			if tt.wantJSON && !json.Valid(w.Body.Bytes()) {
				t.Errorf("not json: %s", w.Body.String())
			}

			if len(files) != 2 {
				t.Fatalf("files = %d, want 2", len(files))
			}

			wheel, sdist := files[0], files[1]
			if wheel.Filename != wheelName || wheel.URL != "/repos/pypi-proxy/files/requests/"+wheelName ||
				wheel.SHA256() != sum(wheelBody) || wheel.RequiresPython != ">=3.8" ||
				!wheel.CoreMetadata || wheel.MetadataSHA256() != sum(metaBody) || wheel.Yanked {
				t.Errorf("wheel entry mangled: %+v", wheel)
			}

			if !sdist.Yanked || sdist.YankedReason != "broken release" {
				t.Errorf("yanked flag lost: %+v", sdist)
			}
		})
	}
}

func TestPyPIProxy_NotAcceptable(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	if w := f.doAccept(http.MethodGet, "simple/requests/", "application/xml"); w.Code != http.StatusNotAcceptable {
		t.Errorf("status = %d, want 406", w.Code)
	}
}

func TestPyPIProxy_NameNormalization(t *testing.T) {
	for _, in := range []string{"Requests", "REQUESTS", "requests"} {
		up := &pypiUpstream{page: htmlPage(sum(wheelBody))}
		f := newPyPIFixture(t, up)

		if w := f.do(http.MethodGet, "simple/"+in+"/"); w.Code != http.StatusOK {
			t.Fatalf("%q: status %d", in, w.Code)
		}

		if got, _ := up.lastProject.Load().(string); got != "requests" {
			t.Errorf("%q requested upstream project %q, want requests", in, got)
		}
	}

	// Separator runs collapse: Foo__Bar.baz -> foo-bar-baz (upstream 404s it,
	// but the normalized name is what must be asked for).
	up := &pypiUpstream{page: htmlPage(sum(wheelBody))}
	f := newPyPIFixture(t, up)

	if w := f.do(http.MethodGet, "simple/Foo__Bar.baz"); w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}

	if got, _ := up.lastProject.Load().(string); got != "foo-bar-baz" {
		t.Errorf("upstream project = %q, want foo-bar-baz", got)
	}
}

func TestPyPIProxy_RejectsUnsafePaths(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	for _, p := range []string{
		"simple/a/b/", "simple/%2e%2e/", "simple/a b/", "simple/..", "files/requests/", "files/requests",
		"files/Requests/" + wheelName, "files/requests/../x", "files/requests/a/b", "other/x", "legacy/",
		"files/requests/" + wheelName + ".metadata.metadata",
	} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("%q: status %d, want 404", p, w.Code)
		}
	}

	if f.hits.Load() != 0 {
		t.Errorf("unsafe paths reached the upstream %d times", f.hits.Load())
	}
}

func TestPyPIProxy_UploadRejected(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if w := f.do(m, "legacy/"); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", m, w.Code)
		}
	}
}

func TestPyPIProxy_IndexRewritten(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	w := f.doAccept(http.MethodGet, "simple/", pypisimple.ContentTypeJSON)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var doc struct {
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v", err)
	}

	if len(doc.Projects) != 2 || doc.Projects[0].Name != "requests" || doc.Projects[1].Name != "zope-interface" {
		t.Errorf("projects = %+v, want normalized [requests zope-interface] without the unsafe name", doc.Projects)
	}

	html := f.do(http.MethodGet, "simple/").Body.String()
	if !strings.Contains(html, `<a href="/repos/pypi-proxy/simple/zope-interface/">zope-interface</a>`) {
		t.Errorf("html index links not rewritten:\n%s", html)
	}
}

func TestPyPIProxy_FileDownloadVerifiedCachedAndServedWithUpstreamDown(t *testing.T) {
	up := &pypiUpstream{page: htmlPage(sum(wheelBody))}
	f := newPyPIFixture(t, up)

	first := f.do(http.MethodGet, "files/requests/"+wheelName)
	if first.Code != http.StatusOK || first.Body.String() != wheelBody {
		t.Fatalf("first: status %d body %q", first.Code, first.Body.String())
	}

	e := f.db.entry("repo-1", "files/requests/"+wheelName)
	if e == nil || e.BlobKey != sum(wheelBody) {
		t.Fatalf("cache entry = %+v", e)
	}

	if !e.ExpiresAt.After(time.Now().AddDate(50, 0, 0)) {
		t.Errorf("distribution files must be immutable, expires %v", e.ExpiresAt)
	}

	// The project page is gone stale and the upstream is down: pip's second
	// install still gets the page (stale) and the file (cache).
	f.db.expire("repo-1", "simple/requests/")
	up.down.Store(true)

	page := f.do(http.MethodGet, "simple/requests/")
	if page.Code != http.StatusOK || page.Header().Get("Warning") == "" {
		t.Errorf("stale page: status %d warning %q", page.Code, page.Header().Get("Warning"))
	}

	second := f.do(http.MethodGet, "files/requests/"+wheelName)
	if second.Code != http.StatusOK || second.Body.String() != wheelBody {
		t.Fatalf("second with upstream down: status %d body %q", second.Code, second.Body.String())
	}
}

func TestPyPIProxy_HashMismatchRejected(t *testing.T) {
	up := &pypiUpstream{page: htmlPage(strings.Repeat("0", 64))}
	f := newPyPIFixture(t, up)

	w := f.do(http.MethodGet, "files/requests/"+wheelName)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", w.Code)
	}

	if strings.Contains(w.Body.String(), "pretend") {
		t.Error("the mismatching body was served")
	}

	if e := f.db.entry("repo-1", "files/requests/"+wheelName); e != nil {
		t.Errorf("mismatching file was cached: %+v", e)
	}

	if len(f.db.blobs) != 1 { // only the project page blob
		t.Errorf("blobs = %v, mismatching file must not be stored", f.db.blobs)
	}
}

func TestPyPIProxy_MetadataFile(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	w := f.do(http.MethodGet, "files/requests/"+wheelName+".metadata")
	if w.Code != http.StatusOK || w.Body.String() != metaBody {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	// The sdist advertises no metadata.
	if w := f.do(http.MethodGet, "files/requests/requests-1.0.tar.gz.metadata"); w.Code != http.StatusNotFound {
		t.Errorf("metadata of a file without core-metadata: status %d, want 404", w.Code)
	}

	// A metadata body that does not match its published hash is refused.
	bad := newPyPIFixture(t, &pypiUpstream{page: strings.ReplaceAll(htmlPage(sum(wheelBody)), sum(metaBody), strings.Repeat("1", 64))})
	if w := bad.do(http.MethodGet, "files/requests/"+wheelName+".metadata"); w.Code != http.StatusBadGateway {
		t.Errorf("tampered metadata: status %d, want 502", w.Code)
	}
}

func TestPyPIProxy_UnlistedFileNotFound(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	if w := f.do(http.MethodGet, "files/requests/requests-9.9.9-py3-none-any.whl"); w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
}

func TestPyPIProxy_RelativeLinksResolveAgainstPage(t *testing.T) {
	page := `<html><body><a href="../../files/` + wheelName + `#sha256=` + sum(wheelBody) + `">` + wheelName + `</a></body></html>`
	f := newPyPIFixture(t, &pypiUpstream{page: page})

	w := f.do(http.MethodGet, "files/requests/"+wheelName)
	if w.Code != http.StatusOK || w.Body.String() != wheelBody {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
}

func TestPyPIProxy_CredentialsOnlyToUpstreamOrigin(t *testing.T) {
	// The file host is a second origin (different port).
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("credentials leaked to the file host: %q", r.Header.Get("Authorization"))
		}

		_, _ = io.WriteString(w, wheelBody)
	}))
	t.Cleanup(other.Close)

	page := `<html><body><a href="` + other.URL + `/x/` + wheelName + `#sha256=` + sum(wheelBody) + `">` + wheelName + `</a></body></html>`
	up := &pypiUpstream{page: page}
	f := newPyPIFixture(t, up)
	f.repo.UpstreamUsername = "svc"

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	// Same origin files do get them.
	sameUp := &pypiUpstream{page: htmlPage(sum(wheelBody))}
	same := newPyPIFixture(t, sameUp)
	same.repo.UpstreamUsername = "svc"

	if w := same.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	if got, _ := sameUp.lastAuth.Load().(string); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("same-origin file request carried Authorization %q, want Basic credentials", got)
	}
}

func TestPyPIProxy_ConditionalGet(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	first := f.do(http.MethodGet, "simple/requests/")
	etag := first.Header().Get("ETag")

	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, "simple/requests/")

	if w.Code != http.StatusNotModified {
		t.Errorf("status %d, want 304", w.Code)
	}

	// The same validator must not match the other serialization.
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	req.Header.Set("Accept", pypisimple.ContentTypeJSON)

	w = httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, "simple/requests/")

	if w.Code != http.StatusOK {
		t.Errorf("json after html validator: status %d, want 200", w.Code)
	}
}

func TestPyPIProxy_UnparsablePageIsNotCached(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: "{not json", pageType: pypisimple.ContentTypeJSON})

	if w := f.do(http.MethodGet, "simple/requests/"); w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", w.Code)
	}

	if e := f.db.entry("repo-1", "simple/requests/"); e != nil {
		t.Errorf("unparsable page stayed cached: %+v", e)
	}
}

func TestPyPIProxy_UpstreamDownWithoutCacheIs502(t *testing.T) {
	up := &pypiUpstream{page: htmlPage(sum(wheelBody))}
	f := newPyPIFixture(t, up)
	up.down.Store(true)

	if w := f.do(http.MethodGet, "simple/requests/"); w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", w.Code)
	}
}
