// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

const (
	// nugetFuzzUpstreamToken stands for the origin of the upstream in the
	// documents of a seed; the target substitutes the real one.
	nugetFuzzUpstreamToken = "UPSTREAM"

	// nugetFuzzOwnOrigin is the origin the repository is served at (oaBase).
	nugetFuzzOwnOrigin = oaBase
)

func nugetFuzzGzip(b []byte) []byte {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()

	return buf.Bytes()
}

// nugetFuzzGunzip is the document a client of the upstream sees: b, or its
// decompression when b is gzip data. ok is false for a gzip stream that does
// not decompress.
func nugetFuzzGunzip(b []byte) ([]byte, bool) {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b, true
	}

	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}

	out, err := io.ReadAll(zr)

	return out, err == nil
}

func nugetFuzzDecode(doc []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	var v any

	return v, dec.Decode(&v) == nil
}

// nugetFuzzWant is the string value an upstream document has after the
// rewrite: the longest upstream base it starts with is replaced by this
// repository's route for it, any other string is untouched.
func nugetFuzzWant(s string, pairs []pkgnuget.URLPrefix) string {
	best := -1

	for i, p := range pairs {
		if p.From != "" && strings.HasPrefix(s, p.From) && (best < 0 || len(p.From) > len(pairs[best].From)) {
			best = i
		}
	}

	if best < 0 {
		return s
	}

	return pairs[best].To + s[len(pairs[best].From):]
}

// nugetFuzzPairs are the replacements the proxy has to make for the upstream
// idx, written from what the service index says: registrations to
// /v3/registration/, flat container to /v3/package/. ok is false when the
// upstream cannot be told apart from this repository or its two bases are the
// same URL.
func nugetFuzzPairs(idx *pkgnuget.ServiceIndex, repoURL string) ([]pkgnuget.URLPrefix, bool) {
	pairs := []pkgnuget.URLPrefix{
		{From: idx.Registrations, To: repoURL + "/v3/registration/"},
		{From: idx.PackageBase, To: repoURL + "/v3/package/"},
	}

	if idx.Registrations != "" && idx.Registrations == idx.PackageBase {
		return nil, false
	}

	for _, p := range pairs {
		if p.From != "" && (strings.HasPrefix(p.From, nugetFuzzOwnOrigin) || strings.HasPrefix(nugetFuzzOwnOrigin, p.From)) {
			return nil, false
		}
	}

	return pairs, true
}

// nugetFuzzCompare checks out against in, string by string: same shape, same
// keys, same scalars, and every string as nugetFuzzWant says.
func nugetFuzzCompare(t *testing.T, where string, in, out any, pairs []pkgnuget.URLPrefix) {
	t.Helper()

	switch iv := in.(type) {
	case string:
		if want := nugetFuzzWant(iv, pairs); out != want {
			t.Fatalf("%s: %q became %#v, want %q", where, iv, out, want)
		}
	case map[string]any:
		ov, ok := out.(map[string]any)
		if !ok || len(ov) != len(iv) {
			t.Fatalf("%s: object became %#v", where, out)
		}

		for k, e := range iv {
			oe, present := ov[k]
			if !present {
				t.Fatalf("%s: key %q lost", where, k)
			}

			nugetFuzzCompare(t, where+"/"+k, e, oe, pairs)
		}
	case []any:
		ov, ok := out.([]any)
		if !ok || len(ov) != len(iv) {
			t.Fatalf("%s: array became %#v", where, out)
		}

		for i, e := range iv {
			nugetFuzzCompare(t, where+"/"+strconv.Itoa(i), e, ov[i], pairs)
		}
	default:
		if !reflect.DeepEqual(in, out) {
			t.Fatalf("%s: %#v became %#v", where, in, out)
		}
	}
}

// nugetFuzzPointAtPages applies to a decoded registration index what the
// proxy documents: a page the upstream does not inline is advertised at
// <pagePrefix><lower>/<upper>.json, the route the proxy serves it at.
func nugetFuzzPointAtPages(in any, pagePrefix string) {
	top, _ := in.(map[string]any)
	pages, _ := top["items"].([]any)

	for _, p := range pages {
		page, _ := p.(map[string]any)
		lower, _ := page["lower"].(string)
		upper, _ := page["upper"].(string)

		if _, inline := page["items"]; page != nil && !inline && lower != "" && upper != "" {
			page["@id"] = pagePrefix + url.PathEscape(lower) + "/" + url.PathEscape(upper) + ".json"
		}
	}
}

// nugetFuzzCheckPages checks the pages a rendered registration index of id
// does not inline: a client that follows the URL reaches the page route of the
// proxy, for the same bounds, whenever the proxy can serve such a route.
func nugetFuzzCheckPages(t *testing.T, out any, id, repoURL string) {
	t.Helper()

	top, _ := out.(map[string]any)
	pages, _ := top["items"].([]any)

	for _, p := range pages {
		page, _ := p.(map[string]any)
		lower, _ := page["lower"].(string)
		upper, _ := page["upper"].(string)

		if _, inline := page["items"]; page == nil || inline || !safeNuGetSegment(lower) || !safeNuGetSegment(upper) {
			continue
		}

		link, _ := page["@id"].(string)

		route, ok := strings.CutPrefix(link, repoURL+"/")
		if !ok {
			t.Fatalf("page %q..%q is advertised at %q, outside the repository", lower, upper, link)
		}

		got, ok := parseNuGetPath(route)
		if !ok || got.kind != nugetRegPage || got.id != id || got.lower != lower || got.upper != upper {
			t.Fatalf("page %q..%q is advertised at %q, which the proxy reads as %+v (%v)", lower, upper, link, got, ok)
		}
	}
}

// nugetFuzzCheckRendered checks a document the proxy rendered from the
// upstream's doc: still JSON of the same shape with every upstream URL
// replaced as the service index prescribes.
func nugetFuzzCheckRendered(t *testing.T, kind nugetKind, id string, doc, out []byte, idx *pkgnuget.ServiceIndex, repoURL string) {
	t.Helper()

	if kind == nugetVersions {
		if !bytes.Equal(out, doc) {
			t.Fatalf("version list changed:\n in: %s\nout: %s", doc, out)
		}

		return
	}

	pairs, ok := nugetFuzzPairs(idx, repoURL)
	if !ok {
		return
	}

	in, ok := nugetFuzzDecode(doc)
	if !ok {
		t.Fatalf("rendered a document that is not JSON: %s", doc)
	}

	rendered, ok := nugetFuzzDecode(out)
	if !ok || !json.Valid(out) {
		t.Fatalf("rendered %q is not JSON", out)
	}

	if kind == nugetRegIndex {
		nugetFuzzPointAtPages(in, repoURL+"/v3/registration/"+id+"/page/")
		nugetFuzzCheckPages(t, rendered, id, repoURL)
	}

	nugetFuzzCompare(t, "", in, rendered, pairs)
}

// FuzzNuGetParsePath feeds arbitrary repository-relative paths to the NuGet
// route parser: whatever it accepts has a canonical key made of safe segments
// only, that key reads back as the same route, every spelling of the id gives
// it, and the upstream URL of the document stays below the upstream's base.
func FuzzNuGetParsePath(f *testing.F) {
	for _, p := range []string{
		"v3/index.json", "v3/query", "v3/autocomplete", "v3/package/Lib.A/index.json",
		"v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg", "v3/package/Lib.A/1.0/LIB.A.1.0.NUPKG", "v3/package/lib.a/1.0.0/lib.a.nuspec",
		"v3/package/lib/1.0.0-Beta+meta/lib.1.0.0-beta.nupkg", "v3/package/lib/Foo/lib.Foo.nupkg", "v3/package/lib/Foo/lib.foo.nupkg",
		"v3/registration/lib.a/index.json", "v3/registration/lib.a/1.0.0.json", "v3/registration/lib.a/1.0.json",
		"v3/registration/big/page/1.0.0/2.0.0.json", "/v3/registration/Big/page/1.0.0/2.0.0.json/",
		"v3/package/../index.json", "v3//package/x/index.json", "v3/package/./index.json", "v3/package/a%2fb/index.json",
		"v3/package/a b/index.json", "v3/registration/x/page/../2.json", "v3/registration/x/..json", "v3/registration/x/.json",
		"v3/package/x/1.0.0/x.1.0.0.nupkg/y", "v3/package/x/1.0.0/..nuspec", "v3/package/" + strings.Repeat("a", 257) + "/index.json", "",
	} {
		f.Add(p)
	}

	f.Fuzz(func(t *testing.T, relPath string) {
		p, ok := parseNuGetPath(relPath)
		if !ok {
			return
		}

		nugetFuzzCheckPath(t, p)
	})
}

func nugetFuzzCheckPath(t *testing.T, p nugetPath) {
	t.Helper()

	switch p.kind {
	case nugetIndex, nugetQuery, nugetAutocomplete:
		if p != (nugetPath{kind: p.kind}) {
			t.Fatalf("route %d carries a package: %+v", p.kind, p)
		}

		return
	case nugetVersions, nugetPackageFile, nugetRegIndex, nugetRegPage, nugetRegLeaf:
	default:
		t.Fatalf("unknown route kind %d", p.kind)
	}

	if !safeNuGetSegment(p.id) || p.id != strings.ToLower(p.id) {
		t.Fatalf("id %q is not a canonical segment", p.id)
	}

	key := p.key()

	nugetFuzzCheckKeyChars(t, key)

	if !strings.HasPrefix(key, "v3/") || path.Clean(key) != key {
		t.Fatalf("key %q is not a clean path below v3/", key)
	}

	if again, ok := parseNuGetPath(key); !ok || again != p {
		t.Fatalf("key %q of %+v reads back as %+v (%v)", key, p, again, ok)
	}

	dir := "v3/package/"
	if p.kind == nugetRegIndex || p.kind == nugetRegPage || p.kind == nugetRegLeaf {
		dir = "v3/registration/"
	}

	// Any spelling of the id is the same document.
	alt := dir + strings.ToUpper(p.id) + strings.TrimPrefix(key, dir+p.id)
	if again, ok := parseNuGetPath(alt); !ok || again != p {
		t.Fatalf("%q reads as %+v (%v), want %+v", alt, again, ok, p)
	}

	nugetFuzzCheckUpstreamURL(t, p, dir, key)
}

// nugetFuzzCheckKeyChars checks that key is made of the characters a safe
// segment and the route's slashes are made of.
func nugetFuzzCheckKeyChars(t *testing.T, key string) {
	t.Helper()

	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && !strings.ContainsRune("./-_+", c) {
			t.Fatalf("key %q has the character %q", key, c)
		}
	}
}

// nugetFuzzCheckUpstreamURL checks that the upstream URLs made of a route are
// clean URLs below the base the service index named.
func nugetFuzzCheckUpstreamURL(t *testing.T, p nugetPath, dir, key string) {
	t.Helper()

	idx := &pkgnuget.ServiceIndex{PackageBase: "https://up.example/flat/", Registrations: "https://up.example/reg/"}
	base := idx.PackageBase

	if dir == "v3/registration/" {
		base = idx.Registrations
	}

	want := base + strings.TrimPrefix(key, dir)

	if got := nugetDocURL(idx, p); got != "" && got != want {
		t.Fatalf("%+v is fetched from %q, want %q", p, got, want)
	}

	u, err := url.Parse(want)
	if err != nil || u.Host != "up.example" || u.RawQuery != "" || u.Fragment != "" || u.Path != strings.TrimPrefix(want, "https://up.example") ||
		path.Clean(u.Path) != u.Path || !strings.HasPrefix(u.Path, strings.TrimPrefix(base, "https://up.example")) {
		t.Fatalf("%+v maps to %q, which leaves the upstream's base (%v)", p, want, err)
	}
}

// FuzzNuGetRenderDoc feeds an arbitrary upstream service index and an
// arbitrary registration or flat container document to the rewrite the proxy
// serves it with: the result is JSON of the same shape, no URL under the
// upstream's registration or package base is left, the one under the longer
// base goes to its own route when the bases nest, and the pages not inlined
// are advertised at routes the proxy serves.
func FuzzNuGetRenderDoc(f *testing.F) {
	nuget := `{"version":"3.0.0","resources":[{"@id":"https://api.nuget.org/v3-flatcontainer/","@type":"PackageBaseAddress/3.0.0"},` +
		`{"@id":"https://api.nuget.org/v3/registration5-gz-semver2/","@type":"RegistrationsBaseUrl/3.6.0"}]}`
	reg := `{"@id":"https://api.nuget.org/v3/registration5-gz-semver2/lib/index.json","count":2,"items":[` +
		`{"@id":"https://api.nuget.org/v3/registration5-gz-semver2/lib/page/1.0.0/2.0.0.json","lower":"1.0.0","upper":"2.0.0","count":2},` +
		`{"@id":"https://api.nuget.org/v3/registration5-gz-semver2/lib/index.json#page/3.0.0/3.0.0","lower":"3.0.0","upper":"3.0.0","count":1,"items":[` +
		`{"@id":"https://api.nuget.org/v3/registration5-gz-semver2/lib/3.0.0.json","packageContent":"https://api.nuget.org/v3-flatcontainer/lib/3.0.0/lib.3.0.0.nupkg",` +
		`"catalogEntry":{"@id":"https://api.nuget.org/v3/catalog0/data/lib.3.0.0.json","version":"3.0.0","n":1.50e3}}]}]}`

	f.Add([]byte(nuget), []byte(reg), uint8(0))
	f.Add([]byte(nuget), []byte(reg), uint8(1))
	f.Add([]byte(nuget), []byte(reg), uint8(2))
	f.Add([]byte(nuget), []byte(`{"versions":["1.0.0"]}`), uint8(3))
	f.Add([]byte(nuget), []byte(`[]`), uint8(0))
	f.Add([]byte(nuget), []byte(`{"items":[null,5,{"lower":"1","upper":"2"},{"lower":"a b","upper":"c/d","items":null}]}`), uint8(0))
	f.Add([]byte(nuget), []byte(`{"@id":"https://api.nuget.org/v3-flatcontainer/","k":[["https://api.nuget.org/v3-flatcontainer/x"]]} trailing`), uint8(1))

	f.Fuzz(func(t *testing.T, indexDoc, doc []byte, sel uint8) {
		idx, err := pkgnuget.ParseServiceIndex(indexDoc)
		if err != nil {
			return
		}

		const id = "lib"

		kind := []nugetKind{nugetRegIndex, nugetRegPage, nugetRegLeaf, nugetVersions}[sel%4]
		repoURL := nugetRepoURL

		out, err := renderNuGetDoc(doc, nugetPath{kind: kind, id: id}, idx, repoURL)

		if _, decodable := nugetFuzzDecode(doc); kind != nugetVersions && decodable != (err == nil) {
			t.Fatalf("renderNuGetDoc(%q) err = %v, but the document is JSON: %v", doc, err, decodable)
		}

		if err != nil {
			return
		}

		nugetFuzzCheckRendered(t, kind, id, doc, out, idx, repoURL)
	})
}

// FuzzNuGetUpstreamDoc feeds arbitrary bytes to the checks of an upstream
// metadata document and of a nuspec: a JSON object (gzipped or not) is the only
// metadata accepted, decompression agrees with compress/gzip and leaves
// plain bodies alone, and a nuspec is accepted only when its root element is
// "package".
func FuzzNuGetUpstreamDoc(f *testing.F) {
	f.Add([]byte(`{"versions":["1.0.0"]}`))
	f.Add(nugetFuzzGzip([]byte(`{"items":[]}`)))
	f.Add(nugetFuzzGzip(nugetFuzzGzip([]byte(`{}`))))
	f.Add([]byte("\x1f\x8b\x08\x00truncated"))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{} {}`))
	f.Add([]byte("<html><body>502 Bad Gateway</body></html>"))
	f.Add([]byte("\ufeff<?xml version=\"1.0\"?><package><metadata><id>a</id></metadata></package>"))
	f.Add([]byte(`<?xml version="1.0" encoding="windows-1252"?><package/>`))
	f.Add([]byte(`<?xml version="1.0"?><!DOCTYPE package [<!ENTITY e "x">]><package>&e;</package>`))
	f.Add([]byte(`<metadata><package/></metadata>`))
	f.Add([]byte(`<package`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		nugetFuzzCheckGunzip(t, data)
		nugetFuzzCheckMetadata(t, data)
		nugetFuzzCheckNuspec(t, data)
	})
}

func nugetFuzzCheckGunzip(t *testing.T, data []byte) {
	t.Helper()

	got, err := gunzipDoc(data)
	want, ok := nugetFuzzGunzip(data)

	if (err == nil) != ok || (ok && !bytes.Equal(got, want)) {
		t.Fatalf("gunzipDoc(%q) = %q, %v; compress/gzip: %q, %v", data, got, err, want, ok)
	}

	if len(data) >= 2 && (data[0] != 0x1f || data[1] != 0x8b) && !bytes.Equal(got, data) {
		t.Fatalf("gunzipDoc changed a body that is not gzip: %q -> %q", data, got)
	}

	if again, err := gunzipDoc(nugetFuzzGzip(data)); err != nil || !bytes.Equal(again, data) {
		t.Fatalf("a gzipped document does not come back: %q -> %q (%v)", data, again, err)
	}
}

func nugetFuzzCheckMetadata(t *testing.T, data []byte) {
	t.Helper()

	err := verifyNuGetObject(bytes.NewReader(data))

	doc, ok := nugetFuzzGunzip(data)

	var top map[string]json.RawMessage

	isObject := ok && json.Unmarshal(doc, &top) == nil && top != nil
	if (err == nil) != isObject {
		t.Fatalf("verifyNuGetObject(%q) = %v, but the document is a JSON object: %v", data, err, isObject)
	}
}

func nugetFuzzCheckNuspec(t *testing.T, data []byte) {
	t.Helper()

	if err := verifyNuspec(bytes.NewReader(data)); err == nil && !bytes.Contains(data, []byte("package")) {
		t.Fatalf("verifyNuspec accepted %q, which has no package element", data)
	}
}

// nugetFuzzFeed is an upstream whose documents are the ones a fuzz input
// gives, whatever the path below the resource base.
type nugetFuzzFeed struct {
	index, reg, flat []byte
	gzipped          bool
	last             atomic.Pointer[[]byte] // the last document asked for, before any gzip
}

// body is the document the upstream has at urlPath, before any gzip.
func (u *nugetFuzzFeed) body(urlPath string) ([]byte, bool) {
	switch {
	case urlPath == "/v3/index.json":
		return u.index, true
	case strings.HasPrefix(urlPath, "/reg/download/"), strings.HasPrefix(urlPath, "/flat/"):
		return u.flat, true
	case strings.HasPrefix(urlPath, "/reg/"):
		return u.reg, true
	}

	return nil, false
}

func (u *nugetFuzzFeed) handler(w http.ResponseWriter, r *http.Request) {
	body, ok := u.body(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	raw := body
	u.last.Store(&raw)

	w.Header().Set("Content-Type", "application/json")

	if u.gzipped {
		w.Header().Set("Content-Encoding", "gzip")
		body = nugetFuzzGzip(body)
	}

	_, _ = w.Write(body)
}

// nugetFuzzUnderBase reports whether every URL the proxy may call (the
// service index's resources) is on the upstream under test.
func nugetFuzzUnderBase(idx *pkgnuget.ServiceIndex, base string) bool {
	for _, u := range []string{idx.PackageBase, idx.Registrations, idx.Search, idx.Autocomplete} {
		if u != "" && !strings.HasPrefix(u, base+"/") {
			return false
		}
	}

	return true
}

// FuzzNuGetProxyServe serves a nuget proxy repository from an upstream whose
// service index, registration documents and flat container documents are
// arbitrary (gzipped or not): the proxy never answers 500 or leaks an
// upstream URL, answers 200 only for documents that pass its checks, serves
// a cached document again unchanged without asking the upstream, and answers
// HEAD like GET.
func FuzzNuGetProxyServe(f *testing.F) {
	index := `{"version":"3.0.0","resources":[{"@id":"UPSTREAM/flat/","@type":"PackageBaseAddress/3.0.0"},` +
		`{"@id":"UPSTREAM/reg/","@type":"RegistrationsBaseUrl/3.6.0"},{"@id":"UPSTREAM/search/query","@type":"SearchQueryService"}]}`
	reg := `{"@id":"UPSTREAM/reg/lib/index.json","count":2,"items":[` +
		`{"@id":"UPSTREAM/reg/lib/page/1.0.0/2.0.0.json","lower":"1.0.0","upper":"2.0.0","count":2},` +
		`{"@id":"UPSTREAM/reg/lib/index.json#page/3.0.0/3.0.0","lower":"3.0.0","upper":"3.0.0","count":1,"items":[` +
		`{"@id":"UPSTREAM/reg/lib/3.0.0.json","packageContent":"UPSTREAM/flat/lib/3.0.0/lib.3.0.0.nupkg",` +
		`"catalogEntry":{"@id":"UPSTREAM/catalog/lib.3.0.0.json","version":"3.0.0"}}]}]}`

	f.Add([]byte(index), []byte(reg), []byte(`{"versions":["1.0.0","3.0.0"]}`), false)
	f.Add([]byte(index), []byte(reg), []byte(`{"versions":["1.0.0","3.0.0"]}`), true)
	f.Add([]byte(index), []byte(`{"@id":"UPSTREAM/reg/lib/1.0.0.json","catalogEntry":"UPSTREAM/catalog/lib.1.0.0.json"}`), []byte(`{"versions":[]}`), false)
	f.Add([]byte(index), []byte(`<html>`), []byte(`[]`), false)
	f.Add([]byte(index), []byte{}, []byte("\x1f\x8bgarbage"), false)
	f.Add([]byte(`{"resources":[]}`), []byte(`{}`), []byte(`{}`), false)
	f.Add([]byte(`[]`), []byte(`{}`), []byte(`{}`), true)

	f.Fuzz(nugetFuzzProxyServe)
}

func nugetFuzzProxyServe(t *testing.T, indexDoc, regDoc, flatDoc []byte, gzipped bool) {
	var feed atomic.Pointer[nugetFuzzFeed]

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { feed.Load().handler(w, r) })
	base := strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")

	substitute := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte(nugetFuzzUpstreamToken), []byte(base)) }
	up := &nugetFuzzFeed{index: substitute(indexDoc), reg: substitute(regDoc), flat: substitute(flatDoc), gzipped: gzipped}
	feed.Store(up)

	f.repo.Format = "nuget"
	f.repo.Name = "nugetorg"
	f.repo.UpstreamURL = base + "/v3/index.json"
	f.svc.SetBaseURL(oaBase + "/")

	var idx *pkgnuget.ServiceIndex

	if raw, ok := nugetFuzzGunzip(up.index); ok && pkgnuget.IsObject(raw) == nil {
		idx, _ = pkgnuget.ParseServiceIndex(raw)
	}

	if idx != nil && !nugetFuzzUnderBase(idx, base) {
		return // the proxy would call a host of the fuzz input
	}

	w := f.do(http.MethodGet, "v3/index.json")
	if idx == nil {
		nugetFuzzWantStatus(t, "v3/index.json", w, http.StatusBadGateway)
	} else {
		nugetFuzzCheckServedIndex(t, w, base)
	}

	for _, p := range []string{"v3/package/Lib/index.json", "v3/registration/lib/index.json", "v3/registration/lib/1.0.json", "v3/registration/lib/page/1.0.0/2.0.0.json"} {
		nugetFuzzCheckServedDoc(t, f, up, idx, p)
	}
}

func nugetFuzzWantStatus(t *testing.T, p string, w *httptest.ResponseRecorder, want int) {
	t.Helper()

	if w.Code != want {
		t.Fatalf("GET %s = %d, want %d: %s", p, w.Code, want, w.Body.String())
	}
}

func nugetFuzzCheckServedIndex(t *testing.T, w *httptest.ResponseRecorder, base string) {
	t.Helper()

	body := w.Body.Bytes()

	if w.Code != http.StatusOK {
		t.Fatalf("GET v3/index.json = %d for an upstream index that is usable: %s", w.Code, body)
	}

	if bytes.Contains(body, []byte(base)) {
		t.Fatalf("the service index leaks the upstream %s: %s", base, body)
	}

	var doc struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}

	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Resources) == 0 {
		t.Fatalf("service index %s is not usable (%v)", body, err)
	}

	for _, r := range doc.Resources {
		if !strings.HasPrefix(r.ID, nugetRepoURL+"/v3/") || r.Type == "" {
			t.Fatalf("resource %+v does not point at the repository", r)
		}
	}
}

func nugetFuzzCheckServedDoc(t *testing.T, f *fixture, up *nugetFuzzFeed, idx *pkgnuget.ServiceIndex, route string) {
	t.Helper()

	p, _ := parseNuGetPath(route)

	up.last.Store(nil)

	w := f.do(http.MethodGet, route)

	if idx == nil {
		nugetFuzzWantStatus(t, route, w, http.StatusBadGateway)
		return
	}

	switch w.Code {
	case http.StatusOK, http.StatusNotFound, http.StatusBadGateway:
	default:
		t.Fatalf("GET %s = %d: %s", route, w.Code, w.Body.String())
	}

	if w.Code != http.StatusOK {
		return
	}

	// The document the proxy served is the last one the upstream gave it.
	last := up.last.Load()
	if last == nil {
		t.Fatalf("GET %s = 200 without a request to the upstream", route)
	}

	src := *last

	doc, ok := nugetFuzzGunzip(src)
	if !ok || pkgnuget.IsObject(doc) != nil {
		t.Fatalf("GET %s = 200 for an upstream document that is not a JSON object: %q", route, src)
	}

	nugetFuzzCheckRendered(t, p.kind, p.id, doc, w.Body.Bytes(), idx, nugetRepoURL)

	hits := f.hits.Load()
	if again := f.do(http.MethodGet, route); again.Code != http.StatusOK || !bytes.Equal(again.Body.Bytes(), w.Body.Bytes()) || f.hits.Load() != hits {
		t.Fatalf("GET %s again = %d (%d upstream requests more): %s", route, again.Code, f.hits.Load()-hits, again.Body.String())
	}

	if head := f.do(http.MethodHead, route); head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != w.Header().Get("Content-Length") {
		t.Fatalf("HEAD %s = %d, %d bytes, length %q; GET gave length %q", route, head.Code, head.Body.Len(), head.Header().Get("Content-Length"), w.Header().Get("Content-Length"))
	}
}

// nugetFuzzPackage builds a .nupkg whose members are the given names with the
// root nuspec text.
func nugetFuzzPackage(entries ...[2]string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			continue
		}

		_, _ = io.WriteString(w, e[1])
	}

	_ = zw.Close()

	return buf.Bytes()
}

// FuzzNuGetProxyPackage serves a .nupkg and a .nuspec from an upstream whose
// bodies and catalog entry are arbitrary: a package is served only when it is a
// zip with a root nuspec that matches the size and hash its catalog entry
// publishes, byte for byte; whatever is refused is not cached; and a package no
// published hash vouches for is not cached as immutable. The catalog text may
// contain HASH64, replaced by the base64 SHA-512 digest of the package.
func FuzzNuGetProxyPackage(f *testing.F) {
	nupkg := nugetFuzzPackage([2]string{"lib.nuspec", `<package><metadata><id>lib</id><version>1.0.0</version></metadata></package>`}, [2]string{"lib/a.dll", "MZ"})
	nuspec := []byte(`<?xml version="1.0"?><package><metadata><id>lib</id></metadata></package>`)
	catalog := `{"id":"lib","version":"1.0.0","packageHash":"HASH64","packageHashAlgorithm":"SHA512","packageSize":0}`

	f.Add(nupkg, nuspec, catalog)
	f.Add(nupkg, nuspec, `{"id":"lib","packageHash":"HASH64","packageHashAlgorithm":"sha256"}`)
	f.Add(nupkg, nuspec, `{"id":"lib","packageHash":"AAAA","packageHashAlgorithm":"SHA512"}`)
	f.Add(nupkg, nuspec, `{"id":"lib","packageSize":12}`)
	f.Add(nupkg, nuspec, `{"id":"lib"}`)
	f.Add(nupkg, nuspec, `not json`)
	f.Add(nupkg[:len(nupkg)/2], []byte("<html>"), catalog)
	f.Add(nugetFuzzPackage([2]string{"content/lib.nuspec", "x"}), nuspec, `{}`)
	f.Add(nugetFuzzPackage([2]string{"a.nuspec", "x"}, [2]string{"B.NUSPEC", "x"}), nuspec, `{}`)
	f.Add([]byte("PK\x05\x06"), []byte(`<metadata/>`), `{}`)

	f.Fuzz(nugetFuzzProxyPackage)
}

func nugetFuzzProxyPackage(t *testing.T, nupkg, nuspec []byte, catalog string) {
	var base atomic.Pointer[string]

	sum := sha512.Sum512(nupkg)
	catalogDoc := strings.NewReplacer("HASH64", base64.StdEncoding.EncodeToString(sum[:])).Replace(catalog)

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		origin := *base.Load()

		switch r.URL.Path {
		case "/v3/index.json":
			_, _ = io.WriteString(w, `{"resources":[{"@id":"`+origin+`/flat/","@type":"PackageBaseAddress/3.0.0"},`+
				`{"@id":"`+origin+`/reg/","@type":"RegistrationsBaseUrl/3.6.0"}]}`)
		case "/reg/lib.a/1.0.0.json":
			_, _ = io.WriteString(w, `{"catalogEntry":"`+origin+`/catalog/lib.a.1.0.0.json"}`)
		case "/catalog/lib.a.1.0.0.json":
			_, _ = io.WriteString(w, catalogDoc)
		case "/flat/lib.a/1.0.0/lib.a.1.0.0.nupkg":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(nupkg)
		case "/flat/lib.a/1.0.0/lib.a.nuspec":
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write(nuspec)
		default:
			http.NotFound(w, r)
		}
	})

	origin := strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	base.Store(&origin)

	f.repo.Format = "nuget"
	f.repo.Name = "nugetorg"
	f.repo.UpstreamURL = origin + "/v3/index.json"
	f.svc.SetBaseURL(oaBase + "/")

	nupkgKey := "v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"
	nugetFuzzCheckNupkg(t, f, f.do(http.MethodGet, nupkgKey), nupkg, pkgnuget.ParseCatalogDigest([]byte(catalogDoc)), nupkgKey)

	nuspecKey := "v3/package/lib.a/1.0.0/lib.a.nuspec"
	w := f.do(http.MethodGet, nuspecKey)

	switch {
	case w.Code == http.StatusOK && !bytes.Equal(w.Body.Bytes(), nuspec):
		t.Fatalf("nuspec served as %q, the upstream has %q", w.Body.Bytes(), nuspec)
	case w.Code == http.StatusOK && verifyNuspec(bytes.NewReader(nuspec)) != nil:
		t.Fatalf("served a nuspec that is no package document: %q", nuspec)
	case w.Code != http.StatusOK && f.db.entry("repo-1", nuspecKey) != nil:
		t.Fatalf("refused nuspec (%d) was cached", w.Code)
	}
}

func nugetFuzzCheckNupkg(t *testing.T, f *fixture, w *httptest.ResponseRecorder, nupkg []byte, digest pkgnuget.Digest, key string) {
	t.Helper()

	body := w.Body.Bytes()

	if w.Code != http.StatusOK {
		if f.db.entry("repo-1", key) != nil {
			t.Fatalf("refused package (%d) was cached", w.Code)
		}

		return
	}

	if !bytes.Equal(body, nupkg) {
		t.Fatalf("package served as %d bytes, the upstream has %d", len(body), len(nupkg))
	}

	if err := pkgnuget.ValidatePackage(bytes.NewReader(nupkg), int64(len(nupkg))); err != nil {
		t.Fatalf("served a package that is no zip with a nuspec: %v", err)
	}

	if digest.Known() {
		if err := digest.Verify(bytes.NewReader(nupkg)); err != nil {
			t.Fatalf("served a package that contradicts its catalog entry %+v: %v", digest, err)
		}
	}

	e := f.db.entry("repo-1", key)
	if e == nil {
		t.Fatal("served package is not cached")
	}

	if pinned := time.Until(e.ExpiresAt) > 50*365*24*time.Hour; pinned != (len(digest.Hash) > 0) {
		t.Fatalf("package cached as immutable = %v with digest %+v", pinned, digest)
	}
}
