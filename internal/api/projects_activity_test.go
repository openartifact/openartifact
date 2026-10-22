// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	activityRepoID  = "11111111-1111-4111-8111-111111111111"
	activityLastID  = "33333333-3333-4333-8333-333333333333"
	activityIP      = "203.0.113.9"
	activityMessage = "repository r created in project Acme"
)

func activityCreated() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

// activityMock returns acme with a two-entry feed: a repository event carrying
// a client address, and a generic request row without message or actor.
func activityMock(more bool) *projectsMockDB {
	ip, repo, actorType := activityIP, activityRepoID, "user"

	db := newProjectsMock()
	db.listAudit = &models.AuditPage{Logs: []*models.AuditLog{{
		ID:           "22222222-2222-4222-8222-222222222222",
		Action:       "project.repository_created",
		RepositoryID: &repo,
		ActorType:    &actorType,
		DetailsJSON:  json.RawMessage(`{"message":"` + activityMessage + `","actor":"owner"}`),
		IPAddress:    &ip,
		CreatedAt:    activityCreated(),
	}, {
		ID:          activityLastID,
		Action:      "http.request",
		DetailsJSON: json.RawMessage(`{"method":"POST","path":"/x","status":200}`),
		CreatedAt:   activityCreated(),
	}}}

	if more {
		db.listAudit.Next = &models.AuditCursor{CreatedAt: activityCreated(), ID: activityLastID}
	}

	return db
}

func TestProjectsActivityMapsEntriesAndScopesQuery(t *testing.T) {
	db := activityMock(true)
	code, body := projectJSON(t, newProjectsRouter(t, db), uView1, http.MethodGet, "/api/v1/projects/acme/activity?actor=own&action=project.", "")

	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if db.listParams.ProjectID != testAcmeID || db.listParams.Limit != activityDefaultPerPage || !db.listParams.NoTotal || db.listParams.After != nil ||
		db.listParams.Actor != "own" || db.listParams.Action != "project." {
		t.Errorf("query = %+v", db.listParams)
	}

	if body["has_more"] != true || body["next_cursor"] == nil || body["next_cursor"] == "" {
		t.Errorf("paging = has_more %v, next_cursor %v", body["has_more"], body["next_cursor"])
	}

	if _, ok := body["total"]; ok {
		t.Errorf("activity must not carry a total: %v", body)
	}
}

func TestProjectsActivityEntryFields(t *testing.T) {
	_, body := projectJSON(t, newProjectsRouter(t, activityMock(false)), uView1, http.MethodGet, "/api/v1/projects/acme/activity", "")

	data := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("entries = %v", data)
	}

	first := data[0].(map[string]any)
	if first["actor"] != "owner" || first["message"] != activityMessage || first["repository_id"] != activityRepoID || first["actor_type"] != "user" {
		t.Errorf("entry = %v", first)
	}

	for _, e := range data {
		if _, leaked := e.(map[string]any)["ip"]; leaked {
			t.Errorf("entry carries the client address: %v", e)
		}
	}

	// Rows without a message or actor still render.
	if second := data[1].(map[string]any); second["actor"] != "" || second["message"] != "" {
		t.Errorf("entry without message = %v", second)
	}
}

func TestProjectsActivityLastPageHasNoCursor(t *testing.T) {
	_, body := projectJSON(t, newProjectsRouter(t, activityMock(false)), uView1, http.MethodGet, "/api/v1/projects/acme/activity", "")
	if body["has_more"] != false {
		t.Errorf("has_more = %v", body["has_more"])
	}

	if _, ok := body["next_cursor"]; ok {
		t.Errorf("next_cursor present on the last page: %v", body)
	}
}

func TestProjectsActivityCursorAndPerPageReachTheQuery(t *testing.T) {
	db := activityMock(false)
	cursor := encodeAuditCursor(&models.AuditCursor{CreatedAt: activityCreated(), ID: activityLastID})

	code, body := projectJSON(t, newProjectsRouter(t, db), uView1, http.MethodGet, "/api/v1/projects/acme/activity?per_page=500&cursor="+cursor, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if db.listParams.Limit != activityMaxPerPage || db.listParams.After == nil || db.listParams.After.ID != activityLastID || !db.listParams.After.CreatedAt.Equal(activityCreated()) {
		t.Errorf("query = %+v (after %+v)", db.listParams, db.listParams.After)
	}
}

func TestProjectsActivityRejectsMalformedCursor(t *testing.T) {
	code, _ := projectJSON(t, newProjectsRouter(t, activityMock(false)), uView1, http.MethodGet, "/api/v1/projects/acme/activity?cursor=%21%21", "")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestProjectsActivityEmptyFeedIsAnEmptyList(t *testing.T) {
	_, body := projectJSON(t, newProjectsRouter(t, newProjectsMock()), uView1, http.MethodGet, "/api/v1/projects/acme/activity", "")
	if data, ok := body["data"].([]any); !ok || len(data) != 0 {
		t.Errorf("data = %#v, want []", body["data"])
	}
}

func TestProjectsActivityHidesTokenEventsBelowMaintainer(t *testing.T) {
	for user, hidden := range map[string]bool{uView1: true, uDev1: true, uMaint1: false, uOwner1: false} {
		db := activityMock(false)
		if code, body := projectJSON(t, newProjectsRouter(t, db), user, http.MethodGet, "/api/v1/projects/acme/activity", ""); code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %v", user, code, body)
		}

		if db.listParams.HideProjectTokenEvents != hidden {
			t.Errorf("%s: HideProjectTokenEvents = %v, want %v", user, db.listParams.HideProjectTokenEvents, hidden)
		}
	}
}
