// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

var errCreateFailed = errors.New("create failed")

type helmDB struct {
	*formattest.DB

	charts    map[string]*models.HelmChart
	createErr error

	limit     int64 // 0 = unlimited
	used      int64
	artifacts int
}

func newHelmDB() *helmDB {
	return &helmDB{DB: formattest.NewDB(), charts: map[string]*models.HelmChart{}}
}

func chartKey(name, version string) string { return name + "@" + version }

func (d *helmDB) CreateHelmChart(_ context.Context, c *models.HelmChart) error {
	if d.createErr != nil {
		return d.createErr
	}

	if _, ok := d.charts[chartKey(c.Name, c.Version)]; ok {
		return database.ErrDuplicate
	}

	c.ID = "chart-1"
	d.charts[chartKey(c.Name, c.Version)] = c

	return nil
}

func (d *helmDB) GetHelmChart(_ context.Context, _, name, version string) (*models.HelmChart, error) {
	return d.charts[chartKey(name, version)], nil
}

func (d *helmDB) DeleteHelmChart(_ context.Context, _, name, version string) error {
	delete(d.charts, chartKey(name, version))
	return nil
}

func (d *helmDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size
	d.artifacts += count

	return true, nil
}

func (d *helmDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.used -= size
	d.artifacts -= count

	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *helmDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := newHelmDB()
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func upload(h *HTTPHandler, chart []byte) int {
	return formattest.Do(h.RegisterRepoRoutes,
		formattest.Request(http.MethodPost, "/repos/myrepo/charts", bytes.NewReader(chart))).Code
}

func TestUploadReservesQuotaStoresBlobAndEmitsEvent(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	chart := createHelmChart(t, sampleChartYAML)

	if code := upload(h, chart); code != http.StatusCreated {
		t.Fatalf("upload status = %d", code)
	}

	if db.used != int64(len(chart)) || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", db.used, db.artifacts, len(chart))
	}

	stored := db.charts[chartKey("mychart", "1.0.0")]
	if stored == nil {
		t.Fatal("chart row not created")
	}

	if _, ok := bs.Blobs[stored.BlobKey]; !ok {
		t.Error("blob not stored")
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "mychart" || a["version"] != "1.0.0" || a["path"] != "charts/mychart-1.0.0.tgz" {
		t.Errorf("artifact = %v", a)
	}
}

func TestUploadOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 10

	if code := upload(h, createHelmChart(t, sampleChartYAML)); code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", code)
	}

	if len(db.charts) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 {
		t.Errorf("charts=%d blobs=%d events=%d used=%d; want nothing persisted", len(db.charts), len(bs.Blobs), len(pub.Events()), db.used)
	}
}

func TestUploadDuplicateDoesNotConsumeQuota(t *testing.T) {
	h, db, _, pub := newTestHandler(t)
	chart := createHelmChart(t, sampleChartYAML)

	if code := upload(h, chart); code != http.StatusCreated {
		t.Fatalf("first upload status = %d", code)
	}

	if code := upload(h, chart); code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", code)
	}

	if db.used != int64(len(chart)) || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after duplicate, want %d/1", db.used, db.artifacts, len(chart))
	}

	if n := len(pub.Events()); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

func TestUploadCreateFailureReleasesQuotaAndBlob(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.createErr = errCreateFailed

	if code := upload(h, createHelmChart(t, sampleChartYAML)); code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota not released: used=%d artifacts=%d", db.used, db.artifacts)
	}

	if len(bs.Blobs) != 0 {
		t.Error("orphan blob left after failed insert")
	}

	if len(pub.Events()) != 0 {
		t.Error("event emitted for a failed upload")
	}
}

func TestUploadRaceDuplicateReturns409AndReleasesQuota(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.createErr = database.ErrDuplicate

	if code := upload(h, createHelmChart(t, sampleChartYAML)); code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}

	if db.used != 0 || db.artifacts != 0 || len(bs.Blobs) != 0 {
		t.Errorf("used=%d artifacts=%d blobs=%d; want all released", db.used, db.artifacts, len(bs.Blobs))
	}
}

func TestDeleteReleasesQuotaAndEmitsEvent(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	chart := createHelmChart(t, sampleChartYAML)

	if code := upload(h, chart); code != http.StatusCreated {
		t.Fatalf("upload status = %d", code)
	}

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/charts/mychart-1.0.0.tgz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota after delete used=%d artifacts=%d, want 0/0", db.used, db.artifacts)
	}

	if len(bs.Blobs) != 0 {
		t.Error("blob not removed")
	}

	evs := pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted || evs[1].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[1].Artifact(); a["name"] != "mychart" || a["version"] != "1.0.0" {
		t.Errorf("deleted artifact = %v", a)
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.charts[chartKey("mychart", "1.0.0")] = &models.HelmChart{
		ID: "chart-9", Name: "mychart", Version: "1.0.0", BlobKey: "abc", Digest: "abc", SizeBytes: 3,
	}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/charts/mychart-1.0.0.tgz", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	for _, key := range []string{"chart-9", "charts/mychart-1.0.0.tgz", "mychart-1.0.0.tgz"} {
		db.Keys = map[string]bool{key: true}

		if c := get(); c != http.StatusForbidden {
			t.Errorf("quarantined via %q: status = %d, want 403", key, c)
		}
	}
}

func TestFindChartHyphenatedVersion(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.charts[chartKey("my-chart", "1.0.0-rc1")] = &models.HelmChart{Name: "my-chart", Version: "1.0.0-rc1"}

	got, err := h.findChart(context.Background(), "repo-1", "my-chart-1.0.0-rc1.tgz")
	if err != nil || got == nil || got.Version != "1.0.0-rc1" {
		t.Fatalf("findChart = %+v, %v", got, err)
	}
}

func chartYAML(name, version string) string {
	return "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"
}

func TestUploadRejectsNonSemverVersion(t *testing.T) {
	for _, v := range []string{"'1-2.0.0'", "'1.0'", "'v1.0.0'", "'1.0.0#x'"} {
		h, db, bs, _ := newTestHandler(t)

		if code := upload(h, createHelmChart(t, chartYAML("a", v))); code != http.StatusBadRequest {
			t.Errorf("version %s: status = %d, want 400", v, code)
		}

		if len(db.charts) != 0 || len(bs.Blobs) != 0 {
			t.Errorf("version %s: persisted despite rejection", v)
		}
	}
}

func TestUploadRefusesFilenameCollision(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	// chart "a-1.0.0-x" 2.0.0 serves file a-1.0.0-x-2.0.0.tgz
	db.charts[chartKey("a-1.0.0-x", "2.0.0")] = &models.HelmChart{Name: "a-1.0.0-x", Version: "2.0.0"}

	if code := upload(h, createHelmChart(t, chartYAML("a", "1.0.0-x-2.0.0"))); code != http.StatusConflict {
		t.Fatalf("colliding upload status = %d, want 409", code)
	}

	if _, ok := db.charts[chartKey("a", "1.0.0-x-2.0.0")]; ok {
		t.Error("colliding chart was stored")
	}
}

func TestIndexURLsArePathEscaped(t *testing.T) {
	idx := buildHelmIndex([]*models.HelmChart{{Name: "a", Version: "1.0.0+b#c?d"}}, "r", "http://h")

	got := idx.Entries["a"][0].URLs[0]
	if want := "http://h/repos/r/charts/a-1.0.0+b%23c%3Fd.tgz"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}
