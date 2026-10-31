// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

const npmMinAgeGate = 48 // hours

// minAgeRegistry serves package "lib" with versions of known ages:
//
//	1.0.0        200h old  (old enough)
//	1.1.0         72h old  (old enough; the newest version that is)
//	2.0.0          1h old  (latest upstream, too new)
//	2.1.0-rc.1     2h old  (tag "next", too new)
//	0.9.0         no time entry (passes)
type minAgeRegistry struct {
	tarballHost string
	times       map[string]string
	tarballGets atomic.Int32
	abbrServed  atomic.Int32
	dropUndated bool // leave 0.9.0 (no time entry) out
}

func newMinAgeRegistry() *minAgeRegistry {
	ago := func(h int) string {
		return time.Now().Add(-time.Duration(h) * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	}

	return &minAgeRegistry{times: map[string]string{
		"created":    ago(1000),
		"modified":   ago(1),
		"1.0.0":      ago(200),
		"1.1.0":      ago(72),
		"2.0.0":      ago(1),
		"2.1.0-rc.1": ago(2),
	}}
}

func (g *minAgeRegistry) versions() []string {
	if g.dropUndated {
		return []string{"1.0.0", "1.1.0", "2.0.0", "2.1.0-rc.1"}
	}

	return []string{"0.9.0", "1.0.0", "1.1.0", "2.0.0", "2.1.0-rc.1"}
}

func (g *minAgeRegistry) doc(abbreviated bool) string {
	versions := map[string]any{}

	for _, v := range g.versions() {
		versions[v] = map[string]any{
			"name": "lib", "version": v,
			"dist": map[string]string{"tarball": g.tarballHost + "/lib/-/lib-" + v + ".tgz"},
		}
	}

	doc := map[string]any{
		"name":      "lib",
		"dist-tags": map[string]string{"latest": "2.0.0", "next": "2.1.0-rc.1", "old": "1.0.0"},
		"versions":  versions,
	}

	if abbreviated {
		doc["modified"] = g.times["modified"]
	} else {
		doc["time"] = g.times
	}

	b, _ := json.Marshal(doc)

	return string(b)
}

func (g *minAgeRegistry) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/lib":
		abbreviated := strings.Contains(r.Header.Get("Accept"), pkgnpm.AbbreviatedContentType)
		if abbreviated {
			g.abbrServed.Add(1)
			w.Header().Set("Content-Type", pkgnpm.AbbreviatedContentType)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}

		_, _ = io.WriteString(w, g.doc(abbreviated))
	case strings.HasPrefix(r.URL.Path, "/lib/-/lib-") && strings.HasSuffix(r.URL.Path, ".tgz"):
		g.tarballGets.Add(1)
		_, _ = io.WriteString(w, "tarball "+r.URL.Path)
	default:
		http.NotFound(w, r)
	}
}

func minAgeFixture(t *testing.T, hours int) (*fixture, *minAgeRegistry) {
	t.Helper()

	reg := newMinAgeRegistry()
	f := newFixture(t, reg.handler)
	reg.tarballHost = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	f.repo.Format = "npm"
	f.repo.Name = "npmjs"
	f.repo.UpstreamURL = reg.tarballHost
	f.repo.MinAgeHours = hours
	f.svc.SetBaseURL(oaBase + "/")

	return f, reg
}

type gatedPackument struct {
	DistTags map[string]string          `json:"dist-tags"` //nolint:tagliatelle // npm wire format
	Versions map[string]json.RawMessage `json:"versions"`
	Time     map[string]string          `json:"time"`
}

func decodeGated(t *testing.T, body string) gatedPackument {
	t.Helper()

	var d gatedPackument
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("not a packument: %v\n%s", err, body)
	}

	return d
}

func TestNPMMinAge_PackumentHidesYoungVersionsAndMovesTags(t *testing.T) {
	f, _ := minAgeFixture(t, npmMinAgeGate)

	w := f.do(http.MethodGet, "lib")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	d := decodeGated(t, w.Body.String())

	for _, v := range []string{"0.9.0", "1.0.0", "1.1.0"} {
		if _, ok := d.Versions[v]; !ok {
			t.Errorf("version %s was hidden, want kept", v)
		}
	}

	for _, v := range []string{"2.0.0", "2.1.0-rc.1"} {
		if _, ok := d.Versions[v]; ok {
			t.Errorf("young version %s is listed", v)
		}

		if _, ok := d.Time[v]; ok {
			t.Errorf("young version %s is still in time", v)
		}
	}

	if d.Time["created"] == "" || d.Time["modified"] == "" {
		t.Errorf("created/modified dropped from time: %v", d.Time)
	}

	if d.DistTags["latest"] != "1.1.0" || d.DistTags["old"] != "1.0.0" {
		t.Errorf("dist-tags = %v, want latest=1.1.0 old=1.0.0", d.DistTags)
	}

	if _, ok := d.DistTags["next"]; ok {
		t.Errorf("tag next points at a hidden version: %v", d.DistTags)
	}
}

func TestNPMMinAge_GateOffLeavesThePackumentUnchanged(t *testing.T) {
	f, _ := minAgeFixture(t, 0)

	d := decodeGated(t, f.do(http.MethodGet, "lib").Body.String())

	if len(d.Versions) != 5 || d.DistTags["latest"] != "2.0.0" || d.DistTags["next"] != "2.1.0-rc.1" {
		t.Errorf("gate off: versions %d, tags %v; want all five versions and the upstream tags", len(d.Versions), d.DistTags)
	}
}

func TestNPMMinAge_AbbreviatedRequestIsAnsweredFromTheFullPackument(t *testing.T) {
	f, reg := minAgeFixture(t, npmMinAgeGate)

	w := f.doWith("lib", map[string]string{"Accept": pkgnpm.AbbreviatedContentType})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if got := reg.abbrServed.Load(); got != 0 {
		t.Errorf("the abbreviated upstream document was fetched %d times; a gated repository must not use it", got)
	}

	d := decodeGated(t, w.Body.String())
	if _, ok := d.Versions["2.0.0"]; ok || d.DistTags["latest"] != "1.1.0" {
		t.Errorf("abbreviated request leaked a young version: versions %d, tags %v", len(d.Versions), d.DistTags)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (the document is the full one)", ct)
	}

	// With the gate off the abbreviated document is served as before.
	f.repo.MinAgeHours = 0

	w = f.doWith("lib", map[string]string{"Accept": pkgnpm.AbbreviatedContentType})
	if got := reg.abbrServed.Load(); got != 1 || w.Header().Get("Content-Type") != pkgnpm.AbbreviatedContentType {
		t.Errorf("gate off: abbreviated fetches %d, Content-Type %q", got, w.Header().Get("Content-Type"))
	}
}

func TestNPMMinAge_VersionManifest(t *testing.T) {
	f, _ := minAgeFixture(t, npmMinAgeGate)

	for path, want := range map[string]int{
		"lib/1.1.0":      http.StatusOK,
		"lib/latest":     http.StatusOK,
		"lib/2.0.0":      http.StatusNotFound,
		"lib/next":       http.StatusNotFound,
		"lib/2.1.0-rc.1": http.StatusNotFound,
	} {
		if w := f.do(http.MethodGet, path); w.Code != want {
			t.Errorf("GET %s = %d, want %d", path, w.Code, want)
		}
	}

	var v struct {
		Version string `json:"version"`
	}

	if err := json.Unmarshal(f.do(http.MethodGet, "lib/latest").Body.Bytes(), &v); err != nil || v.Version != "1.1.0" {
		t.Errorf("lib/latest = %q (%v), want 1.1.0", v.Version, err)
	}
}

func TestNPMMinAge_YoungTarballIsRefusedAndNeverFetched(t *testing.T) {
	f, reg := minAgeFixture(t, npmMinAgeGate)

	w := f.do(http.MethodGet, "lib/-/lib-2.0.0.tgz")
	if w.Code != http.StatusForbidden {
		t.Fatalf("young tarball = %d %q, want 403", w.Code, w.Body.String())
	}

	if got := reg.tarballGets.Load(); got != 0 {
		t.Errorf("the upstream tarball was fetched %d times", got)
	}

	if w := f.do(http.MethodGet, "lib/-/lib-1.1.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("old tarball = %d, want 200", w.Code)
	}

	// A version without a publish time passes.
	if w := f.do(http.MethodGet, "lib/-/lib-0.9.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("tarball of an undated version = %d, want 200", w.Code)
	}
}

func TestNPMMinAge_CachedTarballIsServedAsIs(t *testing.T) {
	f, _ := minAgeFixture(t, 0)

	if w := f.do(http.MethodGet, "lib/-/lib-2.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("tarball with the gate off = %d", w.Code)
	}

	f.repo.MinAgeHours = npmMinAgeGate

	if w := f.do(http.MethodGet, "lib/-/lib-2.0.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("cached tarball with the gate on = %d, want 200 (served as is)", w.Code)
	}
}

func TestNPMMinAge_ValidatorFollowsTheGate(t *testing.T) {
	f, _ := minAgeFixture(t, 0)

	open := f.do(http.MethodGet, "lib").Header().Get("ETag")

	f.repo.MinAgeHours = npmMinAgeGate
	gated := f.do(http.MethodGet, "lib").Header().Get("ETag")

	f.repo.MinAgeHours = npmMinAgeGate * 2
	stricter := f.do(http.MethodGet, "lib").Header().Get("ETag")

	if open == "" || gated == "" || stricter == "" || open == gated || gated == stricter {
		t.Errorf("validators = %q (off), %q (%dh), %q (%dh); want three different ones",
			open, gated, npmMinAgeGate, stricter, npmMinAgeGate*2)
	}

	if w := f.doWith("lib", map[string]string{"If-None-Match": stricter}); w.Code != http.StatusNotModified {
		t.Errorf("revalidating with the current validator = %d, want 304", w.Code)
	}

	if w := f.doWith("lib", map[string]string{"If-None-Match": open}); w.Code != http.StatusOK {
		t.Errorf("revalidating with the validator of an ungated rendering = %d, want 200", w.Code)
	}
}

func TestNPMMinAge_EveryVersionTooNewIsNotFound(t *testing.T) {
	f, reg := minAgeFixture(t, 100000)
	reg.dropUndated = true

	for _, path := range []string{"lib", "lib/latest", "lib/1.0.0"} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (every version is too new)", path, w.Code)
		}
	}
}

func TestRenderNPM_MinAgeBoundaries(t *testing.T) {
	now := time.Date(2024, 5, 10, 12, 0, 0, 0, time.UTC)
	repo := &models.Repository{MinAgeHours: 24}
	p := pkgnpm.Path{Kind: pkgnpm.KindPackument, Package: "p"}

	const doc = `{"dist-tags":{"latest":"3.0.0","beta":"3.0.0"},
	  "versions":{"1.0.0":{},"2.0.0":{},"3.0.0":{}},
	  "time":{"1.0.0":"2024-05-01T00:00:00Z","2.0.0":"2024-05-09T12:00:00Z","3.0.0":"2024-05-09T12:00:01Z"}}`

	out, err := renderNPM([]byte(doc), p, "http://oa.test/repos/r", newGate(repo, nil, now))
	if err != nil {
		t.Fatal(err)
	}

	d := decodeGated(t, string(out))

	// 2.0.0 is exactly 24h old: old enough. 3.0.0 is one second short.
	if _, ok := d.Versions["2.0.0"]; !ok {
		t.Error("a version exactly at the threshold was hidden")
	}

	if _, ok := d.Versions["3.0.0"]; ok {
		t.Error("a version one second short of the threshold is listed")
	}

	if d.DistTags["latest"] != "2.0.0" {
		t.Errorf("latest = %q, want 2.0.0", d.DistTags["latest"])
	}

	if _, ok := d.DistTags["beta"]; ok {
		t.Errorf("beta kept: %v", d.DistTags)
	}

	// The same document, an hour later, offers 3.0.0.
	out, err = renderNPM([]byte(doc), p, "http://oa.test/repos/r", newGate(repo, nil, now.Add(time.Hour)))
	if err != nil || decodeGated(t, string(out)).DistTags["latest"] != "3.0.0" {
		t.Errorf("an hour later: %s, %v; want latest=3.0.0", out, err)
	}

	// Everything dated and too new: no document.
	_, err = renderNPM([]byte(doc), p, "http://oa.test/repos/r", newGate(repo, nil, time.Date(2024, 5, 1, 1, 0, 0, 0, time.UTC)))
	if !errors.Is(err, errNotFound) {
		t.Errorf("every version too new: err = %v, want errNotFound", err)
	}
}
