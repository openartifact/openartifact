// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
)

func helmGated(hours int) *models.Repository {
	return &models.Repository{
		Name: "charts", Format: "helm", RepoType: "proxy",
		UpstreamURL: "https://charts.example/repo", MinAgeHours: hours,
	}
}

// helmMinAgeIndex lists, with a gate of 24 h at now:
//
//	app 2.0.0  1 h old (held back), app 1.0.0  exactly 24 h old, app 0.9.0 states no time
//	fresh 1.0.0  1 h old, the only version of its chart (the chart disappears)
//	twin 1.0.0 and 2.0.0  list one file: the old one keeps it served
//	same-a / same-b 1.0.0  two urls with one file name: the later is named by hash
func helmMinAgeIndex(now time.Time) string {
	stamp := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339Nano) }

	return fmt.Sprintf(`apiVersion: v1
entries:
  app:
  - {name: app, version: 2.0.0, created: %q, urls: [app-2.0.0.tgz]}
  - {name: app, version: 1.0.0, created: %q, urls: [app-1.0.0.tgz]}
  - {name: app, version: 0.9.0, urls: [app-0.9.0.tgz]}
  fresh:
  - {name: fresh, version: 1.0.0, created: %q, urls: [fresh-1.0.0.tgz]}
  twin:
  - {name: twin, version: 2.0.0, created: %q, urls: [twin.tgz]}
  - {name: twin, version: 1.0.0, created: %q, urls: [twin.tgz]}
  same-a:
  - {name: same-a, version: 1.0.0, created: %q, urls: ["a/same-1.0.0.tgz"]}
  same-b:
  - {name: same-b, version: 1.0.0, created: %q, urls: ["b/same-1.0.0.tgz"]}
`, stamp(time.Hour), stamp(24*time.Hour), stamp(time.Hour), stamp(time.Hour), stamp(100*time.Hour),
		stamp(time.Hour), stamp(100*time.Hour))
}

func helmVersions(t *testing.T, body []byte) map[string]string {
	t.Helper()

	ix, err := pkghelm.ParseIndex(body)
	if err != nil {
		t.Fatalf("not an index: %v\n%s", err, body)
	}

	out := map[string]string{}

	for cv := range ix.Versions() {
		out[cv.Name+"@"+cv.Version] = strings.Join(cv.URLs, ",")
	}

	return out
}

func TestRenderHelmIndex_HidesVersionsYoungerThanTheGate(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s := &Service{}

	out, err := s.renderHelmIndex(helmGated(24), []byte(helmMinAgeIndex(now)), now)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	got := helmVersions(t, out)

	for _, want := range []string{"app@1.0.0", "app@0.9.0", "twin@1.0.0", "same-b@1.0.0"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s is missing: %v", want, got)
		}
	}

	for _, gone := range []string{"app@2.0.0", "fresh@1.0.0", "twin@2.0.0", "same-a@1.0.0"} {
		if _, ok := got[gone]; ok {
			t.Errorf("%s is too new and was listed: %v", gone, got)
		}
	}

	if strings.Contains(string(out), "fresh:") {
		t.Errorf("a chart without a version left must disappear:\n%s", out)
	}

	// The held-back same-a took charts/same-1.0.0.tgz; same-b keeps the
	// hash-qualified name it has in an ungated index.
	ungated, err := s.renderHelmIndex(helmGated(0), []byte(helmMinAgeIndex(now)), now)
	if err != nil {
		t.Fatalf("render ungated: %v", err)
	}

	if a, b := helmVersions(t, ungated)["same-b@1.0.0"], got["same-b@1.0.0"]; a != b || !strings.Contains(a, "same-1.0.0-") {
		t.Errorf("same-b is served as %q gated and %q ungated", b, a)
	}

	if len(helmVersions(t, ungated)) != 8 {
		t.Errorf("ungated index = %v", helmVersions(t, ungated))
	}
}

func TestRenderHelmIndex_GateOffAndAging(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s := &Service{}
	doc := []byte(helmMinAgeIndex(now))

	// 23 h 1 s later app 2.0.0 (created an hour before now) is old enough.
	later, err := s.renderHelmIndex(helmGated(24), doc, now.Add(23*time.Hour+time.Second))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if _, ok := helmVersions(t, later)["app@2.0.0"]; !ok {
		t.Errorf("app@2.0.0 did not appear once old enough: %v", helmVersions(t, later))
	}

	// created at exactly the threshold passes (the gate holds back what is younger).
	atEdge, err := s.renderHelmIndex(helmGated(24), doc, now)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if _, ok := helmVersions(t, atEdge)["app@1.0.0"]; !ok {
		t.Errorf("a version exactly at the threshold was held back: %v", helmVersions(t, atEdge))
	}
}

func helmAgedFixture(t *testing.T) (*fixture, *fakeChartRepo) {
	t.Helper()

	f, g := helmFixture(t)
	now := time.Now()

	g.files["young-1.0.0.tgz"] = []byte("young chart bytes")
	g.index = fmt.Sprintf(`apiVersion: v1
entries:
  app:
  - {name: app, version: 1.0.0, created: %q, urls: [app-1.0.0.tgz]}
  young:
  - {name: young, version: 1.0.0, created: %q, urls: [young-1.0.0.tgz]}
`, now.Add(-240*time.Hour).UTC().Format(time.RFC3339), now.Add(-time.Minute).UTC().Format(time.RFC3339))
	f.repo.MinAgeHours = 24

	return f, g
}

func TestHelmProxy_MinAgeHidesNewChartAndRefusesItsFile(t *testing.T) {
	f, g := helmAgedFixture(t)

	w := f.do(http.MethodGet, "index.yaml")
	if w.Code != http.StatusOK {
		t.Fatalf("index.yaml = %d", w.Code)
	}

	got := helmVersions(t, w.Body.Bytes())
	if len(got) != 1 || got["app@1.0.0"] != "charts/app-1.0.0.tgz" {
		t.Errorf("gated index = %v", got)
	}

	if old := f.do(http.MethodGet, "charts/app-1.0.0.tgz"); old.Code != http.StatusOK {
		t.Errorf("old chart = %d", old.Code)
	}

	for _, file := range []string{"charts/young-1.0.0.tgz", "charts/young-1.0.0.tgz.prov"} {
		if w := f.do(http.MethodGet, file); w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", file, w.Code)
		}
	}

	if g.saw("/charts/young-1.0.0.tgz") {
		t.Error("a too-new chart was fetched from the upstream")
	}
}

func TestHelmProxy_MinAgeGateOffServesEverythingAndKeepsCachedFiles(t *testing.T) {
	f, _ := helmAgedFixture(t)
	f.repo.MinAgeHours = 0

	if got := helmVersions(t, f.do(http.MethodGet, "index.yaml").Body.Bytes()); len(got) != 2 {
		t.Fatalf("ungated index = %v", got)
	}

	if w := f.do(http.MethodGet, "charts/young-1.0.0.tgz"); w.Code != http.StatusOK || w.Body.String() != "young chart bytes" {
		t.Fatalf("ungated young chart = %d %q", w.Code, w.Body.String())
	}

	f.repo.MinAgeHours = 24

	if w := f.do(http.MethodGet, "charts/young-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("a cached chart is served as it is when the gate is set later: %d", w.Code)
	}
}

func TestHelmProxy_MinAgeChangesTheValidator(t *testing.T) {
	f, _ := helmAgedFixture(t)

	etag := f.do(http.MethodGet, "index.yaml").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	if w := f.doWith("index.yaml", map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified {
		t.Errorf("same gate revalidation = %d, want 304", w.Code)
	}

	f.repo.MinAgeHours = 0

	if w := f.doWith("index.yaml", map[string]string{"If-None-Match": etag}); w.Code != http.StatusOK {
		t.Errorf("a changed gate must not revalidate the old rendering: %d", w.Code)
	}
}
