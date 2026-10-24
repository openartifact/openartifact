// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"reflect"
	"strings"
	"testing"
)

const (
	sha1hex = "1111111111111111111111111111111111111111111111111111111111111111"
	sha2hex = "2222222222222222222222222222222222222222222222222222222222222222"
)

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		in    string
		want  string
		valid bool
	}{
		{"Requests", "requests", true},
		{"Foo_Bar.baz", "foo-bar-baz", true},
		{"foo__bar--baz..qux", "foo-bar-baz-qux", true},
		{"zope.interface", "zope-interface", true},
		{"a", "a", true},
		{"", "", false},
		{"-", "-", false},
		{"..", "-", false},
		{"a/b", "a/b", false},
		{"a b", "a b", false},
		{"a%2Fb", "a%2fb", false},
	}

	for _, tt := range tests {
		got := NormalizeName(tt.in)
		if got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}

		if v := ValidName(got); v != tt.valid {
			t.Errorf("ValidName(%q) = %v, want %v", got, v, tt.valid)
		}
	}
}

func TestParsePath(t *testing.T) {
	tests := []struct {
		path     string
		kind     PathKind
		project  string
		filename string
	}{
		{"simple/", PathIndex, "", ""},
		{"simple", PathIndex, "", ""},
		{"simple/Foo_Bar/", PathProject, "foo-bar", ""},
		{"simple/foo", PathProject, "foo", ""},
		{"simple/a/b/", PathOther, "", ""},
		{"simple/a b/", PathOther, "", ""},
		{"files/foo/foo-1.0.tar.gz", PathFile, "foo", "foo-1.0.tar.gz"},
		{"files/foo/foo-1.0-py3-none-any.whl.metadata", PathFile, "foo", "foo-1.0-py3-none-any.whl.metadata"},
		{"files/Foo/x.whl", PathOther, "", ""},
		{"files/foo/", PathOther, "", ""},
		{"files/foo/a/b", PathOther, "", ""},
		{"files/foo/..", PathOther, "", ""},
		{"files/foo/x.whl.metadata.metadata", PathOther, "", ""},
		{"legacy/", PathOther, "", ""},
		{"", PathOther, "", ""},
	}

	for _, tt := range tests {
		kind, project, filename := ParsePath(tt.path)
		if kind != tt.kind || project != tt.project || filename != tt.filename {
			t.Errorf("ParsePath(%q) = (%v, %q, %q), want (%v, %q, %q)", tt.path, kind, project, filename, tt.kind, tt.project, tt.filename)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	for name, want := range map[string]bool{
		"foo-1.0-py3-none-any.whl": true,
		"foo-1.0.tar.gz":           true,
		"":                         false,
		".":                        false,
		"..":                       false,
		"a/b":                      false,
		`a\b`:                      false,
		"a\x00b":                   false,
		"a\nb":                     false,
		"x.whl.metadata":           false,
		strings.Repeat("a", 256):   false,
	} {
		if got := SafeFilename(name); got != want {
			t.Errorf("SafeFilename(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestContentTypeDetection(t *testing.T) {
	for ct, want := range map[string]bool{
		ContentTypeJSON: true,
		"application/vnd.pypi.simple.latest+json": true,
		"application/json; charset=utf-8":         true,
		"text/html":                               false,
		ContentTypeHTMLv1:                         false,
		"":                                        false,
	} {
		if got := IsJSON(ct); got != want {
			t.Errorf("IsJSON(%q) = %v, want %v", ct, got, want)
		}
	}
}

const upstreamHTML = `<!DOCTYPE html><html><head><meta name="pypi:repository-version" content="1.0"></head><body>
<a href="https://files.example.org/packages/foo-1.0-py3-none-any.whl#sha256=` + sha1hex + `" data-requires-python="&gt;=3.8" data-core-metadata="sha256=` + sha2hex + `">foo-1.0-py3-none-any.whl</a><br/>
<a href="../../packages/foo-1.0.tar.gz#sha256=` + sha2hex + `" data-dist-info-metadata="true" data-yanked="">foo-1.0.tar.gz</a>
<a href="https://x/foo-0.9.tar.gz#md5=abc">foo-0.9.tar.gz</a>
<a href="https://x/foo-0.8.tar.gz" data-yanked="superseded &amp; unsafe">foo-0.8.tar.gz</a>
</body></html>`

const upstreamJSON = `{"meta":{"api-version":"1.1"},"name":"foo","files":[
{"filename":"foo-1.0-py3-none-any.whl","url":"https://files.example.org/packages/foo-1.0-py3-none-any.whl","hashes":{"sha256":"` + sha1hex + `"},"requires-python":">=3.8","core-metadata":{"sha256":"` + sha2hex + `"},"size":123,"upload-time":"2024-01-02T03:04:05Z"},
{"filename":"foo-1.0.tar.gz","url":"../../packages/foo-1.0.tar.gz","hashes":{"sha256":"` + sha2hex + `"},"dist-info-metadata":true,"yanked":""},
{"filename":"foo-0.9.tar.gz","url":"https://x/foo-0.9.tar.gz","hashes":{"md5":"abc"}},
{"filename":"foo-0.8.tar.gz","url":"https://x/foo-0.8.tar.gz","hashes":{},"yanked":"superseded & unsafe"}]}`

func checkUpstreamFiles(t *testing.T, files []File) {
	t.Helper()

	if len(files) != 4 {
		t.Fatalf("files = %d, want 4: %+v", len(files), files)
	}

	whl, sdist, md5only, yanked := files[0], files[1], files[2], files[3]

	if whl.Filename != "foo-1.0-py3-none-any.whl" || whl.URL != "https://files.example.org/packages/foo-1.0-py3-none-any.whl" ||
		whl.SHA256() != sha1hex || whl.RequiresPython != ">=3.8" || !whl.CoreMetadata || whl.MetadataSHA256() != sha2hex || whl.Yanked {
		t.Errorf("wheel = %+v", whl)
	}

	if sdist.URL != "../../packages/foo-1.0.tar.gz" || !sdist.CoreMetadata || sdist.MetadataSHA256() != "" || !sdist.Yanked || sdist.YankedReason != "" {
		t.Errorf("sdist = %+v", sdist)
	}

	if md5only.SHA256() != "" || md5only.Yanked || md5only.CoreMetadata {
		t.Errorf("md5-only = %+v", md5only)
	}

	if !yanked.Yanked || yanked.YankedReason != "superseded & unsafe" {
		t.Errorf("yanked = %+v", yanked)
	}
}

func TestParseProject_HTMLAndJSONAgree(t *testing.T) {
	html, err := ParseProject([]byte(upstreamHTML), "text/html; charset=utf-8")
	if err != nil {
		t.Fatal(err)
	}

	checkUpstreamFiles(t, html)

	js, err := ParseProject([]byte(upstreamJSON), ContentTypeJSON)
	if err != nil {
		t.Fatal(err)
	}

	checkUpstreamFiles(t, js)

	if js[0].Size != 123 || js[0].UploadTime != "2024-01-02T03:04:05Z" {
		t.Errorf("json size/upload-time lost: %+v", js[0])
	}
}

func TestParseProject_Errors(t *testing.T) {
	if _, err := ParseProject([]byte("{nope"), ContentTypeJSON); err == nil {
		t.Error("malformed json accepted")
	}

	if _, err := ParseProject([]byte(`{"files":[{"filename":"","url":"u"}]}`), ContentTypeJSON); err == nil {
		// An entry without a filename is unusable; it is either dropped or rejected, never kept.
		files, _ := ParseProject([]byte(`{"files":[{"filename":"","url":"u"}]}`), ContentTypeJSON)
		if len(files) != 0 {
			t.Errorf("nameless file kept: %+v", files)
		}
	}
}

// A link that is only a fragment resolves to the page that holds it: it names
// no file, in either serialization.
func TestParseProject_FragmentOnlyLinkIsNotAFile(t *testing.T) {
	for _, tt := range []struct{ body, ct string }{
		{`<a href="#sha256=` + sha1hex + `">foo-1.0.tar.gz</a><a href="  #x ">foo-1.1.tar.gz</a>`, "text/html"},
		{`{"files":[{"filename":"foo-1.0.tar.gz","url":"#sha256=` + sha1hex + `"}]}`, ContentTypeJSON},
	} {
		files, err := ParseProject([]byte(tt.body), tt.ct)
		if err != nil {
			t.Fatal(err)
		}

		if len(files) != 0 {
			t.Errorf("%s: fragment-only links kept: %+v", tt.ct, files)
		}
	}
}

func TestParseIndex_HTMLAndJSON(t *testing.T) {
	html := `<html><body><a href="/simple/Foo_Bar/">Foo_Bar</a><a href="/simple/baz/">baz</a><a href="/simple/bad%2Fname/">bad/name</a></body></html>`
	js := `{"meta":{"api-version":"1.0"},"projects":[{"name":"Foo_Bar"},{"name":"baz"}]}`

	for _, tt := range []struct{ body, ct string }{{html, "text/html"}, {js, ContentTypeJSON}} {
		names, err := ParseIndex([]byte(tt.body), tt.ct)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(names, []string{"foo-bar", "baz"}) {
			t.Errorf("%s: names = %v, want normalized [foo-bar baz]", tt.ct, names)
		}
	}
}

func TestRenderRoundTrip(t *testing.T) {
	files, err := ParseProject([]byte(upstreamJSON), ContentTypeJSON)
	if err != nil {
		t.Fatal(err)
	}

	href := func(f *File) string { return "/repos/r/files/foo/" + f.Filename }

	for _, tt := range []struct {
		format Format
		ct     string
	}{{HTML, "text/html"}, {JSON, ContentTypeJSON}} {
		out, err := RenderProject(tt.format, "foo", files, href)
		if err != nil {
			t.Fatal(err)
		}

		back, err := ParseProject(out, tt.ct)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tt.ct, err, out)
		}

		if len(back) != len(files) {
			t.Fatalf("%s: round trip lost files: %d != %d", tt.ct, len(back), len(files))
		}

		for i := range files {
			want := files[i]
			want.URL = href(&want)
			// HTML carries neither size nor upload time.
			if tt.format == HTML {
				want.Size, want.UploadTime = 0, ""
			}

			// md5-only hashes survive as the page's own hash fragment only
			// for sha256; other algorithms are not republished.
			if want.SHA256() == "" {
				want.Hashes = back[i].Hashes
			}

			if !reflect.DeepEqual(back[i], want) {
				t.Errorf("%s: file %d:\n got  %+v\n want %+v", tt.ct, i, back[i], want)
			}
		}
	}
}

func TestRenderEscapesHostileValues(t *testing.T) {
	files := []File{{
		Filename: `x"><script>alert(1)</script>.whl`, URL: "u", Hashes: map[string]string{"sha256": sha1hex},
		RequiresPython: `">"<b>`, Yanked: true, YankedReason: `"><i>`,
	}}

	out, err := RenderProject(HTML, "foo", files, func(*File) string { return `/repos/r/files/foo/"><script>` })
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), "<script>") || strings.Contains(string(out), "<b>") || strings.Contains(string(out), "<i>") {
		t.Errorf("unescaped markup in output:\n%s", out)
	}
}

func TestNegotiate(t *testing.T) {
	tests := []struct {
		accept string
		ok     bool
		format Format
		ct     string
	}{
		{"", true, HTML, ContentTypeHTMLRaw},
		{"*/*", true, HTML, ContentTypeHTMLRaw},
		{"text/html", true, HTML, ContentTypeHTMLRaw},
		{ContentTypeHTMLv1, true, HTML, ContentTypeHTMLv1},
		{ContentTypeJSON, true, JSON, ContentTypeJSON},
		{"application/vnd.pypi.simple.latest+json", true, JSON, ContentTypeJSON},
		{"application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html; q=0.1, text/html; q=0.01", true, JSON, ContentTypeJSON},
		{"application/vnd.pypi.simple.v1+html, application/vnd.pypi.simple.v1+json; q=0.5", true, HTML, ContentTypeHTMLv1},
		{"application/json;q=0.2, text/html;q=0.9", true, HTML, ContentTypeHTMLRaw},
		{"application/vnd.pypi.simple.v1+json;q=0", false, 0, ""},
		{"application/xml", false, 0, ""},
		{"text/plain, image/png", false, 0, ""},
	}

	for _, tt := range tests {
		got, ok := Negotiate(tt.accept)
		if ok != tt.ok {
			t.Errorf("Negotiate(%q) ok = %v, want %v", tt.accept, ok, tt.ok)
			continue
		}

		if ok && (got.Format != tt.format || got.ContentType != tt.ct) {
			t.Errorf("Negotiate(%q) = %+v, want %v %q", tt.accept, got, tt.format, tt.ct)
		}
	}
}

func TestMerge_FirstListWinsByFilename(t *testing.T) {
	a := []File{
		{Filename: "x-1.whl", Hashes: map[string]string{"sha256": sha1hex}},
		{Filename: "x-2.whl", Hashes: map[string]string{"sha256": sha1hex}},
	}
	b := []File{
		{Filename: "x-2.whl", Hashes: map[string]string{"sha256": sha2hex}},
		{Filename: "x-3.whl", Hashes: map[string]string{"sha256": sha2hex}},
	}

	got := Merge(a, nil, b)

	var names []string

	for _, f := range got {
		names = append(names, f.Filename+"="+f.SHA256()[:1])
	}

	if want := []string{"x-1.whl=1", "x-2.whl=1", "x-3.whl=2"}; !reflect.DeepEqual(names, want) {
		t.Errorf("merged = %v, want %v", names, want)
	}
}

func TestServableFiles_DropsUnsafeNames(t *testing.T) {
	got := ServableFiles([]File{{Filename: "ok.whl"}, {Filename: "a/b.whl"}, {Filename: "x.metadata"}, {Filename: ".."}})
	if len(got) != 1 || got[0].Filename != "ok.whl" {
		t.Errorf("servable = %+v", got)
	}
}

func TestURLHelpers(t *testing.T) {
	if got := ProjectURL("my-repo", "foo-bar"); got != "/repos/my-repo/simple/foo-bar/" {
		t.Errorf("ProjectURL = %q", got)
	}

	if got := FileURL("my-repo", "foo-bar", "foo_bar-1.0.tar.gz"); got != "/repos/my-repo/files/foo-bar/foo_bar-1.0.tar.gz" {
		t.Errorf("FileURL = %q", got)
	}
}
