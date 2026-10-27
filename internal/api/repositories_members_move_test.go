// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/middleware"
)

const pgForeignKeyViolationCode = "23503"

// repoOpsDB is a repository store on top of the projects fixture: repositories
// by name, group membership lists and the recorded writes.
type repoOpsDB struct {
	*projectsMockDB

	repos   map[string]*models.Repository
	members map[string][]string // group repo ID -> member repo IDs

	getErr, listMembersErr, replaceErr, moveErr, deleteErr, metaErr error

	replaced []string
	replaces int
	moves    []string // "repoID->projectID"
	deleted  []string
	order    []string // call order of the metadata read and the delete
	updated  *models.Repository
	updMem   []string
}

func (d *repoOpsDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if d.getErr != nil {
		return nil, d.getErr
	}

	if r := d.repos[name]; r != nil {
		cp := *r
		return &cp, nil
	}

	return nil, nil
}

func (d *repoOpsDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	for _, r := range d.repos {
		if r.ID == id {
			return r, nil
		}
	}

	return nil, nil
}

func (d *repoOpsDB) ListGroupMembers(_ context.Context, groupID string) ([]*models.RepositoryGroupMember, error) {
	if d.listMembersErr != nil {
		return nil, d.listMembersErr
	}

	out := make([]*models.RepositoryGroupMember, 0, len(d.members[groupID]))
	for i, id := range d.members[groupID] {
		out = append(out, &models.RepositoryGroupMember{GroupRepoID: groupID, MemberRepoID: id, Priority: i})
	}

	return out, nil
}

func (d *repoOpsDB) ReplaceGroupMembers(_ context.Context, groupID string, ids []string) error {
	d.replaces++

	if d.replaceErr != nil {
		return d.replaceErr
	}

	d.replaced = slices.Clone(ids)
	d.members[groupID] = slices.Clone(ids)

	return nil
}

func (d *repoOpsDB) UpdateRepositoryAndMembers(_ context.Context, repo *models.Repository, ids []string) error {
	cp := *repo
	d.updated, d.updMem = &cp, slices.Clone(ids)

	return nil
}

func (d *repoOpsDB) UpdateRepository(_ context.Context, repo *models.Repository) error {
	cp := *repo
	d.updated = &cp

	return nil
}

func (d *repoOpsDB) MoveRepositoryToProject(_ context.Context, repoID, projectID string, _ int) error {
	if d.moveErr != nil {
		return d.moveErr
	}

	d.moves = append(d.moves, repoID+"->"+projectID)

	return nil
}

func (d *repoOpsDB) GetRepoMetadataByRepo(context.Context, string) ([]models.RepoMetadata, error) {
	d.order = append(d.order, "metadata")

	return nil, d.metaErr
}

func (d *repoOpsDB) DeleteRepository(_ context.Context, id string) error {
	d.order = append(d.order, "delete")

	if d.deleteErr != nil {
		return d.deleteErr
	}

	d.deleted = append(d.deleted, id)

	return nil
}

var _ database.Database = (*repoOpsDB)(nil)

func newRepoOpsDB() *repoOpsDB {
	acme, system := testAcmeID, models.SystemProjectID

	return &repoOpsDB{
		projectsMockDB: newProjectsMock(),
		members:        map[string][]string{},
		repos: map[string]*models.Repository{
			"grp":        {ID: "id-grp", Name: "grp", Format: "maven", RepoType: "group", ProjectID: &acme},
			"grp2":       {ID: "id-grp2", Name: "grp2", Format: "maven", RepoType: "group", ProjectID: &acme},
			"hosted":     {ID: "id-hosted", Name: "hosted", Format: "maven", RepoType: "hosted", ProjectID: &acme},
			"proxy":      {ID: "id-proxy", Name: "proxy", Format: "maven", RepoType: "proxy", ProjectID: &acme},
			"npm-hosted": {ID: "id-npm", Name: "npm-hosted", Format: "npm", RepoType: "hosted", ProjectID: &acme},
			"sys-maven":  {ID: "id-sys", Name: "sys-maven", Format: "maven", RepoType: "hosted", ProjectID: &system},
		},
	}
}

func (d *repoOpsDB) router(t *testing.T) http.Handler {
	t.Helper()

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(d, log, auth.NewEvaluator(d, log, false))
	router := chi.NewRouter()
	h.RegisterAuthed(router)

	return router
}

func TestRepositoryGetMembers(t *testing.T) {
	tests := []struct {
		name   string
		repo   string
		setup  func(*repoOpsDB)
		want   int
		wantIn []string // member names in the answer, in order
	}{
		{"members in resolution order", "grp", func(d *repoOpsDB) { d.members["id-grp"] = []string{"id-proxy", "id-hosted", "id-sys"} }, http.StatusOK, []string{"proxy", "hosted", "sys-maven"}},
		{"empty group", "grp", nil, http.StatusOK, []string{}},
		{"member row of a deleted repository is skipped", "grp", func(d *repoOpsDB) { d.members["id-grp"] = []string{"id-hosted", "id-gone", "id-proxy"} }, http.StatusOK, []string{"hosted", "proxy"}},
		{"hosted repository is not a group", "hosted", nil, http.StatusBadRequest, nil},
		{"proxy repository is not a group", "proxy", nil, http.StatusBadRequest, nil},
		{"unknown repository", "ghost", nil, http.StatusNotFound, nil},
		{"repository lookup failure", "grp", func(d *repoOpsDB) { d.getErr = errors.New("connection reset") }, http.StatusInternalServerError, nil},
		{"member listing failure", "grp", func(d *repoOpsDB) { d.listMembersErr = errors.New("connection reset") }, http.StatusInternalServerError, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newRepoOpsDB()
			if tt.setup != nil {
				tt.setup(db)
			}

			rw := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories/"+tt.repo+"/members", http.NoBody)
			db.router(t).ServeHTTP(rw, req.WithContext(middleware.WithPrincipal(req.Context(), testPrincipal(uAdmin1))))

			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), "connection reset") {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			if tt.want != http.StatusOK {
				return
			}

			got := membersOf(t, rw.Body.Bytes())
			if !slices.Equal(got, tt.wantIn) {
				t.Errorf("members = %v, want %v", got, tt.wantIn)
			}
		})
	}
}

func TestRepositoryUpdateMembers(t *testing.T) {
	tests := []struct {
		name         string
		user         string
		repo         string
		body         string
		setup        func(*repoOpsDB)
		want         int
		wantReplaced []string // member repo IDs written; nil = no write
	}{
		{"admin sets the order", uAdmin1, "grp", `{"member_repos":["proxy","hosted"]}`, nil, http.StatusOK, []string{"id-proxy", "id-hosted"}},
		{"order follows the request, not the names", uAdmin1, "grp", `{"member_repos":["sys-maven","hosted","proxy"]}`, nil, http.StatusOK, []string{"id-sys", "id-hosted", "id-proxy"}},
		{"empty list clears the group", uAdmin1, "grp", `{"member_repos":[]}`, func(d *repoOpsDB) { d.members["id-grp"] = []string{"id-hosted"} }, http.StatusOK, []string{}},
		{"project maintainer is not an administrator", uMaint1, "grp", `{"member_repos":["hosted"]}`, nil, http.StatusForbidden, nil},
		{"project owner is not an administrator", uOwner1, "grp", `{"member_repos":["hosted"]}`, nil, http.StatusForbidden, nil},
		{"developer", uDev1, "grp", `{"member_repos":["hosted"]}`, nil, http.StatusForbidden, nil},
		{"user without a role", uPlain1, "grp", `{"member_repos":["hosted"]}`, nil, http.StatusForbidden, nil},
		{"unknown repository", uAdmin1, "ghost", `{"member_repos":["hosted"]}`, nil, http.StatusNotFound, nil},
		{"not a group", uAdmin1, "hosted", `{"member_repos":["proxy"]}`, nil, http.StatusBadRequest, nil},
		{"invalid JSON", uAdmin1, "grp", `{"member_repos":`, nil, http.StatusBadRequest, nil},
		{"unknown member", uAdmin1, "grp", `{"member_repos":["hosted","ghost"]}`, nil, http.StatusBadRequest, nil},
		{"group cannot contain itself", uAdmin1, "grp", `{"member_repos":["grp"]}`, nil, http.StatusBadRequest, nil},
		{"groups cannot nest", uAdmin1, "grp", `{"member_repos":["grp2"]}`, nil, http.StatusBadRequest, nil},
		{"two groups cannot contain each other", uAdmin1, "grp2", `{"member_repos":["grp"]}`, func(d *repoOpsDB) { d.members["id-grp"] = []string{"id-hosted"} }, http.StatusBadRequest, nil},
		{"member of another format", uAdmin1, "grp", `{"member_repos":["hosted","npm-hosted"]}`, nil, http.StatusBadRequest, nil},
		{"duplicate member", uAdmin1, "grp", `{"member_repos":["hosted","proxy","hosted"]}`, nil, http.StatusBadRequest, nil},
		{"too many members", uAdmin1, "grp", `{"member_repos":[` + strings.TrimSuffix(strings.Repeat(`"hosted",`, maxGroupMembers+1), ",") + `]}`, nil, http.StatusBadRequest, nil},
		{"repository lookup failure", uAdmin1, "grp", `{"member_repos":["hosted"]}`, func(d *repoOpsDB) { d.getErr = errors.New("connection reset") }, http.StatusInternalServerError, nil},
		{"replace failure", uAdmin1, "grp", `{"member_repos":["hosted"]}`, func(d *repoOpsDB) { d.replaceErr = errors.New("connection reset") }, http.StatusInternalServerError, nil},
		{"listing failure after the replace", uAdmin1, "grp", `{"member_repos":["hosted"]}`, func(d *repoOpsDB) { d.listMembersErr = errors.New("connection reset") }, http.StatusInternalServerError, []string{"id-hosted"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newRepoOpsDB()
			if tt.setup != nil {
				tt.setup(db)
			}

			before := slices.Clone(db.members["id-grp"])

			code, body := projectJSON(t, db.router(t), tt.user, http.MethodPut, "/api/v1/repositories/"+tt.repo+"/members", tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if strings.Contains(fmt.Sprint(body), "connection reset") {
				t.Errorf("response leaks the database error: %v", body)
			}

			if tt.wantReplaced == nil {
				if !slices.Equal(db.members["id-grp"], before) {
					t.Errorf("a rejected request wrote members: %v (was %v)", db.members["id-grp"], before)
				}

				if code < http.StatusInternalServerError && db.replaces != 0 {
					t.Errorf("a rejected request reached the store %d time(s)", db.replaces)
				}

				return
			}

			if !slices.Equal(db.replaced, tt.wantReplaced) {
				t.Errorf("replaced with %v, want %v", db.replaced, tt.wantReplaced)
			}
		})
	}
}

func TestRepositoryUpdateMembersAnonymousIsForbidden(t *testing.T) {
	db := newRepoOpsDB()

	for name, p := range map[string]*auth.Principal{"no principal": nil, "anonymous": {IsAnonymous: true}} {
		code, body := projectJSONAs(t, db.router(t), p, http.MethodPut, "/api/v1/repositories/grp/members", `{"member_repos":["hosted"]}`)
		if code != http.StatusForbidden || db.replaces != 0 {
			t.Errorf("%s: status = %d, replaces = %d; body = %v", name, code, db.replaces, body)
		}
	}
}

// The returned list is the stored one: name, format and type of each member in
// priority order, so the client sees the order that serving uses.
func TestRepositoryUpdateMembersAnswersWithStoredOrder(t *testing.T) {
	db := newRepoOpsDB()

	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/api/v1/repositories/grp/members", strings.NewReader(`{"member_repos":["proxy","hosted"]}`))
	db.router(t).ServeHTTP(rw, req.WithContext(middleware.WithPrincipal(req.Context(), testPrincipal(uAdmin1))))

	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
	}

	if got := membersOf(t, rw.Body.Bytes()); !slices.Equal(got, []string{"proxy", "hosted"}) {
		t.Fatalf("members = %v, want [proxy hosted]", got)
	}

	if !strings.Contains(rw.Body.String(), `"repo_type":"proxy"`) || !strings.Contains(rw.Body.String(), `"format":"maven"`) {
		t.Errorf("members lack repo_type/format: %s", rw.Body.String())
	}
}

// A project maintainer edits a group through PATCH member_repos, but only with
// members they can read themselves: a group serves its members' content.
func TestRepositoryPatchMembersRequiresReadOnMembers(t *testing.T) {
	tests := []struct {
		name string
		user string
		body string
		want int
	}{
		{"maintainer, members in the project", uMaint1, `{"member_repos":["hosted","proxy"]}`, http.StatusOK},
		{"maintainer, member in a project they have no role in", uMaint1, `{"member_repos":["hosted","sys-maven"]}`, http.StatusForbidden},
		{"admin reads everything", uAdmin1, `{"member_repos":["hosted","sys-maven"]}`, http.StatusOK},
		{"developer cannot edit the group", uDev1, `{"member_repos":["hosted"]}`, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newRepoOpsDB()

			code, body := projectJSON(t, db.router(t), tt.user, http.MethodPatch, "/api/v1/repository/grp", tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if (tt.want == http.StatusOK) != (db.updated != nil) {
				t.Errorf("update written = %v, want %v", db.updated != nil, tt.want == http.StatusOK)
			}
		})
	}
}

func membersOf(t *testing.T, raw []byte) []string {
	t.Helper()

	var out struct {
		Members []struct {
			Name     string `json:"name"`
			Priority int    `json:"priority"`
		} `json:"members"`
	}

	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode members: %v: %s", err, raw)
	}

	names := make([]string, 0, len(out.Members))

	prev := -1

	for _, m := range out.Members {
		if m.Priority <= prev {
			t.Errorf("priority of %s = %d does not follow %d", m.Name, m.Priority, prev)
		}

		prev = m.Priority

		names = append(names, m.Name)
	}

	return names
}

func TestRepositoryMovePermissions(t *testing.T) {
	tests := []struct {
		name      string
		user      string
		repo      string
		body      string
		setup     func(*repoOpsDB)
		want      int
		wantMoves []string
	}{
		{"admin moves out of the system project", uAdmin1, "sys-maven", `{"project":"acme"}`, nil, http.StatusOK, []string{"id-sys->" + testAcmeID}},
		{"admin may move into the system project", uAdmin1, "hosted", `{"project":"system"}`, func(d *repoOpsDB) {
			d.projects["system"] = &models.Project{ID: models.SystemProjectID, Name: "system"}
		}, http.StatusOK, []string{"id-hosted->" + models.SystemProjectID}},
		{"moving to the current project is a no-op", uAdmin1, "hosted", `{"project":"acme"}`, nil, http.StatusOK, nil},
		{"project owner cannot move", uOwner1, "hosted", `{"project":"acme"}`, nil, http.StatusForbidden, nil},
		{"project maintainer cannot move", uMaint1, "hosted", `{"project":"acme"}`, func(d *repoOpsDB) {
			d.projects["beta"] = &models.Project{ID: "proj-beta", Name: "beta"}
		}, http.StatusForbidden, nil},
		{"developer cannot move", uDev1, "hosted", `{"project":"acme"}`, nil, http.StatusForbidden, nil},
		{"user without a role cannot move", uPlain1, "sys-maven", `{"project":"acme"}`, nil, http.StatusForbidden, nil},
		{"unknown target project", uAdmin1, "sys-maven", `{"project":"ghost"}`, nil, http.StatusNotFound, nil},
		{"target project quota cannot take the usage", uAdmin1, "sys-maven", `{"project":"acme"}`, func(d *repoOpsDB) { d.moveErr = database.ErrProjectQuotaExceeded }, http.StatusConflict, nil},
		{"repository vanished during the move", uAdmin1, "sys-maven", `{"project":"acme"}`, func(d *repoOpsDB) { d.moveErr = database.ErrRepositoryNotFound }, http.StatusNotFound, nil},
		{"database failure", uAdmin1, "sys-maven", `{"project":"acme"}`, func(d *repoOpsDB) { d.moveErr = errors.New("connection reset") }, http.StatusInternalServerError, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newRepoOpsDB()
			if tt.setup != nil {
				tt.setup(db)
			}

			code, body := projectJSON(t, db.router(t), tt.user, http.MethodPatch, "/api/v1/repository/"+tt.repo, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if strings.Contains(fmt.Sprint(body), "connection reset") {
				t.Errorf("response leaks the database error: %v", body)
			}

			if !slices.Equal(db.moves, tt.wantMoves) {
				t.Errorf("moves = %v, want %v", db.moves, tt.wantMoves)
			}

			if tt.want != http.StatusOK && db.updated != nil {
				t.Errorf("a failed move still wrote the repository: %+v", db.updated)
			}
		})
	}
}

func TestRepositoryDelete(t *testing.T) {
	fk := &pgconn.PgError{Code: pgForeignKeyViolationCode}

	tests := []struct {
		name        string
		user        string
		repo        string
		target      string
		setup       func(*repoOpsDB)
		want        int
		wantDeleted []string
		wantOrder   []string
	}{
		{"admin deletes after reading the metadata", uAdmin1, "hosted", "?confirm=hosted", nil, http.StatusNoContent, []string{"id-hosted"}, []string{"metadata", "delete"}},
		{"project maintainer deletes a repository of the project", uMaint1, "hosted", "?confirm=hosted", nil, http.StatusNoContent, []string{"id-hosted"}, []string{"metadata", "delete"}},
		{"maintainer cannot delete a repository of another project", uMaint1, "sys-maven", "?confirm=sys-maven", nil, http.StatusForbidden, nil, nil},
		{"developer", uDev1, "hosted", "?confirm=hosted", nil, http.StatusForbidden, nil, nil},
		{"no confirmation", uAdmin1, "hosted", "", nil, http.StatusBadRequest, nil, nil},
		{"confirmation of another name", uAdmin1, "hosted", "?confirm=proxy", nil, http.StatusBadRequest, nil, nil},
		{"unknown repository", uAdmin1, "ghost", "?confirm=ghost", nil, http.StatusNotFound, nil, nil},
		{"a metadata listing failure does not block the delete", uAdmin1, "hosted", "?confirm=hosted", func(d *repoOpsDB) { d.metaErr = errors.New("connection reset") }, http.StatusNoContent, []string{"id-hosted"}, []string{"metadata", "delete"}},
		{"still referenced", uAdmin1, "hosted", "?confirm=hosted", func(d *repoOpsDB) { d.deleteErr = fmt.Errorf("delete: %w", fk) }, http.StatusConflict, nil, []string{"metadata", "delete"}},
		{"database failure", uAdmin1, "hosted", "?confirm=hosted", func(d *repoOpsDB) { d.deleteErr = errors.New("connection reset") }, http.StatusInternalServerError, nil, []string{"metadata", "delete"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newRepoOpsDB()
			if tt.setup != nil {
				tt.setup(db)
			}

			code, body := projectJSON(t, db.router(t), tt.user, http.MethodDelete, "/api/v1/repository/"+tt.repo+tt.target, "")
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if strings.Contains(fmt.Sprint(body), "connection reset") || strings.Contains(fmt.Sprint(body), "23503") {
				t.Errorf("response leaks the database error: %v", body)
			}

			if !slices.Equal(db.deleted, tt.wantDeleted) {
				t.Errorf("deleted = %v, want %v", db.deleted, tt.wantDeleted)
			}

			if !slices.Equal(db.order, tt.wantOrder) {
				t.Errorf("call order = %v, want %v", db.order, tt.wantOrder)
			}
		})
	}
}

// The members routes are manage-only: a reader or writer on the repository
// gets nothing, and neither does an anonymous caller.
func TestRepositoryMembersRoutesRequireManage(t *testing.T) {
	grant := func(actions ...string) []*models.PermissionTarget {
		return []*models.PermissionTarget{{ID: "t", RepositoryFilter: ".*", PathPattern: ".*", Actions: actions}}
	}

	users := map[string][]*models.PermissionTarget{
		"reader":  grant("read"),
		"writer":  grant("read", "write", "delete"),
		"manager": grant("manage", "read"),
	}

	tests := []struct {
		name, user, method string
		want               int
	}{
		{"anonymous read", "", http.MethodGet, http.StatusUnauthorized},
		{"reader read", "reader", http.MethodGet, http.StatusForbidden},
		{"reader write", "reader", http.MethodPut, http.StatusForbidden},
		{"writer read", "writer", http.MethodGet, http.StatusForbidden},
		{"writer write", "writer", http.MethodPut, http.StatusForbidden},
		{"manager read", "manager", http.MethodGet, http.StatusOK},
		{"manager write", "manager", http.MethodPut, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &npmRBACDB{npmMemDB: newNPMMemDB(), tokens: map[string]*models.Token{}, targets: users}
			secrets := map[string]string{}

			for user := range users {
				secret, hash, err := auth.NewTokenSecret(auth.TokenKindAccess)
				if err != nil {
					t.Fatal(err)
				}

				db.tokens[hash] = &models.Token{ID: "tok-" + user, UserID: user, Kind: auth.TokenKindAccess, TokenHash: hash, Scopes: []string{}, CreatedAt: time.Now()}
				secrets[user] = secret
			}

			log := formattest.NewLogger(t)
			router := chi.NewRouter()
			router.Use(middleware.RBACMiddleware(middleware.RBACConfig{}, db, auth.NewEvaluator(db, log, false), log))
			router.HandleFunc("/api/v1/repositories/{name}/members", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

			req := httptest.NewRequestWithContext(context.Background(), tt.method, "/api/v1/repositories/grp/members", strings.NewReader(`{"member_repos":[]}`))
			if tt.user != "" {
				req.Header.Set("Authorization", "Bearer "+secrets[tt.user])
			}

			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, req)

			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}
		})
	}
}
