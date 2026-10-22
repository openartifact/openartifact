// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// tokenStore backs the project token methods of projectsMockDB.
type tokenStore struct {
	tokens []*models.Token
}

func (db *projectsMockDB) CreateProjectToken(_ context.Context, spec database.ProjectTokenSpec) (*models.Token, error) {
	active := 0

	for _, t := range db.tokenStore.tokens {
		if t.ProjectID != nil && *t.ProjectID == spec.ProjectID && t.RevokedAt == nil {
			active++

			if t.Name == spec.Name {
				return nil, database.ErrProjectTokenName
			}
		}
	}

	if spec.MaxActive > 0 && active >= spec.MaxActive {
		return nil, database.ErrProjectTokenLimit
	}

	n := len(db.tokenStore.tokens) + 1
	tok := &models.Token{
		ID: fmt.Sprintf("0a000000-0000-4000-8000-%012d", n), UserID: fmt.Sprintf("svc-%d", n), Name: spec.Name, Kind: auth.TokenKindDeploy,
		TokenHash: spec.TokenHash, ExpiresAt: spec.ExpiresAt, CreatedAt: time.Now(),
		ProjectID: &spec.ProjectID, CreatedBy: &spec.CreatedBy, ProjectRole: &spec.Role,
		CreatedByName: testUsernames[spec.CreatedBy], ProjectName: "acme",
	}
	db.tokenStore.tokens = append(db.tokenStore.tokens, tok)

	return tok, nil
}

func (db *projectsMockDB) ListProjectTokens(_ context.Context, projectID string, createdBy *string) ([]*models.Token, error) {
	var out []*models.Token

	for _, t := range db.tokenStore.tokens {
		if t.ProjectID != nil && *t.ProjectID == projectID && t.RevokedAt == nil && (createdBy == nil || (t.CreatedBy != nil && *t.CreatedBy == *createdBy)) {
			out = append(out, t)
		}
	}

	return out, nil
}

func (db *projectsMockDB) GetTokenByID(_ context.Context, id string) (*models.Token, error) {
	for _, t := range db.tokenStore.tokens {
		if t.ID == id {
			return t, nil
		}
	}

	return nil, nil
}

func (db *projectsMockDB) RevokeProjectToken(_ context.Context, projectID, tokenID string) (bool, error) {
	for _, t := range db.tokenStore.tokens {
		if t.ID == tokenID && t.ProjectID != nil && *t.ProjectID == projectID && t.RevokedAt == nil {
			now := time.Now()
			t.RevokedAt = &now

			return true, nil
		}
	}

	return false, nil
}

func sessionPrincipal(userID string) *auth.Principal {
	p := testPrincipal(userID)
	p.Credential = auth.CredentialSession

	return p
}

func inDays(n int) string {
	return time.Now().AddDate(0, 0, n).UTC().Format(time.RFC3339)
}

func mintBody(name, role string, expiresInDays int) string {
	return fmt.Sprintf(`{"name":%q,"role":%q,"expires_at":%q}`, name, role, inDays(expiresInDays))
}

func TestProjectTokenCreate(t *testing.T) {
	tests := []struct {
		name      string
		principal *auth.Principal
		body      string
		status    int
	}{
		{"owner mints developer", sessionPrincipal(uOwner1), mintBody("ci", "developer", 10), http.StatusCreated},
		{"maintainer mints developer", sessionPrincipal(uMaint1), mintBody("ci", "developer", 10), http.StatusCreated},
		{"developer mints developer", sessionPrincipal(uDev1), mintBody("ci", "developer", 10), http.StatusCreated},
		{"developer mints viewer", sessionPrincipal(uDev1), mintBody("ci", "viewer", 10), http.StatusCreated},
		{"viewer mints viewer", sessionPrincipal(uView1), mintBody("ci", "viewer", 10), http.StatusCreated},
		{"admin mints developer", sessionPrincipal(uAdmin1), mintBody("ci", "developer", 10), http.StatusCreated},
		{"viewer cannot mint developer", sessionPrincipal(uView1), mintBody("ci", "developer", 10), http.StatusForbidden},
		{"maintainer role is not a token role", sessionPrincipal(uOwner1), mintBody("ci", "maintainer", 10), http.StatusBadRequest},
		{"owner role is not a token role", sessionPrincipal(uOwner1), mintBody("ci", "owner", 10), http.StatusBadRequest},
		{"missing role", sessionPrincipal(uOwner1), `{"name":"ci","expires_at":"` + inDays(5) + `"}`, http.StatusBadRequest},
		{"missing name", sessionPrincipal(uOwner1), mintBody("  ", "developer", 10), http.StatusBadRequest},
		{"long name", sessionPrincipal(uOwner1), mintBody(strings.Repeat("x", 101), "developer", 10), http.StatusBadRequest},
		{"expiry required by the project cap", sessionPrincipal(uOwner1), `{"name":"ci","role":"developer"}`, http.StatusBadRequest},
		{"expiry beyond the cap", sessionPrincipal(uOwner1), mintBody("ci", "developer", 31), http.StatusBadRequest},
		{"expiry in the past", sessionPrincipal(uOwner1), mintBody("ci", "developer", -1), http.StatusBadRequest},
		{"non-member", sessionPrincipal(uNewbie), mintBody("ci", "viewer", 10), http.StatusNotFound},
		{"personal access token cannot mint", func() *auth.Principal {
			p := testPrincipal(uOwner1)
			p.TokenKind = auth.TokenKindAccess
			p.Credential = auth.CredentialBearerToken

			return p
		}(), mintBody("ci", "developer", 10), http.StatusForbidden},
		{"deploy token cannot mint", deployPrincipal(uOwner1), mintBody("ci", "developer", 10), http.StatusNotFound},
		{"anonymous", nil, mintBody("ci", "developer", 10), http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newProjectsMock()
			router := newProjectsRouter(t, db)

			status, out := projectJSONAs(t, router, tc.principal, http.MethodPost, "/api/v1/projects/acme/tokens", tc.body)
			if status != tc.status {
				t.Fatalf("status = %d, want %d (%v)", status, tc.status, out)
			}

			if tc.status != http.StatusCreated {
				if len(db.tokenStore.tokens) != 0 {
					t.Fatal("a rejected request created a token")
				}

				return
			}

			secret, _ := out["token"].(string)
			if !strings.HasPrefix(secret, auth.TokenPrefixDeploy) {
				t.Fatalf("token = %q, want a deploy token", secret)
			}

			stored := db.tokenStore.tokens[0]
			if stored.TokenHash == "" || strings.Contains(stored.TokenHash, secret) || out["project"] != "acme" {
				t.Fatalf("stored hash %q / response %v", stored.TokenHash, out)
			}

			if *stored.CreatedBy != *tc.principal.UserID {
				t.Fatalf("created_by = %s, want the caller", *stored.CreatedBy)
			}
		})
	}
}

func TestProjectTokenNoExpiryWhenUncapped(t *testing.T) {
	db := newProjectsMock()
	db.projects["acme"].TokenMaxTTLDays = 0

	status, out := projectJSONAs(t, newProjectsRouter(t, db), sessionPrincipal(uOwner1), http.MethodPost,
		"/api/v1/projects/acme/tokens", `{"name":"forever","role":"viewer"}`)
	if status != http.StatusCreated || out["expires_at"] != nil {
		t.Fatalf("status = %d, body %v", status, out)
	}
}

func TestProjectTokenConflicts(t *testing.T) {
	db := newProjectsMock()
	router := newProjectsRouter(t, db)
	owner := sessionPrincipal(uOwner1)

	if status, out := projectJSONAs(t, router, owner, http.MethodPost, "/api/v1/projects/acme/tokens", mintBody("ci", "developer", 5)); status != http.StatusCreated {
		t.Fatalf("first mint = %d %v", status, out)
	}

	if status, _ := projectJSONAs(t, router, owner, http.MethodPost, "/api/v1/projects/acme/tokens", mintBody("ci", "viewer", 5)); status != http.StatusConflict {
		t.Fatalf("duplicate name = %d, want 409", status)
	}

	for i := 1; i < maxProjectTokens; i++ {
		if status, out := projectJSONAs(t, router, owner, http.MethodPost, "/api/v1/projects/acme/tokens", mintBody(fmt.Sprintf("t%d", i), "viewer", 5)); status != http.StatusCreated {
			t.Fatalf("mint %d = %d %v", i, status, out)
		}
	}

	if status, _ := projectJSONAs(t, router, owner, http.MethodPost, "/api/v1/projects/acme/tokens", mintBody("one-too-many", "viewer", 5)); status != http.StatusConflict {
		t.Fatalf("101st token = %d, want 409", status)
	}
}

func TestProjectTokenListAndRevoke(t *testing.T) {
	db := newProjectsMock()
	router := newProjectsRouter(t, db)

	mint := func(user, name string) string {
		t.Helper()

		status, out := projectJSONAs(t, router, sessionPrincipal(user), http.MethodPost, "/api/v1/projects/acme/tokens", mintBody(name, "viewer", 5))
		if status != http.StatusCreated {
			t.Fatalf("mint %s = %d %v", name, status, out)
		}

		id, _ := out["id"].(string)

		return id
	}

	devTok := mint(uDev1, "dev-token")
	viewTok := mint(uView1, "view-token")

	listed := func(user string) []string {
		t.Helper()

		status, out := projectJSONAs(t, router, sessionPrincipal(user), http.MethodGet, "/api/v1/projects/acme/tokens", "")
		if status != http.StatusOK {
			t.Fatalf("list as %s = %d", user, status)
		}

		var names []string

		items, _ := out["tokens"].([]any)
		for _, it := range items {
			m, _ := it.(map[string]any)
			names = append(names, m["name"].(string))

			if _, leaked := m["token"]; leaked {
				t.Fatal("list leaked a token secret")
			}
		}

		return names
	}

	if got := listed(uDev1); len(got) != 1 || got[0] != "dev-token" {
		t.Fatalf("developer sees %v, want only its own token", got)
	}

	if got := listed(uMaint1); len(got) != 2 {
		t.Fatalf("maintainer sees %v, want both tokens", got)
	}

	if got := listed(uAdmin1); len(got) != 2 {
		t.Fatalf("admin sees %v, want both tokens", got)
	}

	del := func(user, id string) int {
		status, _ := projectJSONAs(t, router, sessionPrincipal(user), http.MethodDelete, "/api/v1/projects/acme/tokens/"+id, "")

		return status
	}

	if got := del(uView1, devTok); got != http.StatusForbidden {
		t.Fatalf("viewer revokes another's token = %d, want 403", got)
	}

	if got := del(uNewbie, devTok); got != http.StatusNotFound {
		t.Fatalf("non-member revokes = %d, want 404", got)
	}

	if got := del(uView1, viewTok); got != http.StatusNoContent {
		t.Fatalf("creator revokes own token = %d, want 204", got)
	}

	if got := del(uView1, viewTok); got != http.StatusNotFound {
		t.Fatalf("revoking twice = %d, want 404", got)
	}

	if got := del(uMaint1, devTok); got != http.StatusNoContent {
		t.Fatalf("maintainer revokes any token = %d, want 204", got)
	}

	if got := del(uOwner1, "not-a-uuid"); got != http.StatusNotFound {
		t.Fatalf("malformed id = %d, want 404", got)
	}

	if got := listed(uMaint1); len(got) != 0 {
		t.Fatalf("revoked tokens still listed: %v", got)
	}
}

// TestProjectTokenRevokeIsProjectScoped: a token of another project is not
// reachable through this project's URL.
func TestProjectTokenRevokeIsProjectScoped(t *testing.T) {
	db := newProjectsMock()
	router := newProjectsRouter(t, db)

	other := "other-project"
	foreign := &models.Token{ID: "0b000000-0000-4000-8000-000000000001", Name: "x", ProjectID: &other}
	db.tokenStore.tokens = append(db.tokenStore.tokens, foreign)

	status, _ := projectJSONAs(t, router, sessionPrincipal(uOwner1), http.MethodDelete, "/api/v1/projects/acme/tokens/"+foreign.ID, "")
	if status != http.StatusNotFound || foreign.RevokedAt != nil {
		t.Fatalf("cross-project revoke = %d (revoked: %v), want 404 untouched", status, foreign.RevokedAt != nil)
	}
}

func TestProjectTokenAudit(t *testing.T) {
	db := newProjectsMock()
	finish := captureAudit(t, db)
	router := newProjectsRouter(t, db)
	owner := sessionPrincipal(uOwner1)

	_, out := projectJSONAs(t, router, owner, http.MethodPost, "/api/v1/projects/acme/tokens", mintBody("ci", "developer", 5))
	id, _ := out["id"].(string)

	if status, _ := projectJSONAs(t, router, owner, http.MethodDelete, "/api/v1/projects/acme/tokens/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("revoke = %d", status)
	}

	rows := finish()
	if len(rows) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(rows))
	}

	for i, action := range []string{"project.token_created", "project.token_revoked"} {
		r := rows[i]
		if r.Action != action || deref(r.ProjectID) != testAcmeID || deref(r.ResourceType) != "project_token" || deref(r.ResourceID) != id {
			t.Fatalf("row %d = %s project=%s type=%s resource=%s", i, r.Action, deref(r.ProjectID), deref(r.ResourceType), deref(r.ResourceID))
		}
	}
}
