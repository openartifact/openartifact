// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

// curationPyPIFixture is a project page with one release per file group, all old
// enough for any minimum age used below unless stated otherwise.
func curationPyPIFixture(t *testing.T, hours int) *fixture {
	t.Helper()

	return agePyPIFixture(t, hours,
		ageFile{ageSdist231, 400 * hour},
		ageFile{ageSdist232, 300 * hour},
		ageFile{wheelName, 300 * hour},
		ageFile{ageSdist233, 3 * hour},
		ageFile{ageWheel233, 3 * hour},
		ageFile{ageSdist234, 0},
	)
}

func TestPyPICuration_ProjectPageFollowsTheRules(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		rules []curation.Rule
		want  []string
	}{
		{
			name:  "denylist drops every file of the blocked release",
			mode:  curation.ModeDenylist,
			rules: []curation.Rule{blockRule("requests", "2.33.0")},
			want:  []string{ageSdist231, ageSdist232, wheelName, ageSdist234},
		},
		{
			name:  "version glob",
			mode:  curation.ModeDenylist,
			rules: []curation.Rule{blockRule("req*", "2.32.?")},
			want:  []string{ageSdist231, ageSdist233, ageWheel233, ageSdist234},
		},
		{
			name:  "a release without an upload time is curated all the same",
			mode:  curation.ModeDenylist,
			rules: []curation.Rule{blockRule("requests", "2.34.0")},
			want:  []string{ageSdist231, ageSdist232, wheelName, ageSdist233, ageWheel233},
		},
		{
			name:  "allowlist keeps only the allowed releases",
			mode:  curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("requests", "2.32.*")},
			want:  []string{ageSdist232, wheelName},
		},
		{
			name:  "block beats allow",
			mode:  curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("requests", ""), blockRule("requests", "2.33.*"), blockRule("requests", "2.34.*")},
			want:  []string{ageSdist231, ageSdist232, wheelName},
		},
		{
			name:  "mode off leaves the rules inert",
			mode:  curation.ModeOff,
			rules: []curation.Rule{blockRule("requests", "")},
			want:  []string{ageSdist231, ageSdist232, wheelName, ageSdist233, ageWheel233, ageSdist234},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := curationPyPIFixture(t, 0)
			f.useCuration(tt.mode, tt.rules...)

			w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}

			if got := listedFiles(t, w.Body.Bytes()); !sameNames(got, tt.want) {
				t.Errorf("files = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPyPICuration_FileDownloadIsRefusedWithTheReason(t *testing.T) {
	f := curationPyPIFixture(t, 0)
	f.useCuration(curation.ModeDenylist,
		curation.Rule{Action: curation.ActionBlock, Package: "requests", Version: "2.32.0", Reason: "CVE-2099-2"})

	for _, name := range []string{wheelName, wheelName + pypisimple.MetadataSuffix} {
		w := f.do(http.MethodGet, "files/requests/"+name)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "CVE-2099-2") {
			t.Errorf("%s = %d %q, want 403 naming the reason", name, w.Code, w.Body.String())
		}
	}

	if w := f.do(http.MethodGet, "files/requests/"+ageSdist231); w.Code == http.StatusForbidden {
		t.Errorf("file of another release refused: %q", w.Body.String())
	}
}

func TestPyPICuration_AllowlistWithAnotherPackageLeavesNothing(t *testing.T) {
	f := curationPyPIFixture(t, 0)
	f.useCuration(curation.ModeAllowlist, allowRule("flask", ""))

	if w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON); w.Code != http.StatusNotFound {
		t.Errorf("project page = %d, want 404", w.Code)
	}

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusForbidden {
		t.Errorf("file = %d, want 403", w.Code)
	}
}

func TestPyPICuration_AllowExemptsARecentReleaseFromTheMinimumAge(t *testing.T) {
	f := curationPyPIFixture(t, 24)
	src := f.useCuration(curation.ModeDenylist, blockRule("requests", "2.31.0"))

	page := func() []string {
		return listedFiles(t, f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON).Body.Bytes())
	}

	if got, want := page(), []string{ageSdist232, wheelName, ageSdist234}; !sameNames(got, want) {
		t.Fatalf("without an allow rule: %v, want %v (2.33.0 too new)", got, want)
	}

	src.set(blockRule("requests", "2.31.0"), allowRule("requests", "2.33.0"))

	if got, want := page(), []string{ageSdist232, wheelName, ageSdist233, ageWheel233, ageSdist234}; !sameNames(got, want) {
		t.Errorf("with an allow rule: %v, want %v", got, want)
	}
}

func TestPyPICuration_ValidatorFollowsRulesAndMode(t *testing.T) {
	f := curationPyPIFixture(t, 0)
	etag := func() string {
		return f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON).Header().Get("ETag")
	}

	plain := etag()
	src := f.useCuration(curation.ModeDenylist, blockRule("requests", "2.33.0"))
	first := etag()

	src.set(blockRule("requests", "2.34.0"))
	second := etag()

	if plain == "" || plain == first || first == second {
		t.Errorf("validators %q %q %q; want three different ones", plain, first, second)
	}

	if w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON); w.Header().Get("ETag") != second {
		t.Error("the validator of unchanged rules is not stable")
	}

	f.repo.CurationMode = curation.ModeOff

	if off := etag(); off != plain {
		t.Errorf("curation off changed the validator: %q, want %q", off, plain)
	}
}

func TestPyPICuration_CachedFileIsRefusedOnceItsReleaseIsCurated(t *testing.T) {
	f := curationPyPIFixture(t, 0)

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK {
		t.Fatalf("file with curation off = %d", w.Code)
	}

	f.useCuration(curation.ModeDenylist, blockRule("requests", "2.32.0"))

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusForbidden {
		t.Errorf("cached file of a curated release = %d, want 403", w.Code)
	}
}

func TestGatePyPIFiles_CurationUsesTheNormalizedProjectAndTheSpelledVersion(t *testing.T) {
	repo := &models.Repository{Format: "pypi", RepoType: "proxy", CurationMode: curation.ModeDenylist}
	files := []pypisimple.File{{Filename: "zope.interface-6.0.tar.gz"}, {Filename: "zope.interface-6.1.tar.gz"}}

	gate := func(rules ...curation.Rule) versionGate { return newGate(repo, rules, time.Now()) }

	got, err := gatePyPIFiles(gate(blockRule("zope-interface", "6.0")), "zope-interface", files)
	if err != nil || len(got) != 1 || got[0].Filename != "zope.interface-6.1.tar.gz" {
		t.Errorf("got %v, %v; want only 6.1", got, err)
	}

	if _, err := gatePyPIFiles(gate(blockRule("zope-*", "")), "zope-interface", files); !errors.Is(err, errNotFound) {
		t.Errorf("every file curated: err = %v, want errNotFound", err)
	}
}

func TestPyPIPublished_DatesAReleaseFromTheCachedJSONPage(t *testing.T) {
	f := curationPyPIFixture(t, 0)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "requests", "2.31.0"); ok {
		t.Fatal("a project page that is not cached dated a release")
	}

	if w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON); w.Code != http.StatusOK {
		t.Fatalf("project page = %d", w.Code)
	}

	for _, version := range []string{"2.31.0", "2.31"} {
		got, ok := f.svc.PublishTime(ctx, f.repo, "Requests", version)
		if !ok || time.Since(got) < 399*time.Hour || time.Since(got) > 401*time.Hour {
			t.Errorf("%s published = %v, %v; want about 400h ago", version, got, ok)
		}
	}

	for _, version := range []string{"2.34.0", "9.9.9"} {
		if got, ok := f.svc.PublishTime(ctx, f.repo, "requests", version); ok {
			t.Errorf("%s dated %v; want no date for an undated or unknown release", version, got)
		}
	}
}
