// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const nugetOrgIndex = `{"version":"3.0.0","resources":[
 {"@id":"https://api.nuget.org/v3/query2","@type":"SearchQueryService"},
 {"@id":"https://azuresearch.example/query","@type":"SearchQueryService/3.5.0"},
 {"@id":"https://azuresearch.example/autocomplete","@type":"SearchAutocompleteService/3.5.0"},
 {"@id":"https://api.nuget.org/v3/registration5-gz-semver1/","@type":"RegistrationsBaseUrl"},
 {"@id":"https://api.nuget.org/v3/registration5-gz-semver2","@type":["RegistrationsBaseUrl/3.6.0","RegistrationsBaseUrl/Versioned"]},
 {"@id":"https://api.nuget.org/v3-flatcontainer/","@type":"PackageBaseAddress/3.0.0"},
 {"@id":"https://api.nuget.org/v3/catalog0/index.json","@type":"Catalog/3.0.0"},
 {"@id":"ftp://nope/","@type":"RegistrationsBaseUrl/Versioned"},
 {"@id":"https://api.nuget.org/push","@type":"PackagePublish/2.0.0"}
]}`

func TestServiceIndex_PicksTheBestResources(t *testing.T) {
	idx, err := ParseServiceIndex([]byte(nugetOrgIndex))
	if err != nil {
		t.Fatal(err)
	}

	if idx.PackageBase != "https://api.nuget.org/v3-flatcontainer/" {
		t.Errorf("PackageBase = %q", idx.PackageBase)
	}

	if idx.Registrations != "https://api.nuget.org/v3/registration5-gz-semver2/" || len(idx.RegistrationTypes) != 1 || idx.RegistrationTypes[0] != "RegistrationsBaseUrl/Versioned" {
		t.Errorf("Registrations = %q %v", idx.Registrations, idx.RegistrationTypes)
	}

	if idx.Search != "https://azuresearch.example/query" || idx.SearchType != "SearchQueryService/3.5.0" ||
		idx.Autocomplete != "https://azuresearch.example/autocomplete" {
		t.Errorf("search = %q %q, autocomplete = %q", idx.Search, idx.SearchType, idx.Autocomplete)
	}
}

func TestServiceIndex_Errors(t *testing.T) {
	if _, err := ParseServiceIndex([]byte(`{"resources":[{"@id":"https://x/push","@type":"PackagePublish/2.0.0"}]}`)); !errors.Is(err, ErrNoResources) {
		t.Errorf("no usable resource: %v", err)
	}

	if _, err := ParseServiceIndex([]byte(`<html>`)); err == nil {
		t.Error("HTML parsed as a service index")
	}
}

func TestServiceIndex_RenderPointsAtTheRepository(t *testing.T) {
	idx, err := ParseServiceIndex([]byte(nugetOrgIndex))
	if err != nil {
		t.Fatal(err)
	}

	out, err := idx.Render("http://oa.test/repos/nugetorg")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), "nuget.org") || strings.Contains(string(out), "azuresearch") {
		t.Errorf("upstream URL in the rendered index: %s", out)
	}

	var doc struct {
		Version   string `json:"version"`
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, r := range doc.Resources {
		got[r.Type] = r.ID
	}

	for typ, id := range map[string]string{
		"RegistrationsBaseUrl/Versioned":  "http://oa.test/repos/nugetorg/v3/registration/",
		"PackageBaseAddress/3.0.0":        "http://oa.test/repos/nugetorg/v3/package/",
		"SearchQueryService/3.5.0":        "http://oa.test/repos/nugetorg/v3/query",
		"SearchAutocompleteService/3.5.0": "http://oa.test/repos/nugetorg/v3/autocomplete",
	} {
		if got[typ] != id {
			t.Errorf("%s = %q, want %q", typ, got[typ], id)
		}
	}

	if doc.Version != "3.0.0" {
		t.Errorf("version = %q", doc.Version)
	}
}

func TestRewriteURLs_ByPrefixOnlyAndKeepsNumbersAndMarkup(t *testing.T) {
	in := `{"a":"https://up.example/reg/x/1.json","b":["https://up.example/reg/y","https://other.example/reg/z"],` +
		`"n":12345678901234567890,"f":1.50,"d":"Tom & <Jerry> https://up.example/reg/ stays inside text","k":{"https://up.example/reg/key":"v"}}`

	out, err := RewriteURLs([]byte(in), URLPrefix{From: "https://up.example/reg/", To: "http://oa/v3/registration/"})
	if err != nil {
		t.Fatal(err)
	}

	want := `"a":"http://oa/v3/registration/x/1.json"`
	if !strings.Contains(string(out), want) || !strings.Contains(string(out), `"http://oa/v3/registration/y"`) {
		t.Errorf("not rewritten: %s", out)
	}

	for _, keep := range []string{`"https://other.example/reg/z"`, `12345678901234567890`, `1.50`, `Tom & <Jerry> https://up.example/reg/ stays inside text`, `"https://up.example/reg/key"`} {
		if !strings.Contains(string(out), keep) {
			t.Errorf("%q altered: %s", keep, out)
		}
	}
}

func TestRewriteURLs_TheInnerBaseWinsWhateverTheOrder(t *testing.T) {
	// GitHub Packages serves the flat container below the registrations.
	in := `{"reg":"https://nuget.example/owner/lib/index.json","pkg":"https://nuget.example/owner/download/lib/1.0.0/lib.1.0.0.nupkg"}`
	outer := URLPrefix{From: "https://nuget.example/owner/", To: "http://oa/v3/registration/"}
	inner := URLPrefix{From: "https://nuget.example/owner/download/", To: "http://oa/v3/package/"}

	for name, pairs := range map[string][]URLPrefix{"outer first": {outer, inner}, "inner first": {inner, outer}} {
		out, err := RewriteURLs([]byte(in), pairs...)
		if err != nil {
			t.Fatal(err)
		}

		want := `{"pkg":"http://oa/v3/package/lib/1.0.0/lib.1.0.0.nupkg","reg":"http://oa/v3/registration/lib/index.json"}`
		if string(out) != want {
			t.Errorf("%s: %s, want %s", name, out, want)
		}
	}
}

func TestRewriteRegistrationIndex_PagesGoThroughTheRepository(t *testing.T) {
	in := `{"@id":"https://up/reg/x/index.json","count":3,"items":[` +
		`{"@id":"https://up/reg/x/page/1.0.0/2.0.0.json","lower":"1.0.0","upper":"2.0.0","count":2,"parent":"https://up/reg/x/index.json"},` +
		`{"@id":"https://up/reg/x/index.json#page/3.0.0/3.0.0","lower":"3.0.0","upper":"3.0.0","count":1,"items":[{"@id":"https://up/reg/x/3.0.0.json"}]}]}`

	out, err := RewriteRegistrationIndex([]byte(in), "http://oa/v3/registration/x/page/", URLPrefix{From: "https://up/reg/", To: "http://oa/v3/registration/"})
	if err != nil {
		t.Fatal(err)
	}

	pages, err := ParsePages(out)
	if err != nil {
		t.Fatal(err)
	}

	if len(pages) != 2 || pages[0].ID != "http://oa/v3/registration/x/page/1.0.0/2.0.0.json" || pages[0].Inline ||
		pages[1].ID != "http://oa/v3/registration/x/index.json#page/3.0.0/3.0.0" || !pages[1].Inline || len(pages[1].Items) != 1 {
		t.Errorf("pages = %+v", pages)
	}

	if u, ok := PageURL(mustPages(t, in), "1.0.0", "2.0.0"); !ok || u != "https://up/reg/x/page/1.0.0/2.0.0.json" {
		t.Errorf("PageURL = %q %v", u, ok)
	}

	if _, ok := PageURL(mustPages(t, in), "3.0.0", "3.0.0"); ok {
		t.Error("PageURL returned an inline page")
	}

	if _, ok := PageURL(mustPages(t, in), "9.0.0", "9.0.0"); ok {
		t.Error("PageURL returned an unlisted page")
	}
}

func mustPages(t *testing.T, doc string) []Page {
	t.Helper()

	p, err := ParsePages([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestMergeRegistrationItems(t *testing.T) {
	item := func(v, from string) json.RawMessage {
		return json.RawMessage(`{"@id":"http://oa/v3/registration/x/` + v + `.json","from":"` + from + `"}`)
	}

	out, err := MergeRegistrationItems("http://oa/v3/registration/x/index.json",
		[]json.RawMessage{item("1.0.0", "a"), item("2.0.0-rc.1", "a")},
		[]json.RawMessage{item("2.0.0-rc.1", "b"), item("1.0", "b"), item("10.0.0", "b"), item("2.0.0", "b")})
	if err != nil {
		t.Fatal(err)
	}

	pages := mustPages(t, string(out))
	if len(pages) != 1 || pages[0].Lower != "1.0.0" || pages[0].Upper != "10.0.0" || !pages[0].Inline {
		t.Fatalf("pages = %+v", pages)
	}

	var order []string
	for _, it := range pages[0].Items {
		var v struct{ ID, From string }

		_ = json.Unmarshal(bytes.ReplaceAll(it, []byte("@id"), []byte("id")), &v)
		order = append(order, strings.TrimSuffix(v.ID[strings.LastIndex(v.ID, "/")+1:], ".json")+":"+v.From)
	}

	if got := strings.Join(order, " "); got != "1.0.0:a 2.0.0-rc.1:a 2.0.0:b 10.0.0:b" {
		t.Errorf("items = %s", got)
	}

	// 130 versions are cut into pages of RegistrationPageSize.
	var many []json.RawMessage

	for i := range 130 {
		many = append(many, item("1.0."+strings.TrimSpace(string(rune('0'+i/100)))+string(rune('0'+i/10%10))+string(rune('0'+i%10)), "a"))
	}

	out, err = MergeRegistrationItems("http://oa/i", many)
	if err != nil {
		t.Fatal(err)
	}

	if p := mustPages(t, string(out)); len(p) != 3 || len(p[0].Items) != RegistrationPageSize || len(p[2].Items) != 130-2*RegistrationPageSize {
		t.Errorf("paging: %d pages", len(p))
	}
}

func TestMergeVersionLists(t *testing.T) {
	out, err := MergeVersionLists([]byte(`{"versions":["1.0.0","2.0.0"]}`), []byte(`{"versions":["1.0","1.5.0","2.0.0-beta"]}`))
	if err != nil {
		t.Fatal(err)
	}

	if string(out) != `{"versions":["1.0.0","1.5.0","2.0.0-beta","2.0.0"]}` {
		t.Errorf("merged = %s", out)
	}

	if _, err := MergeVersionLists([]byte(`nope`)); err == nil {
		t.Error("garbage merged")
	}
}

func TestCatalogEntryURL(t *testing.T) {
	for in, want := range map[string]string{
		`{"catalogEntry":"https://c/x.json"}`:               "https://c/x.json",
		`{"catalogEntry":{"@id":"https://c/y.json","a":1}}`: "https://c/y.json",
		`{"packageContent":"x"}`:                            "",
		`nope`:                                              "",
	} {
		if got := CatalogEntryURL([]byte(in)); got != want {
			t.Errorf("CatalogEntryURL(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestDigest(t *testing.T) {
	data := []byte("package bytes")
	h512, h256 := sha512.Sum512(data), sha256.Sum256(data)
	b64 := base64.StdEncoding.EncodeToString

	ok := ParseCatalogDigest([]byte(`{"packageHash":"` + b64(h512[:]) + `","packageHashAlgorithm":"SHA512","packageSize":13}`))
	if !ok.Known() || ok.Verify(bytes.NewReader(data)) != nil {
		t.Errorf("matching digest refused: %+v", ok)
	}

	if err := ok.Verify(bytes.NewReader([]byte("other bytes!!"))); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("same-size different bytes: %v", err)
	}

	if err := ok.Verify(bytes.NewReader(data[:5])); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("truncated: %v", err)
	}

	sha256Only := ParseCatalogDigest([]byte(`{"packageHash":"` + b64(h256[:]) + `","packageHashAlgorithm":"sha256"}`))
	if sha256Only.Verify(bytes.NewReader(data)) != nil || sha256Only.Size != 0 {
		t.Errorf("sha256 digest: %+v", sha256Only)
	}

	// Unknown algorithm and bad base64 fall back to the size alone.
	for _, doc := range []string{
		`{"packageHash":"AAAA","packageHashAlgorithm":"MD5","packageSize":13}`,
		`{"packageHash":"!!!","packageHashAlgorithm":"SHA512","packageSize":13}`,
	} {
		d := ParseCatalogDigest([]byte(doc))
		if len(d.Hash) != 0 || d.Size != 13 || d.Verify(bytes.NewReader(data)) != nil {
			t.Errorf("%s -> %+v", doc, d)
		}
	}

	if d := ParseCatalogDigest([]byte(`{}`)); d.Known() {
		t.Errorf("empty catalog entry constrains: %+v", d)
	}
}

func TestValidatePackage(t *testing.T) {
	build := func(names ...string) []byte {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		for _, n := range names {
			if _, err := zw.Create(n); err != nil {
				t.Fatal(err)
			}
		}

		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}

		return buf.Bytes()
	}

	for name, tc := range map[string]struct {
		data []byte
		ok   bool
	}{
		"nuspec at root":        {build("A.nuspec", "lib/a.dll"), true},
		"upper-case extension":  {build("A.NUSPEC"), true},
		"nuspec only in folder": {build("lib/A.nuspec"), false},
		"no nuspec":             {build("lib/a.dll"), false},
		"not a zip":             {[]byte("plain"), false},
		"empty":                 {nil, false},
	} {
		err := ValidatePackage(bytes.NewReader(tc.data), int64(len(tc.data)))
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrBadPackage)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
