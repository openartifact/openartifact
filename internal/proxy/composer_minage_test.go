// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

// composerGated is a repository with the gate set to hours.
func composerGated(hours int) *models.Repository {
	return &models.Repository{Format: "composer", RepoType: "proxy", MinAgeHours: hours}
}

func composerTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05-07:00") }

// composerMinAgeDoc is a minified upstream document at now with a gate of 24 h:
//
//	1.3.0  1 h old, 1.2.0 inherits its time (both too new)
//	1.1.0  exactly 24 h old (passes: the gate holds back what is younger)
//	1.0.1  24 h less a second old (too new)
//	1.0.0  10 days old
//	0.9.0  states no time (passes; "time" is unset in the minified form)
//	dev-main  1 h old (a branch is dated by its last commit), dev-undated none
func composerMinAgeDoc(t *testing.T, now time.Time) []byte {
	t.Helper()

	entry := func(v, normalized, published string) pkgcomposer.Version {
		e := pkgcomposer.Version{
			"name": "acme/lib", "version": v, "version_normalized": normalized,
			"dist": map[string]any{"type": "zip", "url": "https://dl.example/" + v + ".zip", "reference": "ref-" + v},
		}
		if published != "" {
			e["time"] = published
		}

		return e
	}

	fresh := composerTime(now.Add(-time.Hour))

	body, err := pkgcomposer.EncodeDocument(map[string][]pkgcomposer.Version{"acme/lib": {
		entry("1.3.0", "1.3.0.0", fresh),
		entry("1.2.0", "1.2.0.0", fresh),
		entry("1.1.0", "1.1.0.0", composerTime(now.Add(-24*time.Hour))),
		entry("1.0.1", "1.0.1.0", composerTime(now.Add(-24*time.Hour+time.Second))),
		entry("1.0.0", "1.0.0.0", composerTime(now.Add(-240*time.Hour))),
		entry("0.9.0", "0.9.0.0", ""),
		entry("dev-main", "dev-main", fresh),
		entry("dev-undated", "dev-undated", ""),
	}}, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	return body
}

func composerVersions(t *testing.T, body []byte) []string {
	t.Helper()

	pkgs, err := pkgcomposer.Decode(body)
	if err != nil {
		t.Fatalf("not a p2 document: %v\n%s", err, body)
	}

	var out []string
	for _, v := range pkgs["acme/lib"] {
		out = append(out, v.VersionString())
	}

	return out
}

func TestRenderComposerDoc_HidesVersionsYoungerThanTheGate(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	doc := composerMinAgeDoc(t, now)

	out, err := renderComposerDoc(composerGated(24), doc, "acme/lib", composerBase+"/repos/packagist", now)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// 1.2.0 only knew its time by inheritance, 0.9.0 lost it through __unset.
	if got, want := composerVersions(t, out), []string{"1.1.0", "1.0.0", "0.9.0", "dev-undated"}; !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}

	if !strings.Contains(string(out), `"minified":"composer/2.0"`) {
		t.Errorf("document is not minified: %s", out)
	}

	if strings.Contains(string(out), "dl.example") {
		t.Errorf("a dist url still points at the upstream: %s", out)
	}
}

func TestRenderComposerDoc_GateOffChangesNothing(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	doc := composerMinAgeDoc(t, now)
	root := composerBase + "/repos/packagist"

	want, err := pkgcomposer.RenderSource(doc, "acme/lib", root, nil)
	if err != nil {
		t.Fatalf("RenderSource: %v", err)
	}

	for _, repo := range []*models.Repository{composerGated(0), composerGated(-1), nil} {
		got, err := renderComposerDoc(repo, doc, "acme/lib", root, now)
		if err != nil || string(got) != string(want) {
			t.Errorf("repo %+v: err %v, output differs from the ungated rendering", repo, err)
		}
	}

	if len(composerVersions(t, want)) != 8 {
		t.Errorf("ungated versions = %v", composerVersions(t, want))
	}
}

func TestRenderComposerDoc_AgesPastTheGateWithTheClock(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	doc := composerMinAgeDoc(t, now)
	repo := composerGated(24)

	// One second later 1.0.1 is exactly at the threshold and passes, 1.3.0 is
	// still young.
	out, err := renderComposerDoc(repo, doc, "acme/lib", "https://x/repos/r", now.Add(time.Second))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if got := composerVersions(t, out); !slices.Contains(got, "1.0.1") || slices.Contains(got, "1.3.0") {
		t.Errorf("versions = %v", got)
	}

	out, err = renderComposerDoc(repo, doc, "acme/lib", "https://x/repos/r", now.Add(23*time.Hour+time.Second))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	got := composerVersions(t, out)
	for _, v := range []string{"1.3.0", "1.2.0", "dev-main"} {
		if !slices.Contains(got, v) {
			t.Errorf("%s did not appear once it was old enough: %v", v, got)
		}
	}
}

func TestRenderComposerDoc_EveryVersionTooNewIsNotFound(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	body, err := pkgcomposer.EncodeDocument(map[string][]pkgcomposer.Version{"acme/lib": {
		{"name": "acme/lib", "version": "1.0.0", "version_normalized": "1.0.0.0", "time": composerTime(now.Add(-time.Hour))},
	}}, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if _, err := renderComposerDoc(composerGated(24), body, "acme/lib", "https://x/repos/r", now); !errors.Is(err, errNotFound) {
		t.Errorf("err = %v, want errNotFound", err)
	}
}

func TestGateStamp_ChangesWithGateAndBucket(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	if got := gateStamp(composerGated(0), now); got != "" {
		t.Errorf("stamp with the gate off = %q", got)
	}

	a := gateStamp(composerGated(24), now)

	if a == "" || a == gateStamp(composerGated(48), now) {
		t.Errorf("stamp does not depend on the threshold: %q", a)
	}

	if a != gateStamp(composerGated(24), now.Add(gateStampBucket-time.Second)) {
		t.Errorf("stamp changed inside its bucket")
	}

	if a == gateStamp(composerGated(24), now.Add(gateStampBucket)) {
		t.Errorf("stamp did not change with the next bucket")
	}
}

// composerAgedFixture is a packagist upstream whose 1.1.0 was published a
// minute ago, 1.0.0 ten days ago, with the gate set to 24 h.
func composerAgedFixture(t *testing.T) (*fixture, *fakePackagist) {
	t.Helper()

	p := newFakePackagist(t)

	times := map[string]string{
		"1.1.0": composerTime(time.Now().Add(-time.Minute)),
		"1.0.0": composerTime(time.Now().Add(-240 * time.Hour)),
	}

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/p2/acme/lib.json" {
			http.NotFound(w, r)
			return
		}

		var versions []pkgcomposer.Version

		for _, v := range []string{"1.1.0", "1.0.0"} {
			e := p.version(v, v+".0")
			e["time"] = times[v]
			versions = append(versions, e)
		}

		body, _ := pkgcomposer.EncodeDocument(map[string][]pkgcomposer.Version{"acme/lib": versions}, true)
		_, _ = w.Write(body)
	})
	f.repo.Format = "composer"
	f.repo.Name = "packagist"
	f.repo.UpstreamURL = f.upstream.URL + "/"
	f.repo.MinAgeHours = 24
	f.svc.baseURL = composerBase

	return f, p
}

func TestComposerProxy_MinAgeHidesNewVersionAndRefusesItsDist(t *testing.T) {
	f, p := composerAgedFixture(t)

	w := f.do(http.MethodGet, "p2/acme/lib.json")
	if w.Code != http.StatusOK {
		t.Fatalf("p2 = %d %s", w.Code, w.Body.String())
	}

	versions := composerServed(t, w.Body.Bytes())
	if len(versions) != 1 || versions[0].VersionString() != "1.0.0" {
		t.Fatalf("gated versions = %v", versions)
	}

	if old := f.do(http.MethodGet, distPath(t, versions[0])); old.Code != http.StatusOK || old.Body.String() != "lib 1.0.0 bytes" {
		t.Errorf("old dist = %d %q", old.Code, old.Body.String())
	}

	// The document no longer names 1.1.0, so build its URL as a client that
	// kept it would: the reference the upstream published for it.
	young := pkgcomposer.Dist{URL: p.dists.URL + "/zip/1.1.0", Reference: "ref-1.1.0"}
	hits := p.distHits.Load()

	if w := f.do(http.MethodGet, "dists/acme/lib/1.1.0/"+young.ProxyRef()+".zip"); w.Code != http.StatusForbidden {
		t.Errorf("dist of a too-new version = %d, want 403", w.Code)
	}

	if p.distHits.Load() != hits {
		t.Error("a too-new dist was fetched from its host")
	}
}

func TestComposerProxy_MinAgeGateOffServesEverything(t *testing.T) {
	f, _ := composerAgedFixture(t)
	f.repo.MinAgeHours = 0

	w := f.do(http.MethodGet, "p2/acme/lib.json")
	versions := composerServed(t, w.Body.Bytes())

	if len(versions) != 2 {
		t.Fatalf("ungated versions = %v", versions)
	}

	if d := f.do(http.MethodGet, distPath(t, versions[0])); d.Code != http.StatusOK || d.Body.String() != "lib 1.1.0 bytes" {
		t.Errorf("dist of the new version = %d %q", d.Code, d.Body.String())
	}

	// Files already in the cache are served as they are when the gate is set later.
	f.repo.MinAgeHours = 24

	if d := f.do(http.MethodGet, distPath(t, versions[0])); d.Code != http.StatusOK {
		t.Errorf("cached dist after the gate was set = %d", d.Code)
	}
}

func TestComposerProxy_MinAgeEveryVersionTooNewIs404(t *testing.T) {
	f, _ := composerAgedFixture(t)
	f.repo.MinAgeHours = 24 * 365

	if w := f.do(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusNotFound {
		t.Errorf("p2 with every version too new = %d, want 404", w.Code)
	}
}

func TestComposerProxy_MinAgeChangesTheValidator(t *testing.T) {
	f, _ := composerAgedFixture(t)

	gated := f.do(http.MethodGet, "p2/acme/lib.json").Header().Get("ETag")
	if gated == "" {
		t.Fatal("no ETag")
	}

	if w := f.doWith("p2/acme/lib.json", map[string]string{"If-None-Match": gated}); w.Code != http.StatusNotModified {
		t.Errorf("same gate revalidation = %d, want 304", w.Code)
	}

	f.repo.MinAgeHours = 0

	if w := f.doWith("p2/acme/lib.json", map[string]string{"If-None-Match": gated}); w.Code != http.StatusOK {
		t.Errorf("a changed gate must not revalidate the old rendering: %d", w.Code)
	}
}
