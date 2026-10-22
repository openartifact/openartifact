// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func npmDo(t *testing.T, router http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w
}

func manifestOf(t *testing.T, v *models.NPMVersion) map[string]any {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal(v.ManifestJSON, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}

	return m
}

// FMTA-3: `npm deprecate` PUTs the whole packument with no attachments; the
// deprecation lands on the stored version and the packument ETag moves.
func TestNPMDeprecate_UpdatesStoredVersion(t *testing.T) {
	db := newMockDatabase()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}
	router := setupUploadRouter(db, newMockBlobStore())

	if w := npmDo(t, router, http.MethodPut, "/repos/myrepo/lodash", buildPublishBody(t, "lodash", "1.0.0")); w.Code != http.StatusCreated {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	pkg, _ := db.GetNPMPackageByName(context.Background(), "repo-1", "lodash")
	ver, _ := db.GetNPMVersion(context.Background(), pkg.ID, "1.0.0")
	manifest := manifestOf(t, ver)

	// Packument as the CLI sends it: stored manifests, edited, no _attachments.
	_ = db.UpdateNPMPackageLatestVersion(context.Background(), pkg.ID, "1.0.0") // clears the stale flag

	manifest["deprecated"] = "use 2"
	packument, _ := json.Marshal(map[string]any{
		"name":      "lodash",
		"_id":       "lodash",
		"_rev":      "1-abc",
		"versions":  map[string]any{"1.0.0": manifest},
		"dist-tags": map[string]string{"latest": "1.0.0"},
	})

	if w := npmDo(t, router, http.MethodPut, "/repos/myrepo/lodash", packument); w.Code/100 != 2 {
		t.Fatalf("deprecate = %d: %s", w.Code, w.Body.String())
	}

	ver, _ = db.GetNPMVersion(context.Background(), pkg.ID, "1.0.0")
	if got := manifestOf(t, ver)["deprecated"]; got != "use 2" {
		t.Fatalf("deprecated = %v, want \"use 2\"", got)
	}

	if !pkg.PackumentStale {
		t.Error("packument not marked stale after deprecate")
	}

	// Removing the deprecation (empty message).
	manifest["deprecated"] = ""
	packument, _ = json.Marshal(map[string]any{"name": "lodash", "versions": map[string]any{"1.0.0": manifest}})

	if w := npmDo(t, router, http.MethodPut, "/repos/myrepo/lodash", packument); w.Code/100 != 2 {
		t.Fatalf("un-deprecate = %d: %s", w.Code, w.Body.String())
	}

	ver, _ = db.GetNPMVersion(context.Background(), pkg.ID, "1.0.0")
	if _, ok := manifestOf(t, ver)["deprecated"]; ok {
		t.Error("deprecated field still present after clearing")
	}

	// A new version without an attachment is still rejected.
	manifest2 := map[string]any{"name": "lodash", "version": "2.0.0"}
	packument, _ = json.Marshal(map[string]any{"name": "lodash", "versions": map[string]any{"2.0.0": manifest2}})

	if w := npmDo(t, router, http.MethodPut, "/repos/myrepo/lodash", packument); w.Code != http.StatusBadRequest {
		t.Errorf("new version without attachment = %d, want 400", w.Code)
	}
}

// FMTA-4: dist-tag PUT/DELETE bump the packument ETag via the stale marker.
func TestNPMDistTag_ChangesMarkPackumentStale(t *testing.T) {
	db := newMockDatabase()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}
	router := setupUploadRouter(db, newMockBlobStore())

	for _, v := range []string{"1.0.0", "2.0.0"} {
		if w := npmDo(t, router, http.MethodPut, "/repos/myrepo/lodash", buildPublishBody(t, "lodash", v)); w.Code != http.StatusCreated {
			t.Fatalf("publish %s = %d: %s", v, w.Code, w.Body.String())
		}
	}

	pkg, _ := db.GetNPMPackageByName(context.Background(), "repo-1", "lodash")

	_ = db.UpdateNPMPackageLatestVersion(context.Background(), pkg.ID, "2.0.0")

	if w := npmDo(t, router, http.MethodPut, "/repos/myrepo/-/package/lodash/dist-tags/beta", []byte(`"1.0.0"`)); w.Code != http.StatusOK {
		t.Fatalf("dist-tag put = %d: %s", w.Code, w.Body.String())
	}

	if !pkg.PackumentStale {
		t.Error("dist-tag PUT left the packument ETag unchanged")
	}

	_ = db.UpdateNPMPackageLatestVersion(context.Background(), pkg.ID, "2.0.0")

	if w := npmDo(t, router, http.MethodDelete, "/repos/myrepo/-/package/lodash/dist-tags/beta", nil); w.Code != http.StatusOK {
		t.Fatalf("dist-tag delete = %d: %s", w.Code, w.Body.String())
	}

	if !pkg.PackumentStale {
		t.Error("dist-tag DELETE left the packument ETag unchanged")
	}
}

// FMTA-13: the dist-tag body is capped.
func TestNPMDistTag_BodyCapped(t *testing.T) {
	db := newMockDatabase()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}
	router := setupUploadRouter(db, newMockBlobStore())

	_ = npmDo(t, router, http.MethodPut, "/repos/myrepo/lodash", buildPublishBody(t, "lodash", "1.0.0"))

	huge := append([]byte(`"`), bytes.Repeat([]byte("a"), 1<<20)...)
	huge = append(huge, '"')

	if w := npmDo(t, router, http.MethodPut, "/repos/myrepo/-/package/lodash/dist-tags/beta", huge); w.Code != http.StatusBadRequest {
		t.Errorf("oversized dist-tag body = %d, want 400", w.Code)
	}
}
