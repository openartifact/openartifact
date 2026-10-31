// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/openartifact/openartifact/internal/curation"
)

const (
	condaDemoPath = "linux-64/" + condaPkg
	condaOldPath  = "linux-64/" + condaOldPkg
)

// condaListed is the package files the gated repodata.json of linux-64 lists.
func condaListed(t *testing.T, f *fixture) []string {
	t.Helper()

	w := f.do(http.MethodGet, "linux-64/repodata.json")
	if w.Code != http.StatusOK {
		t.Fatalf("repodata = %d %s", w.Code, w.Body.String())
	}

	tarbz2, condas := condaPackagesOf(t, w.Body.Bytes())
	all := append(tarbz2, condas...)
	slices.Sort(all)

	return all
}

func TestCondaCuration_RepodataAndPackages(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		rules    []curation.Rule
		want     []string // files left in the repodata
		wantDemo int
		wantOld  int
	}{
		{
			name: "denylist drops the blocked package", mode: curation.ModeDenylist,
			rules: []curation.Rule{blockRule("demo", "1.0")},
			want:  []string{condaOldPkg}, wantDemo: http.StatusForbidden, wantOld: http.StatusOK,
		},
		{
			name: "denylist matches a version glob", mode: curation.ModeDenylist,
			rules: []curation.Rule{blockRule("*", "1.*")},
			want:  nil, wantDemo: http.StatusForbidden, wantOld: http.StatusForbidden,
		},
		{
			name: "denylist of another version drops nothing", mode: curation.ModeDenylist,
			rules: []curation.Rule{blockRule("demo", "2.0")},
			want:  []string{condaPkg, condaOldPkg}, wantDemo: http.StatusOK, wantOld: http.StatusOK,
		},
		{
			name: "allowlist keeps only the allowed package", mode: curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("old", "")},
			want:  []string{condaOldPkg}, wantDemo: http.StatusForbidden, wantOld: http.StatusOK,
		},
		{
			name: "block beats allow", mode: curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("*", ""), blockRule("old", "")},
			want:  []string{condaPkg}, wantDemo: http.StatusOK, wantOld: http.StatusForbidden,
		},
		{
			name: "mode off ignores the rules", mode: curation.ModeOff,
			rules: []curation.Rule{blockRule("*", "")},
			want:  []string{condaPkg, condaOldPkg}, wantDemo: http.StatusOK, wantOld: http.StatusOK,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, up := condaFixture(t)
			f.useCuration(c.mode, c.rules...)

			if got := condaListed(t, f); !slices.Equal(got, c.want) && (len(got) != 0 || len(c.want) != 0) {
				t.Errorf("repodata lists %v, want %v", got, c.want)
			}

			if got := f.do(http.MethodGet, condaDemoPath).Code; got != c.wantDemo {
				t.Errorf("demo package = %d, want %d", got, c.wantDemo)
			}

			if got := f.do(http.MethodGet, condaOldPath).Code; got != c.wantOld {
				t.Errorf("old package = %d, want %d", got, c.wantOld)
			}

			if c.wantDemo == http.StatusForbidden {
				if up.hitsOf(condaDemoPath) != 0 || f.db.entry("repo-1", condaDemoPath) != nil {
					t.Error("a curated package was fetched or cached")
				}
			}
		})
	}
}

func TestCondaCuration_CurrentRepodataAndZstdAreFilteredToo(t *testing.T) {
	f, _ := condaFixture(t)
	f.useCuration(curation.ModeDenylist, blockRule("demo", ""))

	for _, p := range []string{"linux-64/current_repodata.json", "linux-64/repodata.json"} {
		w := f.do(http.MethodGet, p)
		if tarbz2, condas := condaPackagesOf(t, w.Body.Bytes()); w.Code != http.StatusOK || len(tarbz2) != 1 || len(condas) != 0 {
			t.Errorf("%s = %d: packages %v, packages.conda %v", p, w.Code, tarbz2, condas)
		}
	}

	w := f.do(http.MethodGet, "linux-64/repodata.json.zst")
	if w.Code != http.StatusOK {
		t.Fatalf("zst = %d", w.Code)
	}

	dec, err := zstd.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	plain, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("zstd body: %v", err)
	}

	if tarbz2, condas := condaPackagesOf(t, plain); len(tarbz2) != 1 || len(condas) != 0 {
		t.Errorf("zst: packages %v, packages.conda %v", tarbz2, condas)
	}
}

func TestCondaCuration_EntryNameCountsAsMuchAsTheFileName(t *testing.T) {
	f, up := condaFixture(t)

	// The file is named demo, the entry says it is evil.
	repodata := condaRepodata(`{"subdir":"linux-64"}`, nil,
		map[string]string{condaPkg: condaEntryJSON("evil", sum(string(condaPkgBody)), "", int64(len(condaPkgBody)))})
	up.set("linux-64/repodata.json", repodata)
	up.set("linux-64/repodata.json.zst", zstdOf(t, repodata))

	f.useCuration(curation.ModeDenylist, blockRule("evil", ""))

	if got := condaListed(t, f); len(got) != 0 {
		t.Errorf("repodata lists %v, want nothing", got)
	}

	if w := f.do(http.MethodGet, condaDemoPath); w.Code != http.StatusForbidden {
		t.Errorf("package = %d, want 403", w.Code)
	}
}

func TestCondaCuration_CachedPackageIsRefusedOnceBlocked(t *testing.T) {
	f, up := condaFixture(t)
	src := f.useCuration(curation.ModeDenylist)

	if w := f.do(http.MethodGet, condaDemoPath); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), condaPkgBody) {
		t.Fatalf("package while nothing is blocked = %d", w.Code)
	}

	src.set(blockRule("demo", ""))

	hits := up.hitsOf(condaDemoPath)

	w := f.do(http.MethodGet, condaDemoPath)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "curation") {
		t.Errorf("cached package after the block = %d %q, want 403", w.Code, w.Body.String())
	}

	if up.hitsOf(condaDemoPath) != hits {
		t.Error("refusal reached the upstream")
	}
}

func TestCondaCuration_AllowExemptsFromTheMinimumAge(t *testing.T) {
	f, _ := condaGateFixture(t) // demo built an hour ago, minimum age 24 hours

	f.useCuration(curation.ModeDenylist, blockRule("other", ""))

	if got := condaListed(t, f); !slices.Equal(got, []string{condaOldPkg}) {
		t.Fatalf("repodata lists %v, want only the old package", got)
	}

	if w := f.do(http.MethodGet, condaDemoPath); w.Code != http.StatusForbidden {
		t.Fatalf("young package without an allow rule = %d, want 403", w.Code)
	}

	f.useCuration(curation.ModeDenylist, allowRule("demo", ""))

	if got := condaListed(t, f); !slices.Equal(got, []string{condaPkg, condaOldPkg}) {
		t.Errorf("repodata lists %v, want both", got)
	}

	if w := f.do(http.MethodGet, condaDemoPath); w.Code != http.StatusOK {
		t.Errorf("allowed young package = %d, want 200", w.Code)
	}
}

func TestCondaCuration_ValidatorFollowsTheRules(t *testing.T) {
	f, _ := condaFixture(t)
	src := f.useCuration(curation.ModeDenylist, blockRule("demo", ""))

	first := f.do(http.MethodGet, "linux-64/repodata.json").Header().Get("ETag")
	if again := f.do(http.MethodGet, "linux-64/repodata.json").Header().Get("ETag"); first == "" || again != first {
		t.Fatalf("ETag %q then %q, want it stable", first, again)
	}

	src.set(blockRule("old", ""))

	if second := f.do(http.MethodGet, "linux-64/repodata.json").Header().Get("ETag"); second == first {
		t.Errorf("ETag %q did not change with the rules", second)
	}

	f.repo.CurationMode = curation.ModeOff

	if off := f.do(http.MethodGet, "linux-64/repodata.json").Header().Get("ETag"); strings.Contains(off, "~") {
		t.Errorf("verbatim ETag %q carries a gate stamp", off)
	}
}

func TestCondaCuration_RulesFailClosed(t *testing.T) {
	f, _ := condaFixture(t)
	f.repo.CurationMode = curation.ModeDenylist // no rule source configured

	for _, p := range []string{"linux-64/repodata.json", condaDemoPath} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", p, w.Code)
		}
	}
}

func TestCondaCuration_PublishedReadsTheCachedRepodataOnly(t *testing.T) {
	f, up := condaGateFixture(t)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "demo", "1.0"); ok {
		t.Fatal("publish time known before the repodata was cached")
	}

	if up.hitsOf("linux-64/repodata.json.zst") != 0 {
		t.Fatal("PublishTime reached the upstream")
	}

	if w := f.do(http.MethodGet, "linux-64/repodata.json.zst"); w.Code != http.StatusOK {
		t.Fatalf("repodata = %d", w.Code)
	}

	hits := up.hitsOf("linux-64/repodata.json.zst")

	got, ok := f.svc.PublishTime(ctx, f.repo, "Demo", "1.0")
	if !ok || time.Since(got) < 50*time.Minute || time.Since(got) > 70*time.Minute {
		t.Errorf("PublishTime(demo) = %v, %v; want about an hour ago", got, ok)
	}

	if got, ok = f.svc.PublishTime(ctx, f.repo, "old", "1.0"); !ok || time.Since(got) < 71*time.Hour {
		t.Errorf("PublishTime(old) = %v, %v; want about three days ago", got, ok)
	}

	if _, ok = f.svc.PublishTime(ctx, f.repo, "demo", "9.9"); ok {
		t.Error("an unknown version has a publish time")
	}

	if up.hitsOf("linux-64/repodata.json.zst") != hits {
		t.Error("PublishTime reached the upstream")
	}
}
