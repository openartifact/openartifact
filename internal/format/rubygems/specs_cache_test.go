// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

type specsCountingDB struct {
	*gemsDB

	lists atomic.Int32
}

func (d *specsCountingDB) ListRubyGemSpecs(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
	d.lists.Add(1)

	return d.gemsDB.ListRubyGemSpecs(ctx, repoID)
}

func newSpecsHandler(t *testing.T) (*HTTPHandler, *specsCountingDB) {
	t.Helper()

	db := &specsCountingDB{gemsDB: &gemsDB{DB: formattest.NewDB()}}

	return NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost"), db
}

func getSpecs(h *HTTPHandler, path, inm string) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, "/repos/myrepo/"+path, nil)
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}

	return formattest.Do(h.RegisterRepoRoutes, req)
}

func TestSpecsFollowUploadYankAndDelete(t *testing.T) {
	h, db := newSpecsHandler(t)
	db.gems = []*models.RubyGem{{Name: "demo", Version: "1.0.0", Platform: "ruby"}}

	first := getSpecs(h, "specs.4.8.gz", "")
	etag := first.Header().Get("ETag")

	db.gems = append(db.gems, &models.RubyGem{Name: "demo", Version: "1.1.0", Platform: "ruby"})

	added := getSpecs(h, "specs.4.8.gz", etag)
	if added.Code != http.StatusOK {
		t.Fatalf("status after publish = %d, want 200 for a stale validator", added.Code)
	}

	if !bytes.Contains(gunzip(t, added), []byte("1.1.0")) {
		t.Error("new gem missing from specs")
	}

	db.gems[1].Yanked = true

	yanked := getSpecs(h, "specs.4.8.gz", "")
	if bytes.Contains(gunzip(t, yanked), []byte("1.1.0")) {
		t.Error("yanked gem still listed")
	}

	if yanked.Header().Get("ETag") != etag {
		t.Error("ETag differs although the listed set is back to the original")
	}
}

func TestSpecsAreRenderedOncePerChange(t *testing.T) {
	h, db := newSpecsHandler(t)
	db.gems = []*models.RubyGem{{Name: "demo", Version: "1.0.0", Platform: "ruby"}}

	a, b := getSpecs(h, "latest_specs.4.8.gz", ""), getSpecs(h, "latest_specs.4.8.gz", "")
	if !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) || a.Code != http.StatusOK {
		t.Fatal("repeated request returned a different body")
	}

	if n := db.lists.Load(); n != 1 {
		t.Errorf("gem listing read %d times for 2 requests, want 1", n)
	}

	// The three indexes share a stamp but not a cache entry.
	if etagA, etagB := a.Header().Get("ETag"), getSpecs(h, "specs.4.8.gz", "").Header().Get("ETag"); etagA == etagB {
		t.Error("different specs indexes share an ETag")
	}
}

func TestSpecsRevalidationSkipsListing(t *testing.T) {
	h, db := newSpecsHandler(t)
	db.gems = []*models.RubyGem{{Name: "demo", Version: "1.0.0", Platform: "ruby"}}

	etag := getSpecs(h, "prerelease_specs.4.8.gz", "").Header().Get("ETag")
	db.lists.Store(0)

	rec := getSpecs(h, "prerelease_specs.4.8.gz", etag)
	if rec.Code != http.StatusNotModified || db.lists.Load() != 0 {
		t.Errorf("status %d, listings %d; want 304 and 0", rec.Code, db.lists.Load())
	}
}

func TestSpecsAreNeverContentEncoded(t *testing.T) {
	h, db := newSpecsHandler(t)

	for i := range 200 {
		db.gems = append(db.gems, &models.RubyGem{Name: "gem" + strconv.Itoa(i), Version: "1.0.0", Platform: "ruby"})
	}

	req := formattest.Request(http.MethodGet, "/repos/myrepo/specs.4.8.gz", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	rec := formattest.Do(h.RegisterRepoRoutes, req)
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding = %q; the specs file is already a .gz", enc)
	}

	if !bytes.Contains(gunzip(t, rec), []byte("gem199")) {
		t.Error("specs body is not a single gzip stream of the index")
	}
}
