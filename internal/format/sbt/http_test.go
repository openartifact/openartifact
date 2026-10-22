// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

const artifactURL = "/repos/myrepo/org.example/mod/1.0/mod-1.0.jar"

// sbtDB keeps one live row per coordinate, as the sbt_artifacts unique index
// does, and (like the CreateSBTArtifact upsert) replaces the blob of a row that
// already exists. art is the row written last.
type sbtDB struct {
	*formattest.DB
	quotaFake

	art  *models.SBTArtifact
	rows map[string]*models.SBTArtifact
}

func rowKey(group, artifact, version, filename string) string {
	return strings.Join([]string{group, artifact, version, filename}, "|")
}

func (d *sbtDB) CreateSBTArtifact(_ context.Context, a *models.SBTArtifact) error {
	if d.rows == nil {
		d.rows = map[string]*models.SBTArtifact{}
	}

	k := rowKey(a.GroupID, a.ArtifactID, a.Version, a.Filename)
	if old, ok := d.rows[k]; ok {
		old.BlobKey, old.SHA256, old.SizeBytes = a.BlobKey, a.SHA256, a.SizeBytes
		a.ID = old.ID
		d.art = old

		return nil
	}

	a.ID = "a-" + strconv.Itoa(len(d.rows)+1)
	d.art = a
	d.rows[k] = a

	return nil
}

func (d *sbtDB) GetSBTArtifact(_ context.Context, _, group, artifact, version, filename string) (*models.SBTArtifact, error) {
	row := d.rows[rowKey(group, artifact, version, filename)]
	if row == nil {
		return nil, nil // not found is (nil, nil) in the Database contract
	}

	cp := *row // a copy, as a database returns: callers keep the old blob key after an overwrite

	return &cp, nil
}

func (d *sbtDB) DeleteSBTArtifact(_ context.Context, id string) error {
	for k, a := range d.rows {
		if a.ID == id {
			delete(d.rows, k)
		}
	}

	d.art = nil

	return nil
}

// seed stores a row directly, as an older release or another client left it.
func (d *sbtDB) seed(a *models.SBTArtifact) {
	if d.rows == nil {
		d.rows = map[string]*models.SBTArtifact{}
	}

	d.rows[rowKey(a.GroupID, a.ArtifactID, a.Version, a.Filename)] = a
	d.art = a
}

func newTestHandler(t *testing.T) (*HTTPHandler, *sbtDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &sbtDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func TestUploadAndDeleteEmitEvents(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, artifactURL, strings.NewReader("data")))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "org.example/mod" || a["version"] != "1.0" || a["path"] != "org.example/mod/1.0/mod-1.0.jar" {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.art.SHA256]; !ok {
		t.Fatal("blob not stored")
	}

	if w = formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, artifactURL, nil)); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}

	if len(bs.Blobs) != 0 {
		t.Error("blob not removed after delete")
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.seed(&models.SBTArtifact{ID: "a", GroupID: "org.example", ArtifactID: "mod", Version: "1.0", Filename: "mod-1.0.jar", BlobKey: "abc", SHA256: "abc", SizeBytes: 3})
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, artifactURL, nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["org.example/mod/1.0/mod-1.0.jar"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}

func send(t *testing.T, h *HTTPHandler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(method, target, strings.NewReader(body)))
}

// TestHeadProbesExistence is the regression test for IVY-1 on the sbt side:
// resolvers probe with HEAD, which answered 405 because only GET/PUT/DELETE
// were routed.
func TestHeadProbesExistence(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	if w := send(t, h, http.MethodHead, artifactURL, ""); w.Code != http.StatusNotFound {
		t.Fatalf("HEAD of a missing file = %d, want 404", w.Code)
	}

	if len(db.rows) != 0 {
		t.Fatalf("HEAD stored %d rows", len(db.rows))
	}

	send(t, h, http.MethodPut, artifactURL, "jar-bytes")

	w := send(t, h, http.MethodHead, artifactURL, "")
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD = %d, want 200", w.Code)
	}

	if w.Body.Len() != 0 || w.Header().Get("Content-Length") != "9" {
		t.Errorf("HEAD body = %q, Content-Length = %q; want no body and 9", w.Body.String(), w.Header().Get("Content-Length"))
	}
}

// TestRepublishOverwrites is the regression test for IVY-2 on the sbt side:
// publishing a file that already exists was a unique violation answered with a
// 500.
func TestRepublishOverwrites(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	send(t, h, http.MethodPut, artifactURL, "first")

	events := len(pub.Events())

	if w := send(t, h, http.MethodPut, artifactURL, "first"); w.Code != http.StatusCreated {
		t.Fatalf("identical re-publish = %d, want 201", w.Code)
	}

	if len(pub.Events()) != events || len(db.rows) != 1 {
		t.Errorf("an identical re-publish changed state: events %d->%d, rows %d", events, len(pub.Events()), len(db.rows))
	}

	if w := send(t, h, http.MethodPut, artifactURL, "second"); w.Code != http.StatusCreated {
		t.Fatalf("overwrite = %d, want 201: %s", w.Code, w.Body.String())
	}

	if got := send(t, h, http.MethodGet, artifactURL, "").Body.String(); got != "second" {
		t.Errorf("downloaded %q after overwrite, want second", got)
	}

	if len(db.rows) != 1 || len(bs.Blobs) != 1 {
		t.Errorf("rows = %d blobs = %d, want 1 and 1 (the replaced blob is freed)", len(db.rows), len(bs.Blobs))
	}
}

func TestImmutableReleaseRefusesOverwrite(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.Repo.ImmutableReleases = true

	snapshot := "/repos/myrepo/org.example/mod/1.0-SNAPSHOT/mod-1.0-SNAPSHOT.jar"

	send(t, h, http.MethodPut, artifactURL, "first")
	send(t, h, http.MethodPut, snapshot, "first")

	if w := send(t, h, http.MethodPut, artifactURL, "tampered"); w.Code != http.StatusConflict {
		t.Fatalf("overwrite of a release = %d, want 409", w.Code)
	}

	if got := send(t, h, http.MethodGet, artifactURL, "").Body.String(); got != "first" {
		t.Errorf("a refused overwrite changed the file: %q", got)
	}

	if w := send(t, h, http.MethodPut, artifactURL, "first"); w.Code != http.StatusCreated {
		t.Errorf("identical re-publish of a release = %d, want 201", w.Code)
	}

	if w := send(t, h, http.MethodPut, snapshot, "second"); w.Code != http.StatusCreated {
		t.Errorf("overwrite of a SNAPSHOT = %d, want 201", w.Code)
	}
}

// TestMavenLayoutGroups is the regression test for SBT-1: sbt publishes
// `com/example/lib_2.13/1.0/lib_2.13-1.0.jar`, which was read as group com,
// artifact example, version lib_2.13, so the immutability guard looked at the
// wrong version.
func TestMavenLayoutGroups(t *testing.T) {
	h, db, _, pub := newTestHandler(t)
	db.Repo.ImmutableReleases = true

	release := "/repos/myrepo/com/example/lib_2.13/1.0/lib_2.13-1.0.jar"
	// An artifact id that merely contains SNAPSHOT: its version is a release.
	trap := "/repos/myrepo/org/acme/thing-SNAPSHOT/2.0/thing-SNAPSHOT-2.0.jar"
	snapshot := "/repos/myrepo/com/example/lib_2.13/1.1-SNAPSHOT/lib_2.13-1.1-20250101.120000-1.jar"

	for _, u := range []string{release, trap, snapshot} {
		// Distinct bytes per file: the fake database does not count blob references.
		if w := send(t, h, http.MethodPut, u, "data of "+u); w.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d: %s", u, w.Code, w.Body.String())
		}
	}

	if db.rows[rowKey("com.example", "lib_2.13", "1.0", "lib_2.13-1.0.jar")] == nil {
		t.Fatalf("no row at com.example/lib_2.13/1.0; rows = %v", db.rows)
	}

	if a := pub.Events()[0].Artifact(); a["name"] != "com.example/lib_2.13" || a["version"] != "1.0" {
		t.Errorf("artifact = %v, want com.example/lib_2.13 1.0", a)
	}

	for _, u := range []string{release, trap} {
		if w := send(t, h, http.MethodDelete, u, ""); w.Code != http.StatusConflict {
			t.Errorf("DELETE %s = %d, want 409 (release)", u, w.Code)
		}
	}

	if w := send(t, h, http.MethodDelete, snapshot, ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE of a SNAPSHOT = %d, want 204", w.Code)
	}

	if w := send(t, h, http.MethodGet, "/repos/myrepo/com.example/lib_2.13/1.0/lib_2.13-1.0.jar", ""); w.Code != http.StatusOK {
		t.Errorf("GET with the dotted group = %d, want 200", w.Code)
	}
}

// TestLegacyRowsStayReachable covers rows an earlier release stored under the
// mis-split coordinates of a Maven-layout path.
func TestLegacyRowsStayReachable(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	db.seed(&models.SBTArtifact{ID: "old", GroupID: "com", ArtifactID: "example", Version: "lib", Filename: "1.0/lib-1.0.jar", BlobKey: "abc", SHA256: "abc", SizeBytes: 3})
	bs.Blobs["abc"] = []byte("xyz")

	u := "/repos/myrepo/com/example/lib/1.0/lib-1.0.jar"

	if w := send(t, h, http.MethodGet, u, ""); w.Code != http.StatusOK || w.Body.String() != "xyz" {
		t.Fatalf("GET of a legacy row = %d %q, want 200 xyz", w.Code, w.Body.String())
	}

	if w := send(t, h, http.MethodDelete, u, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE of a legacy row = %d, want 204", w.Code)
	}

	if w := send(t, h, http.MethodGet, u, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", w.Code)
	}
}

func TestTooShortOrEmptySegmentsAreRejected(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for _, u := range []string{"/repos/myrepo/a/b/c", "/repos/myrepo/a//c/d.jar", "/repos/myrepo/a/b/c/"} {
		if w := send(t, h, http.MethodPut, u, "x"); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", u, w.Code)
		}
	}

	if len(db.rows) != 0 {
		t.Errorf("rows = %v, want none", db.rows)
	}
}

// A row stored under the legacy reading of a multi-segment-group path must
// still be protected: a new row at the current coordinates would shadow it.
func TestImmutableGuardSeesLegacyRow(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.Repo.ImmutableReleases = true
	db.seed(&models.SBTArtifact{ID: "legacy", GroupID: "com", ArtifactID: "example", Version: "lib", Filename: "1.0/lib-1.0.jar", SHA256: "old", BlobKey: "old"})

	u := "/repos/myrepo/com/example/lib/1.0/lib-1.0.jar"
	if w := send(t, h, http.MethodPut, u, "tampered"); w.Code != http.StatusConflict {
		t.Fatalf("overwrite shadowing a legacy release = %d, want 409", w.Code)
	}

	if len(db.rows) != 1 {
		t.Errorf("rows = %v, want only the legacy row", db.rows)
	}
}
