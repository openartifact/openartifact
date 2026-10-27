// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
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
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/passwd"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	idAdminUser   = "11111111-1111-4111-8111-111111111111"
	idAliceUser   = "22222222-2222-4222-8222-222222222222"
	idBobUser     = "33333333-3333-4333-8333-333333333333"
	idServiceUser = "44444444-4444-4444-8444-444444444444"
	idTargetA     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	idTargetB     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	idGroup       = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	idUnknown     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	idTokenAlice  = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	idTokenBob    = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	idTokenGone   = "99999999-9999-4999-8999-999999999999"
)

type permAssignKey struct{ target, ptype, pid string }

// authzFakeDB is a small stateful in-memory database for the user, token and
// permission-target handlers. fail makes the named method return an error.
type authzFakeDB struct {
	databasetest.BaseMockDB

	users       map[string]*models.User
	tokens      map[string]*models.Token
	targets     map[string]*models.PermissionTarget
	assignments map[permAssignKey]bool
	groups      map[string]bool
	held        map[string][]*models.PermissionTarget // user id -> targets it holds

	fail map[string]error

	deletedSessions      []string // user ids whose sessions were all dropped
	keptSession          map[string]string
	passwordUpdates      map[string]string
	deleteUserErr        error
	lastCreateTokenScope []string
}

func newAuthzFakeDB() *authzFakeDB {
	return &authzFakeDB{
		users:           map[string]*models.User{},
		tokens:          map[string]*models.Token{},
		targets:         map[string]*models.PermissionTarget{},
		assignments:     map[permAssignKey]bool{},
		groups:          map[string]bool{idGroup: true},
		held:            map[string][]*models.PermissionTarget{},
		fail:            map[string]error{},
		keptSession:     map[string]string{},
		passwordUpdates: map[string]string{},
	}
}

var errFakeDB = errors.New("fake db failure")

func (db *authzFakeDB) failing(method string) error { return db.fail[method] }

func (db *authzFakeDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	if err := db.failing("GetUserByID"); err != nil {
		return nil, err
	}

	u, ok := db.users[id]
	if !ok {
		return nil, nil
	}

	cp := *u

	return &cp, nil
}

func (db *authzFakeDB) CreateUser(_ context.Context, u *models.User) (*models.User, error) {
	if err := db.failing("CreateUser"); err != nil {
		return nil, err
	}

	cp := *u
	cp.ID = idUnknown
	db.users[cp.ID] = &cp

	return &cp, nil
}

func (db *authzFakeDB) UpdateUser(_ context.Context, u *models.User) error {
	if err := db.failing("UpdateUser"); err != nil {
		return err
	}

	cp := *u
	db.users[u.ID] = &cp

	return nil
}

func (db *authzFakeDB) ListUsers(_ context.Context, _, _ int) ([]*models.User, int, error) {
	return nil, 0, db.failing("ListUsers")
}

func (db *authzFakeDB) DeleteUser(_ context.Context, id string) (bool, error) {
	if db.deleteUserErr != nil {
		return false, db.deleteUserErr
	}

	_, ok := db.users[id]
	delete(db.users, id)

	return ok, nil
}

func (db *authzFakeDB) UpdateUserPassword(_ context.Context, id, hash string, _ bool) error {
	if err := db.failing("UpdateUserPassword"); err != nil {
		return err
	}

	db.passwordUpdates[id] = hash

	return nil
}

func (db *authzFakeDB) DeleteSessionsByUser(_ context.Context, id string) error {
	if err := db.failing("DeleteSessionsByUser"); err != nil {
		return err
	}

	db.deletedSessions = append(db.deletedSessions, id)

	return nil
}

func (db *authzFakeDB) DeleteOtherSessionsByUser(_ context.Context, id, keepHash string) error {
	if err := db.failing("DeleteOtherSessionsByUser"); err != nil {
		return err
	}

	db.keptSession[id] = keepHash

	return nil
}

func (db *authzFakeDB) GetGroupByID(_ context.Context, id string) (*models.Group, error) {
	if !db.groups[id] {
		return nil, nil
	}

	return &models.Group{ID: id}, nil
}

func (db *authzFakeDB) GetPermissionTargetByID(_ context.Context, id string) (*models.PermissionTarget, error) {
	if err := db.failing("GetPermissionTargetByID"); err != nil {
		return nil, err
	}

	t, ok := db.targets[id]
	if !ok {
		return nil, nil
	}

	cp := *t

	return &cp, nil
}

func (db *authzFakeDB) GetPermissionTargetByName(_ context.Context, name string) (*models.PermissionTarget, error) {
	if err := db.failing("GetPermissionTargetByName"); err != nil {
		return nil, err
	}

	for _, t := range db.targets {
		if t.Name == name {
			cp := *t
			return &cp, nil
		}
	}

	return nil, nil
}

func (db *authzFakeDB) UpdatePermissionTarget(_ context.Context, t *models.PermissionTarget) error {
	if err := db.failing("UpdatePermissionTarget"); err != nil {
		return err
	}

	cp := *t
	db.targets[t.ID] = &cp

	return nil
}

func (db *authzFakeDB) CreatePermissionAssignment(_ context.Context, a *models.PermissionAssignment) error {
	db.assignments[permAssignKey{a.PermissionTargetID, a.PrincipalType, a.PrincipalID}] = true
	return nil
}

func (db *authzFakeDB) DeletePermissionAssignment(_ context.Context, target, ptype, pid string) error {
	if err := db.failing("DeletePermissionAssignment"); err != nil {
		return err
	}

	delete(db.assignments, permAssignKey{target, ptype, pid})

	return nil
}

func (db *authzFakeDB) ListPermissionAssignmentsForTarget(_ context.Context, target string) ([]models.PermissionAssignmentView, error) {
	if err := db.failing("ListPermissionAssignmentsForTarget"); err != nil {
		return nil, err
	}

	var out []models.PermissionAssignmentView

	for k := range db.assignments {
		if k.target == target {
			out = append(out, models.PermissionAssignmentView{PrincipalType: k.ptype, PrincipalID: k.pid})
		}
	}

	return out, nil
}

func (db *authzFakeDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	return db.held[userID], nil
}

func (db *authzFakeDB) CreateToken(_ context.Context, t *models.Token) (*models.Token, error) {
	if err := db.failing("CreateToken"); err != nil {
		return nil, err
	}

	cp := *t
	cp.ID = idTokenGone
	db.tokens[cp.ID] = &cp
	db.lastCreateTokenScope = cp.Scopes

	return &cp, nil
}

func (db *authzFakeDB) GetTokenByID(_ context.Context, id string) (*models.Token, error) {
	if err := db.failing("GetTokenByID"); err != nil {
		return nil, err
	}

	t, ok := db.tokens[id]
	if !ok {
		return nil, nil
	}

	return t, nil
}

func (db *authzFakeDB) RevokeToken(_ context.Context, id string) error {
	if err := db.failing("RevokeToken"); err != nil {
		return err
	}

	now := time.Now()
	db.tokens[id].RevokedAt = &now

	return nil
}

func (db *authzFakeDB) ListTokensByUser(_ context.Context, userID string) ([]*models.Token, error) {
	if err := db.failing("ListTokensByUser"); err != nil {
		return nil, err
	}

	var out []*models.Token

	for _, t := range db.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}

	return out, nil
}

func (db *authzFakeDB) ListAllTokens(_ context.Context, _, _ int) ([]*models.Token, int, error) {
	return nil, 0, db.failing("ListAllTokens")
}

type authzHarness struct {
	t      *testing.T
	db     *authzFakeDB
	router chi.Router
	auth   *AuthHandler
}

func newAuthzHarness(t *testing.T) *authzHarness {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	db := newAuthzFakeDB()
	ev := auth.NewEvaluator(db, log, false)
	r := chi.NewRouter()
	NewAdminPermissionsHandler(db, log, ev).Register(r)
	NewAdminUsersHandler(db, log, ev).Register(r)
	NewAdminTokensHandler(db, log, ev).Register(r)

	ah := NewAuthHandler(db, log, ev, nil)
	ah.RegisterAuthed(r)

	return &authzHarness{t: t, db: db, router: r, auth: ah}
}

func azAdmin() *auth.Principal { return &auth.Principal{Username: "admin", IsLegacy: true} }

// azAnon is an unauthenticated caller.
func azAnon() *auth.Principal { return &auth.Principal{IsAnonymous: true} }

func azUser(id string) *auth.Principal {
	return &auth.Principal{UserID: &id, Username: "u-" + id[:4], Credential: auth.CredentialSession}
}

func azToken(id string) *auth.Principal {
	p := azUser(id)
	p.TokenKind = auth.TokenKindAccess
	p.Credential = auth.CredentialBearerToken

	return p
}

type authzCall struct {
	method    string
	path      string
	body      string
	principal *auth.Principal
	cookie    string
}

func (h *authzHarness) do(c authzCall) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), c.method, c.path, strings.NewReader(c.body))
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: c.cookie})
	}

	req = req.WithContext(middleware.WithPrincipal(req.Context(), c.principal))
	rw := httptest.NewRecorder()
	h.router.ServeHTTP(rw, req)

	return rw
}

func (h *authzHarness) seedTarget(id, name string) {
	h.db.targets[id] = &models.PermissionTarget{
		ID: id, Name: name, RepositoryFilter: "dev-.*", PathPattern: "*", Actions: []string{"read"},
	}
}

func (h *authzHarness) seedUser(id, name string, mutate func(*models.User)) {
	u := &models.User{ID: id, Username: name}
	if mutate != nil {
		mutate(u)
	}

	h.db.users[id] = u
}

func wantStatus(t *testing.T, rw *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rw.Code != want {
		body, _ := io.ReadAll(rw.Body)
		t.Fatalf("status = %d, want %d: %s", rw.Code, want, body)
	}
}

func TestPermissionTargetEndpointsRequireAdmin(t *testing.T) {
	t.Parallel()

	calls := []struct {
		name         string
		method, path string
		body         string
	}{
		{"get", http.MethodGet, "/api/v1/permission-targets/" + idTargetA, ""},
		{"patch", http.MethodPatch, "/api/v1/permission-targets/" + idTargetA, `{"name":"hijacked"}`},
		{"unassign", http.MethodDelete, "/api/v1/permission-targets/" + idTargetA + "/assignments?principal_type=user&principal_id=" + idAliceUser, ""},
	}

	principals := map[string]*auth.Principal{
		"anonymous":     azAnon(),
		"regular user":  azUser(idAliceUser),
		"manage token":  azToken(idAliceUser),
		"nil principal": nil,
	}

	for _, c := range calls {
		for pname, p := range principals {
			t.Run(c.name+"/"+pname, func(t *testing.T) {
				t.Parallel()

				h := newAuthzHarness(t)
				h.seedTarget(idTargetA, "ops")
				h.db.assignments[permAssignKey{idTargetA, "user", idAliceUser}] = true

				wantStatus(t, h.do(authzCall{method: c.method, path: c.path, body: c.body, principal: p}), http.StatusForbidden)

				if got := h.db.targets[idTargetA].Name; got != "ops" {
					t.Errorf("target name changed to %q by a denied caller", got)
				}

				if !h.db.assignments[permAssignKey{idTargetA, "user", idAliceUser}] {
					t.Error("assignment removed by a denied caller")
				}
			})
		}
	}
}

func TestPermissionTargetGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		fail string
		want int
	}{
		{"found", idTargetA, "", http.StatusOK},
		{"missing", idUnknown, "", http.StatusNotFound},
		{"non uuid id", "nope", "", http.StatusNotFound},
		{"database error", idTargetA, "GetPermissionTargetByID", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newAuthzHarness(t)
			h.seedTarget(idTargetA, "ops")

			if tt.fail != "" {
				h.db.fail[tt.fail] = errFakeDB
			}

			rw := h.do(authzCall{method: http.MethodGet, path: "/api/v1/permission-targets/" + tt.id, principal: azAdmin()})
			wantStatus(t, rw, tt.want)

			if tt.want == http.StatusInternalServerError && strings.Contains(rw.Body.String(), errFakeDB.Error()) {
				t.Errorf("error response leaks the database error: %s", rw.Body.String())
			}

			if tt.want == http.StatusOK && !strings.Contains(rw.Body.String(), `"name":"ops"`) {
				t.Errorf("body missing target: %s", rw.Body.String())
			}
		})
	}
}

func TestPermissionTargetPatch(t *testing.T) {
	t.Parallel()

	uniq := &pgconn.PgError{Code: "23505"}

	tests := []struct {
		name     string
		id       string
		body     string
		fail     map[string]error
		want     int
		wantName string // expected stored name afterwards
		wantActs []string
	}{
		{"rename and change actions", idTargetA, `{"name":"  renamed  ","actions":["read","write"]}`, nil, http.StatusOK, "renamed", []string{"read", "write"}},
		{"keep own name", idTargetA, `{"name":"ops","path_pattern":"pkg/.*"}`, nil, http.StatusOK, "ops", []string{"read"}},
		{"empty body is a no-op", idTargetA, `{}`, nil, http.StatusOK, "ops", []string{"read"}},
		{"name taken by other target", idTargetA, `{"name":"other"}`, nil, http.StatusConflict, "ops", []string{"read"}},
		{"blank name", idTargetA, `{"name":"   "}`, nil, http.StatusBadRequest, "ops", []string{"read"}},
		{"name too long", idTargetA, `{"name":"` + strings.Repeat("n", 129) + `"}`, nil, http.StatusBadRequest, "ops", []string{"read"}},
		{"unknown action", idTargetA, `{"actions":["read","sudo"]}`, nil, http.StatusBadRequest, "ops", []string{"read"}},
		{"empty actions list", idTargetA, `{"actions":[]}`, nil, http.StatusBadRequest, "ops", []string{"read"}},
		{"empty repository filter", idTargetA, `{"repository_filter":""}`, nil, http.StatusBadRequest, "ops", []string{"read"}},
		{"bad path regex", idTargetA, `{"path_pattern":"("}`, nil, http.StatusBadRequest, "ops", []string{"read"}},
		{"invalid json", idTargetA, `{`, nil, http.StatusBadRequest, "ops", []string{"read"}},
		{"unknown target", idUnknown, `{"name":"x"}`, nil, http.StatusNotFound, "", nil},
		{"lookup error", idTargetA, `{"name":"x"}`, map[string]error{"GetPermissionTargetByID": errFakeDB}, http.StatusInternalServerError, "ops", []string{"read"}},
		{"name lookup error", idTargetA, `{"name":"x"}`, map[string]error{"GetPermissionTargetByName": errFakeDB}, http.StatusInternalServerError, "ops", []string{"read"}},
		{"update error", idTargetA, `{"name":"x"}`, map[string]error{"UpdatePermissionTarget": errFakeDB}, http.StatusInternalServerError, "ops", []string{"read"}},
		{"update unique violation", idTargetA, `{"name":"x"}`, map[string]error{"UpdatePermissionTarget": uniq}, http.StatusConflict, "ops", []string{"read"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newAuthzHarness(t)
			h.seedTarget(idTargetA, "ops")
			h.seedTarget(idTargetB, "other")

			maps.Copy(h.db.fail, tt.fail)

			rw := h.do(authzCall{method: http.MethodPatch, path: "/api/v1/permission-targets/" + tt.id, body: tt.body, principal: azAdmin()})

			wantStatus(t, rw, tt.want)

			if strings.Contains(rw.Body.String(), errFakeDB.Error()) {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			if tt.wantName == "" {
				return
			}

			got := h.db.targets[tt.id]
			if got.Name != tt.wantName || !slices.Equal(got.Actions, tt.wantActs) {
				t.Errorf("stored target = %q %v, want %q %v", got.Name, got.Actions, tt.wantName, tt.wantActs)
			}

			if other := h.db.targets[idTargetB]; other.Name != "other" {
				t.Errorf("sibling target modified: %q", other.Name)
			}
		})
	}
}

func TestPermissionTargetUnassign(t *testing.T) {
	t.Parallel()

	base := "/api/v1/permission-targets/" + idTargetA + "/assignments"

	tests := []struct {
		name    string
		query   string
		fail    bool
		want    int
		removed bool
	}{
		{"user", "?principal_type=user&principal_id=" + idAliceUser, false, http.StatusNoContent, true},
		{"group", "?principal_type=group&principal_id=" + idGroup, false, http.StatusNoContent, true},
		{"missing type", "?principal_id=" + idAliceUser, false, http.StatusBadRequest, false},
		{"bad type", "?principal_type=team&principal_id=" + idAliceUser, false, http.StatusBadRequest, false},
		{"missing id", "?principal_type=user", false, http.StatusBadRequest, false},
		{"non uuid id", "?principal_type=user&principal_id=alice", false, http.StatusBadRequest, false},
		{"database error", "?principal_type=user&principal_id=" + idAliceUser, true, http.StatusInternalServerError, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newAuthzHarness(t)
			h.seedTarget(idTargetA, "ops")
			h.seedTarget(idTargetB, "other")

			keep := []permAssignKey{
				{idTargetA, "user", idBobUser},
				{idTargetB, "user", idAliceUser},
				{idTargetA, "user", idAliceUser},
				{idTargetA, "group", idGroup},
			}
			for _, k := range keep {
				h.db.assignments[k] = true
			}

			if tt.fail {
				h.db.fail["DeletePermissionAssignment"] = errFakeDB
			}

			wantStatus(t, h.do(authzCall{method: http.MethodDelete, path: base + tt.query, principal: azAdmin()}), tt.want)

			// Only the exact (target, type, principal) triple may disappear.
			wantLeft := len(keep)
			if tt.removed {
				wantLeft--
			}

			if got := len(h.db.assignments); got != wantLeft {
				t.Errorf("assignments left = %d, want %d", got, wantLeft)
			}

			if !h.db.assignments[permAssignKey{idTargetB, "user", idAliceUser}] || !h.db.assignments[permAssignKey{idTargetA, "user", idBobUser}] {
				t.Error("unassign removed an assignment of another target or principal")
			}
		})
	}
}

func TestAdminUserCreateValidation(t *testing.T) {
	t.Parallel()

	uniq := &pgconn.PgError{Code: "23505"}

	tests := []struct {
		name string
		body string
		fail error
		want int
	}{
		{"ok", `{"username":"newuser","email":"n@example.com","password":"a-long-enough-password"}`, nil, http.StatusCreated},
		{"no password", `{"username":"newuser"}`, nil, http.StatusCreated},
		{"short username", `{"username":"ab"}`, nil, http.StatusBadRequest},
		{"bad username chars", `{"username":"bad name!"}`, nil, http.StatusBadRequest},
		{"bad email", `{"username":"newuser","email":"nope"}`, nil, http.StatusBadRequest},
		{"short password", `{"username":"newuser","password":"short"}`, nil, http.StatusBadRequest},
		{"invalid json", `{`, nil, http.StatusBadRequest},
		{"duplicate", `{"username":"newuser"}`, uniq, http.StatusConflict},
		{"database error", `{"username":"newuser"}`, errFakeDB, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newAuthzHarness(t)
			if tt.fail != nil {
				h.db.fail["CreateUser"] = tt.fail
			}

			rw := h.do(authzCall{method: http.MethodPost, path: "/api/v1/users", body: tt.body, principal: azAdmin()})
			wantStatus(t, rw, tt.want)

			if tt.want != http.StatusCreated && len(h.db.users) != 0 {
				t.Error("a rejected create stored a user")
			}

			if strings.Contains(rw.Body.String(), "password_hash") || strings.Contains(rw.Body.String(), "$argon2") {
				t.Errorf("response exposes a password hash: %s", rw.Body.String())
			}
		})
	}

	t.Run("non-admin denied", func(t *testing.T) {
		t.Parallel()

		h := newAuthzHarness(t)
		rw := h.do(authzCall{method: http.MethodPost, path: "/api/v1/users", body: `{"username":"newuser"}`, principal: azUser(idAliceUser)})
		wantStatus(t, rw, http.StatusForbidden)
	})
}

func TestAdminUserGetListDelete(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) *authzHarness {
		t.Helper()

		h := newAuthzHarness(t)
		h.seedUser(idAliceUser, "alice", nil)
		h.seedUser(idServiceUser, "svc", func(u *models.User) { u.Kind = models.UserKindService })

		return h
	}

	t.Run("get hides service accounts", func(t *testing.T) {
		t.Parallel()

		h := setup(t)
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idServiceUser, principal: azAdmin()}), http.StatusNotFound)
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idAliceUser, principal: azAdmin()}), http.StatusOK)
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idUnknown, principal: azAdmin()}), http.StatusNotFound)
	})

	t.Run("get and list denied for non-admin", func(t *testing.T) {
		t.Parallel()

		h := setup(t)
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idAliceUser, principal: azUser(idAliceUser)}), http.StatusForbidden)
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users", principal: azUser(idAliceUser)}), http.StatusForbidden)
	})

	t.Run("get and list database errors", func(t *testing.T) {
		t.Parallel()

		h := setup(t)
		h.db.fail["GetUserByID"] = errFakeDB
		h.db.fail["ListUsers"] = errFakeDB
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idAliceUser, principal: azAdmin()}), http.StatusInternalServerError)
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users", principal: azAdmin()}), http.StatusInternalServerError)
	})

	deleteTests := []struct {
		name      string
		id        string
		principal *auth.Principal
		delErr    error
		fail      map[string]error
		want      int
		deleted   bool
		revoked   bool
	}{
		{"admin deletes user and drops sessions", idAliceUser, azAdmin(), nil, nil, http.StatusNoContent, true, true},
		{"non-admin denied", idAliceUser, azUser(idBobUser), nil, nil, http.StatusForbidden, false, false},
		{"self delete refused", idAdminUser, azAdminUser(), nil, nil, http.StatusBadRequest, false, false},
		{"service account hidden", idServiceUser, azAdmin(), nil, nil, http.StatusNotFound, false, false},
		{"unknown user", idUnknown, azAdmin(), nil, nil, http.StatusNotFound, false, false},
		{"last admin", idAliceUser, azAdmin(), database.ErrLastAdmin, nil, http.StatusConflict, false, true},
		{"delete failure", idAliceUser, azAdmin(), errFakeDB, nil, http.StatusInternalServerError, false, true},
		{"session revoke failure keeps user", idAliceUser, azAdmin(), nil, map[string]error{"DeleteSessionsByUser": errFakeDB}, http.StatusInternalServerError, false, false},
		{"lookup failure", idAliceUser, azAdmin(), nil, map[string]error{"GetUserByID": errFakeDB}, http.StatusInternalServerError, false, false},
	}

	for _, tt := range deleteTests {
		t.Run("delete/"+tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			h.seedUser(idAdminUser, "root", nil)
			h.db.deleteUserErr = tt.delErr

			maps.Copy(h.db.fail, tt.fail)

			rw := h.do(authzCall{method: http.MethodDelete, path: "/api/v1/users/" + tt.id, principal: tt.principal})
			wantStatus(t, rw, tt.want)

			if _, exists := h.db.users[tt.id]; tt.id != idUnknown && exists == tt.deleted {
				t.Errorf("user exists = %v after delete, want %v", exists, !tt.deleted)
			}

			if got := slices.Contains(h.db.deletedSessions, tt.id); got != tt.revoked {
				t.Errorf("sessions revoked = %v, want %v", got, tt.revoked)
			}
		})
	}
}

func azAdminUser() *auth.Principal {
	id := idAdminUser

	return &auth.Principal{UserID: &id, Username: "root", IsLegacy: true}
}

func TestAdminUserPatchAuthorization(t *testing.T) {
	t.Parallel()

	adminOnSelf := azAdminUser()

	tests := []struct {
		name      string
		id        string
		principal *auth.Principal
		body      string
		fail      map[string]error
		want      int
		check     func(t *testing.T, h *authzHarness)
	}{
		{
			name: "self may change full name", id: idAliceUser, principal: azUser(idAliceUser),
			body: `{"full_name":"Alice A"}`, want: http.StatusOK,
			check: checkSelfFullName,
		},
		{name: "self cannot set email", id: idAliceUser, principal: azUser(idAliceUser), body: `{"email":"x@y.z"}`, want: http.StatusForbidden},
		{name: "self cannot re-enable", id: idAliceUser, principal: azUser(idAliceUser), body: `{"disabled":false}`, want: http.StatusForbidden},
		{name: "self cannot clear must-change", id: idAliceUser, principal: azUser(idAliceUser), body: `{"must_change_password":false}`, want: http.StatusForbidden},
		{name: "self cannot set password here", id: idAliceUser, principal: azUser(idAliceUser), body: `{"password":"a-long-enough-password"}`, want: http.StatusForbidden},
		{name: "own api token cannot edit profile", id: idAliceUser, principal: azToken(idAliceUser), body: `{"full_name":"x"}`, want: http.StatusForbidden},
		{name: "other user denied", id: idAliceUser, principal: azUser(idBobUser), body: `{"full_name":"x"}`, want: http.StatusForbidden},
		{name: "anonymous denied", id: idAliceUser, principal: azAnon(), body: `{"full_name":"x"}`, want: http.StatusForbidden},
		{name: "admin cannot disable self", id: idAdminUser, principal: adminOnSelf, body: `{"disabled":true}`, want: http.StatusBadRequest},
		{name: "unknown user", id: idUnknown, principal: azAdmin(), body: `{"full_name":"x"}`, want: http.StatusNotFound},
		{name: "service account", id: idServiceUser, principal: azAdmin(), body: `{"full_name":"x"}`, want: http.StatusNotFound},
		{name: "invalid json", id: idAliceUser, principal: azAdmin(), body: `{`, want: http.StatusBadRequest},
		{name: "bad email", id: idAliceUser, principal: azAdmin(), body: `{"email":"nope"}`, want: http.StatusBadRequest},
		{name: "short password", id: idAliceUser, principal: azAdmin(), body: `{"password":"short"}`, want: http.StatusBadRequest},
		{name: "email conflict", id: idAliceUser, principal: azAdmin(), body: `{"email":"x@y.z"}`, fail: map[string]error{"UpdateUser": &pgconn.PgError{Code: "23505"}}, want: http.StatusConflict},
		{name: "update failure", id: idAliceUser, principal: azAdmin(), body: `{"full_name":"x"}`, fail: map[string]error{"UpdateUser": errFakeDB}, want: http.StatusInternalServerError},
		{name: "lookup failure", id: idAliceUser, principal: azAdmin(), body: `{"full_name":"x"}`, fail: map[string]error{"GetUserByID": errFakeDB}, want: http.StatusInternalServerError},
		{
			name: "admin password reset drops sessions", id: idAliceUser, principal: azAdmin(),
			body: `{"password":"a-long-enough-password"}`, want: http.StatusOK,
			check: checkPasswordReset,
		},
		{
			name: "admin disable drops sessions", id: idAliceUser, principal: azAdmin(),
			body: `{"disabled":true}`, want: http.StatusOK,
			check: checkDisableRevokes,
		},
		{
			name: "enable keeps sessions", id: idAliceUser, principal: azAdmin(),
			body: `{"disabled":false,"must_change_password":true}`, want: http.StatusOK,
			check: checkEnableKeepsSessions,
		},
		{name: "revoke failure fails request", id: idAliceUser, principal: azAdmin(), body: `{"disabled":true}`, fail: map[string]error{"DeleteSessionsByUser": errFakeDB}, want: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newAuthzHarness(t)
			h.seedUser(idAliceUser, "alice", nil)
			h.seedUser(idBobUser, "bob", nil)
			h.seedUser(idAdminUser, "root", nil)
			h.seedUser(idServiceUser, "svc", func(u *models.User) { u.Kind = models.UserKindService })

			maps.Copy(h.db.fail, tt.fail)

			rw := h.do(authzCall{method: http.MethodPatch, path: "/api/v1/users/" + tt.id, body: tt.body, principal: tt.principal})
			wantStatus(t, rw, tt.want)

			if strings.Contains(rw.Body.String(), errFakeDB.Error()) {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			if tt.want >= 400 && tt.id == idAliceUser && tt.fail == nil {
				if u := h.db.users[idAliceUser]; u.Disabled || u.PasswordHash != nil || u.Email != nil || u.FullName != nil {
					t.Errorf("rejected patch modified the user: %+v", u)
				}
			}

			if tt.check != nil {
				tt.check(t, h)
			}
		})
	}
}

func TestAdminTokenCreateAndScopes(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	tooFar := time.Now().Add(3 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	path := "/api/v1/users/" + idAliceUser + "/tokens"

	tests := []struct {
		name       string
		principal  *auth.Principal
		path       string
		body       string
		fail       map[string]error
		want       int
		wantScopes []string
	}{
		{"owner creates access token", azUser(idAliceUser), path, `{"name":"ci","kind":"access"}`, nil, http.StatusCreated, nil},
		{"owner creates scoped token with held target", azUser(idAliceUser), path, `{"name":"ci","kind":"deploy","scopes":["` + idTargetA + `"],"expires_at":"` + future + `"}`, nil, http.StatusCreated, []string{idTargetA}},
		{"owner cannot request unheld scope", azUser(idAliceUser), path, `{"name":"ci","kind":"access","scopes":["` + idTargetB + `"]}`, nil, http.StatusForbidden, nil},
		{"admin may scope any existing target", azAdmin(), path, `{"name":"ci","kind":"access","scopes":["` + idTargetB + `"]}`, nil, http.StatusCreated, []string{idTargetB}},
		{"admin unknown scope", azAdmin(), path, `{"name":"ci","kind":"access","scopes":["` + idUnknown + `"]}`, nil, http.StatusBadRequest, nil},
		{"admin scope lookup error", azAdmin(), path, `{"name":"ci","kind":"access","scopes":["` + idTargetA + `"]}`, map[string]error{"GetPermissionTargetByID": errFakeDB}, http.StatusInternalServerError, nil},
		{"other user denied", azUser(idBobUser), path, `{"name":"ci","kind":"access"}`, nil, http.StatusForbidden, nil},
		{"anonymous denied", azAnon(), path, `{"name":"ci","kind":"access"}`, nil, http.StatusForbidden, nil},
		{"token cannot mint tokens", azToken(idAliceUser), path, `{"name":"ci","kind":"access"}`, nil, http.StatusForbidden, nil},
		{"unknown owner", azAdmin(), "/api/v1/users/" + idUnknown + "/tokens", `{"name":"ci","kind":"access"}`, nil, http.StatusNotFound, nil},
		{"service owner", azAdmin(), "/api/v1/users/" + idServiceUser + "/tokens", `{"name":"ci","kind":"access"}`, nil, http.StatusNotFound, nil},
		{"owner lookup error", azAdmin(), path, `{"name":"ci","kind":"access"}`, map[string]error{"GetUserByID": errFakeDB}, http.StatusInternalServerError, nil},
		{"invalid json", azUser(idAliceUser), path, `{`, nil, http.StatusBadRequest, nil},
		{"blank name", azUser(idAliceUser), path, `{"name":"  ","kind":"access"}`, nil, http.StatusBadRequest, nil},
		{"long name", azUser(idAliceUser), path, `{"name":"` + strings.Repeat("n", 101) + `","kind":"access"}`, nil, http.StatusBadRequest, nil},
		{"bad kind", azUser(idAliceUser), path, `{"name":"ci","kind":"root"}`, nil, http.StatusBadRequest, nil},
		{"empty scope value", azUser(idAliceUser), path, `{"name":"ci","kind":"access","scopes":[""]}`, nil, http.StatusBadRequest, nil},
		{"too many scopes", azUser(idAliceUser), path, `{"name":"ci","kind":"access","scopes":[` + strings.TrimSuffix(strings.Repeat(`"x",`, 65), ",") + `]}`, nil, http.StatusBadRequest, nil},
		{"expired expiry", azUser(idAliceUser), path, `{"name":"ci","kind":"access","expires_at":"` + past + `"}`, nil, http.StatusBadRequest, nil},
		{"expiry beyond two years", azUser(idAliceUser), path, `{"name":"ci","kind":"access","expires_at":"` + tooFar + `"}`, nil, http.StatusBadRequest, nil},
		{"store failure", azUser(idAliceUser), path, `{"name":"ci","kind":"access"}`, map[string]error{"CreateToken": errFakeDB}, http.StatusInternalServerError, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newAuthzHarness(t)
			h.seedUser(idAliceUser, "alice", nil)
			h.seedUser(idBobUser, "bob", nil)
			h.seedUser(idServiceUser, "svc", func(u *models.User) { u.Kind = models.UserKindService })
			h.seedTarget(idTargetA, "ops")
			h.seedTarget(idTargetB, "other")
			h.db.held[idAliceUser] = []*models.PermissionTarget{h.db.targets[idTargetA]}

			maps.Copy(h.db.fail, tt.fail)

			rw := h.do(authzCall{method: http.MethodPost, path: tt.path, body: tt.body, principal: tt.principal})
			wantStatus(t, rw, tt.want)

			if strings.Contains(rw.Body.String(), errFakeDB.Error()) {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			if tt.want != http.StatusCreated {
				if len(h.db.tokens) != 0 {
					t.Error("a rejected request stored a token")
				}

				return
			}

			if len(tt.wantScopes) > 0 && !slices.Equal(h.db.lastCreateTokenScope, tt.wantScopes) {
				t.Errorf("stored scopes = %v, want %v", h.db.lastCreateTokenScope, tt.wantScopes)
			}
		})
	}
}

func TestAdminTokenListAndRevokeScoping(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) *authzHarness {
		t.Helper()

		h := newAuthzHarness(t)
		h.seedUser(idAliceUser, "alice", nil)
		h.seedUser(idBobUser, "bob", nil)

		now := time.Now()
		h.db.tokens[idTokenAlice] = &models.Token{ID: idTokenAlice, UserID: idAliceUser, Name: "a", Kind: "access", CreatedAt: now}
		h.db.tokens[idTokenBob] = &models.Token{ID: idTokenBob, UserID: idBobUser, Name: "b", Kind: "access", CreatedAt: now}
		h.db.tokens[idTokenGone] = &models.Token{ID: idTokenGone, UserID: idAliceUser, Name: "old", Kind: "access", CreatedAt: now, RevokedAt: &now}

		return h
	}

	t.Run("list shows only live tokens of the owner", func(t *testing.T) {
		t.Parallel()

		h := setup(t)
		rw := h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idAliceUser + "/tokens", principal: azUser(idAliceUser)})
		wantStatus(t, rw, http.StatusOK)

		body := rw.Body.String()
		if !strings.Contains(body, idTokenAlice) || strings.Contains(body, idTokenBob) || strings.Contains(body, idTokenGone) {
			t.Errorf("unexpected list contents: %s", body)
		}
	})

	t.Run("list denied for other users and tokens", func(t *testing.T) {
		t.Parallel()

		h := setup(t)

		for _, p := range []*auth.Principal{azUser(idBobUser), azToken(idAliceUser), azAnon()} {
			wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idAliceUser + "/tokens", principal: p}), http.StatusForbidden)
		}
	})

	t.Run("list database error", func(t *testing.T) {
		t.Parallel()

		h := setup(t)
		h.db.fail["ListTokensByUser"] = errFakeDB
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/users/" + idAliceUser + "/tokens", principal: azAdmin()}), http.StatusInternalServerError)
	})

	revokeTests := []struct {
		name      string
		user      string
		token     string
		principal *auth.Principal
		fail      map[string]error
		want      int
		revoked   bool
	}{
		{"owner revokes own token", idAliceUser, idTokenAlice, azUser(idAliceUser), nil, http.StatusNoContent, true},
		{"admin revokes", idAliceUser, idTokenAlice, azAdmin(), nil, http.StatusNoContent, true},
		{"token of another user via own path", idAliceUser, idTokenBob, azUser(idAliceUser), nil, http.StatusForbidden, false},
		{"other user denied", idAliceUser, idTokenAlice, azUser(idBobUser), nil, http.StatusForbidden, false},
		{"api token cannot revoke", idAliceUser, idTokenAlice, azToken(idAliceUser), nil, http.StatusForbidden, false},
		{"unknown token", idAliceUser, idUnknown, azUser(idAliceUser), nil, http.StatusNotFound, false},
		{"lookup error", idAliceUser, idTokenAlice, azUser(idAliceUser), map[string]error{"GetTokenByID": errFakeDB}, http.StatusInternalServerError, false},
		{"revoke error", idAliceUser, idTokenAlice, azUser(idAliceUser), map[string]error{"RevokeToken": errFakeDB}, http.StatusInternalServerError, false},
	}

	for _, tt := range revokeTests {
		t.Run("revoke/"+tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			maps.Copy(h.db.fail, tt.fail)

			wantStatus(t, h.do(authzCall{method: http.MethodDelete, path: "/api/v1/users/" + tt.user + "/tokens/" + tt.token, principal: tt.principal}), tt.want)

			if tok := h.db.tokens[tt.token]; tok != nil && (tok.RevokedAt != nil) != tt.revoked {
				t.Errorf("revoked = %v, want %v", tok.RevokedAt != nil, tt.revoked)
			}
		})
	}

	t.Run("admin endpoints", func(t *testing.T) {
		t.Parallel()

		h := setup(t)

		for _, c := range []authzCall{
			{method: http.MethodGet, path: "/api/v1/admin/tokens", principal: azUser(idAliceUser)},
			{method: http.MethodDelete, path: "/api/v1/admin/tokens/" + idTokenBob, principal: azUser(idAliceUser)},
		} {
			wantStatus(t, h.do(c), http.StatusForbidden)
		}

		if h.db.tokens[idTokenBob].RevokedAt != nil {
			t.Error("non-admin revoked a token through the admin route")
		}

		h.db.fail["ListAllTokens"] = errFakeDB
		wantStatus(t, h.do(authzCall{method: http.MethodGet, path: "/api/v1/admin/tokens", principal: azAdmin()}), http.StatusInternalServerError)

		h.db.fail["GetTokenByID"] = errFakeDB
		wantStatus(t, h.do(authzCall{method: http.MethodDelete, path: "/api/v1/admin/tokens/" + idTokenBob, principal: azAdmin()}), http.StatusInternalServerError)

		delete(h.db.fail, "GetTokenByID")
		h.db.fail["RevokeToken"] = errFakeDB
		wantStatus(t, h.do(authzCall{method: http.MethodDelete, path: "/api/v1/admin/tokens/" + idTokenBob, principal: azAdmin()}), http.StatusInternalServerError)

		delete(h.db.fail, "RevokeToken")
		wantStatus(t, h.do(authzCall{method: http.MethodDelete, path: "/api/v1/admin/tokens/" + idTokenBob, principal: azAdmin()}), http.StatusNoContent)

		if h.db.tokens[idTokenBob].RevokedAt == nil {
			t.Error("admin revoke did not revoke")
		}
	})
}

func TestChangePassword(t *testing.T) {
	t.Parallel()

	const (
		current = "current-password-123"
		fresh   = "brand-new-password-456"
	)

	sessionRaw := strings.Repeat("ab", 32)
	sum := sha256.Sum256(mustHex(t, sessionRaw))
	sessionHash := hex.EncodeToString(sum[:])

	tests := []struct {
		name      string
		principal *auth.Principal
		body      string
		cookie    string
		noHash    bool
		fail      map[string]error
		want      int
		changed   bool
		keptHash  string // expected keep-hash for other-session revocation
		allGone   bool   // all sessions dropped (unusable cookie)
	}{
		{name: "success keeps current session only", principal: azUser(idAliceUser), body: `{"current_password":"` + current + `","new_password":"` + fresh + `"}`, cookie: sessionRaw, want: http.StatusNoContent, changed: true, keptHash: sessionHash},
		{name: "success without cookie drops every session", principal: azUser(idAliceUser), body: `{"current_password":"` + current + `","new_password":"` + fresh + `"}`, want: http.StatusNoContent, changed: true, allGone: true},
		{name: "wrong current password", principal: azUser(idAliceUser), body: `{"current_password":"wrong-password-000","new_password":"` + fresh + `"}`, cookie: sessionRaw, want: http.StatusUnauthorized},
		{name: "empty current password", principal: azUser(idAliceUser), body: `{"new_password":"` + fresh + `"}`, cookie: sessionRaw, want: http.StatusUnauthorized},
		{name: "oversized current password", principal: azUser(idAliceUser), body: `{"current_password":"` + strings.Repeat("x", maxLoginFieldBytes+1) + `","new_password":"` + fresh + `"}`, want: http.StatusUnauthorized},
		{name: "new password too short", principal: azUser(idAliceUser), body: `{"current_password":"` + current + `","new_password":"short"}`, cookie: sessionRaw, want: http.StatusBadRequest},
		{name: "new password too long", principal: azUser(idAliceUser), body: `{"current_password":"` + current + `","new_password":"` + strings.Repeat("p", passwd.MaxPasswordLength+1) + `"}`, want: http.StatusBadRequest},
		{name: "invalid json", principal: azUser(idAliceUser), body: `{`, want: http.StatusBadRequest},
		{name: "api token refused", principal: azToken(idAliceUser), body: `{"current_password":"` + current + `","new_password":"` + fresh + `"}`, want: http.StatusForbidden},
		{name: "anonymous refused", principal: azAnon(), body: `{}`, want: http.StatusUnauthorized},
		{name: "legacy key without user refused", principal: azAdmin(), body: `{}`, want: http.StatusUnauthorized},
		{name: "sso user without local password", principal: azUser(idAliceUser), body: `{"current_password":"x","new_password":"` + fresh + `"}`, noHash: true, want: http.StatusBadRequest},
		{name: "unknown user", principal: azUser(idUnknown), body: `{"current_password":"` + current + `","new_password":"` + fresh + `"}`, want: http.StatusInternalServerError},
		{name: "update failure", principal: azUser(idAliceUser), body: `{"current_password":"` + current + `","new_password":"` + fresh + `"}`, fail: map[string]error{"UpdateUserPassword": errFakeDB}, want: http.StatusInternalServerError},
		{name: "revoke failure reported", principal: azUser(idAliceUser), body: `{"current_password":"` + current + `","new_password":"` + fresh + `"}`, cookie: sessionRaw, fail: map[string]error{"DeleteOtherSessionsByUser": errFakeDB}, want: http.StatusInternalServerError, changed: true, keptHash: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newAuthzHarness(t)

			hash, err := passwd.HashPassword(current)
			if err != nil {
				t.Fatalf("hash: %v", err)
			}

			h.seedUser(idAliceUser, "alice", func(u *models.User) {
				if !tt.noHash {
					u.PasswordHash = &hash
				}
			})

			maps.Copy(h.db.fail, tt.fail)

			rw := h.do(authzCall{method: http.MethodPost, path: "/api/v1/auth/change-password", body: tt.body, principal: tt.principal, cookie: tt.cookie})
			wantStatus(t, rw, tt.want)

			if strings.Contains(rw.Body.String(), errFakeDB.Error()) {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			_, updated := h.db.passwordUpdates[idAliceUser]
			if tt.changed && !updated && tt.fail["UpdateUserPassword"] == nil {
				t.Error("password was not updated")
			}

			if !tt.changed && updated {
				t.Error("password updated despite rejected request")
			}

			if updated {
				if ok, vErr := passwd.VerifyPassword(fresh, h.db.passwordUpdates[idAliceUser]); vErr != nil || !ok {
					t.Errorf("stored hash does not verify the new password: %v", vErr)
				}
			}

			assertSessionRevocation(t, h, tt.changed, tt.allGone, tt.keptHash)
		})
	}
}

func TestChangePasswordRateLimitsWrongGuesses(t *testing.T) {
	t.Parallel()

	h := newAuthzHarness(t)

	hash, err := passwd.HashPassword("current-password-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	h.seedUser(idAliceUser, "alice", func(u *models.User) { u.PasswordHash = &hash })

	call := func(current string) *httptest.ResponseRecorder {
		return h.do(authzCall{
			method: http.MethodPost, path: "/api/v1/auth/change-password",
			body:      `{"current_password":"` + current + `","new_password":"brand-new-password-456"}`,
			principal: azUser(idAliceUser),
		})
	}

	for range loginUserFailureLimit {
		wantStatus(t, call("wrong-password-000"), http.StatusUnauthorized)
	}

	// Once the budget is spent even the right password is refused.
	rw := call("current-password-123")
	wantStatus(t, rw, http.StatusTooManyRequests)

	if rw.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}

	if len(h.db.passwordUpdates) != 0 {
		t.Error("password changed while rate limited")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}

	return b
}

func checkSelfFullName(t *testing.T, h *authzHarness) {
	t.Helper()

	if fn := h.db.users[idAliceUser].FullName; fn == nil || *fn != "Alice A" {
		t.Errorf("full name = %v", fn)
	}
}

func checkPasswordReset(t *testing.T, h *authzHarness) {
	t.Helper()

	if !slices.Contains(h.db.deletedSessions, idAliceUser) {
		t.Error("password reset did not revoke sessions")
	}

	hash := h.db.users[idAliceUser].PasswordHash
	if hash == nil || *hash == "a-long-enough-password" {
		t.Fatal("password not stored hashed")
	}

	if ok, err := passwd.VerifyPassword("a-long-enough-password", *hash); err != nil || !ok {
		t.Errorf("stored hash does not verify: %v", err)
	}
}

func checkDisableRevokes(t *testing.T, h *authzHarness) {
	t.Helper()

	if !h.db.users[idAliceUser].Disabled || !slices.Contains(h.db.deletedSessions, idAliceUser) {
		t.Error("disable did not persist and revoke sessions")
	}
}

func checkEnableKeepsSessions(t *testing.T, h *authzHarness) {
	t.Helper()

	if len(h.db.deletedSessions) != 0 || !h.db.users[idAliceUser].MustChangePassword {
		t.Error("enable must not revoke sessions and must set must_change_password")
	}
}

func assertSessionRevocation(t *testing.T, h *authzHarness, changed, allGone bool, keptHash string) {
	t.Helper()

	if !changed {
		if len(h.db.keptSession) != 0 || len(h.db.deletedSessions) != 0 {
			t.Error("sessions revoked by a rejected change")
		}

		return
	}

	if allGone && !slices.Contains(h.db.deletedSessions, idAliceUser) {
		t.Error("an unusable cookie must drop every session")
	}

	if keptHash != "" && h.db.keptSession[idAliceUser] != keptHash {
		t.Errorf("kept session hash = %q, want %q", h.db.keptSession[idAliceUser], keptHash)
	}
}
