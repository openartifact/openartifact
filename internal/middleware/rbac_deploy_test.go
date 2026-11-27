// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// noProjectsDB is a benchRBACDB whose users hold no project role.
type noProjectsDB struct{ *benchRBACDB }

func (noProjectsDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

// deployRBAC serves RBACMiddleware for a user that holds `actions` on
// repositories matching filter only; the repositories are the group "grp"
// (deploy target "tgt" when withTarget) and the hosted "tgt". next records the
// deploy target RBAC authorized.
func deployRBAC(t *testing.T, filter string, actions []string, withTarget bool) (http.Handler, string, *string) {
	t.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	grp := &models.Repository{ID: "g", Name: "grp", Format: "raw", RepoType: "group"}
	if withTarget {
		grp.DeployTarget = "tgt"
	}

	now := time.Now()
	db := &benchRBACDB{
		targets: []*models.PermissionTarget{{ID: "p", RepositoryFilter: filter, PathPattern: ".*", Actions: actions}},
		repos: map[string]*models.Repository{
			"grp": grp, "tgt": {ID: "t", Name: "tgt", Format: "raw", RepoType: "hosted"},
		},
		tokens:   map[string]*models.Token{},
		users:    map[string]*models.User{"user-1": {ID: "user-1", Username: "jane"}},
		sessions: map[string]*models.Session{},
	}

	raw, tok, err := auth.CreateToken(t.Context(), db, auth.TokenKindAccess, "ci", "user-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	tok.LastUsedAt = &now
	db.tokens[tok.TokenHash] = tok

	authorized := new(string)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*authorized = GroupDeployFromContext(r.Context(), "grp")

		w.WriteHeader(http.StatusOK)
	})

	return RBACMiddleware(RBACConfig{}, db, auth.NewEvaluator(noProjectsDB{db}, log, false), log)(next), raw, authorized
}

func deployRequest(t *testing.T, h http.Handler, token, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	return rw
}

// A write through a group is authorized by the deploy target alone: a grant on
// the target is enough, a grant on the group is neither needed nor enough.
func TestRBACGroupDeployAuthorizesTheTargetOnly(t *testing.T) {
	cases := []struct {
		name       string
		filter     string
		withTarget bool
		method     string
		path       string
		want       int
		authorized string
	}{
		{"write on the target publishes through the group", "^tgt$", true, http.MethodPut, "/repos/grp/a.txt", http.StatusOK, "tgt"},
		{"isolated mount", "^tgt$", true, http.MethodPut, "/repos/raw/grp/a.txt", http.StatusOK, "tgt"},
		{"write on the group alone is not enough", "^grp$", true, http.MethodPut, "/repos/grp/a.txt", http.StatusForbidden, ""},
		{"without a target the group is what is checked", "^grp$", false, http.MethodPut, "/repos/grp/a.txt", http.StatusOK, ""},
		{"without a target a grant on it is no use", "^tgt$", false, http.MethodPut, "/repos/grp/a.txt", http.StatusForbidden, ""},
		{"a delete stays a delete on the group", "^tgt$", true, http.MethodDelete, "/repos/grp/a.txt", http.StatusForbidden, ""},
		{"npm unpublish stays on the group", "^tgt$", true, http.MethodPut, "/repos/grp/pkg/-rev/1-abc", http.StatusForbidden, ""},
		{"a read is the group's", "^grp$", true, http.MethodGet, "/repos/grp/a.txt", http.StatusOK, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, token, authorized := deployRBAC(t, c.filter, []string{"read", "write"}, c.withTarget)

			rw := deployRequest(t, h, token, c.method, c.path)
			if rw.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, c.want, rw.Body.String())
			}

			if *authorized != c.authorized {
				t.Errorf("authorized deploy target = %q, want %q", *authorized, c.authorized)
			}
		})
	}
}
