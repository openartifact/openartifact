// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/promote"
	"github.com/openartifact/openartifact/internal/quota"
)

func TestWritePromoteErrorMapsEveryFailure(t *testing.T) {
	db := &bundlesScansMockDB{}
	log, ev := newBundlesScansEnv(t, db)
	h := NewBundlesHandler(db, log, ev)
	bundle := &models.ReleaseBundle{Name: "shop", Version: "1"}
	item := func(err error) error {
		return &promote.ItemError{SHA256: strings.Repeat("a", 64), Path: "p/x.jar", Err: err}
	}

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantInBody string
	}{
		{"immutable release", item(&promote.ConflictError{Format: "maven", Name: "g:a", Version: "1.0", Immutable: true}), http.StatusConflict, "immutable_release", "p/x.jar"},
		{"different content", item(&promote.ConflictError{Format: "maven", Name: "g:a", Version: "1.0-SNAPSHOT"}), http.StatusConflict, "artifact_conflict", "g:a"},
		{"quota", item(quota.ErrExceeded), http.StatusInsufficientStorage, "quota_exceeded", ""},
		{"quarantined", item(promote.ErrQuarantined), http.StatusForbidden, "ARTIFACT_QUARANTINED", "quarantined"},
		{"license", item(&promote.LicenseBlockedError{Licenses: []string{"GPL-3.0"}, Violations: []licensepolicy.Violation{{Action: "block"}}}), http.StatusForbidden, "LICENSE_BLOCKED", "GPL-3.0"},
		{"missing source artifact", item(promote.ErrItemNotFound), http.StatusConflict, "artifact_missing", "no longer exists"},
		{"concurrent change", promote.ErrStageChanged, http.StatusConflict, "conflict", "concurrent"},
		{"not sealed", promote.ErrBundleState, http.StatusConflict, "conflict", "concurrent"},
		{"empty", promote.ErrEmpty, http.StatusConflict, "conflict", "no items"},
		{"docker not indexed", item(promote.ErrDockerNotIndexed), http.StatusConflict, "conflict", "retry later"},
		{"docker tag changed", item(promote.ErrDockerTagChanged), http.StatusConflict, "conflict", "retry later"},
		{"format mismatch", item(promote.ErrFormatMismatch), http.StatusBadRequest, "validation_failed", "different formats"},
		{"not hosted", promote.ErrNotHosted, http.StatusBadRequest, "validation_failed", "hosted"},
		{"same repository", item(promote.ErrSameRepository), http.StatusBadRequest, "validation_failed", "source repository"},
		{"unsupported format", item(promote.ErrUnsupportedFormat), http.StatusBadRequest, "validation_failed", "not supported"},
		{"concurrent insert", fmt.Errorf("copy: %w", &pgconn.PgError{Code: "23505"}), http.StatusConflict, "artifact_conflict", "retry"},
		{"unexpected", errors.New("pq: connection refused to 10.0.0.1"), http.StatusInternalServerError, "internal_error", "promotion failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw := httptest.NewRecorder()
			h.writePromoteError(rw, bundle, tt.err)

			if rw.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (%s)", rw.Code, tt.wantStatus, rw.Body.String())
			}

			var body ErrorResponse
			if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", rw.Body.String(), err)
			}

			if body.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Code, tt.wantCode)
			}

			if !strings.Contains(body.Error, tt.wantInBody) {
				t.Errorf("message %q lacks %q", body.Error, tt.wantInBody)
			}

			if strings.Contains(body.Error, "10.0.0.1") || strings.Contains(body.Error, "pq:") {
				t.Errorf("message %q leaks the internal error", body.Error)
			}
		})
	}
}

func TestBundlePromoteRejectsNonHostedTarget(t *testing.T) {
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

	// The mock's repositories have no repo_type, so they are not hosted: the
	// promotion is refused before anything is copied or recorded.
	rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/b/1/promote", `{"to_stage":"prod","target_repo":"target"}`)
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "validation_failed") {
		t.Fatalf("status = %d body %s, want 400 validation_failed", rw.Code, rw.Body.String())
	}

	if db.createPromotionCalled || db.updateBundleCalled {
		t.Error("a refused promotion was recorded")
	}
}
