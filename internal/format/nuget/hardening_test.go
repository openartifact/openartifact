// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/storage"
)

func pushBytes(t *testing.T, h *HTTPHandler, nupkg []byte) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("package", "x.nupkg")
	_, _ = fw.Write(nupkg)
	_ = mw.Close()

	req := formattest.Request(http.MethodPut, "/repos/myrepo/v3/package", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return formattest.Do(register(h), req)
}

func zipOf(t *testing.T, entries ...[2]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write([]byte(e[1]))
	}

	_ = zw.Close()

	return buf.Bytes()
}

func TestPushBodyTooLarge(t *testing.T) {
	h, db, bs, _ := newV3Handler(t)
	h.maxPush = 1024

	// Over the whole-body cap (limit + form overhead).
	w := pushBytes(t, h, bytes.Repeat([]byte("x"), 4*pushFormOverhead))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body.String())
	}

	// Within the body cap but over the per-package limit.
	w = pushBytes(t, h, bytes.Repeat([]byte("x"), 2048))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body.String())
	}

	if len(db.pkgs) != 0 || len(bs.Blobs) != 0 {
		t.Fatalf("oversized push stored something: pkgs=%d blobs=%d", len(db.pkgs), len(bs.Blobs))
	}
}

func TestPushRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"a/b", "has space", `q"uote`, strings.Repeat("a", 101), "../x"} {
		h, db, bs, _ := newV3Handler(t)

		w := pushBytes(t, h, zipOf(t, [2]string{"p.nuspec", nuspecFor(id, "1.0.0", "d")}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("push id %q = %d, want 400", id, w.Code)
		}

		if len(db.pkgs) != 0 || len(bs.Blobs) != 0 {
			t.Errorf("push id %q stored something", id)
		}
	}
}

// Only the root-level nuspec decides identity; a nested one listed first is
// package content.
func TestPushUsesRootNuspecOnly(t *testing.T) {
	h, db, _, _ := newV3Handler(t)

	decoy := [2]string{"content/decoy.nuspec", nuspecFor("Decoy.Pkg", "9.9.9", "decoy")}

	// No root nuspec: rejected.
	if w := pushBytes(t, h, zipOf(t, decoy)); w.Code != http.StatusBadRequest {
		t.Fatalf("nested-only nuspec = %d, want 400", w.Code)
	}

	// Decoy first, real root nuspec second: the root one wins.
	rootNuspec := [2]string{"Real.Pkg.nuspec", nuspecFor("Real.Pkg", "1.0.0", "real")}
	if w := pushBytes(t, h, zipOf(t, decoy, rootNuspec)); w.Code != http.StatusCreated {
		t.Fatalf("push = %d: %s", w.Code, w.Body.String())
	}

	if len(db.pkgs) != 1 || db.pkgs[0].PackageID != "real.pkg" || db.pkgs[0].Description != "real" {
		t.Fatalf("stored %+v, want real.pkg from the root nuspec", db.pkgs)
	}

	// Two root nuspecs are ambiguous: rejected.
	other := [2]string{"Other.nuspec", nuspecFor("Other.Pkg", "1.0.0", "other")}
	if w := pushBytes(t, h, zipOf(t, rootNuspec, other)); w.Code != http.StatusBadRequest {
		t.Fatalf("two root nuspecs = %d, want 400", w.Code)
	}
}

type countingStore struct {
	storage.BlobStore

	gets atomic.Int32
}

func (c *countingStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	c.gets.Add(1)

	return c.BlobStore.Get(ctx, id) //nolint:wrapcheck // test double
}

// The registration index reads each package once, then serves its dependency
// groups from the cache.
func TestRegistrationIndexCachesDependencyGroups(t *testing.T) {
	h, _, bs, _ := newV3Handler(t)
	cs := &countingStore{BlobStore: bs}
	h.BlobStore = cs

	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		mustPush(t, h, "Contoso.Lib", v)
	}

	url := "/repos/myrepo/v3/registration/contoso.lib/index.json"

	for range 3 {
		w := formattest.Do(register(h), formattest.Request(http.MethodGet, url, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}

		if !strings.Contains(w.Body.String(), "Other.Lib") {
			t.Fatalf("dependency groups missing: %s", w.Body.String())
		}
	}

	if got := cs.gets.Load(); got != 3 {
		t.Fatalf("blob reads = %d, want 3 (one per package, none on repeats)", got)
	}
}

func TestDepCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newDepCache(2)
	c.put("a", nil)
	c.put("b", nil)

	if _, ok := c.get("a"); !ok { // touch a: b is now the oldest
		t.Fatal("a missing")
	}

	c.put("c", nil)

	if _, ok := c.get("b"); ok {
		t.Error("b should have been evicted")
	}

	if _, ok := c.get("a"); !ok {
		t.Error("a should have been kept")
	}

	if _, ok := c.get("c"); !ok {
		t.Error("c missing")
	}

	if len(c.items) != 2 || c.order.Len() != 2 {
		t.Errorf("cache holds %d/%d entries, want 2", len(c.items), c.order.Len())
	}
}
