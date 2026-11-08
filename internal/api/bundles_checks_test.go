// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const checksBundleID = "cccccccc-0000-4000-8000-0000000000c1"

// checksDB adds the checks table and a promotion pipeline to the authorization
// matrix store.
type checksDB struct {
	*authzMockDB

	checks   map[string]*models.BundleCheck // by name; one bundle
	pipeline *models.PromotionPipeline
}

func (d *checksDB) UpsertBundleCheck(_ context.Context, c *models.BundleCheck) error {
	c.UpdatedAt = time.Now().UTC()
	d.checks[c.Name] = c

	return nil
}

func (d *checksDB) ListBundleChecks(context.Context, string) ([]*models.BundleCheck, error) {
	out := make([]*models.BundleCheck, 0, len(d.checks))
	for _, c := range d.checks {
		out = append(out, c)
	}

	return out, nil
}

func (d *checksDB) GetPromotionPipelineByID(context.Context, string) (*models.PromotionPipeline, error) {
	return d.pipeline, nil
}

// newChecksEnv returns a handler over a sealed bundle b/1 owned by "owner" with
// one item per repository in itemRepos.
func newChecksEnv(t *testing.T, itemRepos ...string) (*checksDB, *BundlesHandler, *recordingPublisher) {
	t.Helper()

	base := newAuthzDB()
	db := &checksDB{authzMockDB: base, checks: map[string]*models.BundleCheck{}}

	pipe := authzPipeID
	bundle := &models.ReleaseBundle{
		ID: checksBundleID, Name: "b", Version: "1", State: models.BundleStateSealed,
		PipelineID: &pipe, CreatedBy: "owner", CreatorID: "owner-id",
	}
	base.bundles["b/1"] = bundle

	for _, id := range itemRepos {
		base.items[bundle.ID] = append(base.items[bundle.ID], &models.ReleaseBundleItem{SourceRepoID: id, SHA256: strings.Repeat("a", 64)})
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	pub := &recordingPublisher{}

	return db, NewBundlesHandler(db, log, auth.NewEvaluator(db, log, false)).WithPublisher(pub), pub
}

const checksPath = "/api/v1/bundles/b/1/checks"

func reportBody(name, status string) string {
	return `{"name":"` + name + `","status":"` + status + `","summary":"s","details_url":"https://ci.example.com/1"}`
}

func TestReportCheckPermissions(t *testing.T) {
	tests := []struct {
		name   string
		repos  []string
		grants map[string][]string
		user   string
		want   int
	}{
		{"write on the only source", []string{authzSrcID}, map[string][]string{"src": {"write"}}, "u1", http.StatusOK},
		{
			"write on every source",
			[]string{authzSrcID, authzOtherID},
			map[string][]string{"src": {"write"}, "other": {"write"}},
			"u2",
			http.StatusOK,
		},
		{
			"write on some sources",
			[]string{authzSrcID, authzOtherID},
			map[string][]string{"src": {"read", "write"}, "other": {"read"}},
			"u3",
			http.StatusForbidden,
		},
		{"read only", []string{authzSrcID}, map[string][]string{"src": {"read"}}, "u4", http.StatusForbidden},
		{"write on an unrelated repository", []string{authzSrcID}, map[string][]string{"other": {"write"}}, "u5", http.StatusNotFound},
		{"no access", []string{authzSrcID}, nil, "u6", http.StatusNotFound},
		{"owner without write", []string{authzSrcID}, nil, "owner", http.StatusForbidden},
		{"write but the bundle has no items", nil, map[string][]string{"src": {"write"}}, "u7", http.StatusNotFound},
		{"manage without write", []string{authzSrcID}, map[string][]string{"src": {"read", "manage"}}, "u8", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, h, pub := newChecksEnv(t, tt.repos...)

			for filter, actions := range tt.grants {
				db.grant(tt.user+"-id", filter, actions...)
			}

			rw := authzUser(tt.user).do(t, h, http.MethodPost, checksPath, reportBody("scan", "success"))
			expectStatus(t, tt.name, rw, tt.want)

			wantEvents, wantChecks := 0, 0
			if tt.want == http.StatusOK {
				wantEvents, wantChecks = 1, 1
			}

			if len(db.checks) != wantChecks || len(pub.all()) != wantEvents {
				t.Errorf("stored %d checks and %d events, want %d and %d", len(db.checks), len(pub.all()), wantChecks, wantEvents)
			}
		})
	}
}

func TestReportCheckAdminOnEmptyBundle(t *testing.T) {
	db, h, pub := newChecksEnv(t)

	expectStatus(t, "admin", authzAdmin().do(t, h, http.MethodPost, checksPath, reportBody("scan", "success")), http.StatusOK)

	if len(db.checks) != 1 {
		t.Fatalf("checks = %d", len(db.checks))
	}

	events := pub.all()
	if len(events) != 1 || events[0].repoID != "" || events[0].event != webhook.EventBundleCheckUpdated {
		t.Errorf("events = %+v, want one global bundle.check_updated", events)
	}
}

func TestReportCheckUpsertsAndAnnouncesOnce(t *testing.T) {
	db, h, pub := newChecksEnv(t, authzSrcID)
	db.grant("ci-id", "src", "write")
	ci := authzUser("ci")

	expectStatus(t, "pending", ci.do(t, h, http.MethodPost, checksPath, reportBody("conforma", "pending")), http.StatusOK)

	rw := ci.do(t, h, http.MethodPost, checksPath, reportBody("conforma", "success"))
	expectStatus(t, "success", rw, http.StatusOK)

	var got map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if got["name"] != "conforma" || got["status"] != "success" || got["reported_by"] != "ci" ||
		got["details_url"] != "https://ci.example.com/1" || got["summary"] != "s" {
		t.Errorf("response = %v", got)
	}

	if v, ok := got["expires_at"]; !ok || v != nil {
		t.Errorf("expires_at = %v, want null", v)
	}

	if len(db.checks) != 1 {
		t.Errorf("a second report of the same name must replace the first, have %d", len(db.checks))
	}

	events := pub.all()
	if len(events) != 2 {
		t.Fatalf("events = %d, want one per report", len(events))
	}

	last := events[1]
	check, _ := last.payload["check"].(map[string]any)

	if last.event != webhook.EventBundleCheckUpdated || last.repoID != authzSrcID || check["status"] != "success" {
		t.Errorf("last event = %+v", last)
	}

	list := ci.do(t, h, http.MethodGet, checksPath, "")
	expectStatus(t, "list", list, http.StatusOK)

	var listed struct {
		Checks []map[string]any `json:"checks"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Checks) != 1 {
		t.Errorf("list = %s (%v)", list.Body.String(), err)
	}
}

func TestReportCheckValidation(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"expiry in the future", `{"name":"a","status":"success","expires_at":"` + future + `"}`, http.StatusOK},
		{"minimal", `{"name":"a","status":"error"}`, http.StatusOK},
		{"missing name", `{"status":"success"}`, http.StatusBadRequest},
		{"unknown status", `{"name":"a","status":"passed"}`, http.StatusBadRequest},
		{"expiry in the past", `{"name":"a","status":"success","expires_at":"` + past + `"}`, http.StatusBadRequest},
		{"relative details url", `{"name":"a","status":"success","details_url":"/run/1"}`, http.StatusBadRequest},
		{"javascript details url", `{"name":"a","status":"success","details_url":"javascript:alert(1)"}`, http.StatusBadRequest},
		{"summary too long", `{"name":"a","status":"success","summary":"` + strings.Repeat("x", 2001) + `"}`, http.StatusBadRequest},
		{"not json", `nope`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, h, _ := newChecksEnv(t, authzSrcID)

			expectStatus(t, tt.name, authzAdmin().do(t, h, http.MethodPost, checksPath, tt.body), tt.want)

			if (tt.want == http.StatusOK) != (len(db.checks) == 1) {
				t.Errorf("stored checks = %d for status %d", len(db.checks), tt.want)
			}
		})
	}
}

func TestReportCheckRefusesArchivedAndUnknownBundles(t *testing.T) {
	db, h, _ := newChecksEnv(t, authzSrcID)
	db.bundles["b/1"].State = models.BundleStateArchived

	expectStatus(t, "archived", authzAdmin().do(t, h, http.MethodPost, checksPath, reportBody("a", "success")), http.StatusConflict)
	expectStatus(
		t,
		"unknown",
		authzAdmin().do(t, h, http.MethodPost, "/api/v1/bundles/nope/1/checks", reportBody("a", "success")),
		http.StatusNotFound,
	)
}

func TestListChecksNeedsVisibility(t *testing.T) {
	db, h, _ := newChecksEnv(t, authzSrcID)
	db.grant("r-id", "src", "read")

	expectStatus(t, "reader", authzUser("r").do(t, h, http.MethodGet, checksPath, ""), http.StatusOK)
	expectStatus(t, "stranger", authzUser("x").do(t, h, http.MethodGet, checksPath, ""), http.StatusNotFound)
}

func TestPromotionRequiresChecks(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)

	check := func(name string, status models.BundleCheckStatus, exp *time.Time) *models.BundleCheck {
		return &models.BundleCheck{Name: name, Status: status, ExpiresAt: exp}
	}

	tests := []struct {
		name      string
		stage     models.PipelineStage
		reported  []*models.BundleCheck
		wantBlock bool
		wantText  []string
	}{
		{"no required checks", models.PipelineStage{Name: "prod"}, nil, false, nil},
		{
			"all green", models.PipelineStage{Name: "prod", RequiredChecks: []string{"a", "b"}},
			[]*models.BundleCheck{check("a", models.BundleCheckSuccess, nil), check("b", models.BundleCheckSuccess, &future)}, false, nil,
		},
		{
			"missing", models.PipelineStage{Name: "prod", RequiredChecks: []string{"a", "b"}},
			[]*models.BundleCheck{check("a", models.BundleCheckSuccess, nil)}, true, []string{"missing: b"},
		},
		{
			"failed", models.PipelineStage{Name: "prod", RequiredChecks: []string{"a"}},
			[]*models.BundleCheck{check("a", models.BundleCheckFailure, nil)}, true, []string{"failed: a"},
		},
		{
			"pending", models.PipelineStage{Name: "prod", RequiredChecks: []string{"a"}},
			[]*models.BundleCheck{check("a", models.BundleCheckPending, nil)}, true, []string{"pending: a"},
		},
		{
			"expired", models.PipelineStage{Name: "prod", RequiredChecks: []string{"a"}},
			[]*models.BundleCheck{check("a", models.BundleCheckSuccess, &past)}, true, []string{"expired: a"},
		},
		{
			"unrequired failure does not block", models.PipelineStage{Name: "prod", RequiredChecks: []string{"a"}},
			[]*models.BundleCheck{check("a", models.BundleCheckSuccess, nil), check("extra", models.BundleCheckFailure, nil)}, false, nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, h, _ := newChecksEnv(t, authzSrcID)
			db.pipeline = &models.PromotionPipeline{ID: authzPipeID, Stages: []models.PipelineStage{{Name: "dev"}, tt.stage}}

			for _, c := range tt.reported {
				db.checks[c.Name] = c
			}

			fail := h.validatePromotionStage(t.Context(), db.bundles["b/1"], &models.Repository{ID: authzTargetID}, "prod")

			if !tt.wantBlock {
				if fail != nil {
					t.Fatalf("promotion refused: %+v", fail)
				}

				return
			}

			if fail == nil || fail.status != http.StatusUnprocessableEntity || fail.code != "CHECKS_REQUIRED" {
				t.Fatalf("failure = %+v, want 422 CHECKS_REQUIRED", fail)
			}

			for _, s := range tt.wantText {
				if !strings.Contains(fail.msg, s) {
					t.Errorf("message %q lacks %q", fail.msg, s)
				}
			}
		})
	}
}

func TestValidateStagesRequiredChecks(t *testing.T) {
	_, h, _ := newChecksEnv(t, authzSrcID)

	tests := []struct {
		name     string
		required []string
		valid    bool
	}{
		{"distinct", []string{"a", "b"}, true},
		{"none", nil, true},
		{"duplicate", []string{"a", "a"}, false},
		{"blank name", []string{" "}, false},
	}

	for _, tt := range tests {
		msg, err := h.validateStages(t.Context(), []models.PipelineStage{{Name: "prod", RequiredChecks: tt.required}})
		if err != nil {
			t.Fatal(err)
		}

		if (msg == "") != tt.valid {
			t.Errorf("%s: message = %q", tt.name, msg)
		}
	}
}
