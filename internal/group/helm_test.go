// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"strings"
	"testing"

	pkghelm "github.com/openartifact/openartifact/pkg/helm"
)

// memberIndex builds the index.yaml a member (hosted or proxy) serves:
// relative charts/ URLs, a member-specific
// digest so the merge winner is observable.
func memberIndex(member string, charts map[string][]string) string {
	var b strings.Builder

	b.WriteString("apiVersion: v1\nentries:\n")

	for name, versions := range charts {
		b.WriteString("  " + name + ":\n")

		for _, v := range versions {
			b.WriteString("  - name: " + name + "\n    version: " + v + "\n    description: from " + member + "\n")
			b.WriteString("    digest: " + member + "-" + name + "-" + v + "\n")
			b.WriteString("    urls:\n    - " + "charts/" + name + "-" + v + ".tgz\n")
		}
	}

	return b.String()
}

func helmGroup(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "helm", member("hosted", "helm", "hosted"), member("mirror", "helm", "proxy"))
	return f
}

func parseIndex(t *testing.T, body string) map[string]map[string]pkghelm.ChartVersion {
	t.Helper()

	ix, err := pkghelm.ParseIndex([]byte(body))
	if err != nil {
		t.Fatalf("merged index is not an index: %v\n%s", err, body)
	}

	out := map[string]map[string]pkghelm.ChartVersion{}

	for cv := range ix.Versions() {
		if out[cv.Name] == nil {
			out[cv.Name] = map[string]pkghelm.ChartVersion{}
		}

		out[cv.Name][cv.Version] = cv
	}

	return out
}

func TestHelmGroup_IndexMergePrecedence(t *testing.T) {
	f := helmGroup(t)
	f.set("hosted", "index.yaml", 200, memberIndex("hosted", map[string][]string{"app": {"1.0.0", "1.1.0"}}))
	f.set("mirror", "index.yaml", 200, memberIndex("mirror", map[string][]string{"app": {"1.0.0", "2.0.0"}, "other": {"0.1.0"}}))

	w := f.get(http.MethodGet, "index.yaml")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	got := parseIndex(t, w.Body.String())

	if len(got["app"]) != 3 || len(got["other"]) != 1 {
		t.Fatalf("merged index = %v, want app 1.0.0/1.1.0/2.0.0 and other 0.1.0", got)
	}

	if d := got["app"]["1.0.0"].Digest; d != "hosted-app-1.0.0" {
		t.Errorf("app 1.0.0 digest = %q, want the first member's", d)
	}

	if d := got["app"]["2.0.0"].Digest; d != "mirror-app-2.0.0" {
		t.Errorf("app 2.0.0 digest = %q, want the second member's", d)
	}

	for name, versions := range got {
		for v, cv := range versions {
			want := "charts/" + name + "-" + v + ".tgz"
			if len(cv.URLs) != 1 || cv.URLs[0] != want {
				t.Errorf("%s %s urls = %v, want [%s] (relative to the group)", name, v, cv.URLs, want)
			}
		}
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q", ct)
	}

	// Revalidation: same members, same validator.
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("merged index has no ETag")
	}

	if again := f.get(http.MethodGet, "index.yaml"); again.Header().Get("ETag") != etag {
		t.Errorf("ETag changed between identical requests: %q vs %q", again.Header().Get("ETag"), etag)
	}
}

func TestHelmGroup_ChartsAreFirstHit(t *testing.T) {
	f := helmGroup(t)
	f.set("mirror", "charts/app-2.0.0.tgz", 200, "chart-bytes")
	f.set("mirror", "charts/app-2.0.0.tgz.prov", 200, "prov-bytes")

	for path, want := range map[string]string{"charts/app-2.0.0.tgz": "chart-bytes", "charts/app-2.0.0.tgz.prov": "prov-bytes"} {
		f.asked = nil

		w := f.get(http.MethodGet, path)
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s = %d %q", path, w.Code, w.Body.String())
		}

		if got := f.asked; len(got) != 2 || got[0] != "hosted" || got[1] != "mirror" {
			t.Errorf("%s: members asked = %v, want hosted then mirror", path, got)
		}
	}
}

func TestHelmGroup_OnlyIndexAndChartsAreServed(t *testing.T) {
	f := helmGroup(t)
	// A hosted member answers its chart listing on GET /charts.
	f.set("hosted", "charts", 200, `[{"name":"secret"}]`)
	f.set("hosted", "other", 200, "x")

	for _, p := range []string{"charts", "charts/", "other", "charts/a/b.tgz", ""} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d %q, want 404", p, w.Code, w.Body.String())
		}
	}
}

func TestHelmGroup_MemberFailureHandling(t *testing.T) {
	good := func(f *fixture) {
		f.set("hosted", "index.yaml", 200, memberIndex("hosted", map[string][]string{"app": {"1.0.0"}}))
	}

	t.Run("a failing member is ignored while another contributes", func(t *testing.T) {
		f := helmGroup(t)
		good(f)
		f.set("mirror", "index.yaml", 502, "upstream down")

		if w := f.get(http.MethodGet, "index.yaml"); w.Code != http.StatusOK {
			t.Errorf("status %d, want 200", w.Code)
		}
	})

	t.Run("an unreadable member index is ignored", func(t *testing.T) {
		f := helmGroup(t)
		good(f)
		f.set("mirror", "index.yaml", 200, "<html>nope</html>")

		w := f.get(http.MethodGet, "index.yaml")
		if w.Code != http.StatusOK || len(parseIndex(t, w.Body.String())["app"]) != 1 {
			t.Errorf("status %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("nobody has it", func(t *testing.T) {
		if w := helmGroup(t).get(http.MethodGet, "index.yaml"); w.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", w.Code)
		}
	})

	t.Run("nobody has it and one member failed", func(t *testing.T) {
		f := helmGroup(t)
		f.set("mirror", "index.yaml", 502, "down")

		if w := f.get(http.MethodGet, "index.yaml"); w.Code != http.StatusBadGateway {
			t.Errorf("status %d, want 502", w.Code)
		}
	})

	t.Run("a member the caller cannot read contributes nothing", func(t *testing.T) {
		f := helmGroup(t)
		good(f)
		f.denied["hosted"] = true

		if w := f.get(http.MethodGet, "index.yaml"); w.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", w.Code)
		}
	})

	t.Run("writing to a group is refused", func(t *testing.T) {
		if w := helmGroup(t).get(http.MethodPost, "charts"); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST = %d, want 405", w.Code)
		}
	})
}
