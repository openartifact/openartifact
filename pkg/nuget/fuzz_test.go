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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	fuzzServiceIndex = `{"version":"3.0.0","resources":[` +
		`{"@id":"https://api.example/v3/query","@type":"SearchQueryService"},` +
		`{"@id":"https://api.example/v3/query35","@type":"SearchQueryService/3.5.0"},` +
		`{"@id":"https://api.example/v3/complete","@type":"SearchAutocompleteService/3.5.0"},` +
		`{"@id":"https://api.example/v3/reg/","@type":"RegistrationsBaseUrl"},` +
		`{"@id":"https://api.example/v3/reg6","@type":["RegistrationsBaseUrl/3.6.0","RegistrationsBaseUrl/Versioned"]},` +
		`{"@id":"https://api.example/v3-flat/","@type":"PackageBaseAddress/3.0.0"},` +
		`{"@id":"ftp://nope/","@type":"RegistrationsBaseUrl/Versioned"}]}`

	fuzzRegistrationPage = `{"@id":"https://up.example/reg/demo/index.json","count":2,"items":[` +
		`{"@id":"https://up.example/reg/demo/page/1.0.0/2.0.0.json","lower":"1.0.0","upper":"2.0.0","count":2,"items":[` +
		`{"@id":"https://up.example/reg/demo/1.0.0.json","catalogEntry":{"@id":"https://up.example/cat/1","version":"1.0.0"}},` +
		`{"@id":"https://up.example/reg/demo/2.0.0-Beta.json","catalogEntry":{"version":"2.0.0-Beta+meta"}}]},` +
		`{"@id":"https://up.example/reg/demo/page/3.0.0/4.0.0.json","lower":"3.0.0","upper":"4.0.0","count":2}]}`
)

func fuzzNupkg(entries ...[2]string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			continue
		}

		_, _ = w.Write([]byte(e[1]))
	}

	_ = zw.Close()

	return buf.Bytes()
}

// fuzzCompareSign is the sign of CompareStrings(a, b).
func fuzzCompareSign(a, b string) int {
	switch c := CompareStrings(a, b); {
	case c < 0:
		return -1
	case c > 0:
		return 1
	default:
		return 0
	}
}

// fuzzVersionChars reports whether s is made only of the characters of a
// normalized version: it ends up in URL paths and database keys.
func fuzzVersionChars(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && c != '.' && c != '-' {
			return false
		}
	}

	return s != ""
}

// FuzzParseVersion feeds arbitrary version strings to the parser, the
// normalizer and the comparison.
func FuzzParseVersion(f *testing.F) {
	f.Add("1.0.0", "1.0", "1.0.0-beta.1")
	f.Add("01.2.3.4-Beta.1+build5", "1.2.3.4-beta.01", "1.2.3.4-beta.1.2")
	f.Add("  1.0  ", "4294967295.0.0", "4294967296.0.0")
	f.Add("1.0.0-", "1.0.0-a..b", "1.0.0+")
	f.Add("", "-", "+")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		for _, s := range []string{a, b, c} {
			fuzzCheckVersionForm(t, s)
		}

		fuzzCheckVersionOrder(t, a, b, c)
	})
}

func fuzzCheckVersionForm(t *testing.T, s string) {
	t.Helper()

	v, err := ParseVersion(s)
	norm, ok := Normalize(s)

	if (err == nil) != ok {
		t.Fatalf("%q: ParseVersion err = %v but Normalize ok = %v", s, err, ok)
	}

	if err != nil {
		return
	}

	if norm != v.String() {
		t.Fatalf("%q: Normalize = %q, String = %q", s, norm, v.String())
	}

	if !fuzzVersionChars(norm) {
		t.Fatalf("%q normalizes to %q, which holds characters outside [0-9a-z.-]", s, norm)
	}

	again, err := ParseVersion(norm)
	if err != nil {
		t.Fatalf("%q normalizes to %q, which does not parse: %v", s, norm, err)
	}

	if again.String() != norm || again.Compare(v) != 0 || again.IsPrerelease() != v.IsPrerelease() {
		t.Fatalf("%q: normalized form %q is not a fixed point (%q)", s, norm, again.String())
	}

	if n2, ok2 := Normalize(norm); !ok2 || n2 != norm {
		t.Fatalf("Normalize(%q) = %q, %v", norm, n2, ok2)
	}
}

func fuzzCheckVersionOrder(t *testing.T, a, b, c string) {
	t.Helper()

	for _, pair := range [][2]string{{a, b}, {b, c}, {a, c}} {
		if fuzzCompareSign(pair[0], pair[1]) != -fuzzCompareSign(pair[1], pair[0]) {
			t.Fatalf("CompareStrings is not antisymmetric on %q, %q", pair[0], pair[1])
		}
	}

	for _, s := range []string{a, b, c} {
		if CompareStrings(s, s) != 0 {
			t.Fatalf("CompareStrings(%q, %q) != 0", s, s)
		}
	}

	// Transitivity of <= over every ordering of the three.
	for _, p := range [][3]string{{a, b, c}, {a, c, b}, {b, a, c}, {b, c, a}, {c, a, b}, {c, b, a}} {
		if CompareStrings(p[0], p[1]) <= 0 && CompareStrings(p[1], p[2]) <= 0 && CompareStrings(p[0], p[2]) > 0 {
			t.Fatalf("CompareStrings is not transitive on %q <= %q <= %q", p[0], p[1], p[2])
		}
	}
}

// FuzzParseServiceIndex feeds arbitrary upstream service indexes to the parser
// and renders what it picked back.
func FuzzParseServiceIndex(f *testing.F) {
	f.Add([]byte(fuzzServiceIndex))
	f.Add([]byte(`{"resources":[{"@id":"https://x/","@type":"PackageBaseAddress/3.0.0"}]}`))
	f.Add([]byte(`{"resources":[{"@id":"https://x","@type":null},{"@id":"//x/y","@type":[1]}]}`))
	f.Add([]byte(`{"resources":null}`))
	f.Add([]byte(`<html>`))

	f.Fuzz(func(t *testing.T, doc []byte) {
		idx, err := ParseServiceIndex(doc)
		if err != nil {
			return
		}

		fuzzCheckServiceIndex(t, idx)
	})
}

func fuzzCheckServiceIndex(t *testing.T, idx *ServiceIndex) {
	t.Helper()

	if idx.PackageBase == "" && idx.Registrations == "" {
		t.Fatal("a service index without a package base and registrations was accepted")
	}

	for _, base := range []string{idx.PackageBase, idx.Registrations} {
		if base != "" && (!absoluteHTTP(base) || !strings.HasSuffix(base, "/")) {
			t.Fatalf("base URL %q is not an absolute http(s) URL ending in a slash", base)
		}
	}

	for _, u := range []string{idx.Search, idx.Autocomplete} {
		if u != "" && !absoluteHTTP(u) {
			t.Fatalf("endpoint %q is not an absolute http(s) URL", u)
		}
	}

	if (idx.Registrations != "") != (len(idx.RegistrationTypes) == 1) {
		t.Fatalf("Registrations %q with types %v", idx.Registrations, idx.RegistrationTypes)
	}

	fuzzCheckRendered(t, idx)
}

// fuzzCheckRendered renders idx for a repository and parses the result back.
func fuzzCheckRendered(t *testing.T, idx *ServiceIndex) {
	t.Helper()

	const repoURL = "http://oa.test/repos/r"

	out, err := idx.Render(repoURL)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	back, err := ParseServiceIndex(out)
	if err != nil {
		t.Fatalf("rendered service index %s does not parse: %v", out, err)
	}

	if (back.PackageBase != "") != (idx.PackageBase != "") ||
		(back.Registrations != "") != (idx.Registrations != "") ||
		(back.Search != "") != (idx.Search != "") || (back.Autocomplete != "") != (idx.Autocomplete != "") {
		t.Fatalf("rendering lost or invented resources: %+v -> %+v", idx, back)
	}

	if !slices.Equal(back.RegistrationTypes, idx.RegistrationTypes) || back.SearchType != idx.SearchType ||
		back.AutocompleteType != idx.AutocompleteType {
		t.Fatalf("rendering changed the resource types: %+v -> %+v", idx, back)
	}

	for _, u := range []string{back.PackageBase, back.Registrations, back.Search, back.Autocomplete} {
		if u != "" && !strings.HasPrefix(u, repoURL+"/v3/") {
			t.Fatalf("rendered resource %q points outside the repository", u)
		}
	}
}

// fuzzRewritten reports why out is not in with every string value rewritten by
// the From -> To prefix pair, "" when it is. Keys and the document shape must be
// left alone.
func fuzzRewritten(in, out any, from, to string) string {
	switch a := in.(type) {
	case string:
		want := a
		if from != "" && strings.HasPrefix(a, from) {
			want = to + a[len(from):]
		}

		if b, ok := out.(string); !ok || b != want {
			return "string " + strconv.Quote(a) + " became " + strconv.Quote(want) + " expected"
		}
	case map[string]any:
		b, ok := out.(map[string]any)
		if !ok || len(a) != len(b) {
			return "object changed shape"
		}

		for k, v := range a {
			w, present := b[k]
			if !present {
				return "key " + strconv.Quote(k) + " lost"
			}

			if why := fuzzRewritten(v, w, from, to); why != "" {
				return why
			}
		}
	case []any:
		b, ok := out.([]any)
		if !ok || len(a) != len(b) {
			return "array changed shape"
		}

		for i := range a {
			if why := fuzzRewritten(a[i], b[i], from, to); why != "" {
				return why
			}
		}
	default:
		if a != out {
			return "scalar changed"
		}
	}

	return ""
}

// FuzzRewriteRegistrationIndex feeds arbitrary registration indexes to the URL
// rewriting a proxy applies to upstream documents.
func FuzzRewriteRegistrationIndex(f *testing.F) {
	f.Add([]byte(fuzzRegistrationPage), "http://oa.test/repos/r/v3/registration/demo/page/", "https://up.example/", "http://oa.test/repos/r/")
	f.Add([]byte(`{"items":[{"lower":"1/0","upper":"2#0"}]}`), "p/", "", "x")
	f.Add([]byte(`{"items":[null,1,"s",{"items":null,"lower":"a","upper":"b"}]}`), "", "a", "b")
	f.Add([]byte(`[1e400,"https://up.example/x"]`), "p", "https://", "")
	f.Add([]byte(`{`), "", "", "")

	f.Fuzz(func(t *testing.T, doc []byte, pagePrefix, from, to string) {
		if !utf8.ValidString(pagePrefix) || !utf8.ValidString(from) || !utf8.ValidString(to) {
			return // encoding/json maps each invalid byte to U+FFFD, which no longer matches
		}

		flat, flatErr := RewriteURLs(doc, URLPrefix{from, to})
		out, err := RewriteRegistrationIndex(doc, pagePrefix, URLPrefix{from, to})

		if (err == nil) != (flatErr == nil) {
			t.Fatalf("RewriteURLs err = %v, RewriteRegistrationIndex err = %v", flatErr, err)
		}

		if err != nil {
			return
		}

		if !json.Valid(flat) || !json.Valid(out) {
			t.Fatalf("rewritten documents are not JSON: %q / %q", flat, out)
		}

		orig, _ := decodeJSON(doc)
		got, _ := decodeJSON(flat)

		if why := fuzzRewritten(orig, got, from, to); why != "" {
			t.Fatalf("RewriteURLs: %s", why)
		}

		fuzzCheckPageIDs(t, doc, pagePrefix)
	})
}

// fuzzPages returns the page objects of a registration index document, nil
// when it has none.
func fuzzPages(doc []byte) []map[string]any {
	v, err := decodeJSON(doc)
	if err != nil {
		return nil
	}

	top, _ := v.(map[string]any)
	items, _ := top["items"].([]any)

	pages := make([]map[string]any, len(items))
	for i, p := range items {
		pages[i], _ = p.(map[string]any)
	}

	return pages
}

// fuzzCheckPageIDs rewrites doc without any prefix pair and checks that every
// page the upstream left out is pointed at pagePrefix<lower>/<upper>.json, with
// lower and upper recoverable from that URL.
func fuzzCheckPageIDs(t *testing.T, doc []byte, pagePrefix string) {
	t.Helper()

	out, err := RewriteRegistrationIndex(doc, pagePrefix)
	if err != nil {
		t.Fatalf("RewriteRegistrationIndex without pairs: %v", err)
	}

	in, rewritten := fuzzPages(doc), fuzzPages(out)
	if len(in) != len(rewritten) {
		t.Fatalf("rewriting changed the page count from %d to %d", len(in), len(rewritten))
	}

	for i, page := range in {
		lower, _ := page["lower"].(string)
		upper, _ := page["upper"].(string)

		if _, inline := page["items"]; page == nil || inline || lower == "" || upper == "" {
			continue
		}

		id, _ := rewritten[i]["@id"].(string)

		rest, ok := strings.CutPrefix(id, pagePrefix)
		segs := strings.Split(rest, "/")

		if !ok || len(segs) != 2 || !strings.HasSuffix(segs[1], ".json") {
			t.Fatalf("page %q..%q got @id %q, want %s<lower>/<upper>.json", lower, upper, id, pagePrefix)
		}

		gotLower, errLower := url.PathUnescape(segs[0])
		gotUpper, errUpper := url.PathUnescape(strings.TrimSuffix(segs[1], ".json"))

		if errLower != nil || errUpper != nil || gotLower != lower || gotUpper != upper {
			t.Fatalf("page %q..%q cannot be recovered from its @id %q", lower, upper, id)
		}
	}
}

// fuzzItems returns every package item a registration document carries: the
// items of a page document and the inline pages of an index.
func fuzzItems(doc []byte) []json.RawMessage {
	items, _ := ParsePageItems(doc)
	pages, _ := ParsePages(doc)

	for _, p := range pages {
		items = append(items, p.Items...)
	}

	return items
}

// FuzzMergeRegistrationItems feeds arbitrary registration documents to the
// merge the group repositories serve.
func FuzzMergeRegistrationItems(f *testing.F) {
	f.Add([]byte(fuzzRegistrationPage), []byte(`{"items":[{"@id":"x/1.0.0.json"},{"@id":"x/0.9.json"},{"@id":"x/index.json"}]}`))
	f.Add([]byte(`{"items":[{"catalogEntry":{"version":"1.0.0-Beta"}},{"catalogEntry":{"version":"1.0.0-beta"}}]}`),
		[]byte(`{"items":[{"catalogEntry":{"version":"not a version/#"},"@id":"a"}]}`))
	f.Add([]byte(`{"items":[{"@id":"1.0.json"},{"@id":"1.0.0.json"},{"@id":"1.0.0-01.json"},{"@id":"1.0.0-1.json"}]}`), []byte(`{}`))
	f.Add([]byte(`{"items":[1,null,"x",[]]}`), []byte(`{`))

	f.Fuzz(func(t *testing.T, a, b []byte) {
		const indexURL = "http://oa.test/repos/r/v3/registration/demo/index.json"

		first, second := fuzzItems(a), fuzzItems(b)

		out, err := MergeRegistrationItems(indexURL, first, second)
		if err != nil {
			t.Fatalf("MergeRegistrationItems: %v", err)
		}

		pages, err := ParsePages(out)
		if err != nil {
			t.Fatalf("merged index does not parse: %v", err)
		}

		var merged []json.RawMessage

		var versions []string

		for _, p := range pages {
			if !p.Inline || len(p.Items) == 0 || len(p.Items) > RegistrationPageSize {
				t.Fatalf("page %q..%q: inline %v with %d items", p.Lower, p.Upper, p.Inline, len(p.Items))
			}

			lowest, _ := itemVersion(p.Items[0])
			highest, _ := itemVersion(p.Items[len(p.Items)-1])

			if p.Lower != lowest || p.Upper != highest {
				t.Fatalf("page spans %q..%q but holds %q..%q", p.Lower, p.Upper, lowest, highest)
			}

			for _, it := range p.Items {
				v, ok := itemVersion(it)
				if !ok {
					t.Fatalf("merged item %s has no version", it)
				}

				versions = append(versions, v)
			}

			merged = append(merged, p.Items...)
		}

		if len(merged) > len(first)+len(second) {
			t.Fatalf("merge invented items: %d from %d + %d", len(merged), len(first), len(second))
		}

		fuzzCheckSortedUnique(t, versions)

		again, err := MergeRegistrationItems(indexURL, merged)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("merging a merged index changed it (err %v):\n%s\n%s", err, out, again)
		}
	})
}

func fuzzCheckSortedUnique(t *testing.T, versions []string) {
	t.Helper()

	for i := 1; i < len(versions); i++ {
		if CompareStrings(versions[i-1], versions[i]) > 0 {
			t.Fatalf("versions out of order: %q before %q", versions[i-1], versions[i])
		}
	}

	seen := make(map[string]bool, len(versions))

	for _, v := range versions {
		if seen[v] {
			t.Fatalf("version %q listed twice in %q", v, versions)
		}

		seen[v] = true
	}
}

// FuzzCatalogDigest feeds arbitrary catalog entries and package bytes to the
// digest parsing and verification.
func FuzzCatalogDigest(f *testing.F) {
	sum := sha512.Sum512([]byte("package"))

	f.Add([]byte(`{"packageHash":"`+base64.StdEncoding.EncodeToString(sum[:])+`","packageHashAlgorithm":"SHA512","packageSize":7}`), []byte("package"))
	f.Add([]byte(`{"packageHash":"!!","packageHashAlgorithm":"sha256","packageSize":-3}`), []byte("x"))
	f.Add([]byte(`{"packageHashAlgorithm":"MD5","packageHash":"AAAA"}`), []byte{})
	f.Add([]byte(`{"catalogEntry":{"@id":"https://x/y"}}`), []byte("z"))
	f.Add([]byte(`{"catalogEntry":"https://x/y","packageSize":1e3}`), []byte("z"))

	f.Fuzz(func(t *testing.T, doc, data []byte) {
		_ = CatalogEntryURL(doc)

		d := ParseCatalogDigest(doc)

		if d.Size < 0 || d.Known() != (len(d.Hash) > 0 || d.Size > 0) ||
			(len(d.Hash) > 0 && d.Algorithm != "SHA512" && d.Algorithm != "SHA256") {
			t.Fatalf("ParseCatalogDigest(%s) = %+v", doc, d)
		}

		if err := d.Verify(bytes.NewReader(data)); err == nil {
			fuzzCheckVerified(t, d, data)
		} else if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("Verify over a byte reader failed with %v", err)
		}

		fuzzCheckOwnDigest(t, data)
	})
}

// fuzzCheckVerified checks that a package Verify accepted meets d.
func fuzzCheckVerified(t *testing.T, d Digest, data []byte) {
	t.Helper()

	if d.Size > 0 && d.Size != int64(len(data)) {
		t.Fatalf("Verify accepted %d bytes for a published size of %d", len(data), d.Size)
	}

	var want []byte

	switch {
	case len(d.Hash) == 0:
		return
	case d.Algorithm == "SHA256":
		s := sha256.Sum256(data)
		want = s[:]
	default:
		s := sha512.Sum512(data)
		want = s[:]
	}

	if !bytes.Equal(want, d.Hash) {
		t.Fatalf("Verify accepted a package whose %s differs from the published one", d.Algorithm)
	}
}

// fuzzCheckOwnDigest checks that the digest of data accepts data and nothing
// else.
func fuzzCheckOwnDigest(t *testing.T, data []byte) {
	t.Helper()

	sum := sha512.Sum512(data)

	doc, err := json.Marshal(map[string]any{
		"packageHash":          base64.StdEncoding.EncodeToString(sum[:]),
		"packageHashAlgorithm": "SHA512",
		"packageSize":          len(data),
	})
	if err != nil {
		t.Fatal(err)
	}

	d := ParseCatalogDigest(doc)

	if err := d.Verify(bytes.NewReader(data)); err != nil {
		t.Fatalf("a package does not match its own digest: %v", err)
	}

	if err := d.Verify(bytes.NewReader(append(slices.Clone(data), 0))); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("a longer package matched the digest of a shorter one: %v", err)
	}

	if len(data) == 0 {
		return
	}

	flipped := slices.Clone(data)
	flipped[0] ^= 1

	if err := d.Verify(bytes.NewReader(flipped)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("a modified package matched the digest: %v", err)
	}
}

// FuzzValidatePackage feeds arbitrary bytes to the .nupkg structure check.
func FuzzValidatePackage(f *testing.F) {
	f.Add(fuzzNupkg([2]string{"demo.nuspec", "<package/>"}, [2]string{"lib/net8.0/demo.dll", "MZ"}))
	f.Add(fuzzNupkg([2]string{"content/demo.nuspec", "<package/>"}))
	f.Add(fuzzNupkg([2]string{"A.NUSPEC", ""}))
	f.Add(fuzzNupkg())
	f.Add([]byte("PK\x05\x06"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		err := ValidatePackage(bytes.NewReader(data), int64(len(data)))
		if err != nil && !errors.Is(err, ErrBadPackage) {
			t.Fatalf("ValidatePackage returned %v, which is not ErrBadPackage", err)
		}

		if err != nil {
			return
		}

		zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if zerr != nil {
			t.Fatalf("ValidatePackage accepted a body that is not a zip: %v", zerr)
		}

		for _, zf := range zr.File {
			if !strings.Contains(zf.Name, "/") && strings.HasSuffix(strings.ToLower(zf.Name), ".nuspec") {
				return
			}
		}

		t.Fatal("ValidatePackage accepted a package without a root nuspec")
	})
}
