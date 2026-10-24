// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// fuzzHTMLText is the normalization the HTML tokenizer applies to text:
// newlines are unified.
func fuzzHTMLText(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// fuzzHTMLAttr is the one it applies to attribute values and RCDATA (the
// title): NUL also becomes U+FFFD, as in HTML5.
func fuzzHTMLAttr(s string) string {
	return strings.ReplaceAll(fuzzHTMLText(s), "\x00", "\uFFFD")
}

// fuzzAnchor is one <a> element of a rendered page.
type fuzzAnchor struct {
	attrs map[string]string
	// nattrs counts the attributes as written, duplicates included.
	nattrs int
	text   string
}

// fuzzPage is the structure of a rendered HTML page, recovered with the
// tokenizer: whatever the input held, only the fixed template elements may
// appear, the input may only surface as decoded attribute values and text.
type fuzzPage struct {
	tags    map[string]int
	title   string
	heading string
	anchors []fuzzAnchor
}

func fuzzScanPage(t *testing.T, page []byte) fuzzPage {
	t.Helper()

	var (
		out     = fuzzPage{tags: map[string]int{}}
		z       = html.NewTokenizer(strings.NewReader(string(page)))
		current string
	)

	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); !errors.Is(err, io.EOF) {
				t.Fatalf("rendered page does not tokenize: %v", err)
			}

			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			out.tags[tag]++
			current = tag

			if tag == "a" {
				out.anchors = append(out.anchors, fuzzReadAttrs(z, hasAttr))
			}
		case html.TextToken:
			switch current {
			case "title":
				out.title += string(z.Text())
			case "h1":
				out.heading += string(z.Text())
			case "a":
				out.anchors[len(out.anchors)-1].text += string(z.Text())
			}
		case html.EndTagToken:
			current = ""
		case html.DoctypeToken:
			out.tags["!doctype"]++
		case html.CommentToken:
			out.tags["!comment"]++
		}
	}
}

func fuzzReadAttrs(z *html.Tokenizer, hasAttr bool) fuzzAnchor {
	a := fuzzAnchor{attrs: map[string]string{}}

	for hasAttr {
		var key, val []byte

		key, val, hasAttr = z.TagAttr()
		a.attrs[string(key)] = string(val)
		a.nattrs++
	}

	return a
}

// fuzzWantTags is the element census of a page with n anchors.
func fuzzWantTags(n int, project bool) map[string]int {
	want := map[string]int{"!doctype": 1, "html": 1, "head": 1, "meta": 1, "title": 1, "body": 1}
	if project {
		want["h1"] = 1
	}

	if n > 0 {
		want["a"], want["br"] = n, n
	}

	return want
}

// fuzzWantAttrs is what the renderer must have written for a file whose link
// is href.
func fuzzWantAttrs(f *File, href string) map[string]string {
	want := map[string]string{}

	if algo, hex, ok := preferredHash(f.Hashes); ok {
		href += "#" + algo + "=" + hex
	}

	want["href"] = fuzzHTMLAttr(href)

	if f.RequiresPython != "" {
		want["data-requires-python"] = fuzzHTMLAttr(f.RequiresPython)
	}

	if f.CoreMetadata {
		v := "true"
		if algo, hex, ok := preferredHash(f.CoreMetadataHashes); ok {
			v = algo + "=" + hex
		}

		v = fuzzHTMLAttr(v)
		want["data-core-metadata"], want["data-dist-info-metadata"] = v, v
	}

	if f.Yanked {
		want["data-yanked"] = fuzzHTMLAttr(f.YankedReason)
	}

	return want
}

func fuzzCheckTags(t *testing.T, got fuzzPage, n int, project bool) {
	t.Helper()

	if want := fuzzWantTags(n, project); !reflect.DeepEqual(got.tags, want) {
		t.Fatalf("rendered page elements = %v, want %v", got.tags, want)
	}
}

func fuzzCheckAnchor(t *testing.T, got fuzzAnchor, wantAttrs map[string]string, wantText string) {
	t.Helper()

	if got.nattrs != len(wantAttrs) || !reflect.DeepEqual(got.attrs, wantAttrs) {
		t.Fatalf("anchor attributes = %q (%d written), want %q", got.attrs, got.nattrs, wantAttrs)
	}

	if want := fuzzHTMLText(wantText); got.text != want {
		t.Fatalf("anchor text = %q, want %q", got.text, want)
	}
}

// fuzzValidFiles reports whether every string of the files is valid UTF-8,
// the only kind JSON can carry back unchanged.
func fuzzValidFiles(files []File) bool {
	for i := range files {
		f := &files[i]

		for _, s := range []string{f.Filename, f.URL, f.RequiresPython, f.YankedReason, f.UploadTime} {
			if !utf8.ValidString(s) {
				return false
			}
		}

		for _, m := range []map[string]string{f.Hashes, f.CoreMetadataHashes} {
			for k, v := range m {
				if !utf8.ValidString(k) || !utf8.ValidString(v) {
					return false
				}
			}
		}
	}

	return true
}

func fuzzSameFiles(a, b []File) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}

	return reflect.DeepEqual(a, b)
}

// fuzzTrimmedURL is the link an HTML page can carry back: the parser trims
// attribute values.
func fuzzTrimmedURL(f *File) string { return strings.TrimSpace(f.URL) }

func fuzzIdentityURL(f *File) string { return f.URL }

// fuzzCheckParsedFiles asserts what every parsed file list guarantees.
func fuzzCheckParsedFiles(t *testing.T, files []File) {
	t.Helper()

	for i := range files {
		f := &files[i]

		if f.Filename == "" || f.URL == "" {
			t.Fatalf("file %d has an empty filename or url: %+v", i, *f)
		}

		if strings.Contains(f.URL, "#") {
			t.Fatalf("file %d url keeps its fragment: %q", i, f.URL)
		}

		if f.Size < 0 {
			t.Fatalf("file %d has a negative size: %d", i, f.Size)
		}

		for _, m := range []map[string]string{f.Hashes, f.CoreMetadataHashes} {
			for k, v := range m {
				if k != strings.ToLower(k) || v != strings.ToLower(v) {
					t.Fatalf("file %d hash %q=%q is not lowercase", i, k, v)
				}
			}
		}
	}

	for _, f := range ServableFiles(files) {
		if !SafeFilename(f.Filename) {
			t.Fatalf("ServableFiles kept unsafe filename %q", f.Filename)
		}
	}

	merged := Merge(files, files)

	seen := map[string]bool{}

	for i := range merged {
		if seen[merged[i].Filename] {
			t.Fatalf("Merge kept duplicate filename %q", merged[i].Filename)
		}

		seen[merged[i].Filename] = true
	}

	if again := Merge(merged, files); !fuzzSameFiles(again, merged) {
		t.Fatal("Merge is not idempotent")
	}
}

func fuzzProjectSeeds(f *testing.F) {
	f.Helper()

	f.Add([]byte(upstreamHTML), "text/html")
	f.Add([]byte(upstreamHTML), ContentTypeHTMLv1)
	f.Add([]byte(upstreamJSON), ContentTypeJSON)
	f.Add([]byte(upstreamJSON), "text/html")
	f.Add([]byte(`<a href=" x/y.whl#md5=C0FFEE " data-yanked>y</a><a href="z"></a>`), "")
	f.Add([]byte(`<a href="x/ y.tar.gz?q=1"></a><a data-core-metadata="false" href=a>"><script>alert(1)</script></a>`), "text/html")
	f.Add([]byte(`{"files":[{"filename":"a","url":"b#c","hashes":{"SHA256":"AB"},"core-metadata":{"MD5":"X"},"yanked":"","size":-5}]}`), "application/json")
	f.Add([]byte(`{"files":[{"filename":"a","url":"b","yanked":3,"core-metadata":null,"requires-python":null,"size":1.5}]}`), ContentTypeJSON)
	f.Add([]byte(`{"files":`), ContentTypeJSON)
	f.Add([]byte("<a href=x"), "")
	f.Add([]byte{}, "")
}

// FuzzParseProject feeds arbitrary upstream project pages, in either
// serialization, to the parsers and re-renders what they accepted.
func FuzzParseProject(f *testing.F) {
	fuzzProjectSeeds(f)

	f.Fuzz(func(t *testing.T, body []byte, contentType string) {
		files, err := ParseProject(body, contentType)
		if err != nil {
			return
		}

		if again, _ := ParseProject(body, contentType); !fuzzSameFiles(files, again) {
			t.Fatalf("parsing is not deterministic:\n%+v\n%+v", files, again)
		}

		fuzzCheckParsedFiles(t, files)

		if IsJSON(contentType) {
			fuzzCheckJSONRoundTrip(t, files)
			fuzzCheckHTMLSafe(t, files)

			return
		}

		fuzzCheckHTMLRoundTrip(t, files)

		if fuzzValidFiles(files) {
			fuzzCheckJSONRoundTrip(t, files)
		}
	})
}

// fuzzCheckJSONRoundTrip renders files as PEP 691 JSON and parses them back.
func fuzzCheckJSONRoundTrip(t *testing.T, files []File) {
	t.Helper()

	out, err := RenderProject(JSON, "demo", files, fuzzIdentityURL)
	if err != nil {
		t.Fatalf("render json: %v", err)
	}

	back, err := ParseProjectJSON(out)
	if err != nil {
		t.Fatalf("rendered json does not parse: %v\n%s", err, out)
	}

	if !fuzzSameFiles(files, back) {
		t.Fatalf("json round trip changed the files:\n%+v\n%+v", files, back)
	}
}

// fuzzCheckHTMLRoundTrip renders files as PEP 503 HTML and parses them back.
func fuzzCheckHTMLRoundTrip(t *testing.T, files []File) {
	t.Helper()

	fuzzCheckHTMLSafe(t, files)

	out, err := RenderProject(HTML, "demo", files, fuzzTrimmedURL)
	if err != nil {
		t.Fatalf("render html: %v", err)
	}

	back, err := ParseProjectHTML(strings.NewReader(string(out)))
	if err != nil {
		t.Fatalf("rendered html does not parse: %v\n%s", err, out)
	}

	// The parser trims the anchor text, the renderer writes it verbatim: only
	// a filename recovered from the link can carry outer whitespace.
	want := make([]File, len(files))
	copy(want, files)

	for i := range want {
		want[i].Filename = strings.TrimSpace(want[i].Filename)
		want[i].URL = strings.TrimSpace(want[i].URL)
	}

	if !fuzzSameFiles(want, back) {
		t.Fatalf("html round trip changed the files:\n%+v\n%+v\n%s", want, back, out)
	}
}

// fuzzCheckHTMLSafe renders files as HTML and asserts the page structure and
// the attribute values are exactly what the files dictate.
func fuzzCheckHTMLSafe(t *testing.T, files []File) {
	t.Helper()

	out, err := RenderProject(HTML, "<demo>&\"", files, fuzzTrimmedURL)
	if err != nil {
		t.Fatalf("render html: %v", err)
	}

	page := fuzzScanPage(t, out)

	fuzzCheckTags(t, page, len(files), true)

	if want := "Links for <demo>&\""; page.title != want || page.heading != want {
		t.Fatalf("page title %q / heading %q, want %q", page.title, page.heading, want)
	}

	for i := range files {
		fuzzCheckAnchor(t, page.anchors[i], fuzzWantAttrs(&files[i], fuzzTrimmedURL(&files[i])), files[i].Filename)
	}
}

// FuzzParseIndex feeds arbitrary upstream project indexes to the parsers and
// checks the names that survive are safe path segments.
func FuzzParseIndex(f *testing.F) {
	f.Add([]byte(`<a href="/simple/foo/">Foo_Bar</a><br/><a href="x"> baz.qux </a><a>../etc</a><a>a/b</a><a>-x</a>`), "text/html")
	f.Add([]byte(`{"meta":{"api-version":"1.0"},"projects":[{"name":"Foo.Bar"},{"name":""},{"name":"a b"},{"name":"foo-bar"}]}`), ContentTypeJSON)
	f.Add([]byte(`{"projects":[{"name":"\u212a"},{"name":"ǅ"}]}`), "application/json")
	f.Add([]byte(`{"projects":`), ContentTypeJSON)
	f.Add([]byte{}, "")

	f.Fuzz(func(t *testing.T, body []byte, contentType string) {
		names, err := ParseIndex(body, contentType)
		if err != nil {
			return
		}

		seen := map[string]bool{}

		for _, n := range names {
			if !ValidName(n) || NormalizeName(n) != n {
				t.Fatalf("ParseIndex returned a name that is not normalized and valid: %q", n)
			}

			if seen[n] {
				t.Fatalf("ParseIndex returned %q twice", n)
			}

			seen[n] = true
		}

		if again := MergeNames(names, names); !reflect.DeepEqual(again, MergeNames(names)) || len(again) != len(names) {
			t.Fatalf("MergeNames is not idempotent: %q", again)
		}

		fuzzCheckIndexRoundTrip(t, names)
	})
}

// fuzzCheckIndexRoundTrip renders names in both serializations and parses
// them back.
func fuzzCheckIndexRoundTrip(t *testing.T, names []string) {
	t.Helper()

	href := func(name string) string { return ProjectURL("repo", name) }

	for _, format := range []Format{HTML, JSON} {
		out, err := RenderIndex(format, names, href)
		if err != nil {
			t.Fatalf("render index: %v", err)
		}

		ct := ContentTypeHTMLRaw
		if format == JSON {
			ct = ContentTypeJSON
		}

		back, err := ParseIndex(out, ct)
		if err != nil {
			t.Fatalf("rendered index does not parse: %v\n%s", err, out)
		}

		if len(names) != len(back) || (len(names) > 0 && !reflect.DeepEqual(names, back)) {
			t.Fatalf("index round trip changed the names: %q -> %q", names, back)
		}
	}
}

// FuzzRenderIndex renders arbitrary project names: the page may only hold the
// template elements, and the names and links come back verbatim.
func FuzzRenderIndex(f *testing.F) {
	f.Add("demo", "x\"><script>alert(1)</script>", "/simple/x/")
	f.Add("a&amp;b", "'\r\n<!--", "javascript:alert(1)")
	f.Add("", " padded ", "")

	f.Fuzz(func(t *testing.T, first, second, href string) {
		names := []string{first, second}
		link := func(n string) string { return href + n }

		out, err := RenderIndex(HTML, names, link)
		if err != nil {
			t.Fatalf("render html index: %v", err)
		}

		page := fuzzScanPage(t, out)

		fuzzCheckTags(t, page, len(names), false)

		for i, n := range names {
			fuzzCheckAnchor(t, page.anchors[i], map[string]string{"href": fuzzHTMLAttr(link(n))}, n)
		}

		out, err = RenderIndex(JSON, names, link)
		if err != nil {
			t.Fatalf("render json index: %v", err)
		}

		var doc struct {
			Projects []struct {
				Name string `json:"name"`
			} `json:"projects"`
		}

		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("rendered json index is invalid: %v\n%s", err, out)
		}

		if len(doc.Projects) != len(names) {
			t.Fatalf("json index lists %d projects, want %d", len(doc.Projects), len(names))
		}

		if utf8.ValidString(first) && utf8.ValidString(second) {
			for i, n := range names {
				if doc.Projects[i].Name != n {
					t.Fatalf("json index project %d = %q, want %q", i, doc.Projects[i].Name, n)
				}
			}
		}
	})
}

// FuzzRenderProject renders arbitrary file records: in HTML the page may only
// hold the template elements with exactly the attributes the file dictates,
// in JSON the document must parse back to the same file.
func FuzzRenderProject(f *testing.F) {
	f.Add("demo-1.0.whl", "https://x/demo-1.0.whl", "sha256", "ab", "sha256", "cd", ">=3.8", true, true, "reason", "2024-01-02T03:04:05Z", int64(12), "demo")
	f.Add("a\"b'<c>&.whl", "u\r\n\"onmouseover=\"x", "MD5", "0F", "", "", "\"><script>", true, true, "\r\"/>", "", int64(-3), "<x>")
	f.Add("", "", "", "", "", "", "", false, false, "", "", int64(0), "")

	f.Fuzz(func(t *testing.T, filename, link, algo, hex, metaAlgo, metaHex, requiresPython string,
		coreMetadata, yanked bool, reason, uploadTime string, size int64, project string,
	) {
		file := File{
			Filename:       filename,
			URL:            link,
			Hashes:         map[string]string{strings.ToLower(algo): strings.ToLower(hex)},
			RequiresPython: requiresPython,
			CoreMetadata:   coreMetadata,
			Yanked:         yanked,
			YankedReason:   reason,
			Size:           size,
			UploadTime:     uploadTime,
		}

		if coreMetadata {
			file.CoreMetadataHashes = map[string]string{strings.ToLower(metaAlgo): strings.ToLower(metaHex)}
		}

		out, err := RenderProject(HTML, project, []File{file}, fuzzIdentityURL)
		if err != nil {
			t.Fatalf("render html: %v", err)
		}

		page := fuzzScanPage(t, out)

		fuzzCheckTags(t, page, 1, true)
		fuzzCheckAnchor(t, page.anchors[0], fuzzWantAttrs(&file, link), filename)

		// The tokenizer reads <title> as RCDATA, where NUL becomes U+FFFD, and
		// keeps it in the heading text.
		wantTitle, wantHeading := fuzzHTMLAttr("Links for "+project), fuzzHTMLText("Links for "+project)
		if page.title != wantTitle || page.heading != wantHeading {
			t.Fatalf("page title %q / heading %q, want %q / %q", page.title, page.heading, wantTitle, wantHeading)
		}

		fuzzCheckRenderedJSON(t, &file, project)
	})
}

func fuzzCheckRenderedJSON(t *testing.T, file *File, project string) {
	t.Helper()

	out, err := RenderProject(JSON, project, []File{*file}, fuzzIdentityURL)
	if err != nil {
		t.Fatalf("render json: %v", err)
	}

	if !json.Valid(out) {
		t.Fatalf("rendered json is invalid:\n%s", out)
	}

	back, err := ParseProjectJSON(out)
	if err != nil {
		t.Fatalf("rendered json does not parse: %v", err)
	}

	if !fuzzValidFiles([]File{*file}) || !utf8.ValidString(project) {
		return
	}

	want := *file
	want.URL = stripFragment(file.URL)
	want.Hashes = lowerHashes(want.Hashes)
	want.Size = max(want.Size, 0)

	if want.CoreMetadata {
		want.CoreMetadataHashes = lowerHashes(want.CoreMetadataHashes)
	} else {
		want.CoreMetadataHashes = nil
	}

	if !want.Yanked {
		want.YankedReason = ""
	}

	if want.Filename == "" || want.URL == "" {
		if len(back) != 0 {
			t.Fatalf("json parser kept a file without filename or url: %+v", back)
		}

		return
	}

	if !fuzzSameFiles([]File{want}, back) {
		t.Fatalf("json round trip changed the file:\n%+v\n%+v\n%s", want, back, out)
	}
}

// FuzzNegotiate feeds arbitrary Accept headers to the content negotiation.
func FuzzNegotiate(f *testing.F) {
	f.Add("")
	f.Add(UpstreamAccept)
	f.Add("application/vnd.pypi.simple.v1+json, text/html;q=0.5")
	f.Add("text/html;q=0, application/vnd.pypi.simple.v1+json;q=0")
	f.Add("*/*;q=NaN, application/*;q=+Inf, text/*;q=-1e999")
	f.Add("text/html;q=1;q=0,,;;,\"\\, application/vnd.pypi.simple.latest+json;q=0x1p-2")
	f.Add("application/json")

	f.Fuzz(func(t *testing.T, accept string) {
		offer, ok := Negotiate(accept)
		if again, ok2 := Negotiate(accept); again != offer || ok2 != ok {
			t.Fatalf("Negotiate(%q) is not deterministic", accept)
		}

		if !ok {
			if offer != (Offer{}) {
				t.Fatalf("Negotiate(%q) refused but returned %+v", accept, offer)
			}

			if strings.TrimSpace(accept) == "" {
				t.Fatal("an empty Accept header must be acceptable")
			}

			return
		}

		switch offer.Format {
		case JSON:
			if offer.ContentType != ContentTypeJSON {
				t.Fatalf("Negotiate(%q) = JSON as %q", accept, offer.ContentType)
			}
		case HTML:
			if offer.ContentType != ContentTypeHTMLv1 && offer.ContentType != ContentTypeHTMLRaw {
				t.Fatalf("Negotiate(%q) = HTML as %q", accept, offer.ContentType)
			}
		default:
			t.Fatalf("Negotiate(%q) returned unknown format %d", accept, offer.Format)
		}

		// The advertised content type must be read back as the format it
		// names by the response parsers.
		if IsJSON(offer.ContentType) != (offer.Format == JSON) {
			t.Fatalf("Negotiate(%q) = %+v, but IsJSON(%q) = %v", accept, offer, offer.ContentType, IsJSON(offer.ContentType))
		}
	})
}

// FuzzParsePath classifies arbitrary request paths and checks that whatever
// is reported as a project or file is a safe, normalized path segment.
func FuzzParsePath(f *testing.F) {
	f.Add("simple/")
	f.Add("/simple/Foo_Bar/")
	f.Add("files/foo/foo-1.0-py3-none-any.whl.metadata")
	f.Add("files/foo/../../etc/passwd")
	f.Add("simple/%2e%2e/")
	f.Add("simple/..%2f/")
	f.Add("files/foo/a\x00b")
	f.Add("files//x")
	f.Add("")

	f.Fuzz(func(t *testing.T, relPath string) {
		kind, project, filename := ParsePath(relPath)

		switch kind {
		case PathIndex, PathOther:
			if project != "" || filename != "" {
				t.Fatalf("ParsePath(%q) = (%v, %q, %q): kind carries no names", relPath, kind, project, filename)
			}
		case PathProject:
			if !ValidName(project) || NormalizeName(project) != project || filename != "" {
				t.Fatalf("ParsePath(%q) project = %q (filename %q)", relPath, project, filename)
			}
		case PathFile:
			if !ValidName(project) || NormalizeName(project) != project {
				t.Fatalf("ParsePath(%q) file project = %q", relPath, project)
			}

			stem := strings.TrimSuffix(filename, MetadataSuffix)
			if !SafeFilename(stem) || strings.ContainsAny(filename, `/\`) || filename == ".." || filename == "." {
				t.Fatalf("ParsePath(%q) filename = %q", relPath, filename)
			}
		default:
			t.Fatalf("ParsePath(%q) returned unknown kind %d", relPath, kind)
		}
	})
}

// FuzzLinks builds the repository URLs of arbitrary repo, project and file
// names: every name must stay one path segment, and a file URL that names a
// servable file must be routed back to it.
func FuzzLinks(f *testing.F) {
	f.Add("repo", "demo", "demo-1.0.tar.gz")
	f.Add("re/po", "../x", "a b?c#d%2f")
	f.Add("", "", "")

	f.Fuzz(func(t *testing.T, repo, project, filename string) {
		fuzzCheckLinkSegments(t, repo, project, filename)

		if ValidName(project) && NormalizeName(project) == project && SafeFilename(filename) {
			kind, gotProject, gotFilename := ParsePath("files/" + project + "/" + filename)
			if kind != PathFile || gotProject != project || gotFilename != filename {
				t.Fatalf("ParsePath(file %q/%q) = (%v, %q, %q)", project, filename, kind, gotProject, gotFilename)
			}

			if kind, gotProject, _ = ParsePath("simple/" + project + "/"); kind != PathProject || gotProject != project {
				t.Fatalf("ParsePath(project %q) = (%v, %q)", project, kind, gotProject)
			}
		}
	})
}

// fuzzCheckLinkSegments asserts the repo, project and file names each stay one
// path segment of the URLs built from them.
func fuzzCheckLinkSegments(t *testing.T, repo, project, filename string) {
	t.Helper()

	for _, u := range []string{ProjectURL(repo, project), FileURL(repo, project, filename)} {
		if _, err := url.Parse(u); err != nil {
			t.Fatalf("%q does not parse as a URL: %v", u, err)
		}
	}

	segs := strings.Split(FileURL(repo, project, filename), "/")
	if len(segs) != 6 || segs[0] != "" || segs[1] != "repos" || segs[3] != "files" {
		t.Fatalf("FileURL(%q, %q, %q) = %q: a name escaped its segment", repo, project, filename, strings.Join(segs, "/"))
	}

	for i, want := range map[int]string{2: repo, 4: project, 5: filename} {
		if got, err := url.PathUnescape(segs[i]); err != nil || got != want {
			t.Fatalf("FileURL segment %d unescapes to %q (%v), want %q", i, got, err, want)
		}
	}

	if segs = strings.Split(ProjectURL(repo, project), "/"); len(segs) != 6 || segs[5] != "" || segs[3] != "simple" {
		t.Fatalf("ProjectURL(%q, %q) = %q: a name escaped its segment", repo, project, strings.Join(segs, "/"))
	}
}

// FuzzNormalizeName checks the PEP 503 normalization is idempotent and agrees
// with the name validator.
func FuzzNormalizeName(f *testing.F) {
	f.Add("Foo_Bar.baz")
	f.Add("--..__")
	f.Add("\u212a-İ\xff")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		norm := NormalizeName(name)
		if again := NormalizeName(norm); again != norm {
			t.Fatalf("NormalizeName is not idempotent: %q -> %q -> %q", name, norm, again)
		}

		if ValidName(norm) && (strings.Contains(norm, "/") || strings.Contains(norm, "..")) {
			t.Fatalf("valid name %q (from %q) is not a plain path segment", norm, name)
		}
	})
}
