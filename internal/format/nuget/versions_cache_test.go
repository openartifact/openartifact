// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

const flatURL = "/repos/myrepo/v3/package/contoso.lib/index.json"

func getFlat(h *HTTPHandler, header map[string]string) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, flatURL, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	return formattest.Do(register(h), req)
}

func flatVersions(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var doc struct {
		Versions []string `json:"versions"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}

	return doc.Versions
}

func deleteVersion(t *testing.T, h *HTTPHandler, version string) {
	t.Helper()

	if w := formattest.Do(
		register(h),
		formattest.Request(http.MethodDelete, "/repos/myrepo/v3/package/contoso.lib/"+version, nil),
	); w.Code >= 300 {
		t.Fatalf("delete %s = %d", version, w.Code)
	}
}

// sortedVersions orders by semantic version, not by string, and puts a
// prerelease before its release.
func TestSortedVersionsOrder(t *testing.T) {
	in := []string{"1.0.10", "1.0.2", "2.0.0-beta.1", "2.0.0", "1.0.0", "not-a-version", "1.0.2-rc.1", "bad"}

	pkgs := make([]*models.NuGetPackage, len(in))
	for i, v := range in {
		pkgs[i] = &models.NuGetPackage{Version: v}
	}

	var got []string
	for _, p := range sortedVersions(pkgs) {
		got = append(got, p.Version)
	}

	want := []string{"bad", "not-a-version", "1.0.0", "1.0.2-rc.1", "1.0.2", "1.0.10", "2.0.0-beta.1", "2.0.0"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted = %v, want %v", got, want)
	}

	if in[0] != "1.0.10" {
		t.Error("input slice was reordered")
	}
}

// Revalidating a version list or registration index is answered from the stamp
// alone: no row is loaded.
func TestVersionDocumentsRevalidateFromStampOnly(t *testing.T) {
	for _, url := range []string{flatURL, registrationURL} {
		t.Run(url, func(t *testing.T) {
			h, db, _, _ := newV3Handler(t)
			cdb := &countingDB{memDB: db}
			h.DB = cdb

			mustPush(t, h, "Contoso.Lib", "1.0.0")

			get := func(header map[string]string) *httptest.ResponseRecorder {
				req := formattest.Request(http.MethodGet, url, nil)
				for k, v := range header {
					req.Header.Set(k, v)
				}

				return formattest.Do(register(h), req)
			}

			first := get(nil)
			etag := first.Header().Get("ETag")

			if first.Code != http.StatusOK || etag == "" {
				t.Fatalf("first = %d, etag %q", first.Code, etag)
			}

			lists := cdb.lists.Load()
			if lists != 1 {
				t.Fatalf("first request listed rows %d times, want 1", lists)
			}

			for range 3 {
				if rec := get(map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
					t.Fatalf("revalidation = %d with %d body bytes", rec.Code, rec.Body.Len())
				}
			}

			// A plain repeat is served from the render cache.
			if again := get(nil); again.Code != http.StatusOK || again.Body.String() != first.Body.String() {
				t.Fatal("repeat differs")
			}

			if got := cdb.lists.Load(); got != lists {
				t.Errorf("rows listed %d times after warm requests, want %d", got, lists)
			}
		})
	}
}

// The cached documents follow every change of the version set: publish and
// delete (a yank is a delete or a trash, both of which remove the live row).
func TestFlatVersionsFollowChanges(t *testing.T) {
	h := newHandlerOnly(t)

	mustPush(t, h, "Contoso.Lib", "1.0.2")
	mustPush(t, h, "Contoso.Lib", "1.0.10")

	first := getFlat(h, nil)
	if got := flatVersions(t, first); !slices.Equal(got, []string{"1.0.2", "1.0.10"}) {
		t.Fatalf("versions = %v", got)
	}

	etag := first.Header().Get("ETag")

	mustPush(t, h, "Contoso.Lib", "1.0.3")

	updated := getFlat(h, map[string]string{"If-None-Match": etag})
	if got := flatVersions(t, updated); !slices.Equal(got, []string{"1.0.2", "1.0.3", "1.0.10"}) || updated.Header().Get("ETag") == etag {
		t.Fatalf("after publish: versions %v, etag %q", got, updated.Header().Get("ETag"))
	}

	deleteVersion(t, h, "1.0.3")

	if got := flatVersions(t, getFlat(h, nil)); !slices.Equal(got, []string{"1.0.2", "1.0.10"}) {
		t.Fatalf("after delete: versions %v", got)
	}

	// Deleting every version makes the package unknown.
	deleteVersion(t, h, "1.0.2")
	deleteVersion(t, h, "1.0.10")

	if w := getFlat(h, nil); w.Code != http.StatusNotFound {
		t.Fatalf("empty package = %d, want 404", w.Code)
	}
}

// The registration index follows a delete and a republish of the same version
// (the new row has a new id, so the validator moves).
func TestRegistrationFollowsDeleteAndRepublish(t *testing.T) {
	h := newHandlerOnly(t)

	mustPush(t, h, "Contoso.Lib", "1.0.0")

	etag := getRegistration(h, nil).Header().Get("ETag")

	deleteVersion(t, h, "1.0.0")
	mustPush(t, h, "Contoso.Lib", "1.0.0")

	if rec := getRegistration(h, map[string]string{"If-None-Match": etag}); rec.Code != http.StatusOK || rec.Header().Get("ETag") == etag {
		t.Errorf("republish: %d, etag %q (was %q)", rec.Code, rec.Header().Get("ETag"), etag)
	}
}

// Gzip and identity renderings of the flat list are never mixed up, and a
// gzipped response is compressed exactly once.
func TestFlatVersionsEncodings(t *testing.T) {
	h, db, _, _ := newV3Handler(t)

	// Enough versions for the document to be worth compressing.
	want := make([]string, 0, 200)

	for i := range 200 {
		v := fmt.Sprintf("1.0.%d", i)
		want = append(want, v)
		db.pkgs = append(db.pkgs, &models.NuGetPackage{ID: "row-" + v, PackageID: "contoso.lib", Version: v})
	}

	zipped := getFlat(h, map[string]string{"Accept-Encoding": "gzip"})
	plain := getFlat(h, nil)

	if zipped.Header().Get("Content-Encoding") != "gzip" || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("encodings: gzip %q, identity %q", zipped.Header().Get("Content-Encoding"), plain.Header().Get("Content-Encoding"))
	}

	zr, err := gzip.NewReader(bytes.NewReader(zipped.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	inflated, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(inflated, plain.Body.Bytes()) {
		t.Errorf("gzip body inflates to %q (a second compression?), identity is %q", inflated, plain.Body.Bytes())
	}

	if got := flatVersions(t, plain); !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}

	zipTag := strings.TrimPrefix(zipped.Header().Get("ETag"), "W/")
	if zipTag != plain.Header().Get("ETag") || plain.Header().Get("Content-Type") != "application/json" {
		t.Errorf("validators differ: %q vs %q, type %q", zipped.Header().Get("ETag"), plain.Header().Get("ETag"), plain.Header().Get("Content-Type"))
	}
}

// Unknown packages are 404 for both documents and are never remembered as
// present.
func TestVersionDocumentsUnknownPackage(t *testing.T) {
	h := newHandlerOnly(t)

	for _, url := range []string{flatURL, registrationURL} {
		if w := formattest.Do(register(h), formattest.Request(http.MethodGet, url, nil)); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", url, w.Code)
		}
	}
}
