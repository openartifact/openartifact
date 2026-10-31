// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// A settings change is recorded against the repository with the names of the
// settings it touched and none of their values (the upstream password is one).
func TestRepositoryUpdateAuditsChangedSettingNamesOnly(t *testing.T) {
	rows := recordAudit(t)

	db := &updateCapturingDB{repo: &models.Repository{
		ID: "r1", Name: "px", Format: "maven", RepoType: "proxy", UpstreamURL: "https://93.184.216.34/maven",
	}}
	db.targets = adminTargets()

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	req := adminRequest(&db.repoCreateMockDB, http.MethodPatch, "/api/v1/repositories/px",
		`{"description":"mirror","upstream_password":"hunter2-secret","cache_ttl_seconds":60}`)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "px")

	rw := httptest.NewRecorder()
	h.handleUpdate(rw, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))

	if rw.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rw.Code, rw.Body.String())
	}

	got := wantAudit(t, rows(), audit.ActionRepositoryUpdated)[0]
	if got.RepositoryID == nil || *got.RepositoryID != "r1" {
		t.Errorf("repository id = %v, want r1", got.RepositoryID)
	}

	wantMessage(t, got, "description", "upstream password", "cache TTL")

	for _, leaked := range []string{"hunter2-secret", "mirror"} {
		if strings.Contains(string(got.DetailsJSON), leaked) {
			t.Errorf("audit details leak a setting value %q: %s", leaked, got.DetailsJSON)
		}
	}
}

func TestRepositoryUpdateChangedFields(t *testing.T) {
	d, f := "x", false

	cases := []struct {
		name string
		body updateRepositoryBody
		want string
	}{
		{"nothing", updateRepositoryBody{}, ""},
		{"two", updateRepositoryBody{Description: &d, Public: &f}, "description,public"},
		{"group members", updateRepositoryBody{MemberRepos: &[]string{}}, "group members"},
	}

	for _, c := range cases {
		if got := strings.Join(c.body.changedFields(), ","); got != c.want {
			t.Errorf("%s: changedFields = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestQuarantineIsAuditedAgainstItsRepository(t *testing.T) {
	rows := recordAudit(t)
	repo := &models.Repository{ID: "r1", Name: "libs"}
	req := adminRequest(nil, http.MethodPut, "/x", "")

	auditQuarantine(req, repo, true, "vulnerability", "7f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11", "lib", "1.0")
	auditQuarantine(req, repo, false, "secret", "lib@2.0", "", "")

	got := wantAudit(t, rows(), audit.ActionArtifactQuarantined, audit.ActionArtifactReleased)

	for _, r := range got {
		if r.RepositoryID == nil || *r.RepositoryID != "r1" {
			t.Errorf("%s repository id = %v", r.Action, r.RepositoryID)
		}
	}

	wantMessage(t, got[0], "quarantined", "vulnerability", "lib 1.0")
	wantMessage(t, got[1], "released", "secret", "lib@2.0")

	if strings.Contains(string(got[0].DetailsJSON), "7f1c2f0e") {
		t.Errorf("a package row id must not be shown as a name: %s", got[0].DetailsJSON)
	}
}

// Promoting a bundle is announced on the target (bundle.promoted, "in") and on
// every other repository the items were copied from (artifact.promoted, "out").
func TestPromotionIsAuditedOnSourcesAndTarget(t *testing.T) {
	rows := recordAudit(t)
	target := &models.Repository{ID: "t", Name: "prod"}
	bundle := &models.ReleaseBundle{ID: "b1", Name: "app", Version: "1.0"}
	items := []*models.ReleaseBundleItem{
		{SourceRepoID: "s2"}, {SourceRepoID: "s1"}, {SourceRepoID: "s2"}, {SourceRepoID: "t"},
	}

	auditPromotedOut(adminRequest(nil, http.MethodPost, "/x", ""), bundle, target, items, "prod")

	got := wantAudit(t, rows(), audit.ActionArtifactPromoted, audit.ActionArtifactPromoted)

	if *got[0].RepositoryID != "s1" || *got[1].RepositoryID != "s2" {
		t.Errorf("sources = %s, %s, want s1, s2 (the target is not a source of itself)", *got[0].RepositoryID, *got[1].RepositoryID)
	}

	wantMessage(t, got[1], "2 items", "app 1.0", "prod")
}
