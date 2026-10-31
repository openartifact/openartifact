// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

const nugetAgeGate = 48 // hours

const nugetUnlisted = "1900-01-01T00:00:00+00:00"

// nugetAgeFeed is a v3 feed whose packages have versions of known ages:
//
//	lib.a  1.0.0 200h, 1.1.0 72h, 2.0.0 1h (too new), 3.0.0-beta unlisted (year 1900),
//	       2.1.0 in the flat container but not yet in the registration
//	big    paged registration: 1.0.0 200h and 2.0.0 100h on page one,
//	       3.0.0 1h alone on page two
//	fresh  1.0.0 1h (the only version)
type nugetAgeFeed struct {
	t          *testing.T
	base       string
	nupkgGets  atomic.Int32
	noLeafTime bool // leaves state no "published" (the catalog entry does)
}

func nugetAgo(h int) string {
	return time.Now().Add(-time.Duration(h) * time.Hour).UTC().Format(time.RFC3339Nano)
}

// nugetAgePublished is the publish time of id's version v, "" for a version
// the feed does not have.
func nugetAgePublished(id, v string) string {
	switch id + "/" + v {
	case "lib.a/1.0.0", "big/1.0.0":
		return nugetAgo(200)
	case "lib.a/1.1.0":
		return nugetAgo(72)
	case "big/2.0.0":
		return nugetAgo(100)
	case "lib.a/2.0.0", "big/3.0.0", "fresh/1.0.0":
		return nugetAgo(1)
	case "lib.a/3.0.0-beta":
		return nugetUnlisted
	}

	return ""
}

func (g *nugetAgeFeed) item(id, v string) string {
	return fmt.Sprintf(`{"@id":"%[1]s/reg/%[2]s/%[3]s.json","@type":"Package","catalogEntry":{"@id":"%[1]s/catalog/%[2]s.%[3]s.json",`+
		`"id":%[2]q,"version":%[3]q,"published":%[4]q,"listed":true},"packageContent":"%[1]s/flat/%[2]s/%[3]s/%[2]s.%[3]s.nupkg"}`,
		g.base, id, v, nugetAgePublished(id, v))
}

func (g *nugetAgeFeed) page(id, lower, upper string, versions ...string) string {
	items := make([]string, len(versions))
	for i, v := range versions {
		items[i] = g.item(id, v)
	}

	return fmt.Sprintf(`{"@id":"%[1]s/reg/%[2]s/page/%[3]s/%[4]s.json","count":%[5]d,"lower":%[3]q,"upper":%[4]q,`+
		`"parent":"%[1]s/reg/%[2]s/index.json","items":[%[6]s]}`, g.base, id, lower, upper, len(versions), strings.Join(items, ","))
}

func (g *nugetAgeFeed) leaf(id, v string) string {
	published := ""
	if !g.noLeafTime {
		published = fmt.Sprintf(`"published":%q,`, nugetAgePublished(id, v))
	}

	return fmt.Sprintf(`{"@id":"%[1]s/reg/%[2]s/%[3]s.json","catalogEntry":"%[1]s/catalog/%[2]s.%[3]s.json","listed":true,%[4]s`+
		`"packageContent":"%[1]s/flat/%[2]s/%[3]s/%[2]s.%[3]s.nupkg","registration":"%[1]s/reg/%[2]s/index.json"}`, g.base, id, v, published)
}

func (g *nugetAgeFeed) handler(w http.ResponseWriter, r *http.Request) {
	json200 := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}

	p := r.URL.Path

	switch {
	case p == "/v3/index.json":
		json200(fmt.Sprintf(`{"version":"3.0.0","resources":[{"@id":"%[1]s/flat/","@type":"PackageBaseAddress/3.0.0"},`+
			`{"@id":"%[1]s/reg/","@type":"RegistrationsBaseUrl/3.6.0"}]}`, g.base))
	case p == "/flat/lib.a/index.json":
		json200(`{"versions":["1.0.0","1.1.0","2.0.0","2.1.0","3.0.0-beta"]}`)
	case p == "/flat/big/index.json":
		json200(`{"versions":["1.0.0","2.0.0","3.0.0"]}`)
	case p == "/flat/fresh/index.json":
		json200(`{"versions":["1.0.0"]}`)
	case strings.HasSuffix(p, ".nupkg"):
		g.nupkgGets.Add(1)
		_, _ = w.Write(buildNupkg(g.t, "lib.a", "1.0.0"))
	case strings.HasSuffix(p, ".nuspec"):
		_, _ = io.WriteString(w, `<?xml version="1.0"?><package><metadata><id>x</id></metadata></package>`)
	case p == "/reg/lib.a/index.json":
		json200(fmt.Sprintf(`{"@id":"%[1]s/reg/lib.a/index.json","count":1,"items":[%[2]s]}`, g.base,
			g.page("lib.a", "1.0.0", "3.0.0-beta", "1.0.0", "1.1.0", "2.0.0", "3.0.0-beta")))
	case p == "/reg/big/index.json":
		json200(fmt.Sprintf(`{"@id":"%[1]s/reg/big/index.json","count":2,"items":[`+
			`{"@id":"%[1]s/reg/big/page/1.0.0/2.0.0.json","count":2,"lower":"1.0.0","upper":"2.0.0","parent":"%[1]s/reg/big/index.json"},`+
			`{"@id":"%[1]s/reg/big/page/3.0.0/3.0.0.json","count":1,"lower":"3.0.0","upper":"3.0.0","parent":"%[1]s/reg/big/index.json"}]}`, g.base))
	case p == "/reg/big/page/1.0.0/2.0.0.json":
		json200(g.page("big", "1.0.0", "2.0.0", "1.0.0", "2.0.0"))
	case p == "/reg/big/page/3.0.0/3.0.0.json":
		json200(g.page("big", "3.0.0", "3.0.0", "3.0.0"))
	case p == "/reg/fresh/index.json":
		json200(
			fmt.Sprintf(
				`{"@id":"%[1]s/reg/fresh/index.json","count":1,"items":[%[2]s]}`,
				g.base,
				g.page("fresh", "1.0.0", "1.0.0", "1.0.0"),
			),
		)
	case strings.HasPrefix(p, "/reg/") && strings.HasSuffix(p, ".json"):
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(p, "/reg/"), ".json"), "/")
		if len(parts) == 2 && nugetAgePublished(parts[0], parts[1]) != "" {
			json200(g.leaf(parts[0], parts[1]))

			return
		}

		http.NotFound(w, r)
	case strings.HasPrefix(p, "/catalog/") && strings.HasSuffix(p, ".json"):
		g.catalog(w, r)
	default:
		http.NotFound(w, r)
	}
}

// catalog answers a catalog entry: /catalog/<id>.<version>.json.
func (g *nugetAgeFeed) catalog(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/catalog/"), ".json")

	for _, id := range []string{"lib.a", "big", "fresh"} {
		if v, ok := strings.CutPrefix(name, id+"."); ok && nugetAgePublished(id, v) != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":%q,"version":%q,"published":%q}`, id, v, nugetAgePublished(id, v))

			return
		}
	}

	http.NotFound(w, r)
}

func nugetAgeFixture(t *testing.T, hours int) (*fixture, *nugetAgeFeed) {
	t.Helper()

	feed := &nugetAgeFeed{t: t}
	f := newFixture(t, feed.handler)
	feed.base = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	f.repo.Format = "nuget"
	f.repo.Name = "nugetorg"
	f.repo.UpstreamURL = feed.base + "/v3/index.json"
	f.repo.MinAgeHours = hours
	f.svc.SetBaseURL(oaBase + "/")

	return f, feed
}

// nugetRegVersions lists the versions of the items of a registration document
// (index or page), in order, and the "count" fields met on the way.
func nugetRegVersions(t *testing.T, body []byte) ([]string, []int) {
	t.Helper()

	var (
		versions []string
		counts   []int
	)

	var walk func(v any)

	walk = func(v any) {
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}

		if n, ok := obj["count"].(float64); ok {
			counts = append(counts, int(n))
		}

		if entry, ok := obj["catalogEntry"].(map[string]any); ok {
			versions = append(versions, entry["version"].(string))
		}

		items, _ := obj["items"].([]any)
		for _, it := range items {
			walk(it)
		}
	}

	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}

	walk(doc)

	return versions, counts
}

func nugetFlatVersions(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()

	var doc struct {
		Versions []string `json:"versions"`
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("not a version list: %v", err)
	}

	return doc.Versions
}

func TestNuGetMinAge_RegistrationIndexDropsYoungVersions(t *testing.T) {
	f, _ := nugetAgeFixture(t, nugetAgeGate)

	w := f.do(http.MethodGet, "v3/registration/lib.a/index.json")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	versions, counts := nugetRegVersions(t, w.Body.Bytes())

	// 3.0.0-beta is unlisted (published 1900): no date, so it passes.
	if want := []string{"1.0.0", "1.1.0", "3.0.0-beta"}; !slices.Equal(versions, want) {
		t.Errorf("versions = %v, want %v", versions, want)
	}

	if want := []int{1, 3}; !slices.Equal(counts, want) {
		t.Errorf("counts = %v, want %v (index of one page, the page of three versions)", counts, want)
	}

	if !strings.Contains(w.Body.String(), nugetRepoURL+"/v3/registration/") {
		t.Errorf("URLs are not rewritten to the repository: %s", w.Body.String())
	}
}

func TestNuGetMinAge_GateOffLeavesRegistrationAndVersionsUnchanged(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)

	versions, _ := nugetRegVersions(t, f.do(http.MethodGet, "v3/registration/lib.a/index.json").Body.Bytes())
	if want := []string{"1.0.0", "1.1.0", "2.0.0", "3.0.0-beta"}; !slices.Equal(versions, want) {
		t.Errorf("registration versions = %v, want %v", versions, want)
	}

	list := nugetFlatVersions(t, f.do(http.MethodGet, "v3/package/lib.a/index.json"))
	if want := []string{"1.0.0", "1.1.0", "2.0.0", "2.1.0", "3.0.0-beta"}; !slices.Equal(list, want) {
		t.Errorf("flat versions = %v, want %v", list, want)
	}

	if w := f.do(http.MethodGet, "v3/registration/lib.a/2.0.0.json"); w.Code != http.StatusOK {
		t.Errorf("leaf of the young version = %d, want 200 with the gate off", w.Code)
	}

	if w := f.do(http.MethodGet, "v3/package/lib.a/2.0.0/lib.a.2.0.0.nupkg"); w.Code != http.StatusOK {
		t.Errorf("young nupkg = %d, want 200 with the gate off", w.Code)
	}
}

func TestNuGetMinAge_PagedRegistration(t *testing.T) {
	f, _ := nugetAgeFixture(t, nugetAgeGate)

	// The index does not inline the pages: it is served as it is, the pages are gated.
	idx, _ := nugetRegVersions(t, f.do(http.MethodGet, "v3/registration/big/index.json").Body.Bytes())
	if len(idx) != 0 {
		t.Errorf("index versions = %v, want none (pages are not inline)", idx)
	}

	one, _ := nugetRegVersions(t, f.do(http.MethodGet, "v3/registration/big/page/1.0.0/2.0.0.json").Body.Bytes())
	if want := []string{"1.0.0", "2.0.0"}; !slices.Equal(one, want) {
		t.Errorf("page one = %v, want %v", one, want)
	}

	w := f.do(http.MethodGet, "v3/registration/big/page/3.0.0/3.0.0.json")

	two, counts := nugetRegVersions(t, w.Body.Bytes())
	if len(two) != 0 || !slices.Equal(counts, []int{0}) {
		t.Errorf("page two = %v counts %v, want no items and count 0", two, counts)
	}
}

func TestNuGetMinAge_FlatContainerListIsFilteredByTheRegistration(t *testing.T) {
	f, _ := nugetAgeFixture(t, nugetAgeGate)

	// 2.0.0 is young, 2.1.0 is not in the registration yet (so it is new), the
	// unlisted 3.0.0-beta has no date and passes.
	list := nugetFlatVersions(t, f.do(http.MethodGet, "v3/package/Lib.A/index.json"))
	if want := []string{"1.0.0", "1.1.0", "3.0.0-beta"}; !slices.Equal(list, want) {
		t.Errorf("lib.a versions = %v, want %v", list, want)
	}

	// The dates of a paged registration come from all its pages.
	list = nugetFlatVersions(t, f.do(http.MethodGet, "v3/package/big/index.json"))
	if want := []string{"1.0.0", "2.0.0"}; !slices.Equal(list, want) {
		t.Errorf("big versions = %v, want %v", list, want)
	}
}

func TestNuGetMinAge_PackageWithOnlyYoungVersionsIsNotFound(t *testing.T) {
	f, _ := nugetAgeFixture(t, nugetAgeGate)

	for _, p := range []string{
		"v3/registration/fresh/index.json",
		"v3/registration/fresh/1.0.0.json",
		"v3/package/fresh/index.json",
	} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}
}

func TestNuGetMinAge_LeafOfYoungVersionIsNotFound(t *testing.T) {
	f, _ := nugetAgeFixture(t, nugetAgeGate)

	for p, want := range map[string]int{
		"v3/registration/lib.a/1.1.0.json":      http.StatusOK,
		"v3/registration/lib.a/3.0.0-beta.json": http.StatusOK,
		"v3/registration/lib.a/2.0.0.json":      http.StatusNotFound,
	} {
		if w := f.do(http.MethodGet, p); w.Code != want {
			t.Errorf("GET %s = %d, want %d", p, w.Code, want)
		}
	}
}

func TestNuGetMinAge_YoungFilesAreRefusedAndNeverFetched(t *testing.T) {
	f, feed := nugetAgeFixture(t, nugetAgeGate)

	for _, p := range []string{
		"v3/package/lib.a/2.0.0/lib.a.2.0.0.nupkg",
		"v3/package/lib.a/2.0.0/lib.a.nuspec",
	} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d %q, want 403", p, w.Code, w.Body.String())
		}
	}

	if got := feed.nupkgGets.Load(); got != 0 {
		t.Errorf("the upstream .nupkg was fetched %d times", got)
	}

	for _, p := range []string{
		"v3/package/lib.a/1.1.0/lib.a.1.1.0.nupkg",
		"v3/package/lib.a/1.1.0/lib.a.nuspec",
		"v3/package/lib.a/3.0.0-beta/lib.a.3.0.0-beta.nupkg", // unlisted: no date
	} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d %q, want 200", p, w.Code, w.Body.String())
		}
	}
}

func TestNuGetMinAge_PublishTimeFallsBackToTheCatalogEntry(t *testing.T) {
	f, feed := nugetAgeFixture(t, nugetAgeGate)
	feed.noLeafTime = true

	if w := f.do(http.MethodGet, "v3/package/lib.a/2.0.0/lib.a.2.0.0.nupkg"); w.Code != http.StatusForbidden {
		t.Errorf("young nupkg = %d, want 403 (age from the catalog entry)", w.Code)
	}

	if w := f.do(http.MethodGet, "v3/package/lib.a/1.1.0/lib.a.nuspec"); w.Code != http.StatusOK {
		t.Errorf("old nuspec = %d, want 200", w.Code)
	}

	if w := f.do(http.MethodGet, "v3/package/lib.a/2.0.0/lib.a.nuspec"); w.Code != http.StatusForbidden {
		t.Errorf("young nuspec = %d, want 403 (age from the catalog entry)", w.Code)
	}
}

func TestNuGetMinAge_CachedFileIsServedAsIs(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)

	const file = "v3/package/lib.a/2.0.0/lib.a.2.0.0.nupkg"

	if w := f.do(http.MethodGet, file); w.Code != http.StatusOK {
		t.Fatalf("ungated fetch = %d", w.Code)
	}

	f.repo.MinAgeHours = nugetAgeGate

	if w := f.do(http.MethodGet, file); w.Code != http.StatusOK {
		t.Errorf("cached young nupkg = %d, want 200 (admitted before the gate)", w.Code)
	}

	if w := f.do(http.MethodGet, "v3/package/lib.a/2.0.0/lib.a.nuspec"); w.Code != http.StatusForbidden {
		t.Errorf("uncached young nuspec = %d, want 403", w.Code)
	}
}

func TestNuGetMinAge_ValidatorFollowsTheGate(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)

	const doc = "v3/package/lib.a/index.json"

	etag := func() string { return f.do(http.MethodGet, doc).Header().Get("ETag") }

	ungated := etag()
	if ungated == "" || etag() != ungated {
		t.Fatalf("ungated ETag %q is not stable", ungated)
	}

	f.repo.MinAgeHours = nugetAgeGate

	gated := etag()
	if gated == "" || gated == ungated {
		t.Errorf("gated ETag %q equals the ungated one: a 304 would hide the gate", gated)
	}

	f.repo.MinAgeHours = 24

	if other := etag(); other == gated {
		t.Errorf("ETag does not change with the threshold")
	}

	f.repo.MinAgeHours = 0

	if etag() != ungated {
		t.Errorf("gate off: ETag differs from the ungated one")
	}
}

func TestNuGetMinAge_GateStampMovesWithTime(t *testing.T) {
	repo := &models.Repository{MinAgeHours: 24}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	g := nugetGate{repo: repo, now: now}
	if later := (nugetGate{repo: repo, now: now.Add(gateStampBucket)}); later.stamp() == g.stamp() {
		t.Error("stamp does not change as time passes")
	}

	if off := (nugetGate{repo: &models.Repository{}, now: now}); off.stamp() != "" {
		t.Errorf("gate off: stamp = %q", off.stamp())
	}
}

// nugetItemAt is a registration item published at published.
func nugetItemAt(version, published string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"@id":"x/%[1]s.json","catalogEntry":{"version":%[1]q,"published":%[2]q}}`, version, published))
}

func TestGateNuGetItems_Boundaries(t *testing.T) {
	repo := &models.Repository{MinAgeHours: 48}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	at := func(age time.Duration) string { return now.Add(-age).Format(time.RFC3339Nano) }

	items := []json.RawMessage{
		nugetItemAt("1.0.0", at(48*time.Hour)),                 // exactly the threshold: old enough
		nugetItemAt("2.0.0", at(48*time.Hour-time.Nanosecond)), // a nanosecond short
		nugetItemAt("3.0.0", nugetUnlisted),                    // unlisted: unknown
		nugetItemAt("4.0.0", ""),                               // no time stated
		nugetItemAt("5.0.0", "not a time"),                     // unreadable: unknown
		nugetItemAt("6.0.0", at(-time.Hour)),                   // published in the future: new
	}

	var got []string

	for _, it := range gateNuGetItems(repo, items, now) {
		v, _ := nugetItemMeta(it)
		got = append(got, v)
	}

	if want := []string{"1.0.0", "3.0.0", "4.0.0", "5.0.0"}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}

	if kept := gateNuGetItems(&models.Repository{}, items, now); len(kept) != len(items) {
		t.Errorf("gate off kept %d of %d", len(kept), len(items))
	}
}

func TestGateNuGetVersions(t *testing.T) {
	repo := &models.Repository{MinAgeHours: 24}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	doc := []byte(`{"versions":["1.0.0","2.0.0","3.0.0","4.0.0-Beta"]}`)
	dates := &nugetDates{complete: true, published: map[string]time.Time{
		"1.0.0":      now.Add(-24 * time.Hour),
		"2.0.0":      now.Add(-23 * time.Hour),
		"3.0.0":      {},
		"4.0.0-beta": now.Add(-48 * time.Hour),
	}}

	out, err := gateNuGetVersions(repo, doc, dates, now)
	if err != nil {
		t.Fatal(err)
	}

	var list struct{ Versions []string }
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}

	if want := []string{"1.0.0", "3.0.0", "4.0.0-Beta"}; !slices.Equal(list.Versions, want) {
		t.Errorf("versions = %v, want %v", list.Versions, want)
	}

	// Not in the registration yet: new, unless the registration is incomplete.
	extra := []byte(`{"versions":["1.0.0","9.0.0"]}`)

	out, _ = gateNuGetVersions(repo, extra, dates, now)
	if strings.Contains(string(out), "9.0.0") {
		t.Errorf("a version unknown to the registration was kept: %s", out)
	}

	dates.complete = false

	out, _ = gateNuGetVersions(repo, extra, dates, now)
	if !strings.Contains(string(out), "9.0.0") {
		t.Errorf("incomplete registration: unknown version dropped: %s", out)
	}

	// Nothing left.
	if _, err := gateNuGetVersions(repo, []byte(`{"versions":["2.0.0"]}`), dates, now); !errors.Is(err, errNotFound) {
		t.Errorf("all young: err = %v, want errNotFound", err)
	}

	// No dates: unchanged.
	if out, err := gateNuGetVersions(repo, doc, nil, now); err != nil || string(out) != string(doc) {
		t.Errorf("no dates: %s, %v", out, err)
	}
}

func TestGateNuGetRegIndex_Pages(t *testing.T) {
	repo := &models.Repository{MinAgeHours: 24}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-72 * time.Hour).Format(time.RFC3339)
	young := now.Add(-time.Hour).Format(time.RFC3339)

	page := func(lower, upper string, items ...json.RawMessage) string {
		b, _ := json.Marshal(map[string]any{"lower": lower, "upper": upper, "count": len(items), "items": items})

		return string(b)
	}
	doc := func(pages ...string) []byte {
		return []byte(fmt.Sprintf(`{"@id":"x","count":%d,"items":[%s]}`, len(pages), strings.Join(pages, ",")))
	}

	// One page narrowed, one emptied (and dropped), one not inline (kept).
	out, err := gateNuGetRegIndex(repo, doc(
		page("1.0.0", "2.0.0", nugetItemAt("1.0.0", old), nugetItemAt("2.0.0", young)),
		page("3.0.0", "3.0.0", nugetItemAt("3.0.0", young)),
		`{"@id":"p3","lower":"4.0.0","upper":"5.0.0","count":2}`,
	), now)
	if err != nil {
		t.Fatal(err)
	}

	versions, counts := nugetRegVersions(t, out)
	if want := []string{"1.0.0"}; !slices.Equal(versions, want) {
		t.Errorf("versions = %v, want %v", versions, want)
	}

	if want := []int{2, 1, 2}; !slices.Equal(counts, want) {
		t.Errorf("counts = %v, want %v (index of two pages, narrowed page, page not inlined)", counts, want)
	}

	// Nothing hidden: the document is returned as it is.
	same := doc(page("1.0.0", "1.0.0", nugetItemAt("1.0.0", old)))
	if out, err := gateNuGetRegIndex(repo, same, now); err != nil || !bytes.Equal(out, same) {
		t.Errorf("nothing hidden: %s, %v", out, err)
	}

	// Everything hidden and every page inline: no package.
	if _, err := gateNuGetRegIndex(repo, doc(page("1.0.0", "1.0.0", nugetItemAt("1.0.0", young))), now); !errors.Is(err, errNotFound) {
		t.Errorf("all young: err = %v, want errNotFound", err)
	}
}
