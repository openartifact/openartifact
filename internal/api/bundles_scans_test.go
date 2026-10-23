// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	testRepoID   = "11111111-1111-1111-1111-111111111111"
	testPolicyID = "22222222-2222-2222-2222-222222222222"
	testScanID   = "33333333-3333-3333-3333-333333333333"
)

type bundlesScansMockDB struct {
	database.BaseMockDB

	policy *models.LicensePolicy

	bundle   *models.ReleaseBundle
	pipeline *models.PromotionPipeline

	props map[string]string

	deleteLicensePolicyCalled bool
	deleteVulnErr             error
	deleteVulnCalled          bool
	sealCalled                bool
	createPromotionCalled     bool
	updateBundleCalled        bool
	setPropsCalls             int
	setPropsReplace           bool
	setPropsArg               map[string]string
	sbomBlob                  string
	deleteSBOMCalled          bool
}

func (db *bundlesScansMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: testRepoID, Name: name, Format: "raw"}, nil
}

func (db *bundlesScansMockDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	return &models.Repository{ID: id, Name: "target", Format: "raw"}, nil
}

func (db *bundlesScansMockDB) GetLicensePolicy(_ context.Context, _ string) (*models.LicensePolicy, error) {
	return db.policy, nil
}

func (db *bundlesScansMockDB) UpdateLicensePolicy(_ context.Context, _ *models.LicensePolicy) error {
	return nil
}

func (db *bundlesScansMockDB) DeleteLicensePolicy(_ context.Context, _ string) error {
	db.deleteLicensePolicyCalled = true
	return nil
}

func (db *bundlesScansMockDB) DeleteVulnScanResult(_ context.Context, _, _ string) error {
	db.deleteVulnCalled = true
	return db.deleteVulnErr
}

func (db *bundlesScansMockDB) GetReleaseBundle(_ context.Context, _, _ string) (*models.ReleaseBundle, error) {
	return db.bundle, nil
}

func (db *bundlesScansMockDB) GetPromotionPipelineByID(_ context.Context, _ string) (*models.PromotionPipeline, error) {
	return db.pipeline, nil
}

func (db *bundlesScansMockDB) ListBundleItems(_ context.Context, _ string) ([]*models.ReleaseBundleItem, error) {
	return []*models.ReleaseBundleItem{{SHA256: strings.Repeat("a", 64)}}, nil
}

func (db *bundlesScansMockDB) SealReleaseBundle(_ context.Context, _, _ string, _ time.Time, _ *models.BundleSignature) (bool, error) {
	db.sealCalled = true
	return true, nil
}

func (db *bundlesScansMockDB) CreateBundlePromotion(_ context.Context, _ *models.BundlePromotion) error {
	db.createPromotionCalled = true
	return nil
}

func (db *bundlesScansMockDB) UpdateReleaseBundle(_ context.Context, _ *models.ReleaseBundle) error {
	db.updateBundleCalled = true
	return nil
}

func (db *bundlesScansMockDB) SetArtifactProperties(_ context.Context, _, _ string, props map[string]string, replace bool) error {
	db.setPropsCalls++
	db.setPropsReplace = replace
	db.setPropsArg = props

	return nil
}

func (db *bundlesScansMockDB) ListArtifactProperties(_ context.Context, _, _ string) (map[string]string, error) {
	return db.props, nil
}

func (db *bundlesScansMockDB) DeleteSBOMReportInRepo(_ context.Context, _, _ string) (string, error) {
	db.deleteSBOMCalled = true
	return db.sbomBlob, nil
}

func newBundlesScansEnv(t *testing.T, db *bundlesScansMockDB) (*logger.Logger, *auth.Evaluator) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	return log, auth.NewEvaluator(db, log, false)
}

// serveAs routes a request through a chi router with an admin (legacy) principal.
func serveAs(register func(chi.Router), method, path, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := &auth.Principal{Username: "admin", IsLegacy: true}
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	})
	register(router)

	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func TestLicenseRepoScopedPolicyEndpointsRejectGlobalPolicies(t *testing.T) {
	db := &bundlesScansMockDB{policy: &models.LicensePolicy{ID: testPolicyID, RepositoryID: nil, Name: "global", Action: "warn"}}
	log, ev := newBundlesScansEnv(t, db)
	h := NewLicenseHandler(db, log, ev, nil, nil)

	path := "/api/v1/repositories/repo/license/policies/" + testPolicyID

	if rw := serveAs(h.RegisterRoutes, http.MethodPut, path, `{"name":"x"}`); rw.Code != http.StatusNotFound {
		t.Errorf("PUT global policy via repo URL: status = %d, want 404", rw.Code)
	}

	if rw := serveAs(h.RegisterRoutes, http.MethodDelete, path, ""); rw.Code != http.StatusNotFound {
		t.Errorf("DELETE global policy via repo URL: status = %d, want 404", rw.Code)
	}

	if db.deleteLicensePolicyCalled {
		t.Error("global policy was deleted through a repository-scoped endpoint")
	}
}

func TestLicensePolicyActionValidation(t *testing.T) {
	repoID := testRepoID
	db := &bundlesScansMockDB{policy: &models.LicensePolicy{ID: testPolicyID, RepositoryID: &repoID, Name: "p", Action: "warn"}}
	log, ev := newBundlesScansEnv(t, db)
	h := NewLicenseHandler(db, log, ev, nil, nil)

	rw := serveAs(h.RegisterRoutes, http.MethodPut, "/api/v1/repositories/repo/license/policies/"+testPolicyID, `{"action":"explode"}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("update with invalid action: status = %d, want 400", rw.Code)
	}

	rw = serveAs(h.RegisterRoutes, http.MethodPost, "/api/v1/repositories/repo/license/policies", `{"name":"p","action":"explode"}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("create with invalid action: status = %d, want 400", rw.Code)
	}

	rw = serveAs(h.RegisterRoutes, http.MethodPost, "/api/v1/admin/license/policies", `{"name":"p","action":"explode"}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("create global with invalid action: status = %d, want 400", rw.Code)
	}

	rw = serveAs(h.RegisterRoutes, http.MethodPut, "/api/v1/repositories/repo/license/policies/"+testPolicyID, `{"action":"block"}`)
	if rw.Code != http.StatusOK {
		t.Errorf("update with valid action: status = %d, want 200 (%s)", rw.Code, rw.Body.String())
	}
}

func TestVulnDeleteScanActuallyDeletes(t *testing.T) {
	db := &bundlesScansMockDB{}
	log, ev := newBundlesScansEnv(t, db)
	h := NewVulnHandler(db, log, ev, nil)
	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

	rw := serveAs(register, http.MethodDelete, "/api/v1/repositories/repo/vuln/scans/"+testScanID, "")
	if rw.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rw.Code, rw.Body.String())
	}

	if !db.deleteVulnCalled {
		t.Error("DELETE returned 204 without deleting the scan result")
	}

	db.deleteVulnErr = database.ErrVulnScanNotFound

	rw = serveAs(register, http.MethodDelete, "/api/v1/repositories/repo/vuln/scans/"+testScanID, "")
	if rw.Code != http.StatusNotFound {
		t.Errorf("missing scan: status = %d, want 404", rw.Code)
	}
}

func TestBundlePromoteIsNotImplemented(t *testing.T) {
	pipelineID := "44444444-4444-4444-4444-444444444444"
	db := &bundlesScansMockDB{
		bundle: &models.ReleaseBundle{
			ID: "55555555-5555-5555-5555-555555555555", Name: "b", Version: "1",
			State: models.BundleStateSealed, PipelineID: &pipelineID,
		},
		pipeline: &models.PromotionPipeline{ID: pipelineID, Stages: []models.PipelineStage{{Name: "dev"}, {Name: "prod"}}},
	}
	log, ev := newBundlesScansEnv(t, db)
	h := NewBundlesHandler(db, log, ev)

	rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/b/1/promote", `{"to_stage":"prod","target_repo":"target"}`)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (%s)", rw.Code, rw.Body.String())
	}

	if db.createPromotionCalled || db.updateBundleCalled {
		t.Error("promotion recorded success/state although nothing was copied")
	}

	rw = serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/b/1/promote", `{"to_stage":"nope","target_repo":"target"}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("unknown stage: status = %d, want 400", rw.Code)
	}
}

func TestBundleSealRejectsMalformedBody(t *testing.T) {
	db := &bundlesScansMockDB{
		bundle: &models.ReleaseBundle{ID: "55555555-5555-5555-5555-555555555555", Name: "b", Version: "1", State: models.BundleStateDraft},
	}
	log, ev := newBundlesScansEnv(t, db)
	h := NewBundlesHandler(db, log, ev)

	rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/b/1/seal", `{not json`)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status = %d, want 400", rw.Code)
	}

	if db.sealCalled {
		t.Error("bundle sealed despite malformed body")
	}

	rw = serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/b/1/seal", "")
	if rw.Code != http.StatusOK || !db.sealCalled {
		t.Errorf("empty body: status = %d sealCalled = %v, want 200/true (%s)", rw.Code, db.sealCalled, rw.Body.String())
	}
}

func TestPropertiesBodyValidationAndTransaction(t *testing.T) {
	db := &bundlesScansMockDB{}
	log, _ := newBundlesScansEnv(t, db)
	h := NewPropertiesHandler(log, db, nil)
	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }
	path := "/api/v1/repositories/repo/artifacts/pkg-1.0/properties"

	rw := serveAs(register, http.MethodPut, path, `{"properties":{"a":"1","b":"2"}}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("wrapped PUT: status = %d (%s)", rw.Code, rw.Body.String())
	}

	if db.setPropsCalls != 1 || !db.setPropsReplace || len(db.setPropsArg) != 2 || db.setPropsArg["a"] != "1" {
		t.Errorf("PUT must replace in one call: calls=%d replace=%v args=%v", db.setPropsCalls, db.setPropsReplace, db.setPropsArg)
	}

	db.setPropsCalls = 0

	rw = serveAs(register, http.MethodPatch, path, `{"a":"1"}`)
	if rw.Code != http.StatusOK || db.setPropsCalls != 1 || db.setPropsReplace {
		t.Errorf("PATCH: status=%d calls=%d replace=%v", rw.Code, db.setPropsCalls, db.setPropsReplace)
	}

	db.setPropsCalls = 0

	var sb strings.Builder

	sb.WriteString("{")

	for i := 0; i <= maxPropertyCount; i++ {
		if i > 0 {
			sb.WriteString(",")
		}

		sb.WriteString(`"k` + strconv.Itoa(i) + `":"v"`)
	}

	sb.WriteString("}")

	if rw := serveAs(register, http.MethodPut, path, sb.String()); rw.Code != http.StatusBadRequest {
		t.Errorf("too many properties: status = %d, want 400", rw.Code)
	}

	big := `{"k":"` + strings.Repeat("x", maxPropertyValueLen+1) + `"}`
	if rw := serveAs(register, http.MethodPut, path, big); rw.Code != http.StatusBadRequest {
		t.Errorf("oversized value: status = %d, want 400", rw.Code)
	}

	if rw := serveAs(register, http.MethodPut, path, `{"k":5}`); rw.Code != http.StatusBadRequest {
		t.Errorf("non-string value: status = %d, want 400", rw.Code)
	}

	if db.setPropsCalls != 0 {
		t.Error("invalid property bodies reached the database")
	}
}

func TestSBOMDeleteIsRepoScoped(t *testing.T) {
	db := &bundlesScansMockDB{}
	log, ev := newBundlesScansEnv(t, db)
	h := NewSBOMHandler(db, log, ev, nil, nil)

	rw := serveAs(h.RegisterAuthed, http.MethodDelete, "/api/v1/repositories/repo/sbom/"+testScanID, "")
	if rw.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rw.Code, rw.Body.String())
	}

	if !db.deleteSBOMCalled {
		t.Error("SBOM report was not deleted through the repo-scoped query")
	}
}

func TestLicenseScanRejectsClientMetadata(t *testing.T) {
	db := &bundlesScansMockDB{props: map[string]string{"license": "MIT"}}
	log, ev := newBundlesScansEnv(t, db)
	h := NewLicenseHandler(db, log, ev, nil, nil)

	rw := serveAs(h.RegisterRoutes, http.MethodPost, "/api/v1/repositories/repo/license/scan", `{"artifact_id":"pkg","metadata":{"license":"GPL-3.0"}}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("client metadata: status = %d, want 400 (%s)", rw.Code, rw.Body.String())
	}

	rw = serveAs(h.RegisterRoutes, http.MethodPost, "/api/v1/repositories/repo/license/scan", `{"artifact_id":""}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("empty artifact_id: status = %d, want 400", rw.Code)
	}
}
