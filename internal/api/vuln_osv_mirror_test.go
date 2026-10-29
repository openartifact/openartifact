// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database/models"
)

type osvStatusMockDB struct {
	VulnStore

	states []models.OSVMirrorState
}

func (db *osvStatusMockDB) ListOSVMirrorStates(context.Context) ([]models.OSVMirrorState, error) {
	return db.states, nil
}

func serveOSVStatus(t *testing.T, db *osvStatusMockDB, cfg config.OSVMirrorConfig) responses.OSVMirrorStatusResponse {
	t.Helper()

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	vh := NewVulnHandler(db, log, ev, nil).WithOSVMirror(cfg)

	rw := serveAs(vh.RegisterOverviewRoutes, http.MethodGet, "/api/v1/vuln/osv-mirror", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
	}

	var resp responses.OSVMirrorStatusResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp
}

func TestOSVMirrorStatusDisabledStillCarriesAttribution(t *testing.T) {
	resp := serveOSVStatus(t, &osvStatusMockDB{}, config.OSVMirrorConfig{})

	if resp.Enabled || len(resp.Ecosystems) != 0 || resp.Attribution.License != "CC-BY-4.0" || resp.Attribution.URL != "https://osv.dev" {
		t.Fatalf("disabled status = %+v", resp)
	}
}

func TestOSVMirrorStatusReportsEcosystemsAndHidesCredentials(t *testing.T) {
	hw := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	db := &osvStatusMockDB{states: []models.OSVMirrorState{
		{Ecosystem: "npm", HighWater: &hw, Advisories: 42, LastImportSeconds: 1.5, LastError: "boom"},
	}}

	resp := serveOSVStatus(t, db, config.OSVMirrorConfig{
		Enabled: true, BaseURL: "https://user:secret@osv.internal/data", Ecosystems: []string{"npm", "PyPI"}, Interval: 24 * time.Hour,
	})

	if resp.SourceURL != "https://osv.internal/data" || resp.IntervalSeconds != 86400 || len(resp.Ecosystems) != 2 {
		t.Fatalf("status = %+v", resp)
	}

	npm, pypi := resp.Ecosystems[0], resp.Ecosystems[1]
	if !npm.Populated || npm.Advisories != 42 || npm.HighWater == nil || *npm.HighWater != "2024-05-01T10:00:00Z" ||
		npm.LastError == nil || *npm.LastError != "boom" {
		t.Fatalf("npm = %+v", npm)
	}

	if pypi.Populated || pypi.HighWater != nil || pypi.LastError != nil {
		t.Fatalf("an ecosystem never imported must report not populated: %+v", pypi)
	}
}
