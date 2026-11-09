// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestPropertySearchListsArtifacts checks the answer shape: artifact_ids as before, and
// artifacts with name, version and format only for ids that name a stored file.
func TestPropertySearchListsArtifacts(t *testing.T) {
	str := func(s string) *string { return &s }

	db := &bundlesScansMockDB{searchMatches: []models.PropertyMatch{
		{ArtifactID: "0b7e5c2a-3f1d-4c8e-9a6b-1d2e3f4a5b6c", Name: str("landing/v2/index.html"), Version: str(""), Format: str("raw")},
		{ArtifactID: "build-482"},
	}}
	log, _ := newBundlesScansEnv(t, db)
	h := NewPropertiesHandler(log, db, nil)
	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

	rw := serveAs(register, http.MethodGet, "/api/v1/repositories/repo/artifacts/search?key=status&value=approved", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	var got struct {
		ArtifactIDs []string                     `json:"artifact_ids"`
		Artifacts   []map[string]json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if len(got.ArtifactIDs) != 2 || got.ArtifactIDs[0] != "0b7e5c2a-3f1d-4c8e-9a6b-1d2e3f4a5b6c" || got.ArtifactIDs[1] != "build-482" {
		t.Errorf("artifact_ids = %v, want the ids unchanged", got.ArtifactIDs)
	}

	if len(got.Artifacts) != 2 {
		t.Fatalf("artifacts = %v, want 2 entries", got.Artifacts)
	}

	resolved, external := got.Artifacts[0], got.Artifacts[1]
	if string(resolved["name"]) != `"landing/v2/index.html"` || string(resolved["version"]) != `""` ||
		string(resolved["format"]) != `"raw"` {
		t.Errorf("resolved entry = %s, want name, an empty version and format", rw.Body.String())
	}

	if len(external) != 1 || string(external["artifact_id"]) != `"build-482"` {
		t.Errorf("unresolved entry = %v, want only artifact_id", external)
	}

	db.searchMatches = nil

	rw = serveAs(register, http.MethodGet, "/api/v1/repositories/repo/artifacts/search?key=status&value=none", "")
	if rw.Code != http.StatusOK || rw.Body.String() != "{\"artifact_ids\":[],\"artifacts\":[]}\n" {
		t.Errorf("no match = %d %q, want empty lists, not null", rw.Code, rw.Body.String())
	}
}
