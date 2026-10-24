// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

const (
	nugetFuzzID = "lib"

	// nugetFuzzVersionsPath is both the version list of the package and the
	// document hosted wins probes the hosted member for.
	nugetFuzzVersionsPath = "v3/package/" + nugetFuzzID + "/index.json"
	nugetFuzzIndexPath    = "v3/registration/" + nugetFuzzID + "/index.json"
	nugetFuzzPagePrefix   = "v3/registration/" + nugetFuzzID + "/page/"

	nugetFuzzGroupV3 = nugetBase + "/repos/all/v3/"
	nugetFuzzPageMax = 64
)

// nugetFuzzMemberV3 is the base of the URLs member serves.
func nugetFuzzMemberV3(member string) string {
	return nugetBase + "/repos/" + member + "/v3/"
}

// nugetFuzzRewrite is v with every string value that starts with from
// starting with to instead, keys untouched.
func nugetFuzzRewrite(v any, from, to string) any {
	switch t := v.(type) {
	case string:
		if rest, ok := strings.CutPrefix(t, from); ok {
			return to + rest
		}
	case map[string]any:
		for k, e := range t {
			t[k] = nugetFuzzRewrite(e, from, to)
		}
	case []any:
		for i, e := range t {
			t[i] = nugetFuzzRewrite(e, from, to)
		}
	}

	return v
}

// nugetFuzzAsk is the document the group works with after asking member for
// doc: the first JSON value of it, as the group's URLs. ok is false when doc is
// no JSON.
func nugetFuzzAsk(doc []byte, member string) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}

	out, err := json.Marshal(nugetFuzzRewrite(v, nugetFuzzMemberV3(member), nugetFuzzGroupV3))

	return out, err == nil
}

// nugetFuzzItemVersion is the normalized version a registration item stands
// for: its catalog entry's version, else the file its @id names.
func nugetFuzzItemVersion(item json.RawMessage) (string, bool) {
	var in struct {
		ID           string          `json:"@id"`
		CatalogEntry json.RawMessage `json:"catalogEntry"` //nolint:tagliatelle // NuGet wire format
	}

	if json.Unmarshal(item, &in) != nil {
		return "", false
	}

	var entry struct {
		Version string `json:"version"`
	}

	if json.Unmarshal(in.CatalogEntry, &entry) == nil && entry.Version != "" {
		v, _ := pkgnuget.Normalize(entry.Version)
		return v, true
	}

	base := in.ID[strings.LastIndex(in.ID, "/")+1:]
	if name, ok := strings.CutSuffix(base, ".json"); ok && name != "" && name != "index" {
		v, _ := pkgnuget.Normalize(name)
		return v, true
	}

	return "", false
}

// nugetFuzzMemberDocs are the documents of a member of the group under test.
type nugetFuzzMemberDocs struct {
	name   string
	served bool   // the member has the document at all
	doc    []byte // the version list or the registration index
	page   []byte // the registration page at any page route
	pages  bool   // the member serves pages
}

// nugetFuzzMemberItems are the items the group should read from a member's
// registration: ok is false when it cannot deliver the registration whole, and
// failed that this is an error (not just a 404).
func nugetFuzzMemberItems(m nugetFuzzMemberDocs) ([]json.RawMessage, bool, bool) {
	if !m.served {
		return nil, false, false
	}

	idx, ok := nugetFuzzAsk(m.doc, m.name)
	if !ok {
		return nil, false, true
	}

	var in struct {
		Items []struct {
			ID    string            `json:"@id"`
			Lower string            `json:"lower"`
			Upper string            `json:"upper"`
			Items []json.RawMessage `json:"items"`
		} `json:"items"`
	}

	if json.Unmarshal(idx, &in) != nil {
		return nil, false, true
	}

	var items []json.RawMessage

	for _, p := range in.Items {
		if p.Items != nil {
			items = append(items, p.Items...)
			continue
		}

		page, ok := nugetFuzzAsk(m.page, m.name)
		if !ok || !m.pages || !nugetSegment(p.Lower) || !nugetSegment(p.Upper) {
			return nil, false, true
		}

		var pi struct {
			Items []json.RawMessage `json:"items"`
		}

		if json.Unmarshal(page, &pi) != nil {
			return nil, false, true
		}

		items = append(items, pi.Items...)
	}

	return items, true, false
}

// nugetFuzzVersionsOf are the versions the group should read from a member's
// version list.
func nugetFuzzMemberVersions(m nugetFuzzMemberDocs) ([]string, bool, bool) {
	if !m.served {
		return nil, false, false
	}

	doc, ok := nugetFuzzAsk(m.doc, m.name)
	if !ok {
		return nil, false, true
	}

	var in struct {
		Versions []string `json:"versions"`
	}

	if json.Unmarshal(doc, &in) != nil {
		return nil, false, true
	}

	return in.Versions, true, false
}

// fixtureResult is what a group answered.
type fixtureResult struct {
	code  int
	body  []byte
	cache string
}

// nugetFuzzServe asks a nuget group of a hosted and a proxy member for relPath
// (a registration index or a version list), the members serving their
// documents there and the page at every page route. A hosted member that serves
// a document holds the package, which is what hosted wins probes the version
// list route for. hostedWins turns off the union of proxy versions.
func nugetFuzzServe(t *testing.T, hostedWins bool, relPath string, members ...nugetFuzzMemberDocs) *fixtureResult {
	t.Helper()

	fx := newFixture(t, "nuget", member("hosted", "nuget", "hosted"), member("mirror", "nuget", "proxy"))
	fx.h.SetBaseURL(nugetBase + "/")
	fx.grp.MergeProxyVersions = !hostedWins

	be := fx.backend()
	be.ServeMember = func(w http.ResponseWriter, r *http.Request, m *models.Repository, rel string) {
		for _, d := range members {
			if d.name != m.Name {
				continue
			}

			var body []byte

			switch {
			case d.served && rel == relPath:
				body = d.doc
			case d.served && rel == nugetFuzzVersionsPath && d.name == "hosted":
				body = []byte(`{"versions":[]}`)
			case d.pages && strings.HasPrefix(rel, nugetFuzzPagePrefix) && strings.HasSuffix(rel, ".json"):
				body = d.page
			default:
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)

			return
		}

		http.NotFound(w, r)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	fx.h.Serve(w, req, fx.grp, relPath, be)

	return &fixtureResult{code: w.Code, body: w.Body.Bytes(), cache: w.Header().Get("Cache-Control")}
}

// nugetFuzzNarrow returns the members the group should ask: with hosted wins
// and a hosted member that holds the package, that member alone.
func nugetFuzzNarrow(hostedWins bool, hosted, mirror nugetFuzzMemberDocs) []nugetFuzzMemberDocs {
	if hostedWins && hosted.served {
		return []nugetFuzzMemberDocs{hosted}
	}

	return []nugetFuzzMemberDocs{hosted, mirror}
}

// nugetFuzzFlags are the settings of a group request decoded from the flag
// byte of a fuzz input.
type nugetFuzzFlags struct {
	hostedWins                        bool
	hostedServed, mirrorServed, pages bool
}

func nugetFuzzParseFlags(flags uint8) nugetFuzzFlags {
	return nugetFuzzFlags{
		hostedWins:   flags&1 != 0,
		hostedServed: flags&2 != 0,
		mirrorServed: flags&4 != 0,
		pages:        flags&8 != 0,
	}
}

// nugetFuzzStatus checks the status of a group request against the members
// that were asked: 200 when one of them delivered, else 502 when one failed and
// 404 when none had it.
func nugetFuzzStatus(t *testing.T, res *fixtureResult, delivered, failed bool) {
	t.Helper()

	want := http.StatusNotFound

	switch {
	case delivered:
		want = http.StatusOK
	case failed:
		want = http.StatusBadGateway
	}

	if res.code != want {
		t.Fatalf("status %d, want %d (delivered %v, failed %v): %s", res.code, want, delivered, failed, res.body)
	}

	if res.code == http.StatusOK && failed && !strings.Contains(res.cache, "no-store") {
		t.Fatalf("an answer that lacks a failed member is cacheable (Cache-Control %q)", res.cache)
	}
}

// FuzzNuGetGroupVersions merges the flat container version lists of a hosted
// and a proxy member, both arbitrary, in a group with or without hosted wins:
// the answer lists every version a member that delivered lists, once, in
// ascending order, and nothing else; with hosted wins the proxy adds nothing to
// what the hosted member lists; and a merged answer merges to itself.
func FuzzNuGetGroupVersions(f *testing.F) {
	f.Add([]byte(`{"versions":["1.0.0","2.0.0"]}`), []byte(`{"versions":["2.0.0","3.0.0-beta.1","1.0.0+meta"]}`), uint8(2|4))
	f.Add([]byte(`{"versions":["1.0.0"]}`), []byte(`{"versions":["9.9.9"]}`), uint8(1|2|4))
	f.Add([]byte(`{"versions":["1.0.0"]}`), []byte(`{"versions":["9.9.9"]}`), uint8(1|4))
	f.Add([]byte(`{"versions":["1.0","01.00.00.0","1.0.0-RC.1","1.0.0-rc.1"," 1.0.0 "]}`), []byte(`{"versions":["x","","X","1.0.0.0"]}`), uint8(2|4))
	f.Add([]byte(`{"versions":["`+nugetFuzzMemberV3("mirror")+`x"]}`), []byte(`{"versions":["`+nugetFuzzMemberV3("hosted")+`x"]}`), uint8(2|4))
	f.Add([]byte(`not json`), []byte(`{"versions":["1.0.0"]}`), uint8(2|4))
	f.Add([]byte(`{"versions":null}`), []byte(`{"versions":["1.0.0"]}`), uint8(2|4))
	f.Add([]byte(`{}`), []byte(`{"versions":[]} trailing`), uint8(2|4))
	f.Add([]byte(`{"versions":["1.0.0"]}`), []byte(`{"versions":["1.0.0"]}`), uint8(0))

	f.Fuzz(nugetFuzzGroupVersions)
}

func nugetFuzzGroupVersions(t *testing.T, hostedDoc, mirrorDoc []byte, flags uint8) {
	fl := nugetFuzzParseFlags(flags)
	hosted := nugetFuzzMemberDocs{name: "hosted", served: fl.hostedServed, doc: hostedDoc}
	mirror := nugetFuzzMemberDocs{name: "mirror", served: fl.mirrorServed, doc: mirrorDoc}

	res := nugetFuzzServe(t, fl.hostedWins, nugetFuzzVersionsPath, hosted, mirror)

	var (
		want                []string
		delivered, anyError bool
	)

	for _, m := range nugetFuzzNarrow(fl.hostedWins, hosted, mirror) {
		versions, ok, failed := nugetFuzzMemberVersions(m)
		delivered = delivered || ok
		anyError = anyError || failed

		for _, v := range versions {
			n, _ := pkgnuget.Normalize(v)
			want = append(want, n)
		}
	}

	nugetFuzzStatus(t, res, delivered, anyError)

	if res.code != http.StatusOK {
		return
	}

	var got struct {
		Versions []string `json:"versions"`
	}

	if err := json.Unmarshal(res.body, &got); err != nil || got.Versions == nil {
		t.Fatalf("answer %s is no version list (%v)", res.body, err)
	}

	for i := 1; i < len(got.Versions); i++ {
		if pkgnuget.CompareStrings(got.Versions[i-1], got.Versions[i]) >= 0 {
			t.Fatalf("versions %q and %q are not in ascending order: %s", got.Versions[i-1], got.Versions[i], res.body)
		}
	}

	slices.Sort(want)

	if gotSorted := slices.Sorted(slices.Values(got.Versions)); !slices.Equal(slices.Compact(want), gotSorted) {
		t.Fatalf("versions %q, want %q", got.Versions, slices.Compact(want))
	}

	if bytes.Contains(res.body, []byte(nugetFuzzMemberV3("hosted"))) {
		return // a URL of the hosted member is rewritten when it is fed back as its document
	}

	again := nugetFuzzServe(t, false, nugetFuzzVersionsPath, nugetFuzzMemberDocs{name: "hosted", served: true, doc: res.body})
	if again.code != http.StatusOK || !bytes.Equal(again.body, res.body) {
		t.Fatalf("merging the answer again gave %d %s, want %s", again.code, again.body, res.body)
	}
}

// FuzzNuGetGroupRegistration merges the registration indexes of a hosted and a
// proxy member, both arbitrary, with the pages they leave out fetched from the
// members, in a group with or without hosted wins: the merged index is a valid
// document of the group with at most 64 items per page and one item per
// version, in ascending order, the item the first member that has the version
// lists, rewritten to the group, and nothing of a member that failed; and a
// merged answer merges to itself.
func FuzzNuGetGroupRegistration(f *testing.F) {
	hosted := nugetInlineIndex("hosted", nugetFuzzID, "1.0.0", "2.0.0")
	mirror := nugetInlineIndex("mirror", nugetFuzzID, "2.0.0", "3.0.0-beta")
	mirrorPaged := `{"@id":"` + nugetFuzzMemberV3("mirror") + `registration/lib/index.json","count":1,"items":[` +
		`{"@id":"` + nugetFuzzMemberV3("mirror") + `registration/lib/page/1.0.0/4.0.0.json","lower":"1.0.0","upper":"4.0.0","count":2}]}`
	page := `{"@id":"x","items":[` + nugetItem("mirror", nugetFuzzID, "4.0.0") + `,` + nugetItem("mirror", nugetFuzzID, "1.0.0") + `]}`

	// More versions than fit a page of the merged index.
	var low, high []string

	for i := range 70 {
		low = append(low, fmt.Sprintf("1.%d.0", i))
		high = append(high, fmt.Sprintf("1.%d.0", i+40))
	}

	f.Add([]byte(nugetInlineIndex("hosted", nugetFuzzID, low...)), []byte(nugetInlineIndex("mirror", nugetFuzzID, high...)), []byte(page), uint8(2|4))
	f.Add([]byte(hosted), []byte(mirror), []byte(page), uint8(2|4|8))
	f.Add([]byte(hosted), []byte(mirror), []byte(page), uint8(1|2|4|8))
	f.Add([]byte(hosted), []byte(mirror), []byte(page), uint8(1|4|8))
	f.Add([]byte(hosted), []byte(mirrorPaged), []byte(page), uint8(2|4|8))
	f.Add([]byte(hosted), []byte(mirrorPaged), []byte(page), uint8(2|4))
	f.Add([]byte(hosted), []byte(mirrorPaged), []byte(`{"items":3}`), uint8(2|4|8))
	f.Add([]byte(`{"items":[{"lower":"a/b","upper":"c","count":1}]}`), []byte(mirror), []byte(page), uint8(2|4|8))
	f.Add([]byte(`{"items":[{"items":[{"@id":"x/Foo.json"},{"@id":"x/1.json","catalogEntry":{"version":"1.0.0-A"}},{"@id":"x/index.json"},7,{"catalogEntry":{"version":"1"}}]}]}`), []byte(`[]`), []byte(page), uint8(2|4|8))
	f.Add([]byte(`not json`), []byte(mirror), []byte(page), uint8(2|4|8))
	f.Add([]byte(`{}`), []byte(`{"items":null}`), []byte(page), uint8(2|4|8))

	f.Fuzz(nugetFuzzGroupRegistration)
}

func nugetFuzzGroupRegistration(t *testing.T, hostedDoc, mirrorDoc, pageDoc []byte, flags uint8) {
	fl := nugetFuzzParseFlags(flags)
	hosted := nugetFuzzMemberDocs{name: "hosted", served: fl.hostedServed, doc: hostedDoc, page: pageDoc, pages: fl.pages}
	mirror := nugetFuzzMemberDocs{name: "mirror", served: fl.mirrorServed, doc: mirrorDoc, page: pageDoc, pages: fl.pages}

	res := nugetFuzzServe(t, fl.hostedWins, nugetFuzzIndexPath, hosted, mirror)

	var (
		sources             [][]json.RawMessage
		delivered, anyError bool
	)

	for _, m := range nugetFuzzNarrow(fl.hostedWins, hosted, mirror) {
		items, ok, failed := nugetFuzzMemberItems(m)
		delivered = delivered || ok
		anyError = anyError || failed

		if ok {
			sources = append(sources, items)
		}
	}

	nugetFuzzStatus(t, res, delivered, anyError)

	if res.code != http.StatusOK {
		return
	}

	nugetFuzzCheckRegistration(t, res.body, sources)

	if bytes.Contains(res.body, []byte(nugetFuzzMemberV3("hosted"))) {
		return // a URL of the hosted member is rewritten when it is fed back as its document
	}

	again := nugetFuzzServe(t, false, nugetFuzzIndexPath, nugetFuzzMemberDocs{name: "hosted", served: true, doc: res.body})
	if again.code != http.StatusOK || !bytes.Equal(again.body, res.body) {
		t.Fatalf("merging the answer again gave %d %s\nwant %s", again.code, again.body, res.body)
	}
}

// nugetFuzzPage is a page of the merged registration index as the group wrote
// it.
type nugetFuzzPage struct {
	ID     string            `json:"@id"`
	Count  int               `json:"count"`
	Lower  string            `json:"lower"`
	Upper  string            `json:"upper"`
	Parent string            `json:"parent"`
	Items  []json.RawMessage `json:"items"`
}

func nugetFuzzCheckRegistration(t *testing.T, body []byte, sources [][]json.RawMessage) {
	t.Helper()

	indexURL := nugetFuzzGroupV3 + "registration/" + nugetFuzzID + "/index.json"

	var top struct {
		ID    string          `json:"@id"`
		Count int             `json:"count"`
		Items []nugetFuzzPage `json:"items"`
	}

	if err := json.Unmarshal(body, &top); err != nil || top.ID != indexURL || top.Count != len(top.Items) {
		t.Fatalf("not the registration index of the group (%v): %s", err, body)
	}

	var versions []string

	got := map[string]json.RawMessage{}

	for i, p := range top.Items {
		if p.Count != len(p.Items) || len(p.Items) == 0 || len(p.Items) > nugetFuzzPageMax || (i < len(top.Items)-1 && len(p.Items) != nugetFuzzPageMax) || p.Parent != indexURL {
			t.Fatalf("page %d is not a page of the index (count %d, %d items): %s", i, p.Count, len(p.Items), body)
		}

		for _, it := range p.Items {
			v, ok := nugetFuzzItemVersion(it)
			if !ok {
				t.Fatalf("item %s has no version: %s", it, body)
			}

			versions = append(versions, v)
			got[v] = it
		}

		first, _ := nugetFuzzItemVersion(p.Items[0])
		last, _ := nugetFuzzItemVersion(p.Items[len(p.Items)-1])

		if p.Lower != first || p.Upper != last || p.ID != indexURL+"#page/"+first+"/"+last {
			t.Fatalf("page %d bounds %q..%q (%q) do not match its items %q..%q", i, p.Lower, p.Upper, p.ID, first, last)
		}
	}

	for i := 1; i < len(versions); i++ {
		if pkgnuget.CompareStrings(versions[i-1], versions[i]) >= 0 {
			t.Fatalf("versions %q and %q are not in ascending order: %s", versions[i-1], versions[i], body)
		}
	}

	nugetFuzzCheckItems(t, got, sources)
}

// nugetFuzzCheckItems checks that the merged items are the first item of each
// version of the sources, as the group's documents.
func nugetFuzzCheckItems(t *testing.T, got map[string]json.RawMessage, sources [][]json.RawMessage) {
	t.Helper()

	want := map[string]json.RawMessage{}

	for _, items := range sources {
		for _, it := range items {
			if v, ok := nugetFuzzItemVersion(it); ok && want[v] == nil {
				want[v] = it
			}
		}
	}

	if len(got) != len(want) {
		t.Fatalf("%d versions merged, want %d", len(got), len(want))
	}

	for v, it := range got {
		if !nugetFuzzSameJSON(it, want[v]) {
			t.Fatalf("version %q: item %s, want %s", v, it, want[v])
		}
	}
}

func nugetFuzzDecode(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()

	var v any

	return v, dec.Decode(&v) == nil
}

func nugetFuzzSameJSON(a, b json.RawMessage) bool {
	av, aok := nugetFuzzDecode(a)
	bv, bok := nugetFuzzDecode(b)

	return aok && bok && reflect.DeepEqual(av, bv)
}
