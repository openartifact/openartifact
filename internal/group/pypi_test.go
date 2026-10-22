// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// pypiMembers fakes the member side of a pypi group: simple pages by member
// name and path, plus the paths each member was asked for.
type pypiMembers struct {
	pages  map[string]map[string]memberPage // member -> path -> reply
	asked  []string                         // "member path"
	files  map[string]string                // "member path" -> file body
	fail   map[string]bool                  // member -> answers 500
	accept string                           // Accept the members were last asked with
}

func (p *pypiMembers) backend() Backend {
	return Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, path string) {
			p.asked = append(p.asked, m.Name+" "+path)
			p.accept = r.Header.Get("Accept")

			if p.fail[m.Name] {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}

			if body, ok := p.files[m.Name+" "+path]; ok {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = io.WriteString(w, body)

				return
			}

			pg, ok := p.pages[m.Name][path]
			if !ok {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", pg.contentType)
			_, _ = w.Write(pg.body)
		},
	}
}

func hostedPage(files ...string) memberPage {
	var b strings.Builder

	b.WriteString("<!DOCTYPE html>\n<html><body>\n")

	for i := 0; i+1 < len(files); i += 2 {
		b.WriteString(`<a href="/repos/hosted/files/` + files[i] + `#sha256=` + files[i+1] + `" data-requires-python="&gt;=3.9">` + files[i] + "</a>\n")
	}

	b.WriteString("</body></html>\n")

	return memberPage{body: []byte(b.String()), contentType: "text/html; charset=utf-8"}
}

func proxyJSONPage(entries string) memberPage {
	return memberPage{
		body:        []byte(`{"meta":{"api-version":"1.0"},"name":"foo","files":[` + entries + `]}`),
		contentType: pypisimple.ContentTypeJSON,
	}
}

func jsonFileEntry(name, sha, extra string) string {
	return `{"filename":"` + name + `","url":"/repos/upstream/files/foo/` + name + `","hashes":{"sha256":"` + sha + `"}` + extra + `}`
}

type pypiGroupFixture struct {
	*fixture

	members *pypiMembers
}

func newPypiGroup(t *testing.T) *pypiGroupFixture {
	t.Helper()

	f := newFixture(t, "pypi", member("hosted", "pypi", "hosted"), member("upstream", "pypi", "proxy"))
	pm := &pypiMembers{
		pages: map[string]map[string]memberPage{"hosted": {}, "upstream": {}},
		files: map[string]string{},
		fail:  map[string]bool{},
	}

	return &pypiGroupFixture{fixture: f, members: pm}
}

func (g *pypiGroupFixture) get(method, path, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	w := httptest.NewRecorder()
	g.h.Serve(w, req, g.grp, path, g.members.backend())

	return w
}

func parse(t *testing.T, w *httptest.ResponseRecorder) []pypisimple.File {
	t.Helper()

	files, err := pypisimple.ParseProject(w.Body.Bytes(), w.Header().Get("Content-Type"))
	if err != nil {
		t.Fatalf("response does not parse: %v\n%s", err, w.Body.String())
	}

	return files
}

func TestPyPIGroup_MergePrecedenceAndRewrite(t *testing.T) {
	g := newPypiGroup(t)
	g.members.pages["hosted"]["simple/foo/"] = hostedPage("foo-1.0-py3-none-any.whl", hashA)
	g.members.pages["upstream"]["simple/foo/"] = proxyJSONPage(
		jsonFileEntry("foo-1.0-py3-none-any.whl", hashB, "") + "," +
			jsonFileEntry("foo-2.0-py3-none-any.whl", hashC, `,"requires-python":">=3.10","yanked":"bad build","core-metadata":true`))

	w := g.get(http.MethodGet, "simple/foo/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	files := parse(t, w)
	if len(files) != 2 {
		t.Fatalf("files = %+v, want the union of both members (2)", files)
	}

	v1, v2 := files[0], files[1]

	if v1.Filename != "foo-1.0-py3-none-any.whl" || v1.SHA256() != hashA || v1.RequiresPython != ">=3.9" {
		t.Errorf("duplicate filename must come from the first member (hosted): %+v", v1)
	}

	if v2.Filename != "foo-2.0-py3-none-any.whl" || v2.SHA256() != hashC || v2.RequiresPython != ">=3.10" ||
		!v2.Yanked || v2.YankedReason != "bad build" || !v2.CoreMetadata {
		t.Errorf("proxy-only file lost data: %+v", v2)
	}

	for _, f := range files {
		if want := "/repos/all/files/foo/" + f.Filename; f.URL != want {
			t.Errorf("link %q not rewritten to the group, want %q", f.URL, want)
		}
	}

	if g.members.accept == "" || !strings.Contains(g.members.accept, pypisimple.ContentTypeJSON) {
		t.Errorf("members asked with Accept %q, want PEP 691 json preferred", g.members.accept)
	}
}

func TestPyPIGroup_MemberOrderDecidesNotFormat(t *testing.T) {
	// Reverse membership: the proxy first, hosted second.
	g := newPypiGroup(t)
	g.members.pages["hosted"]["simple/foo/"] = hostedPage("foo-1.0-py3-none-any.whl", hashA)
	g.members.pages["upstream"]["simple/foo/"] = proxyJSONPage(jsonFileEntry("foo-1.0-py3-none-any.whl", hashB, ""))

	links := g.h.db.(*groupDB).links
	links[0], links[1] = links[1], links[0]

	files := parse(t, g.get(http.MethodGet, "simple/foo/", ""))
	if len(files) != 1 || files[0].SHA256() != hashB {
		t.Errorf("files = %+v, want the proxy's entry (first member wins)", files)
	}
}

func TestPyPIGroup_JSONAndHTMLResponses(t *testing.T) {
	g := newPypiGroup(t)
	g.members.pages["upstream"]["simple/foo/"] = proxyJSONPage(jsonFileEntry("foo-1.0.tar.gz", hashB, ""))

	html := g.get(http.MethodGet, "simple/foo/", "")
	if ct := html.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("default content type = %q", ct)
	}

	if !strings.Contains(html.Body.String(), `href="/repos/all/files/foo/foo-1.0.tar.gz#sha256=`+hashB+`"`) {
		t.Errorf("html:\n%s", html.Body.String())
	}

	js := g.get(http.MethodGet, "simple/foo/", pypisimple.ContentTypeJSON)
	if ct := js.Header().Get("Content-Type"); ct != pypisimple.ContentTypeJSON {
		t.Errorf("json content type = %q", ct)
	}

	if !strings.Contains(js.Body.String(), `"url":"/repos/all/files/foo/foo-1.0.tar.gz"`) ||
		!strings.Contains(js.Body.String(), `"sha256":"`+hashB+`"`) {
		t.Errorf("json:\n%s", js.Body.String())
	}

	if w := g.get(http.MethodGet, "simple/foo/", "text/plain"); w.Code != http.StatusNotAcceptable {
		t.Errorf("unacceptable Accept: status %d, want 406", w.Code)
	}

	// Validators differ per serialization.
	if html.Header().Get("ETag") == js.Header().Get("ETag") || html.Header().Get("ETag") == "" {
		t.Errorf("etags html=%q json=%q", html.Header().Get("ETag"), js.Header().Get("ETag"))
	}
}

func TestPyPIGroup_NormalizesProjectName(t *testing.T) {
	g := newPypiGroup(t)
	g.members.pages["upstream"]["simple/foo-bar/"] = proxyJSONPage(jsonFileEntry("foo_bar-1.0.tar.gz", hashB, ""))

	w := g.get(http.MethodGet, "simple/Foo__Bar/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	for _, a := range g.members.asked {
		if !strings.HasSuffix(a, " simple/foo-bar/") {
			t.Errorf("member asked %q, want normalized simple/foo-bar/", a)
		}
	}

	if f := parse(t, w); len(f) != 1 || f[0].URL != "/repos/all/files/foo-bar/foo_bar-1.0.tar.gz" {
		t.Errorf("files = %+v", f)
	}
}

func TestPyPIGroup_Index(t *testing.T) {
	g := newPypiGroup(t)
	g.members.pages["hosted"]["simple/"] = memberPage{
		body:        []byte(`<html><body><a href="/repos/hosted/simple/zeta/">zeta</a><a href="/repos/hosted/simple/foo/">foo</a></body></html>`),
		contentType: "text/html",
	}
	g.members.pages["upstream"]["simple/"] = memberPage{
		body:        []byte(`{"meta":{"api-version":"1.0"},"projects":[{"name":"alpha"},{"name":"foo"}]}`),
		contentType: pypisimple.ContentTypeJSON,
	}

	w := g.get(http.MethodGet, "simple/", pypisimple.ContentTypeJSON)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	names, err := pypisimple.ParseIndexJSON(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(names, ",") != "alpha,foo,zeta" {
		t.Errorf("names = %v, want union sorted without duplicates", names)
	}

	html := g.get(http.MethodGet, "simple/", "").Body.String()
	if !strings.Contains(html, `<a href="/repos/all/simple/alpha/">alpha</a>`) {
		t.Errorf("index links not pointing at the group:\n%s", html)
	}
}

func TestPyPIGroup_FileResolution(t *testing.T) {
	g := newPypiGroup(t)
	g.members.files["upstream files/foo/foo-2.0.tar.gz"] = "upstream-bytes"

	w := g.get(http.MethodGet, "files/foo/foo-2.0.tar.gz", "")
	if w.Code != http.StatusOK || w.Body.String() != "upstream-bytes" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	// Hosted members are keyed by filename, proxy members by project/filename.
	want := []string{"hosted files/foo-2.0.tar.gz", "upstream files/foo/foo-2.0.tar.gz"}
	if strings.Join(g.members.asked, "|") != strings.Join(want, "|") {
		t.Errorf("asked = %v, want %v", g.members.asked, want)
	}

	g.members.files["hosted files/foo-1.0.tar.gz"] = "hosted-bytes"
	g.members.files["upstream files/foo/foo-1.0.tar.gz"] = "upstream-bytes"

	if w := g.get(http.MethodGet, "files/foo/foo-1.0.tar.gz", ""); w.Body.String() != "hosted-bytes" {
		t.Errorf("file present in both: %q, want the hosted (first) member's", w.Body.String())
	}

	if w := g.get(http.MethodGet, "files/foo/missing.whl", ""); w.Code != http.StatusNotFound {
		t.Errorf("missing file: status %d, want 404", w.Code)
	}
}

func TestPyPIGroup_MemberFailures(t *testing.T) {
	t.Run("no contributor and a failing member is 502", func(t *testing.T) {
		g := newPypiGroup(t)
		g.members.fail["upstream"] = true

		if w := g.get(http.MethodGet, "simple/foo/", ""); w.Code != http.StatusBadGateway {
			t.Errorf("status %d, want 502", w.Code)
		}
	})

	t.Run("nobody has the project is 404", func(t *testing.T) {
		g := newPypiGroup(t)

		if w := g.get(http.MethodGet, "simple/foo/", ""); w.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", w.Code)
		}
	})

	t.Run("partial page is served uncacheable", func(t *testing.T) {
		g := newPypiGroup(t)
		g.members.pages["hosted"]["simple/foo/"] = hostedPage("foo-1.0-py3-none-any.whl", hashA)
		g.members.fail["upstream"] = true

		w := g.get(http.MethodGet, "simple/foo/", "")
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
			t.Errorf("status %d cache-control %q etag %q", w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
		}
	})

	t.Run("file from a failing member falls through, then 502", func(t *testing.T) {
		g := newPypiGroup(t)
		g.members.fail["hosted"] = true

		if w := g.get(http.MethodGet, "files/foo/x.whl", ""); w.Code != http.StatusBadGateway {
			t.Errorf("status %d, want 502", w.Code)
		}
	})
}

func TestPyPIGroup_ReadOnlyAndOtherPaths(t *testing.T) {
	g := newPypiGroup(t)

	if w := g.get(http.MethodPost, "legacy/", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("upload to a group: status %d, want 405", w.Code)
	}

	// Paths outside the simple API resolve first-hit (e.g. the JSON API).
	g.members.pages["hosted"]["json/foo"] = memberPage{body: []byte(`{"info":{}}`), contentType: "application/json"}

	if w := g.get(http.MethodGet, "json/foo", ""); w.Code != http.StatusOK {
		t.Errorf("json/foo: status %d", w.Code)
	}
}

// A path-scoped grant on a member must see the path a direct request to that
// member is authorized with: files/<filename> for hosted, files/<project>/<filename>
// for proxy members.
func TestPyPIGroup_FileAuthorizedWithMemberPath(t *testing.T) {
	g := newPypiGroup(t)
	g.members.files["hosted files/foo-1.0.tar.gz"] = "payload"

	authorized := map[string]string{}
	be := g.members.backend()
	be.Authorize = func(_ *http.Request, m *models.Repository, relPath string) bool {
		authorized[m.Name] = relPath
		return true
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	g.h.Serve(w, req, g.grp, "files/foo/foo-1.0.tar.gz", be)

	if w.Code != http.StatusOK || w.Body.String() != "payload" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	want := map[string]string{"hosted": "files/foo-1.0.tar.gz", "upstream": "files/foo/foo-1.0.tar.gz"}
	for name, p := range want {
		if authorized[name] != p {
			t.Errorf("member %s authorized with %q, want %q", name, authorized[name], p)
		}
	}

	// Non-file paths are unchanged.
	clear(authorized)
	g.h.Serve(httptest.NewRecorder(), req, g.grp, "simple/foo/", be)

	for name := range want {
		if authorized[name] != "simple/foo/" {
			t.Errorf("member %s authorized with %q for a project page", name, authorized[name])
		}
	}
}
