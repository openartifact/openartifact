// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

// hostedWinsCase is one format's dependency-confusion scenario. The hosted
// member holds a package at version "1.0.0"; the proxy member (named "mirror")
// holds the same package with the extra version "9.9.9" and a file for it, and
// also a package the hosted member does not hold.
type hostedWinsCase struct {
	format string
	setup  func(t *testing.T, f *fixture)
	meta   string // package metadata request
	probe  string // what the hosted member is asked about the package
	// hostedHas and proxyOnly are told apart in the metadata body.
	hostedHas, proxyOnly string
	file                 string // the proxy-only file
	other                string // metadata of a package only the proxy holds
}

func hostedWinsCases() map[string]hostedWinsCase {
	return map[string]hostedWinsCase{
		"npm": {
			format: "npm",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()

				f.h.SetBaseURL(npmBase)
				f.set("hosted", "lib", 200, packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))
				f.set("mirror", "lib", 200, packument(t, "mirror", "lib", map[string]string{"1.0.0": "BBBB", "9.9.9": "CCCC"}, map[string]string{"latest": "9.9.9"}))
				f.set("mirror", "lib/-/lib-9.9.9.tgz", 200, "evil")
				f.set("mirror", "other", 200, packument(t, "mirror", "other", map[string]string{"1.0.0": "DDDD"}, map[string]string{"latest": "1.0.0"}))
			},
			meta: "lib", probe: "lib",
			hostedHas: `"1.0.0"`, proxyOnly: `"9.9.9"`,
			file: "lib/-/lib-9.9.9.tgz", other: "other",
		},
		"go": {
			format: "go",
			setup: func(_ *testing.T, f *fixture) {
				f.set("hosted", "example.com/mod/@v/list", 200, "v1.0.0")
				f.set("mirror", "example.com/mod/@v/list", 200, "v1.0.0\nv9.9.9")
				f.set("mirror", "example.com/mod/@v/v9.9.9.info", 200, `{"Version":"v9.9.9"}`)
				f.set("mirror", "example.com/other/@v/list", 200, "v1.0.0")
			},
			meta: "example.com/mod/@v/list", probe: "example.com/mod/@v/list",
			hostedHas: "v1.0.0", proxyOnly: "v9.9.9",
			file: "example.com/mod/@v/v9.9.9.info", other: "example.com/other/@v/list",
		},
		"cargo": {
			format: "cargo",
			setup: func(_ *testing.T, f *fixture) {
				f.set("hosted", "index/de/mo/demo", 200, cargoLine("hosted", "1.0.0"))
				f.set("mirror", "index/de/mo/demo", 200, cargoLine("mirror", "1.0.0")+cargoLine("mirror", "9.9.9"))
				f.set("mirror", "api/v1/crates/demo/9.9.9/download", 200, "evil")
				f.set("mirror", "index/ot/he/other", 200, cargoLine("mirror", "1.0.0"))
			},
			meta: "index/de/mo/demo", probe: "index/de/mo/demo",
			hostedHas: `"1.0.0"`, proxyOnly: `"9.9.9"`,
			file: "api/v1/crates/demo/9.9.9/download", other: "index/ot/he/other",
		},
		"nuget": {
			format: "nuget",
			setup: func(_ *testing.T, f *fixture) {
				f.h.SetBaseURL(nugetBase + "/")
				f.set("hosted", "v3/package/lib/index.json", 200, `{"versions":["1.0.0"]}`)
				f.set("mirror", "v3/package/lib/index.json", 200, `{"versions":["1.0.0","9.9.9"]}`)
				f.set("hosted", "v3/registration/lib/index.json", 200, nugetInlineIndex("hosted", "lib", "1.0.0"))
				f.set("mirror", "v3/registration/lib/index.json", 200, nugetInlineIndex("mirror", "lib", "1.0.0", "9.9.9"))
				f.set("mirror", "v3/package/lib/9.9.9/lib.9.9.9.nupkg", 200, "evil")
				f.set("mirror", "v3/package/other/index.json", 200, `{"versions":["1.0.0"]}`)
			},
			meta: "v3/registration/lib/index.json", probe: "v3/package/lib/index.json",
			hostedHas: `"1.0.0"`, proxyOnly: `"9.9.9"`,
			file: "v3/package/lib/9.9.9/lib.9.9.9.nupkg", other: "v3/package/other/index.json",
		},
		"maven": {
			format: "maven",
			setup: func(_ *testing.T, f *fixture) {
				f.set("hosted", metaPath, 200, versionsDoc("1", "1.0"))
				f.set("mirror", metaPath, 200, versionsDoc("2", "1.0", "9.9"))
				f.set("mirror", "org/x/lib/9.9/lib-9.9.jar", 200, "evil")
				f.set("mirror", "org/x/other/maven-metadata.xml", 200, versionsDoc("3", "1.0"))
			},
			meta: metaPath, probe: metaPath,
			hostedHas: "<version>1.0</version>", proxyOnly: "<version>9.9</version>",
			file: "org/x/lib/9.9/lib-9.9.jar", other: "org/x/other/maven-metadata.xml",
		},
	}
}

func newHostedWins(t *testing.T, c hostedWinsCase) *fixture {
	t.Helper()

	f := newFixture(t, c.format, member("hosted", c.format, "hosted"), member("mirror", c.format, "proxy"))
	f.grp.MergeProxyVersions = false
	c.setup(t, f)

	return f
}

func TestHostedWins_HostedNameHidesTheProxysVersionsAndFiles(t *testing.T) {
	for name, c := range hostedWinsCases() {
		t.Run(name, func(t *testing.T) {
			f := newHostedWins(t, c)

			w := f.get(http.MethodGet, c.meta)
			if w.Code != http.StatusOK {
				t.Fatalf("metadata = %d: %s", w.Code, w.Body.String())
			}

			if body := w.Body.String(); !strings.Contains(body, c.hostedHas) || strings.Contains(body, c.proxyOnly) {
				t.Errorf("metadata must list the hosted version only:\n%s", body)
			}

			if slices.Contains(f.asked, "mirror") {
				t.Errorf("proxy asked for a name a hosted member holds: %v", f.asked)
			}

			f.asked = nil

			if w := f.get(http.MethodGet, c.file); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "evil") {
				t.Errorf("proxy-only file = %d %q, want 404", w.Code, w.Body.String())
			}

			if slices.Contains(f.asked, "mirror") {
				t.Errorf("proxy asked for a file of a hosted name: %v", f.asked)
			}
		})
	}
}

func TestHostedWins_MergeProxyVersionsKeepsTheUnion(t *testing.T) {
	for name, c := range hostedWinsCases() {
		t.Run(name, func(t *testing.T) {
			f := newHostedWins(t, c)
			f.grp.MergeProxyVersions = true

			w := f.get(http.MethodGet, c.meta)
			if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, c.hostedHas) || !strings.Contains(body, c.proxyOnly) {
				t.Errorf("metadata = %d, want the union:\n%s", w.Code, body)
			}

			if w := f.get(http.MethodGet, c.file); w.Code != http.StatusOK || w.Body.String() == "" {
				t.Errorf("proxy-only file = %d, want it served", w.Code)
			}
		})
	}
}

func TestHostedWins_NamesNoHostedMemberHoldsStayWithTheProxy(t *testing.T) {
	for name, c := range hostedWinsCases() {
		t.Run(name, func(t *testing.T) {
			f := newHostedWins(t, c)

			if w := f.get(http.MethodGet, c.other); w.Code != http.StatusOK {
				t.Errorf("proxy-only package = %d, want 200: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHostedWins_FailingHostedProbeFailsClosed(t *testing.T) {
	for name, c := range hostedWinsCases() {
		t.Run(name, func(t *testing.T) {
			f := newHostedWins(t, c)
			f.set("hosted", c.probe, http.StatusInternalServerError, "boom")

			if w := f.get(http.MethodGet, c.meta); w.Code != http.StatusBadGateway {
				t.Errorf("metadata with a failing hosted member = %d, want 502", w.Code)
			}

			if w := f.get(http.MethodGet, c.file); w.Code != http.StatusBadGateway {
				t.Errorf("file with a failing hosted member = %d, want 502", w.Code)
			}
		})
	}
}

func TestHostedWins_UnreadableHostedMemberDoesNotHideTheProxy(t *testing.T) {
	for name, c := range hostedWinsCases() {
		t.Run(name, func(t *testing.T) {
			f := newHostedWins(t, c)
			f.denied["hosted"] = true

			w := f.get(http.MethodGet, c.meta)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), c.proxyOnly) {
				t.Errorf("metadata = %d, want the proxy's (the hosted member is unreadable):\n%s", w.Code, w.Body.String())
			}

			if w := f.get(http.MethodGet, c.file); w.Code != http.StatusOK {
				t.Errorf("proxy-only file = %d, want 200", w.Code)
			}
		})
	}
}

func TestHostedWins_HostedFileIsServedAheadOfTheProxy(t *testing.T) {
	f := newHostedWins(t, hostedWinsCases()["npm"])
	f.set("hosted", "lib/-/lib-1.0.0.tgz", 200, "hosted-bytes")
	f.set("mirror", "lib/-/lib-1.0.0.tgz", 200, "proxy-bytes")

	if w := f.get(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Body.String() != "hosted-bytes" {
		t.Errorf("file = %q, want the hosted member's", w.Body.String())
	}
}

func TestHostedWins_GroupWithoutProxyOrHostedIsNotProbed(t *testing.T) {
	f := newFixture(t, "npm", member("a", "npm", "hosted"), member("b", "npm", "hosted"))
	f.grp.MergeProxyVersions = false
	f.set("a", "lib/-/lib-1.0.0.tgz", 200, "bytes")

	if w := f.get(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	if len(f.asked) != 1 {
		t.Errorf("asked = %v, want the plain first hit", f.asked)
	}
}

func TestHostedWins_BlockedGroupIs403(t *testing.T) {
	f := newFixture(t, "raw", member("a", "raw", "hosted"))
	f.set("a", "f", 200, "x")
	f.grp.Blocked = true

	if w := f.get(http.MethodGet, "f"); w.Code != http.StatusForbidden {
		t.Errorf("blocked group = %d, want 403", w.Code)
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked on a blocked group: %v", f.asked)
	}
}

// helm: index.yaml is filtered, chart files are decided from the hosted index.
func helmHostedWins(t *testing.T) *fixture {
	t.Helper()

	f := helmGroup(t)
	f.grp.MergeProxyVersions = false
	f.set("hosted", "index.yaml", 200, memberIndex("hosted", map[string][]string{"app": {"1.0.0"}}))
	f.set("mirror", "index.yaml", 200, memberIndex("mirror", map[string][]string{"app": {"1.0.0", "9.9.9"}, "other": {"0.1.0"}}))
	f.set("mirror", "charts/app-9.9.9.tgz", 200, "evil")
	f.set("mirror", "charts/app-9.9.9.tgz.prov", 200, "evil-prov")
	f.set("mirror", "charts/other-0.1.0.tgz", 200, "other-bytes")

	return f
}

func TestHostedWins_HelmIndexDropsHostedChartsFromProxies(t *testing.T) {
	f := helmHostedWins(t)

	w := f.get(http.MethodGet, "index.yaml")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	got := parseIndex(t, w.Body.String())
	if len(got["app"]) != 1 || got["app"]["1.0.0"].Digest != "hosted-app-1.0.0" {
		t.Errorf("app = %v, want the hosted 1.0.0 only", got["app"])
	}

	if len(got["other"]) != 1 {
		t.Errorf("other = %v, a chart no hosted member holds stays", got["other"])
	}

	f.grp.MergeProxyVersions = true

	if got := parseIndex(t, f.get(http.MethodGet, "index.yaml").Body.String()); len(got["app"]) != 2 {
		t.Errorf("merge flag: app = %v, want the union", got["app"])
	}
}

func TestHostedWins_HelmChartFiles(t *testing.T) {
	f := helmHostedWins(t)

	for _, p := range []string{"charts/app-9.9.9.tgz", "charts/app-9.9.9.tgz.prov"} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (the hosted member holds the chart)", p, w.Code)
		}
	}

	if w := f.get(http.MethodGet, "charts/other-0.1.0.tgz"); w.Code != http.StatusOK || w.Body.String() != "other-bytes" {
		t.Errorf("proxy-only chart = %d %q", w.Code, w.Body.String())
	}

	f.set("hosted", "charts/app-1.0.0.tgz", 200, "hosted-bytes")

	if w := f.get(http.MethodGet, "charts/app-1.0.0.tgz"); w.Body.String() != "hosted-bytes" {
		t.Errorf("hosted chart = %q", w.Body.String())
	}

	f.denied["hosted"] = true

	if w := f.get(http.MethodGet, "charts/app-9.9.9.tgz"); w.Code != http.StatusOK {
		t.Errorf("unreadable hosted member hid the proxy chart: %d", w.Code)
	}
}

func TestHostedWins_HelmFailingHostedIndexFailsClosed(t *testing.T) {
	f := helmHostedWins(t)
	f.set("hosted", "index.yaml", http.StatusInternalServerError, "boom")

	for _, p := range []string{"index.yaml", "charts/app-9.9.9.tgz"} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusBadGateway {
			t.Errorf("%s = %d, want 502", p, w.Code)
		}
	}
}

func TestIsChartFileOf(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		want       bool
	}{
		{"app", "app-1.0.0.tgz", true},
		{"app", "app-v1.0.0.tgz.prov", true},
		{"app", "app-extra-1.0.0.tgz", false},
		{"app", "app-.tgz", false},
		{"app", "other-1.0.0.tgz", false},
		{"app", "app-v.tgz", false},
	} {
		if got := isChartFileOf(tc.name, tc.file); got != tc.want {
			t.Errorf("isChartFileOf(%q, %q) = %v, want %v", tc.name, tc.file, got, tc.want)
		}
	}
}

// pypi has its own member fake (HTML and JSON pages with content types).
func pypiHostedWins(t *testing.T) *pypiGroupFixture {
	t.Helper()

	g := newPypiGroup(t)
	g.grp.MergeProxyVersions = false
	g.members.pages["hosted"]["simple/foo/"] = hostedPage("foo-1.0-py3-none-any.whl", hashA)
	g.members.pages["upstream"]["simple/foo/"] = proxyJSONPage(
		jsonFileEntry("foo-1.0-py3-none-any.whl", hashB, "") + "," + jsonFileEntry("foo-9.9-py3-none-any.whl", hashC, ""))
	g.members.pages["upstream"]["simple/bar/"] = proxyJSONPage(jsonFileEntry("bar-1.0.tar.gz", hashB, ""))
	g.members.files["upstream files/foo/foo-9.9-py3-none-any.whl"] = "evil"

	return g
}

func filenames(files []pypisimple.File) []string {
	out := make([]string, len(files))
	for i := range files {
		out[i] = files[i].Filename
	}

	return out
}

func TestHostedWins_PyPI(t *testing.T) {
	g := pypiHostedWins(t)

	w := g.get(http.MethodGet, "simple/foo/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if got := filenames(parse(t, w)); !slices.Equal(got, []string{"foo-1.0-py3-none-any.whl"}) {
		t.Errorf("files = %v, want the hosted file only", got)
	}

	for _, accept := range []string{"", pypisimple.ContentTypeJSON} {
		if w := g.get(http.MethodGet, "files/foo/foo-9.9-py3-none-any.whl", accept); w.Code != http.StatusNotFound {
			t.Errorf("proxy-only file = %d %q, want 404", w.Code, w.Body.String())
		}
	}

	if w := g.get(http.MethodGet, "simple/bar/", ""); w.Code != http.StatusOK {
		t.Errorf("project only the proxy holds = %d, want 200", w.Code)
	}

	g.grp.MergeProxyVersions = true

	if got := filenames(parse(t, g.get(http.MethodGet, "simple/foo/", ""))); len(got) != 2 {
		t.Errorf("merge flag: files = %v, want the union", got)
	}

	if w := g.get(http.MethodGet, "files/foo/foo-9.9-py3-none-any.whl", ""); w.Code != http.StatusOK {
		t.Errorf("merge flag: proxy-only file = %d, want 200", w.Code)
	}
}

func TestHostedWins_PyPIFailuresAndUnreadable(t *testing.T) {
	g := pypiHostedWins(t)
	g.members.fail["hosted"] = true

	for _, p := range []string{"simple/foo/", "files/foo/foo-9.9-py3-none-any.whl"} {
		if w := g.get(http.MethodGet, p, ""); w.Code != http.StatusBadGateway {
			t.Errorf("%s with a failing hosted member = %d, want 502", p, w.Code)
		}
	}

	g.members.fail["hosted"] = false

	be := g.members.backend()
	be.Authorize = func(_ *http.Request, m *models.Repository, _ string) bool { return m.Name != "hosted" }

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	g.h.Serve(w, r, g.grp, "simple/foo/", be)

	if got := filenames(parse(t, w)); len(got) != 2 || !slices.Contains(got, "foo-9.9-py3-none-any.whl") {
		t.Errorf("unreadable hosted member hid the proxy: files = %v", got)
	}
}

// docker: the image is probed through its tag list.
func (f *fixture) getOCI(path string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/all/"+path, nil)
	w := httptest.NewRecorder()
	f.h.ServeOCI(w, r, f.grp, ocipath.Parse("all/"+path), path, f.backend())

	return w
}

func ociHostedWins(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "docker", member("hosted", "docker", "hosted"), member("mirror", "docker", "proxy"))
	f.grp.MergeProxyVersions = false
	f.set("hosted", "img/tags/list", 200, `{"name":"img","tags":["h1"]}`)
	f.set("mirror", "img/tags/list", 200, `{"name":"img","tags":["m1"]}`)
	f.set("hosted", "img/manifests/h1", 200, "hosted-manifest")
	f.set("mirror", "img/manifests/m1", 200, "evil-manifest")
	f.set("mirror", "img/blobs/sha256:aa", 200, "evil-blob")
	f.set("mirror", "other/tags/list", 200, `{"name":"other","tags":["m1"]}`)
	f.set("mirror", "other/manifests/m1", 200, "proxy-manifest")

	return f
}

func TestHostedWins_DockerImages(t *testing.T) {
	f := ociHostedWins(t)

	if w := f.getOCI("img/tags/list"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"h1"`) || strings.Contains(w.Body.String(), "m1") {
		t.Errorf("tags = %d %s, want the hosted tags only", w.Code, w.Body.String())
	}

	if w := f.getOCI("img/manifests/h1"); w.Body.String() != "hosted-manifest" {
		t.Errorf("hosted manifest = %q", w.Body.String())
	}

	f.asked = nil

	for _, p := range []string{"img/manifests/m1", "img/blobs/sha256:aa"} {
		w := f.getOCI(p)
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "evil") {
			t.Errorf("%s = %d %q, want 404", p, w.Code, w.Body.String())
		}
	}

	if slices.Contains(f.asked, "mirror") {
		t.Errorf("proxy asked for an image a hosted member holds: %v", f.asked)
	}

	if w := f.getOCI("other/manifests/m1"); w.Code != http.StatusOK || w.Body.String() != "proxy-manifest" {
		t.Errorf("image only the proxy holds = %d %q", w.Code, w.Body.String())
	}

	f.grp.MergeProxyVersions = true

	if w := f.getOCI("img/tags/list"); !strings.Contains(w.Body.String(), `"h1","m1"`) {
		t.Errorf("merge flag: tags = %s, want the union", w.Body.String())
	}

	if w := f.getOCI("img/manifests/m1"); w.Body.String() != "evil-manifest" {
		t.Errorf("merge flag: proxy manifest = %q", w.Body.String())
	}
}

func TestHostedWins_DockerFailuresUnreadableAndBlocked(t *testing.T) {
	f := ociHostedWins(t)
	f.set("hosted", "img/tags/list", http.StatusInternalServerError, "boom")

	for _, p := range []string{"img/tags/list", "img/manifests/m1"} {
		if w := f.getOCI(p); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "UNAVAILABLE") {
			t.Errorf("%s with a failing hosted member = %d %s, want 502 UNAVAILABLE", p, w.Code, w.Body.String())
		}
	}

	f = ociHostedWins(t)
	f.denied["hosted"] = true

	if w := f.getOCI("img/manifests/m1"); w.Code != http.StatusOK {
		t.Errorf("unreadable hosted member hid the proxy image: %d", w.Code)
	}

	f.grp.Blocked = true

	if w := f.getOCI("img/manifests/m1"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "DENIED") {
		t.Errorf("blocked group = %d %s, want 403 DENIED", w.Code, w.Body.String())
	}
}

// The merge gate is taken when a member starts delivering, once per request,
// and freed when the request is done.
func TestGroupGate_TakenLazilyOncePerRequest(t *testing.T) {
	f := npmGroup(t)

	var before, during int

	doc := packument(t, "m", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"})
	be := Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, _ *http.Request, m *models.Repository, _ string) {
			if m.Name == "hosted" {
				before = len(f.h.packuments) // still waiting on its upstream

				w.WriteHeader(http.StatusOK)
				during = len(f.h.packuments)
			}

			_, _ = io.WriteString(w, doc)
		},
	}

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	f.h.Serve(w, r, f.grp, "lib", be)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if before != 0 || during != 1 {
		t.Errorf("gate slots held before/after the member started delivering = %d/%d, want 0/1", before, during)
	}

	if n := len(f.h.packuments); n != 0 {
		t.Errorf("%d gate slots still held after the request", n)
	}
}

func TestGroupGate_CanceledWhileWaitingAbortsTheMember(t *testing.T) {
	f := npmGroup(t)

	for range cap(f.h.packuments) {
		f.h.packuments <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var writeErr error

	be := Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, _ *http.Request, _ *models.Repository, _ string) {
			w.WriteHeader(http.StatusOK)

			_, writeErr = io.WriteString(w, "{}")
		},
	}

	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	f.h.Serve(w, r, f.grp, "lib", be)

	if writeErr == nil {
		t.Error("member write did not fail although the gate wait was canceled")
	}

	if w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", w.Code)
	}
}

func TestMergeSession_RequestBudgetCapsAllMembers(t *testing.T) {
	h := newFixture(t, "raw").h
	s := h.newSession(context.Background())

	s.used = maxRequestBytes - 2

	cw := s.capture(maxRequestBytes)
	if _, err := cw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}

	if !cw.overflow {
		t.Error("a capture beyond the request budget must overflow (failed member)")
	}

	if !s.reserve(2) || s.reserve(1) {
		t.Errorf("budget accounting off: used %d of %d", s.used, maxRequestBytes)
	}
}
