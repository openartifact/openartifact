// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

const (
	ageSdist231 = "requests-2.31.0.tar.gz"
	ageSdist232 = "requests-2.32.0.tar.gz"
	ageSdist233 = "requests-2.33.0.tar.gz"
	ageWheel233 = "requests-2.33.0-py3-none-any.whl"
	ageSdist234 = "requests-2.34.0.tar.gz"
)

// ageFile is one file entry of an upstream JSON page; uploaded is the age of
// the upload, 0 for a file without upload-time.
type ageFile struct {
	name     string
	uploaded time.Duration
}

// agePage is a PEP 691/700 upstream page whose files were uploaded the given
// time ago, with the upstream's own "versions" list.
func agePage(files ...ageFile) string {
	var b strings.Builder

	b.WriteString(`{"meta":{"api-version":"1.1"},"name":"requests","versions":["2.31.0","2.32.0","2.33.0","2.34.0"],"files":[`)

	for i, f := range files {
		if i > 0 {
			b.WriteString(",")
		}

		sha := strings.Repeat("cd", 32)
		if f.name == wheelName {
			sha = sum(wheelBody)
		}

		b.WriteString(`{"filename":"` + f.name + `","url":"%FILES%/files/` + f.name + `","hashes":{"sha256":"` + sha + `"}`)

		if f.uploaded > 0 {
			b.WriteString(`,"upload-time":"` + time.Now().Add(-f.uploaded).UTC().Format(time.RFC3339) + `"`)
		}

		b.WriteString("}")
	}

	b.WriteString("]}")

	return b.String()
}

func agePyPIFixture(t *testing.T, hours int, files ...ageFile) *fixture {
	t.Helper()

	f := newPyPIFixture(t, &pypiUpstream{page: agePage(files...), pageType: pypisimple.ContentTypeJSON})
	f.repo.MinAgeHours = hours

	return f
}

// listedFiles is the filenames of a JSON project page.
func listedFiles(t *testing.T, body []byte) []string {
	t.Helper()

	var doc struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}

	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("bad json %s: %v", body, err)
	}

	names := make([]string, 0, len(doc.Files))
	for _, f := range doc.Files {
		names = append(names, f.Filename)
	}

	return names
}

func sameNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

const hour = time.Hour

func TestPyPIProxyMinAge_ProjectPageHidesNewReleases(t *testing.T) {
	f := agePyPIFixture(t, 24,
		ageFile{ageSdist231, 400 * hour},
		ageFile{wheelName, 100 * hour},
		// A late sdist of an old release: the release stays old.
		ageFile{ageSdist232, 2 * hour},
		// A brand-new release, both files hidden whatever their own age.
		ageFile{ageSdist233, 3 * hour},
		ageFile{ageWheel233, 30 * time.Minute},
		// No upload-time: passes.
		ageFile{ageSdist234, 0},
	)

	w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	want := []string{ageSdist231, wheelName, ageSdist232, ageSdist234}
	if got := listedFiles(t, w.Body.Bytes()); !sameNames(got, want) {
		t.Errorf("json files = %v, want %v", got, want)
	}

	// The PEP 700 versions list of the upstream is not republished, so it
	// cannot name the hidden release.
	if strings.Contains(w.Body.String(), "2.33.0") || strings.Contains(w.Body.String(), `"versions"`) {
		t.Errorf("hidden release leaks into the page: %s", w.Body.String())
	}

	h := f.doAccept(http.MethodGet, "simple/requests/", "")
	for _, hidden := range []string{ageSdist233, ageWheel233} {
		if strings.Contains(h.Body.String(), hidden) {
			t.Errorf("html page lists hidden %s: %s", hidden, h.Body.String())
		}
	}

	for _, shown := range want {
		if !strings.Contains(h.Body.String(), shown) {
			t.Errorf("html page lacks %s: %s", shown, h.Body.String())
		}
	}
}

func TestPyPIProxyMinAge_GateOffLeavesPageUnchanged(t *testing.T) {
	files := []ageFile{{ageSdist231, 400 * hour}, {ageSdist233, 10 * time.Minute}}
	f := agePyPIFixture(t, 0, files...)

	w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON)
	if got := listedFiles(t, w.Body.Bytes()); !sameNames(got, []string{ageSdist231, ageSdist233}) {
		t.Errorf("gate off listed %v", got)
	}

	// The validator of an ungated page is not salted with a gate.
	if etag := w.Header().Get("ETag"); etag != pageETag(f.repo, "simple/requests/", f.db.entry("repo-1", "simple/requests/").BlobKey,
		pypisimple.Offer{Format: pypisimple.JSON, ContentType: pypisimple.ContentTypeJSON}, "") {
		t.Errorf("etag %s changed with the gate off", etag)
	}
}

func TestPyPIProxyMinAge_AllTooNewIsNotFound(t *testing.T) {
	f := agePyPIFixture(t, 24, ageFile{ageSdist233, hour}, ageFile{ageWheel233, 2 * hour})

	for _, accept := range []string{"", pypisimple.ContentTypeJSON} {
		if w := f.doAccept(http.MethodGet, "simple/requests/", accept); w.Code != http.StatusNotFound {
			t.Errorf("accept %q: status %d, want 404: %s", accept, w.Code, w.Body.String())
		}
	}
}

func TestPyPIProxyMinAge_HTMLUpstreamHasNoTimesAndPasses(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})
	f.repo.MinAgeHours = 24 * 365

	w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON)
	if got := listedFiles(t, w.Body.Bytes()); !sameNames(got, []string{wheelName, "requests-1.0.tar.gz"}) {
		t.Errorf("html upstream listed %v", got)
	}

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK {
		t.Errorf("html upstream download: status %d", w.Code)
	}
}

func TestPyPIProxyMinAge_DownloadOfTooNewFileIsForbidden(t *testing.T) {
	f := agePyPIFixture(t, 24,
		ageFile{wheelName, 100 * hour},
		ageFile{ageSdist233, hour},
		ageFile{ageWheel233, 30 * time.Minute},
	)

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK || w.Body.String() != wheelBody {
		t.Fatalf("old file: status %d body %q", w.Code, w.Body.String())
	}

	// Every file of the new release is refused.
	for _, name := range []string{ageSdist233, ageWheel233} {
		w := f.do(http.MethodGet, "files/requests/"+name)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), errTooNew.Error()) {
			t.Errorf("%s: status %d body %q, want 403", name, w.Code, w.Body.String())
		}
	}

	// A file the page does not list is still plainly not found.
	if w := f.do(http.MethodGet, "files/requests/requests-9.9.9.tar.gz"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted: status %d, want 404", w.Code)
	}
}

func TestPyPIProxyMinAge_CachedFileIsServedAsIs(t *testing.T) {
	f := agePyPIFixture(t, 0, ageFile{wheelName, hour})

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK {
		t.Fatalf("before the gate: status %d", w.Code)
	}

	f.repo.MinAgeHours = 24

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK || w.Body.String() != wheelBody {
		t.Errorf("cached file under the gate: status %d body %q", w.Code, w.Body.String())
	}
}

func TestPyPIProxyMinAge_ValidatorFollowsTheGate(t *testing.T) {
	f := agePyPIFixture(t, 24, ageFile{wheelName, 100 * hour}, ageFile{ageSdist233, hour})

	etag := func(hours int) string {
		f.repo.MinAgeHours = hours

		return f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON).Header().Get("ETag")
	}

	off, day, week := etag(0), etag(24), etag(168)
	if off == "" || off == day || day == week || off == week {
		t.Errorf("validators must differ per gate: off=%s 24h=%s 168h=%s", off, day, week)
	}

	// The index lists names only: the gate does not touch its validator.
	f.repo.MinAgeHours = 0
	plain := f.doAccept(http.MethodGet, "simple/", "").Header().Get("ETag")
	f.repo.MinAgeHours = 24

	if gated := f.doAccept(http.MethodGet, "simple/", "").Header().Get("ETag"); gated != plain {
		t.Errorf("index validator changed with the gate: %s vs %s", gated, plain)
	}
}

func TestPageETag_GateStampSalts(t *testing.T) {
	repo := &models.Repository{Name: "p"}
	offer := pypisimple.Offer{Format: pypisimple.JSON, ContentType: pypisimple.ContentTypeJSON}
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

	repo.MinAgeHours = 24

	a := pageETag(repo, "simple/x/", "blob", offer, gateStamp(repo, now))
	same := pageETag(repo, "simple/x/", "blob", offer, gateStamp(repo, now.Add(time.Minute)))
	later := pageETag(repo, "simple/x/", "blob", offer, gateStamp(repo, now.Add(gateStampBucket)))

	if a != same {
		t.Errorf("one bucket, two validators: %s %s", a, same)
	}

	if a == later {
		t.Errorf("a later bucket must change the validator, both %s", a)
	}

	repo.MinAgeHours = 0

	off := pageETag(repo, "simple/x/", "blob", offer, gateStamp(repo, now))
	if off == a || off != pageETag(repo, "simple/x/", "blob", offer, "") {
		t.Errorf("gate off validator %s", off)
	}
}

func TestGatePyPIFiles_Boundaries(t *testing.T) {
	repo := &models.Repository{MinAgeHours: 24}
	now := time.Date(2024, 5, 2, 12, 0, 0, 0, time.UTC)
	stamp := func(age time.Duration) string { return now.Add(-age).Format(time.RFC3339Nano) }

	tests := []struct {
		name  string
		age   time.Duration
		keeps bool
	}{
		{"exactly at the threshold", 24 * hour, true},
		{"one second short", 24*hour - time.Second, false},
		{"older", 25 * hour, true},
		{"just uploaded", 0, false},
	}

	for _, tt := range tests {
		files := []pypisimple.File{{Filename: "foo-2.0.tar.gz", UploadTime: stamp(tt.age)}}

		got, err := gatePyPIFiles(repo, "foo", files, now)
		if tt.keeps && (err != nil || len(got) != 1) {
			t.Errorf("%s: got %v, %v; want kept", tt.name, got, err)
		}

		if !tt.keeps && !errors.Is(err, errNotFound) {
			t.Errorf("%s: got %v, %v; want errNotFound", tt.name, got, err)
		}
	}

	// The newest release is hidden, the older one stays; an undated file and
	// an unparsable time pass; an empty upstream page stays an empty page.
	files := []pypisimple.File{
		{Filename: "foo-1.0.tar.gz", UploadTime: stamp(72 * hour)},
		{Filename: "foo-2.0.tar.gz", UploadTime: stamp(hour)},
		{Filename: "foo-3.0.tar.gz"},
		{Filename: "foo-4.0.tar.gz", UploadTime: "garbage"},
	}

	got, err := gatePyPIFiles(repo, "foo", files, now)
	if err != nil || len(got) != 3 || got[0].Filename != "foo-1.0.tar.gz" || got[1].Filename != "foo-3.0.tar.gz" ||
		got[2].Filename != "foo-4.0.tar.gz" {
		t.Errorf("got %v, %v", got, err)
	}

	if got, err := gatePyPIFiles(repo, "foo", nil, now); err != nil || len(got) != 0 {
		t.Errorf("empty page: %v, %v", got, err)
	}

	// Gate off: the very same slice, untouched.
	off := &models.Repository{}
	if got, err := gatePyPIFiles(off, "foo", files, now); err != nil || len(got) != len(files) {
		t.Errorf("gate off: %v, %v", got, err)
	}
}
