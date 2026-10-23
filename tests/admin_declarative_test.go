// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

const (
	declAdminID  = "5d1a7a0e-52d1-5a51-9a4e-0c1f6f7a9b01"
	declUserID   = "5d1a7a0e-52d1-5a51-9a4e-0c1f6f7a9b02"
	declGroupID  = "5d1a7a0e-52d1-5a51-9a4e-0c1f6f7a9b03"
	declTargetID = "5d1a7a0e-52d1-5a51-9a4e-0c1f6f7a9b04"
	declMissing  = "5d1a7a0e-52d1-5a51-9a4e-0c1f6f7a9b99"
)

// declarativeDB layers in-memory ownership markers and the per-target
// assignment listing over the shared MockDatabase.
type declarativeDB struct {
	*MockDatabase

	markers map[[2]string]models.DeclarativeObject
}

func (d *declarativeDB) ListDeclarativeObjects(context.Context) ([]models.DeclarativeObject, error) {
	out := make([]models.DeclarativeObject, 0, len(d.markers))
	for _, m := range d.markers {
		out = append(out, m)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}

		return out[i].Name < out[j].Name
	})

	return out, nil
}

func (d *declarativeDB) UpsertDeclarativeObjects(_ context.Context, items []models.DeclarativeObject) ([]models.DeclarativeObject, error) {
	out := make([]models.DeclarativeObject, 0, len(items))

	for _, it := range items {
		it.AppliedAt = time.Now()
		d.markers[[2]string{it.Kind, it.Name}] = it
		out = append(out, it)
	}

	return out, nil
}

func (d *declarativeDB) DeleteDeclarativeObject(_ context.Context, kind, name string) (bool, error) {
	_, ok := d.markers[[2]string{kind, name}]
	delete(d.markers, [2]string{kind, name})

	return ok, nil
}

func (d *declarativeDB) ListPermissionAssignmentsForTarget(_ context.Context, targetID string) ([]models.PermissionAssignmentView, error) {
	var out []models.PermissionAssignmentView

	for _, a := range d.permissionAssignments {
		if a.PermissionTargetID != targetID {
			continue
		}

		v := models.PermissionAssignmentView{PrincipalType: a.PrincipalType, PrincipalID: a.PrincipalID}
		if u := d.authUsers[a.PrincipalID]; a.PrincipalType == "user" && u != nil {
			v.PrincipalName = u.Username
		}

		if g := d.authGroups[a.PrincipalID]; a.PrincipalType == "group" && g != nil {
			v.PrincipalName = g.Name
		}

		out = append(out, v)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].PrincipalType != out[j].PrincipalType {
			return out[i].PrincipalType < out[j].PrincipalType
		}

		return out[i].PrincipalName < out[j].PrincipalName
	})

	return out, nil
}

// setupDeclarativeRouter seeds an admin, a plain user, a group and a permission
// target assigned to both, and mounts both handlers. Requests run as the admin
// unless the test sets X-Test-User to the plain user.
func setupDeclarativeRouter(t *testing.T) chi.Router {
	t.Helper()

	db := &declarativeDB{MockDatabase: newMockDatabase(), markers: map[[2]string]models.DeclarativeObject{}}
	ctx := context.Background()
	now := time.Now()

	for _, u := range []*models.User{
		{ID: declAdminID, Username: "decladmin", CreatedAt: now, UpdatedAt: now},
		{ID: declUserID, Username: "alice", CreatedAt: now, UpdatedAt: now},
	} {
		if _, err := db.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}

	if _, err := db.CreateGroup(ctx, &models.Group{ID: declGroupID, Name: "devs", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if _, err := db.CreatePermissionTarget(ctx, &models.PermissionTarget{ID: declTargetID, Name: "readers", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read"}}); err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	for _, a := range []models.PermissionAssignment{
		{PermissionTargetID: "981948bd-de8f-5c65-b90b-23d72cf37aab", PrincipalType: "user", PrincipalID: declAdminID},
		{PermissionTargetID: declTargetID, PrincipalType: "user", PrincipalID: declUserID},
		{PermissionTargetID: declTargetID, PrincipalType: "group", PrincipalID: declGroupID},
	} {
		if err := db.CreatePermissionAssignment(ctx, &a); err != nil {
			t.Fatalf("CreatePermissionAssignment: %v", err)
		}
	}

	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, name := declAdminID, "decladmin"
			if r.Header.Get("X-Test-User") == "alice" {
				uid, name = declUserID, "alice"
			}

			p := &auth.Principal{UserID: &uid, Username: name, Credential: auth.CredentialSession}
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	})
	api.NewAdminDeclarativeHandler(db, log, eval).Register(router)
	api.NewAdminPermissionsHandler(db, log, eval).Register(router)

	return router
}

func declDo(router chi.Router, method, target, body, user string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if user != "" {
		req.Header.Set("X-Test-User", user)
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

type declItems struct {
	Items []map[string]any `json:"items"`
}

func TestAdminDeclarative_RoundTrip(t *testing.T) {
	router := setupDeclarativeRouter(t)

	put := `{"items":[{"kind":"Repository","name":"maven/a","object_id":"id-1","applied_hash":"h1"},{"kind":"User","name":"bob","object_id":"id-2"}]}`
	if rw := declDo(router, http.MethodPut, "/api/v1/admin/declarative", put, ""); rw.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rw.Code, rw.Body.String())
	}

	// Upsert again with a changed id: same key, replaced value.
	again := `{"items":[{"kind":"User","name":"bob","object_id":"id-3","applied_hash":"h2"}]}`
	if rw := declDo(router, http.MethodPut, "/api/v1/admin/declarative", again, ""); rw.Code != http.StatusOK {
		t.Fatalf("put again: %d %s", rw.Code, rw.Body.String())
	}

	rw := declDo(router, http.MethodGet, "/api/v1/admin/declarative", "", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rw.Code, rw.Body.String())
	}

	var got declItems
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.Items) != 2 {
		t.Fatalf("items = %v, want 2", got.Items)
	}

	if got.Items[0]["kind"] != "Repository" || got.Items[0]["name"] != "maven/a" || got.Items[0]["applied_hash"] != "h1" {
		t.Errorf("first item = %v", got.Items[0])
	}

	if got.Items[1]["object_id"] != "id-3" || got.Items[1]["applied_hash"] != "h2" {
		t.Errorf("second item = %v", got.Items[1])
	}

	del := "/api/v1/admin/declarative?kind=Repository&name=" + url.QueryEscape("maven/a")
	if rw := declDo(router, http.MethodDelete, del, "", ""); rw.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rw.Code, rw.Body.String())
	}

	if rw := declDo(router, http.MethodDelete, del, "", ""); rw.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d, want 404", rw.Code)
	}
}

func TestAdminDeclarative_Validation(t *testing.T) {
	router := setupDeclarativeRouter(t)

	tooMany := `{"items":[` + strings.Repeat(`{"kind":"User","name":"x","object_id":"1"},`, 1000) + `{"kind":"User","name":"x","object_id":"1"}]}`

	tests := []struct {
		name, method, target, body string
		want                       int
	}{
		{"bad json", http.MethodPut, "/api/v1/admin/declarative", `{`, http.StatusBadRequest},
		{"unknown kind", http.MethodPut, "/api/v1/admin/declarative", `{"items":[{"kind":"Nope","name":"a","object_id":"1"}]}`, http.StatusBadRequest},
		{"empty name", http.MethodPut, "/api/v1/admin/declarative", `{"items":[{"kind":"User","name":"","object_id":"1"}]}`, http.StatusBadRequest},
		{"long name", http.MethodPut, "/api/v1/admin/declarative", `{"items":[{"kind":"User","name":"` + strings.Repeat("a", 513) + `","object_id":"1"}]}`, http.StatusBadRequest},
		{"empty object id", http.MethodPut, "/api/v1/admin/declarative", `{"items":[{"kind":"User","name":"a","object_id":""}]}`, http.StatusBadRequest},
		{"long hash", http.MethodPut, "/api/v1/admin/declarative", `{"items":[{"kind":"User","name":"a","object_id":"1","applied_hash":"` + strings.Repeat("h", 129) + `"}]}`, http.StatusBadRequest},
		{"too many items", http.MethodPut, "/api/v1/admin/declarative", tooMany, http.StatusBadRequest},
		{"delete without name", http.MethodDelete, "/api/v1/admin/declarative?kind=User", "", http.StatusBadRequest},
		{"delete unknown kind", http.MethodDelete, "/api/v1/admin/declarative?kind=Nope&name=a", "", http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rw := declDo(router, tc.method, tc.target, tc.body, "")
			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rw.Code, tc.want, rw.Body.String())
			}
		})
	}
}

func TestAdminDeclarative_NonAdminForbidden(t *testing.T) {
	router := setupDeclarativeRouter(t)

	tests := []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/admin/declarative", ""},
		{http.MethodPut, "/api/v1/admin/declarative", `{"items":[]}`},
		{http.MethodDelete, "/api/v1/admin/declarative?kind=User&name=a", ""},
		{http.MethodGet, "/api/v1/permission-targets/" + declTargetID + "/assignments", ""},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			if rw := declDo(router, tc.method, tc.target, tc.body, "alice"); rw.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rw.Code, rw.Body.String())
			}
		})
	}
}

func TestAdminPermissions_ListAssignments(t *testing.T) {
	router := setupDeclarativeRouter(t)

	rw := declDo(router, http.MethodGet, "/api/v1/permission-targets/"+declTargetID+"/assignments", "", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
	}

	var got struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := []map[string]string{
		{"principal_type": "group", "principal_id": declGroupID, "principal_name": "devs"},
		{"principal_type": "user", "principal_id": declUserID, "principal_name": "alice"},
	}
	if len(got.Data) != len(want) {
		t.Fatalf("data = %v, want %v", got.Data, want)
	}

	for i := range want {
		for k, v := range want[i] {
			if got.Data[i][k] != v {
				t.Errorf("data[%d][%s] = %q, want %q", i, k, got.Data[i][k], v)
			}
		}
	}

	if rw := declDo(router, http.MethodGet, "/api/v1/permission-targets/"+declMissing+"/assignments", "", ""); rw.Code != http.StatusNotFound {
		t.Fatalf("missing target status = %d, want 404", rw.Code)
	}

	if rw := declDo(router, http.MethodGet, "/api/v1/permission-targets/not-a-uuid/assignments", "", ""); rw.Code != http.StatusNotFound {
		t.Fatalf("bad id status = %d, want 404", rw.Code)
	}
}
