// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

const registrationURL = "/repos/myrepo/v3/registration/contoso.lib/index.json"

// countingDB counts registration row loads.
type countingDB struct {
	*memDB

	lists atomic.Int32
}

func (d *countingDB) ListNuGetPackageVersions(ctx context.Context, repoID, id string) ([]*models.NuGetPackage, error) {
	d.lists.Add(1)

	return d.memDB.ListNuGetPackageVersions(ctx, repoID, id)
}

func getRegistration(h *HTTPHandler, header map[string]string) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, registrationURL, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	return formattest.Do(register(h), req)
}

// A pushed package carries its dependency groups in its row, so the index is
// built without opening a single package.
func TestRegistrationIndexReadsNoPackages(t *testing.T) {
	h, db, bs, _ := newV3Handler(t)

	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		mustPush(t, h, "Contoso.Lib", v)
	}

	for _, p := range db.pkgs {
		if p.DependencyGroups == nil {
			t.Fatalf("%s %s was stored without dependency groups", p.PackageID, p.Version)
		}
	}

	cs := &countingStore{BlobStore: bs}
	h.BlobStore = cs

	w := getRegistration(h, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Other.Lib") {
		t.Fatalf("status %d, dependency groups missing: %s", w.Code, w.Body.String())
	}

	if got := cs.gets.Load(); got != 0 {
		t.Errorf("blob reads = %d, want 0", got)
	}

	if got := db.groupWrites; got != 0 {
		t.Errorf("dependency groups rewritten %d times", got)
	}
}

// Rows written before the column existed are completed on first use: each
// package is read once, its groups are stored, and later requests (even after
// the process-local caches are gone) read nothing.
func TestRegistrationIndexBackfillsLegacyRows(t *testing.T) {
	h, db, bs, _ := newV3Handler(t)

	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		mustPush(t, h, "Contoso.Lib", v)
	}

	for _, p := range db.pkgs {
		p.DependencyGroups = nil // as if published before the migration
	}

	cs := &countingStore{BlobStore: bs}
	h.BlobStore = cs

	w := getRegistration(h, nil)
	if !strings.Contains(w.Body.String(), "Other.Lib") {
		t.Fatalf("dependency groups missing: %s", w.Body.String())
	}

	if got := cs.gets.Load(); got != 3 {
		t.Fatalf("blob reads = %d, want 3 (one per package)", got)
	}

	if db.groupWrites != 3 {
		t.Fatalf("stored %d dependency groups, want 3", db.groupWrites)
	}

	// A new handler has no caches; the stored values are enough.
	h2 := NewHTTPHandler(formattest.NewLogger(t), db, cs, "http://localhost")
	if again := getRegistration(h2, nil); again.Body.String() != w.Body.String() {
		t.Error("registration differs once the groups are read from the rows")
	}

	if got := cs.gets.Load(); got != 3 {
		t.Errorf("blob reads = %d after backfill, want still 3", got)
	}
}

func TestRegistrationIndexRevalidation(t *testing.T) {
	h, db, _, _ := newV3Handler(t)
	cdb := &countingDB{memDB: db}
	h.DB = cdb

	mustPush(t, h, "Contoso.Lib", "1.0.0")

	first := getRegistration(h, nil)
	etag := first.Header().Get("ETag")

	if etag == "" || first.Header().Get("Cache-Control") == "" {
		t.Fatalf("missing validators: %v", first.Header())
	}

	if rec := getRegistration(h, map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("revalidation = %d with %d body bytes, want 304 and none", rec.Code, rec.Body.Len())
	}

	// A new version changes the validator, so a client holding the old one
	// gets the new document rather than a 304.
	mustPush(t, h, "Contoso.Lib", "2.0.0")

	updated := getRegistration(h, map[string]string{"If-None-Match": etag})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"2.0.0"`) || updated.Header().Get("ETag") == etag {
		t.Fatalf("after publish: %d etag %q", updated.Code, updated.Header().Get("ETag"))
	}

	// Deleting a version does too.
	if w := formattest.Do(register(h), formattest.Request(http.MethodDelete, "/repos/myrepo/v3/package/contoso.lib/2.0.0", nil)); w.Code >= 300 {
		t.Fatalf("delete = %d", w.Code)
	}

	deleted := getRegistration(h, nil)
	if strings.Contains(deleted.Body.String(), `"2.0.0"`) || deleted.Header().Get("ETag") != etag {
		t.Errorf("after delete: etag %q (want %q), body %s", deleted.Header().Get("ETag"), etag, deleted.Body.String())
	}
}

func TestRegistrationIndexGzip(t *testing.T) {
	h := newHandlerOnly(t)

	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0", "1.5.0"} {
		mustPush(t, h, "Contoso.Lib", v)
	}

	plain := getRegistration(h, nil)
	zipped := getRegistration(h, map[string]string{"Accept-Encoding": "gzip"})

	if plain.Header().Get("Content-Encoding") != "" || zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("encodings: plain %q, gzip %q", plain.Header().Get("Content-Encoding"), zipped.Header().Get("Content-Encoding"))
	}

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := io.ReadAll(zr); !bytes.Equal(got, plain.Body.Bytes()) {
		t.Error("decompressed registration differs from the plain one")
	}
}
