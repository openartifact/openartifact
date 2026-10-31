// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

func TestFilterAnsibleVersions(t *testing.T) {
	now := time.Date(2025, 6, 2, 12, 0, 0, 0, time.UTC)
	repo := &models.Repository{MinAgeHours: 24}

	refs := []pkgansible.VersionRef{
		{Version: "3.0.0", CreatedAt: "2025-06-02T11:59:59.999999Z"},
		{Version: "2.1.0", CreatedAt: "2025-06-01T12:00:00.000001Z"}, // a microsecond short
		{Version: "2.0.0", CreatedAt: "2025-06-01T12:00:00.000000Z"}, // exactly at the threshold
		{Version: "1.1.0", CreatedAt: ""},                            // undated
		{Version: "1.0.0", CreatedAt: "not a time"},                  // unreadable
	}

	versions := func(rs []pkgansible.VersionRef) []string {
		out := make([]string, 0, len(rs))
		for _, r := range rs {
			out = append(out, r.Version)
		}

		return out
	}

	got := versions(filterAnsibleVersions(repo, refs, now))
	if want := []string{"2.0.0", "1.1.0", "1.0.0"}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}

	if got := versions(filterAnsibleVersions(&models.Repository{}, refs, now)); len(got) != len(refs) {
		t.Errorf("gate 0 kept %v, want everything", got)
	}
}

func ansibleGateFixture(t *testing.T) (*fixture, *fakeGalaxy) {
	t.Helper()

	g := newFakeGalaxy()
	now := time.Now().UTC()
	g.created = map[string]string{
		"1.0.0":      now.Add(-72 * time.Hour).Format("2006-01-02T15:04:05.000000Z"),
		"1.1.0":      now.Add(-time.Hour).Format("2006-01-02T15:04:05.000000Z"),
		"2.0.0-rc.1": now.Add(-time.Minute).Format("2006-01-02T15:04:05.000000Z"),
	}

	f := ansibleFixture(t, g)
	f.repo.MinAgeHours = 24

	return f, g
}

func TestAnsibleGate_VersionListAndHighestVersionLeaveOutTooNewVersions(t *testing.T) {
	f, _ := ansibleGateFixture(t)

	var page struct {
		Meta map[string]int `json:"meta"`
		Data []struct {
			Version string `json:"version"`
		} `json:"data"`
	}

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/", &page); w.Code != http.StatusOK {
		t.Fatalf("versions = %d %s", w.Code, w.Body.String())
	}

	if page.Meta["count"] != 1 || len(page.Data) != 1 || page.Data[0].Version != "1.0.0" {
		t.Fatalf("versions = %+v, want 1.0.0 only", page)
	}

	var doc pkgansible.CollectionDoc

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/", &doc); w.Code != http.StatusOK {
		t.Fatalf("collection = %d", w.Code)
	}

	if doc.HighestVersion == nil || doc.HighestVersion.Version != "1.0.0" {
		t.Errorf("highest version = %+v, want 1.0.0 (never a removed version)", doc.HighestVersion)
	}
}

func TestAnsibleGate_VersionDocumentAndTarballOfATooNewVersionAreRefused(t *testing.T) {
	f, g := ansibleGateFixture(t)

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/1.1.0/", nil); w.Code != http.StatusForbidden {
		t.Errorf("too new version document = %d, want 403", w.Code)
	}

	const tarball = "api/v3/artifacts/collections/ns-demo-1.1.0.tar.gz"

	if w := ansibleGet(t, f, tarball, nil); w.Code != http.StatusForbidden {
		t.Errorf("too new tarball = %d, want 403", w.Code)
	}

	if g.tarballHits.Load() != 0 {
		t.Error("a too new tarball was fetched from the upstream")
	}

	if f.db.entry("repo-1", tarball) != nil {
		t.Error("a too new tarball was cached")
	}

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/1.0.0/", nil); w.Code != http.StatusOK {
		t.Errorf("old enough version document = %d, want 200", w.Code)
	}

	w := ansibleGet(t, f, "api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz", nil)
	if w.Code != http.StatusOK || w.Body.String() != ansibleTarball {
		t.Errorf("old enough tarball = %d", w.Code)
	}
}

func TestAnsibleGate_EveryVersionTooNewIsNotFound(t *testing.T) {
	f, g := ansibleGateFixture(t)
	g.created["1.0.0"] = time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000Z")

	for _, p := range []string{"api/v3/collections/ns/demo/", "api/v3/collections/ns/demo/versions/"} {
		if w := ansibleGet(t, f, p, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}
}

func TestAnsibleGate_ValidatorChangesWhenAVersionAges(t *testing.T) {
	f, g := ansibleGateFixture(t)

	first := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/", nil)

	// 1.1.0 was published long enough ago by now: the same cached list shows it.
	g.created["1.1.0"] = time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02T15:04:05.000000Z")
	f.db.expire("repo-1", "api/v3/collections/ns/demo/versions/")

	second := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/", nil)
	if first.Header().Get("ETag") == "" || first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Errorf("ETags %q and %q must differ when the listing does", first.Header().Get("ETag"), second.Header().Get("ETag"))
	}
}

func TestAnsibleGate_OffIsUnchanged(t *testing.T) {
	f, _ := ansibleGateFixture(t)
	f.repo.MinAgeHours = 0

	var page struct {
		Meta map[string]int `json:"meta"`
	}

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/", &page); w.Code != http.StatusOK || page.Meta["count"] != 3 {
		t.Fatalf("versions = %d %+v, want all 3", w.Code, page)
	}

	if w := ansibleGet(t, f, "api/v3/artifacts/collections/ns-demo-1.1.0.tar.gz", nil); w.Code != http.StatusOK {
		t.Errorf("tarball = %d, want 200", w.Code)
	}
}
