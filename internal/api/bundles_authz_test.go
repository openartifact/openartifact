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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	authzSrcID    = "aaaaaaaa-0000-4000-8000-000000000001"
	authzOtherID  = "aaaaaaaa-0000-4000-8000-000000000002"
	authzTargetID = "aaaaaaaa-0000-4000-8000-000000000003"
	authzPipeID   = "bbbbbbbb-0000-4000-8000-000000000001"
)

// authzMockDB is a small in-memory store for the bundle authorization matrix:
// three repositories, permission targets per user id and one bundle per name.
type authzMockDB struct {
	database.BaseMockDB

	repos   []*models.Repository
	targets map[string][]*models.PermissionTarget // by user id
	bundles map[string]*models.ReleaseBundle
	items   map[string][]*models.ReleaseBundleItem // by bundle id
	nextID  int
}

func newAuthzDB() *authzMockDB {
	return &authzMockDB{
		repos: []*models.Repository{
			{ID: authzSrcID, Name: "src", Format: "raw"},
			{ID: authzOtherID, Name: "other", Format: "raw"},
			{ID: authzTargetID, Name: "target", Format: "raw"},
		},
		targets: map[string][]*models.PermissionTarget{},
		bundles: map[string]*models.ReleaseBundle{},
		items:   map[string][]*models.ReleaseBundleItem{},
	}
}

func (d *authzMockDB) grant(userID, repoFilter string, actions ...string) {
	d.targets[userID] = append(d.targets[userID], &models.PermissionTarget{
		ID: userID + repoFilter, RepositoryFilter: repoFilter, PathPattern: ".*", Actions: actions,
	})
}

func (d *authzMockDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	return d.targets[userID], nil
}

func (d *authzMockDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

func (d *authzMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	for _, r := range d.repos {
		if r.Name == name {
			return r, nil
		}
	}

	return nil, nil
}

func (d *authzMockDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	for _, r := range d.repos {
		if r.ID == id {
			return r, nil
		}
	}

	return nil, nil
}

func (d *authzMockDB) ListRepositories(_ context.Context, page, limit int, _ string) ([]*models.Repository, error) {
	if page > 1 || limit < len(d.repos) {
		return nil, nil
	}

	return d.repos, nil
}

func (d *authzMockDB) GetPromotionPipelineByName(_ context.Context, name string) (*models.PromotionPipeline, error) {
	if name == "release" {
		return &models.PromotionPipeline{ID: authzPipeID, Name: name}, nil
	}

	return nil, nil
}

func (d *authzMockDB) GetPromotionPipelineByID(context.Context, string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (d *authzMockDB) CreateReleaseBundle(_ context.Context, b *models.ReleaseBundle) error {
	d.nextID++
	b.ID = "cccccccc-0000-4000-8000-00000000000" + string(rune('0'+d.nextID))
	d.bundles[b.Name+"/"+b.Version] = b

	return nil
}

func (d *authzMockDB) GetReleaseBundle(_ context.Context, name, version string) (*models.ReleaseBundle, error) {
	return d.bundles[name+"/"+version], nil
}

func (d *authzMockDB) ListBundleItems(_ context.Context, id string) ([]*models.ReleaseBundleItem, error) {
	return d.items[id], nil
}

func (d *authzMockDB) AddBundleItems(_ context.Context, id string, items []*models.ReleaseBundleItem) error {
	d.items[id] = append(d.items[id], items...)
	return nil
}

func (d *authzMockDB) SealReleaseBundle(_ context.Context, id, _ string, _ time.Time, _ *models.BundleSignature) (bool, error) {
	for _, b := range d.bundles {
		if b.ID == id {
			b.State = models.BundleStateSealed
		}
	}

	return true, nil
}

func (d *authzMockDB) UpdateReleaseBundle(context.Context, *models.ReleaseBundle) error { return nil }

func (d *authzMockDB) DeleteReleaseBundle(_ context.Context, id string) error {
	for k, b := range d.bundles {
		if b.ID == id {
			delete(d.bundles, k)
		}
	}

	return nil
}

func (d *authzMockDB) ListReleaseBundles(context.Context, string, string, int, int) ([]*models.ReleaseBundle, int, error) {
	return nil, 0, nil
}

func (d *authzMockDB) ListBundlePromotions(context.Context, string) ([]*models.BundlePromotion, error) {
	return nil, nil
}

type authzCaller struct {
	name string
	p    *auth.Principal
}

func authzUser(name string) authzCaller {
	id := name + "-id"

	return authzCaller{name: name, p: &auth.Principal{UserID: &id, Username: name, TokenKind: auth.TokenKindAccess}}
}

func authzAdmin() authzCaller {
	return authzCaller{name: "admin", p: &auth.Principal{Username: "admin", IsLegacy: true}}
}

func (c authzCaller) do(t *testing.T, h *BundlesHandler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), c.p)))
		})
	})
	h.RegisterAuthed(router)

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func newAuthzHandler(t *testing.T, db *authzMockDB) *BundlesHandler {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	return NewBundlesHandler(db, log, auth.NewEvaluator(db, log, false))
}

func authzItemBody(repoID string) string {
	return `{"items":[{"source_repo_id":"` + repoID + `","sha256":"` + strings.Repeat("a", 64) + `","artifact_path":"x.bin"}]}`
}

func expectStatus(t *testing.T, what string, rw *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rw.Code != want {
		t.Errorf("%s: status = %d, want %d (%s)", what, rw.Code, want, rw.Body.String())
	}
}

func TestBundleAuthzCreateAndOwnership(t *testing.T) {
	db := newAuthzDB()
	db.grant("ci-id", "src", "read")
	db.grant("ci-id", "target", "read", "write")
	h := newAuthzHandler(t, db)

	ci, nobody, other, admin := authzUser("ci"), authzUser("nobody"), authzUser("other"), authzAdmin()
	db.grant("other-id", "src", "read")

	// A caller that can read nothing may not create drafts.
	expectStatus(t, "create without any read access", nobody.do(t, h, http.MethodPost, "/api/v1/bundles", `{"name":"b","version":"1"}`), http.StatusForbidden)
	expectStatus(t, "create as ci", ci.do(t, h, http.MethodPost, "/api/v1/bundles", `{"name":"b","version":"1","pipeline":"release"}`), http.StatusCreated)

	if got := db.bundles["b/1"]; got == nil || got.CreatedBy != "ci" || got.PipelineID == nil || *got.PipelineID != authzPipeID {
		t.Fatalf("stored bundle = %+v, want created by ci with pipeline", got)
	}

	expectStatus(t, "unknown pipeline name", ci.do(t, h, http.MethodPost, "/api/v1/bundles", `{"name":"c","version":"1","pipeline":"nope"}`), http.StatusBadRequest)

	// The owner adds items from a repository it can read, but not from one it cannot.
	expectStatus(t, "add from readable repo", ci.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", authzItemBody(authzSrcID)), http.StatusCreated)

	rw := ci.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", `{"items":[{"source_repo_id":"`+authzOtherID+`","sha256":"`+strings.Repeat("b", 64)+`","artifact_path":"y"}]}`)
	expectStatus(t, "add from unreadable repo", rw, http.StatusBadRequest)

	if !strings.Contains(rw.Body.String(), "does not reference an existing repository") {
		t.Errorf("unreadable source repo must look unknown, got %s", rw.Body.String())
	}

	// Somebody who can read the sources sees the bundle but may not change it.
	expectStatus(t, "view by reader of sources", other.do(t, h, http.MethodGet, "/api/v1/bundles/b/1", ""), http.StatusOK)
	expectStatus(t, "items by reader of sources", other.do(t, h, http.MethodGet, "/api/v1/bundles/b/1/items", ""), http.StatusOK)
	expectStatus(t, "add items by non-owner", other.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", authzItemBody(authzSrcID)), http.StatusForbidden)
	expectStatus(t, "seal by non-owner", other.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/seal", ""), http.StatusForbidden)
	expectStatus(t, "delete by non-owner", other.do(t, h, http.MethodDelete, "/api/v1/bundles/b/1", ""), http.StatusForbidden)

	// Somebody who cannot read the sources does not learn the bundle exists.
	for _, path := range []string{"/api/v1/bundles/b/1", "/api/v1/bundles/b/1/items", "/api/v1/bundles/b/1/promotions", "/api/v1/bundles/b/1/manifest"} {
		expectStatus(t, "view "+path+" by stranger", nobody.do(t, h, http.MethodGet, path, ""), http.StatusNotFound)
	}

	expectStatus(t, "seal by stranger", nobody.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/seal", ""), http.StatusNotFound)

	// The list is administrative.
	expectStatus(t, "list by ci", ci.do(t, h, http.MethodGet, "/api/v1/bundles", ""), http.StatusForbidden)
	expectStatus(t, "list by admin", admin.do(t, h, http.MethodGet, "/api/v1/bundles", ""), http.StatusOK)

	// An administrator may reference any repository and change any bundle.
	expectStatus(t, "admin adds from any repo", admin.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", `{"items":[{"source_repo_id":"`+authzOtherID+`","sha256":"`+strings.Repeat("c", 64)+`","artifact_path":"z"}]}`), http.StatusCreated)

	// The owner lost read on a source repository added by an administrator: it
	// cannot seal what it cannot read.
	expectStatus(t, "seal without read on every source", ci.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/seal", ""), http.StatusForbidden)

	db.grant("ci-id", "other", "read")
	expectStatus(t, "seal by owner with reads", ci.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/seal", ""), http.StatusOK)

	if db.bundles["b/1"].State != models.BundleStateSealed {
		t.Errorf("state = %q, want sealed", db.bundles["b/1"].State)
	}
}

func TestBundleAuthzPipelinesStayAdministrative(t *testing.T) {
	db := newAuthzDB()
	db.grant("ci-id", ".*", "read", "write")
	h := newAuthzHandler(t, db)
	ci := authzUser("ci")

	expectStatus(t, "list pipelines", ci.do(t, h, http.MethodGet, "/api/v1/pipelines", ""), http.StatusForbidden)
	expectStatus(t, "get pipeline", ci.do(t, h, http.MethodGet, "/api/v1/pipelines/release", ""), http.StatusForbidden)
	expectStatus(t, "create pipeline", ci.do(t, h, http.MethodPost, "/api/v1/pipelines", `{"name":"p"}`), http.StatusForbidden)
}

func TestBundleAuthzPromote(t *testing.T) {
	db := newAuthzDB()
	h := newAuthzHandler(t, db)

	pipe := authzPipeID
	db.bundles["b/1"] = &models.ReleaseBundle{ID: "cccccccc-0000-4000-8000-000000000099", Name: "b", Version: "1", State: models.BundleStateSealed, PipelineID: &pipe, CreatedBy: "owner"}
	db.items[db.bundles["b/1"].ID] = []*models.ReleaseBundleItem{{SourceRepoID: authzSrcID, SHA256: strings.Repeat("a", 64)}}

	body := `{"to_stage":"prod","target_repo":"target"}`

	tests := []struct {
		name   string
		grants map[string][]string // repo filter -> actions
		user   string
		want   int
		hint   string
	}{
		{"read sources and write target", map[string][]string{"src": {"read"}, "target": {"write"}}, "u1", http.StatusConflict, "pipeline no longer exists"},
		{"write only on target", map[string][]string{"target": {"read", "write"}}, "u2", http.StatusNotFound, "bundle not found"},
		{"read sources only", map[string][]string{"src": {"read"}}, "u3", http.StatusNotFound, "target repository not found"},
		{"read sources and read target", map[string][]string{"src": {"read"}, "target": {"read"}}, "u4", http.StatusForbidden, "write access to the target"},
		{"write target but no read on sources", map[string][]string{"target": {"write"}}, "u5", http.StatusNotFound, "bundle not found"},
		{"no access", nil, "u6", http.StatusNotFound, "bundle not found"},
		{"owner without read on sources", map[string][]string{"target": {"write"}}, "owner", http.StatusForbidden, "read access to every source"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for filter, actions := range tt.grants {
				db.grant(tt.user+"-id", filter, actions...)
			}

			rw := authzUser(tt.user).do(t, h, http.MethodPost, "/api/v1/bundles/b/1/promote", body)
			expectStatus(t, tt.name, rw, tt.want)

			if !strings.Contains(rw.Body.String(), tt.hint) {
				t.Errorf("body %s lacks %q", rw.Body.String(), tt.hint)
			}
		})
	}
}
