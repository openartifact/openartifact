// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

type indexCountingDB struct {
	*cargoDB

	lists atomic.Int32
}

func (d *indexCountingDB) ListCargoIndexVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	d.lists.Add(1)

	return d.cargoDB.ListCargoIndexVersions(ctx, repoID, name)
}

func newIndexHandler(t *testing.T) (*HTTPHandler, *indexCountingDB) {
	t.Helper()

	db := &indexCountingDB{cargoDB: &cargoDB{DB: formattest.NewDB()}}
	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost")
	h.SetPublisher(&formattest.Publisher{})

	return h, db
}

func getIndex(h *HTTPHandler, inm string) (int, http.Header, []byte) {
	req := formattest.Request(http.MethodGet, repoPrefix+"/index/de/mo/demo", nil)
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}

	rec := formattest.Do(h.RegisterRepoRoutes, req)

	return rec.Code, rec.Header(), rec.Body.Bytes()
}

func TestSparseIndexRenderedOncePerChange(t *testing.T) {
	h, db := newIndexHandler(t)

	if w := publish(t, h, "demo", "0.1.0", []byte("crate-1")); w.Code != http.StatusOK {
		t.Fatalf("publish = %d", w.Code)
	}

	_, hdr, first := getIndex(h, "")
	etag := hdr.Get("ETag")

	_, _, second := getIndex(h, "")
	if !bytes.Equal(first, second) || db.lists.Load() != 1 {
		t.Fatalf("2 identical requests read the versions %d times (want 1)", db.lists.Load())
	}

	// A new version changes the validator and the body.
	if w := publish(t, h, "demo", "0.2.0", []byte("crate-2")); w.Code != http.StatusOK {
		t.Fatalf("publish = %d", w.Code)
	}

	code, hdr, body := getIndex(h, etag)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"vers":"0.2.0"`)) || hdr.Get("ETag") == etag {
		t.Fatalf("after publish: status %d etag %q body %s", code, hdr.Get("ETag"), body)
	}

	// Yank flips the entry and the validator; unyank restores both.
	call(h, http.MethodDelete, "/api/v1/crates/demo/0.2.0/yank", nil)

	_, yHdr, yBody := getIndex(h, "")
	if !bytes.Contains(yBody, []byte(`"yanked":true`)) || yHdr.Get("ETag") == hdr.Get("ETag") {
		t.Errorf("yank not reflected: etag %q body %s", yHdr.Get("ETag"), yBody)
	}

	call(h, http.MethodPut, "/api/v1/crates/demo/0.2.0/unyank", nil)

	if _, uHdr, _ := getIndex(h, ""); uHdr.Get("ETag") != hdr.Get("ETag") {
		t.Error("ETag differs although the index is back to its previous content")
	}
}

func TestSparseIndexRevalidationSkipsVersionListing(t *testing.T) {
	h, db := newIndexHandler(t)

	publish(t, h, "demo", "0.1.0", []byte("crate-1"))

	_, hdr, _ := getIndex(h, "")
	db.lists.Store(0)

	code, _, body := getIndex(h, hdr.Get("ETag"))
	if code != http.StatusNotModified || len(body) != 0 || db.lists.Load() != 0 {
		t.Errorf("status %d, body %d bytes, listings %d; want 304/0/0", code, len(body), db.lists.Load())
	}
}

func TestSparseIndexUnknownCrateIs404WithoutValidators(t *testing.T) {
	h, _ := newIndexHandler(t)

	code, hdr, _ := getIndex(h, "")
	if code != http.StatusNotFound || hdr.Get("ETag") != "" {
		t.Errorf("status %d etag %q, want 404 without validator", code, hdr.Get("ETag"))
	}
}
