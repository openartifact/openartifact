// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

const (
	ansibleVersionsPath = "api/v3/collections/ns/demo/versions/"
	ansibleTarball10    = "api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz"
	ansibleTarball11    = "api/v3/artifacts/collections/ns-demo-1.1.0.tar.gz"
)

// ansibleListed is the versions the collection's version list shows, sorted.
func ansibleListed(t *testing.T, f *fixture) ([]string, int) {
	t.Helper()

	var page struct {
		Data []struct {
			Version string `json:"version"`
		} `json:"data"`
	}

	w := ansibleGet(t, f, ansibleVersionsPath, &page)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}

	out := make([]string, 0, len(page.Data))
	for _, v := range page.Data {
		out = append(out, v.Version)
	}

	slices.Sort(out)

	return out, w.Code
}

func TestAnsibleCuration_VersionsAndFiles(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		rules []curation.Rule
		want  []string // versions listed; nil: the collection is not found
		doc11 int      // the detail document of 1.1.0
		tar10 int
		tar11 int
	}{
		{
			name: "denylist hides the blocked version", mode: curation.ModeDenylist,
			rules: []curation.Rule{blockRule("ns.demo", "1.1.0")},
			want:  []string{"1.0.0", "2.0.0-rc.1"}, doc11: http.StatusForbidden, tar10: http.StatusOK, tar11: http.StatusForbidden,
		},
		{
			name: "denylist of the whole collection by glob", mode: curation.ModeDenylist,
			rules: []curation.Rule{blockRule("ns.*", "")},
			want:  nil, doc11: http.StatusForbidden, tar10: http.StatusForbidden, tar11: http.StatusForbidden,
		},
		{
			name: "rules about another collection change nothing", mode: curation.ModeDenylist,
			rules: []curation.Rule{blockRule("other.demo", ""), blockRule("ns.other", "")},
			want:  []string{"1.0.0", "1.1.0", "2.0.0-rc.1"}, doc11: http.StatusOK, tar10: http.StatusOK, tar11: http.StatusOK,
		},
		{
			name: "allowlist keeps the allowed version only", mode: curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("ns.demo", "1.0.0")},
			want:  []string{"1.0.0"}, doc11: http.StatusForbidden, tar10: http.StatusOK, tar11: http.StatusForbidden,
		},
		{
			name: "allowlist of another collection leaves this one out", mode: curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("ns.other", "")},
			want:  nil, doc11: http.StatusForbidden, tar10: http.StatusForbidden, tar11: http.StatusForbidden,
		},
		{
			name: "block beats allow", mode: curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("ns.demo", ""), blockRule("ns.demo", "1.1.0")},
			want:  []string{"1.0.0", "2.0.0-rc.1"}, doc11: http.StatusForbidden, tar10: http.StatusOK, tar11: http.StatusForbidden,
		},
		{
			name: "mode off ignores the rules", mode: curation.ModeOff,
			rules: []curation.Rule{blockRule("ns.demo", "")},
			want:  []string{"1.0.0", "1.1.0", "2.0.0-rc.1"}, doc11: http.StatusOK, tar10: http.StatusOK, tar11: http.StatusOK,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newFakeGalaxy()
			f := ansibleFixture(t, g)
			f.useCuration(c.mode, c.rules...)

			got, code := ansibleListed(t, f)
			if c.want == nil {
				if code != http.StatusNotFound {
					t.Errorf("version list = %d %v, want 404", code, got)
				}

				if w := ansibleGet(t, f, "api/v3/collections/ns/demo/", nil); w.Code != http.StatusNotFound {
					t.Errorf("collection = %d, want 404", w.Code)
				}
			} else if !slices.Equal(got, c.want) {
				t.Errorf("version list = %v, want %v", got, c.want)
			}

			if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/1.1.0/", nil); w.Code != c.doc11 {
				t.Errorf("1.1.0 document = %d, want %d", w.Code, c.doc11)
			}

			if w := ansibleGet(t, f, ansibleTarball10, nil); w.Code != c.tar10 {
				t.Errorf("1.0.0 tarball = %d, want %d", w.Code, c.tar10)
			}

			if w := ansibleGet(t, f, ansibleTarball11, nil); w.Code != c.tar11 {
				t.Errorf("1.1.0 tarball = %d, want %d", w.Code, c.tar11)
			}

			if c.tar11 == http.StatusForbidden && f.db.entry("repo-1", ansibleTarball11) != nil {
				t.Error("a curated tarball was cached")
			}
		})
	}
}

func TestAnsibleCuration_HighestVersionIsTheNewestLeft(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())
	f.useCuration(curation.ModeDenylist, blockRule("ns.demo", "1.1.0"), blockRule("ns.demo", "2.*"))

	var doc pkgansible.CollectionDoc

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/", &doc); w.Code != http.StatusOK {
		t.Fatalf("collection = %d", w.Code)
	}

	if doc.HighestVersion == nil || doc.HighestVersion.Version != "1.0.0" {
		t.Errorf("highest version = %+v, want 1.0.0", doc.HighestVersion)
	}
}

func TestAnsibleCuration_RefusalNamesTheReason(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())
	f.useCuration(curation.ModeDenylist, curation.Rule{Action: curation.ActionBlock, Package: "ns.demo", Version: "1.1.0", Reason: "cve-9"})

	for _, p := range []string{ansibleTarball11, "api/v3/collections/ns/demo/versions/1.1.0/"} {
		if w := ansibleGet(t, f, p, nil); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "cve-9") {
			t.Errorf("GET %s = %d %q, want 403 naming the reason", p, w.Code, w.Body.String())
		}
	}
}

func TestAnsibleCuration_CachedTarballIsRefusedOnceBlocked(t *testing.T) {
	g := newFakeGalaxy()
	f := ansibleFixture(t, g)
	src := f.useCuration(curation.ModeDenylist)

	if w := ansibleGet(t, f, ansibleTarball11, nil); w.Code != http.StatusOK {
		t.Fatalf("tarball while nothing is blocked = %d", w.Code)
	}

	hits := g.tarballHits.Load()

	src.set(blockRule("ns.demo", "1.1.0"))

	if w := ansibleGet(t, f, ansibleTarball11, nil); w.Code != http.StatusForbidden {
		t.Errorf("cached tarball after the block = %d, want 403", w.Code)
	}

	if g.tarballHits.Load() != hits {
		t.Error("refusal reached the upstream")
	}
}

func TestAnsibleCuration_AllowExemptsFromTheMinimumAge(t *testing.T) {
	f, _ := ansibleGateFixture(t) // 1.1.0 and 2.0.0-rc.1 are young, minimum age 24 hours

	f.useCuration(curation.ModeDenylist, blockRule("other.demo", ""))

	if w := ansibleGet(t, f, ansibleTarball11, nil); w.Code != http.StatusForbidden {
		t.Fatalf("young tarball without an allow rule = %d, want 403", w.Code)
	}

	f.useCuration(curation.ModeDenylist, allowRule("ns.demo", "1.1.0"))

	if got, _ := ansibleListed(t, f); !slices.Equal(got, []string{"1.0.0", "1.1.0"}) {
		t.Errorf("version list = %v, want 1.0.0 and the exempt 1.1.0", got)
	}

	if w := ansibleGet(t, f, ansibleTarball11, nil); w.Code != http.StatusOK {
		t.Errorf("exempt young tarball = %d, want 200", w.Code)
	}
}

func TestAnsibleCuration_ValidatorFollowsTheRules(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())
	src := f.useCuration(curation.ModeDenylist, blockRule("ns.demo", "1.1.0"))

	first := ansibleGet(t, f, ansibleVersionsPath, nil).Header().Get("ETag")
	if again := ansibleGet(t, f, ansibleVersionsPath, nil).Header().Get("ETag"); first == "" || again != first {
		t.Fatalf("ETag %q then %q, want it stable", first, again)
	}

	src.set(blockRule("ns.demo", "1.0.0"))

	if second := ansibleGet(t, f, ansibleVersionsPath, nil).Header().Get("ETag"); second == first {
		t.Errorf("ETag %q did not change with the rules", second)
	}
}

func TestAnsibleCuration_RulesFailClosed(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())
	f.repo.CurationMode = curation.ModeDenylist // no rule source configured

	for _, p := range []string{ansibleVersionsPath, "api/v3/collections/ns/demo/", ansibleTarball10} {
		if w := ansibleGet(t, f, p, nil); w.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", p, w.Code)
		}
	}
}

func TestAnsibleCuration_PublishedReadsTheCachedVersionListOnly(t *testing.T) {
	g := newFakeGalaxy()
	g.created = map[string]string{"1.0.0": "2025-01-02T03:04:05.000000Z"}
	f := ansibleFixture(t, g)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "ns.demo", "1.0.0"); ok {
		t.Fatal("publish time known before the version list was cached")
	}

	if f.hits.Load() != 0 {
		t.Fatalf("PublishTime reached the upstream: %d requests", f.hits.Load())
	}

	if w := ansibleGet(t, f, ansibleVersionsPath, nil); w.Code != http.StatusOK {
		t.Fatalf("versions = %d", w.Code)
	}

	hits := f.hits.Load()

	got, ok := f.svc.PublishTime(ctx, f.repo, "ns.demo", "1.0.0")
	if want := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("PublishTime = %v, %v; want %v", got, ok, want)
	}

	if _, ok = f.svc.PublishTime(ctx, f.repo, "ns.demo", "9.9.9"); ok {
		t.Error("an unknown version has a publish time")
	}

	if _, ok = f.svc.PublishTime(ctx, f.repo, "nodots", "1.0.0"); ok {
		t.Error("a name that is no collection has a publish time")
	}

	if f.hits.Load() != hits {
		t.Error("PublishTime reached the upstream")
	}
}
