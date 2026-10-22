// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// stampDB serves index.yaml from the chart map of helmDB and counts how often
// the full chart listing is read.
type stampDB struct {
	*helmDB

	lists atomic.Int32
}

func (d *stampDB) HelmIndexStamp(_ context.Context, _ string) (string, error) {
	ids := make([]string, 0, len(d.charts))
	for _, c := range d.charts {
		ids = append(ids, c.Name+"@"+c.Version+"#"+c.ID)
	}

	slices.Sort(ids)

	return strings.Join(ids, ","), nil
}

func (d *stampDB) ListHelmCharts(_ context.Context, _ string) ([]*models.HelmChart, error) {
	d.lists.Add(1)

	out := make([]*models.HelmChart, 0, len(d.charts))
	for _, c := range d.charts {
		out = append(out, c)
	}

	return out, nil
}

func newStampHandler(t *testing.T) (*HTTPHandler, *stampDB) {
	t.Helper()

	db := &stampDB{helmDB: newHelmDB()}
	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost")

	return h, db
}

func (d *stampDB) addChart(name, version string) {
	d.charts[chartKey(name, version)] = &models.HelmChart{
		ID: name + "-" + version, Name: name, Version: version, APIVersion: "v2", Digest: fmt.Sprintf("%064x", len(d.charts)+1),
	}
}

func getIndex(h *HTTPHandler, header map[string]string) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, "/repos/myrepo/index.yaml", nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	return formattest.Do(h.RegisterRepoRoutes, req)
}

func TestIndexIsRenderedOncePerChartSet(t *testing.T) {
	h, db := newStampHandler(t)
	db.addChart("web", "1.0.0")

	first := getIndex(h, nil)
	second := getIndex(h, nil)

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("status = %d, %d", first.Code, second.Code)
	}

	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Error("identical chart sets rendered differently")
	}

	if n := db.lists.Load(); n != 1 {
		t.Errorf("chart listing read %d times for 2 requests, want 1", n)
	}
}

func TestIndexChangesWithChartSet(t *testing.T) {
	h, db := newStampHandler(t)
	db.addChart("web", "1.0.0")

	before := getIndex(h, nil)
	etagBefore := before.Header().Get("ETag")

	db.addChart("web", "1.1.0")

	added := getIndex(h, map[string]string{"If-None-Match": etagBefore})
	if added.Code != http.StatusOK {
		t.Fatalf("status after upload = %d, want 200 (stale validator must not 304)", added.Code)
	}

	if !strings.Contains(added.Body.String(), "version: 1.1.0") {
		t.Errorf("new chart missing from index:\n%s", added.Body.String())
	}

	if added.Header().Get("ETag") == etagBefore {
		t.Error("ETag unchanged after an upload")
	}

	delete(db.charts, chartKey("web", "1.1.0"))

	removed := getIndex(h, nil)
	if strings.Contains(removed.Body.String(), "version: 1.1.0") {
		t.Errorf("deleted chart still listed:\n%s", removed.Body.String())
	}

	if removed.Header().Get("ETag") != etagBefore {
		t.Error("ETag differs although the chart set is back to the original")
	}
}

func TestIndexRevalidationReadsNoCharts(t *testing.T) {
	h, db := newStampHandler(t)
	db.addChart("web", "1.0.0")

	etag := getIndex(h, nil).Header().Get("ETag")
	db.lists.Store(0)

	rec := getIndex(h, map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}

	if n := db.lists.Load(); n != 0 {
		t.Errorf("304 read the chart listing %d times, want 0", n)
	}
}

func TestIndexGzipOnlyWhenAccepted(t *testing.T) {
	h, db := newStampHandler(t)

	for i := range 40 {
		db.addChart(fmt.Sprintf("chart-%d", i), "1.0.0")
	}

	plain := getIndex(h, nil)
	if plain.Header().Get("Content-Encoding") != "" {
		t.Fatal("response compressed without Accept-Encoding")
	}

	zipped := getIndex(h, map[string]string{"Accept-Encoding": "gzip"})
	if zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", zipped.Header().Get("Content-Encoding"))
	}

	if vary := strings.Join(zipped.Header().Values("Vary"), ","); !strings.Contains(vary, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding", vary)
	}

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, plain.Body.Bytes()) {
		t.Error("decompressed body differs from the plain body")
	}

	if zipped.Body.Len() >= plain.Body.Len() {
		t.Errorf("gzip body %d bytes is not smaller than plain %d", zipped.Body.Len(), plain.Body.Len())
	}
}
