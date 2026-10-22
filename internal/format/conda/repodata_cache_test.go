// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// listCountingDB counts full package listings.
type listCountingDB struct {
	*condaDB

	lists atomic.Int32
}

func (d *listCountingDB) ListCondaPackages(ctx context.Context, repoID, platform string) ([]*models.CondaPackage, error) {
	d.lists.Add(1)

	return d.condaDB.ListCondaPackages(ctx, repoID, platform)
}

func newCountingHandler(t *testing.T) (*HTTPHandler, *listCountingDB) {
	t.Helper()

	db := &listCountingDB{condaDB: &condaDB{DB: formattest.NewDB()}}

	return NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost"), db
}

func addPkg(db *condaDB, i int) {
	db.pkgs = append(db.pkgs, &models.CondaPackage{
		ID: "id-" + strconv.Itoa(i), Name: "pkg" + strconv.Itoa(i), Version: "1.0", BuildString: "py_0",
		Platform: "noarch", Extension: models.CondaExtConda, SHA256: strings.Repeat("a", 64), SizeBytes: 10,
	})
}

func getRepodataReq(h *HTTPHandler, header map[string]string) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, "/repos/myrepo/noarch/repodata.json", nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	return formattest.Do(h.RegisterRepoRoutes, req)
}

func TestRepodataRenderedOncePerPackageSet(t *testing.T) {
	h, db := newCountingHandler(t)
	addPkg(db.condaDB, 1)

	a, b := getRepodataReq(h, nil), getRepodataReq(h, nil)
	if a.Code != http.StatusOK || !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Fatalf("status %d, bodies equal = %v", a.Code, bytes.Equal(a.Body.Bytes(), b.Body.Bytes()))
	}

	if n := db.lists.Load(); n != 1 {
		t.Errorf("package listing read %d times for 2 requests, want 1", n)
	}
}

func TestRepodataFollowsUploadsAndDeletes(t *testing.T) {
	h, db := newCountingHandler(t)
	addPkg(db.condaDB, 1)

	first := getRepodataReq(h, nil)
	etag := first.Header().Get("ETag")

	addPkg(db.condaDB, 2)

	// A client holding the old validator must get the new document.
	added := getRepodataReq(h, map[string]string{"If-None-Match": etag})
	if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), "pkg2-1.0-py_0.conda") {
		t.Fatalf("after upload: status %d, body %s", added.Code, added.Body.String())
	}

	if added.Header().Get("ETag") == etag {
		t.Error("ETag unchanged after upload")
	}

	if err := db.DeleteCondaPackage(context.Background(), "id-2"); err != nil {
		t.Fatal(err)
	}

	removed := getRepodataReq(h, nil)
	if strings.Contains(removed.Body.String(), "pkg2-1.0-py_0.conda") {
		t.Error("deleted package still in repodata")
	}
}

func TestRepodataRevalidationSkipsListing(t *testing.T) {
	h, db := newCountingHandler(t)
	addPkg(db.condaDB, 1)

	etag := getRepodataReq(h, nil).Header().Get("ETag")
	db.lists.Store(0)

	rec := getRepodataReq(h, map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}

	if rec.Body.Len() != 0 || db.lists.Load() != 0 {
		t.Errorf("304 body %d bytes, listings %d; want 0/0", rec.Body.Len(), db.lists.Load())
	}
}

func TestRepodataGzip(t *testing.T) {
	h, db := newCountingHandler(t)

	for i := range 50 {
		addPkg(db.condaDB, i)
	}

	plain := getRepodataReq(h, nil)
	zipped := getRepodataReq(h, map[string]string{"Accept-Encoding": "gzip"})

	if zipped.Header().Get("Content-Encoding") != "gzip" || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding plain=%q gzip=%q", plain.Header().Get("Content-Encoding"), zipped.Header().Get("Content-Encoding"))
	}

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, plain.Body.Bytes()) {
		t.Error("decompressed body differs")
	}
}
