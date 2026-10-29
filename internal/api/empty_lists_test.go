// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// emptyListsDBStore is what emptyListsDB is used as.
type emptyListsDBStore interface {
	BackupStore
	BundlesStore
	CleanupStore
	LicenseStore
	SBOMStore
	SecretsStore
	VulnStore
	auth.EvaluatorStore
}

// emptyListsDB answers every list a handler reads with an empty, non-nil slice,
// the way the Postgres layer does for a repository with no rows.
type emptyListsDB struct {
	emptyListsDBStore
}

func (*emptyListsDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: "repo-1", Name: name, Format: "npm"}, nil
}

func (*emptyListsDB) ListBackupJobs(context.Context, int) ([]*models.BackupJob, error) {
	return []*models.BackupJob{}, nil
}

func (*emptyListsDB) ListCleanupPolicies(context.Context, string) ([]*models.CleanupPolicy, error) {
	return []*models.CleanupPolicy{}, nil
}

func (*emptyListsDB) ListPromotionPipelines(context.Context) ([]*models.PromotionPipeline, error) {
	return []*models.PromotionPipeline{}, nil
}

func (*emptyListsDB) ListReleaseBundles(context.Context, string, string, int, int) ([]*models.ReleaseBundle, int, error) {
	return []*models.ReleaseBundle{}, 0, nil
}

func (*emptyListsDB) ListSBOMReports(context.Context, string) ([]*models.SBOMReport, error) {
	return []*models.SBOMReport{}, nil
}

func (*emptyListsDB) ListLicenseScanResults(context.Context, string) ([]*models.LicenseScanResult, error) {
	return []*models.LicenseScanResult{}, nil
}

func (*emptyListsDB) ListSecretScanResults(context.Context, string, bool) ([]*models.SecretScanResult, error) {
	return []*models.SecretScanResult{}, nil
}

func (*emptyListsDB) ListVulnScanResults(context.Context, string, bool) ([]*models.VulnScanResult, error) {
	return []*models.VulnScanResult{}, nil
}

// A list with no rows is an empty JSON array under its key, never null: the
// published schema says array and clients iterate over it.
func TestEmptyListsSerialiseAsArrays(t *testing.T) {
	db := &emptyListsDB{}
	log := newFixTestLogger(t)
	ev := auth.NewEvaluator(db, log, false)

	backups := NewBackupHandler(db, log, ev, nil)
	bundles := NewBundlesHandler(db, log, ev)
	cleanup := NewCleanupHandler(db, log, ev, nil)
	sboms := NewSBOMHandler(db, log, ev, nil, nil)
	licenses := NewLicenseHandler(db, log, ev, nil, nil)
	secretScans := NewSecretsHandler(db, log, nil)
	vulns := NewVulnHandler(db, log, ev, nil)

	cases := []struct {
		name    string
		pattern string
		handler http.HandlerFunc
		path    string
		key     string
	}{
		{"backups", "/backups", backups.handleListBackups, "/backups", "backups"},
		{"cleanup policies", "/repos/{name}/cleanup", cleanup.handleList, "/repos/r/cleanup", "policies"},
		{"pipelines", "/pipelines", bundles.handleListPipelines, "/pipelines", "pipelines"},
		{"bundles", "/bundles", bundles.handleListBundles, "/bundles", "bundles"},
		{"sbom reports", "/repos/{name}/sbom", sboms.handleList, "/repos/r/sbom", "reports"},
		{"license scans", "/repos/{name}/license", licenses.handleListScans, "/repos/r/license", "scans"},
		{"secret scans", "/repos/{name}/secrets", secretScans.handleListScans, "/repos/r/secrets", "scans"},
		{"vulnerability scans", "/repos/{name}/vuln", vulns.handleListScans, "/repos/r/vuln", "scans"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := chi.NewRouter()
			r.Get(tc.pattern, tc.handler)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, nil)
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

			rw := httptest.NewRecorder()
			r.ServeHTTP(rw, req)

			if rw.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rw.Code, rw.Body.String())
			}

			var body map[string]json.RawMessage
			if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %q: %v", rw.Body.String(), err)
			}

			if got := string(body[tc.key]); got != "[]" {
				t.Fatalf("%s = %s, want [] (body %s)", tc.key, got, rw.Body.String())
			}
		})
	}
}

// A pipeline stored without stages (null in the database) still answers
// "stages": [] as the schema promises.
func TestPipelineWithoutStagesSerialisesAsEmptyArray(t *testing.T) {
	for name, p := range map[string]*models.PromotionPipeline{
		"nil stages":         {Name: "p"},
		"null stages in raw": {Name: "p", StagesRaw: []byte("null")},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(pipelineToMap(p))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var body map[string]json.RawMessage
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got := string(body["stages"]); got != "[]" {
				t.Fatalf("stages = %s, want []", got)
			}
		})
	}
}
