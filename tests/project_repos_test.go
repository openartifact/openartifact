// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Fixture users (see newPZFixture): olivia owns acme, mark is its maintainer,
// dana its developer, vic its viewer and beta's owner; otto and nina hold no
// role anywhere until a test grants one.
const (
	prAdmin  = "00000000-0000-4000-8000-0000000000a1"
	prOlivia = "00000000-0000-4000-8000-0000000000a2"
	prMark   = "00000000-0000-4000-8000-0000000000a3"
	prDana   = "00000000-0000-4000-8000-0000000000a4"
	prVic    = "00000000-0000-4000-8000-0000000000a5"
	prOtto   = "00000000-0000-4000-8000-0000000000a6"
	prNina   = "00000000-0000-4000-8000-0000000000a7"
)

// prDB adds repositories to the project fixture store.
type prDB struct {
	*pzDB

	repos   map[string]*models.Repository // by name
	created []*models.Repository
	deleted []string
	// extraTargets are permission targets of non-admin users.
	extraTargets map[string][]*models.PermissionTarget
}

func (d *prDB) GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groups []string) ([]*models.PermissionTarget, error) {
	if d.admins[userID] {
		return d.pzDB.GetPermissionTargetsForPrincipal(ctx, userID, groups)
	}

	return d.extraTargets[userID], nil
}

func (d *prDB) GetProject(_ context.Context, id string) (*models.Project, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if p := d.projects[id]; p != nil {
		c := *p
		return &c, nil
	}

	return nil, nil
}

func (d *prDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if r := d.repos[name]; r != nil {
		c := *r
		return &c, nil
	}

	return nil, nil
}

func (d *prDB) liveIn(projectID string) int {
	n := 0

	for _, r := range d.repos {
		if r.ProjectID != nil && *r.ProjectID == projectID {
			n++
		}
	}

	return n
}

func (d *prDB) insert(repo *models.Repository) {
	if d.repos == nil {
		d.repos = map[string]*models.Repository{}
	}

	repo.ID = "repo-" + repo.Name
	d.repos[repo.Name] = repo
	d.created = append(d.created, repo)
}

func (d *prDB) CreateRepositoryWithMembers(_ context.Context, repo *models.Repository, _ []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.insert(repo)

	return nil
}

func (d *prDB) CreateProjectRepository(_ context.Context, repo *models.Repository, _ []string, maxRepositories int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.liveIn(*repo.ProjectID) >= maxRepositories {
		return database.ErrProjectRepoLimit
	}

	d.insert(repo)

	return nil
}

func (d *prDB) MoveRepositoryToProject(_ context.Context, repoID, toProjectID string, maxRepositories int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if maxRepositories > 0 && d.liveIn(toProjectID) >= maxRepositories {
		return database.ErrProjectRepoLimit
	}

	for _, r := range d.repos {
		if r.ID == repoID {
			r.ProjectID = &toProjectID
			return nil
		}
	}

	return database.ErrRepositoryNotFound
}

func (d *prDB) UpdateRepository(_ context.Context, repo *models.Repository) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.repos[repo.Name] = repo

	return nil
}

func (d *prDB) DeleteRepository(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for name, r := range d.repos {
		if r.ID == id {
			d.deleted = append(d.deleted, name)
			delete(d.repos, name)
		}
	}

	return nil
}

func (d *prDB) GetRepoMetadataByRepo(context.Context, string) ([]models.RepoMetadata, error) {
	return nil, nil
}

func (d *prDB) ListCleanupPolicies(context.Context, string) ([]*models.CleanupPolicy, error) {
	return nil, nil
}

func (d *prDB) GetOrCreateStorageQuota(_ context.Context, repoID string) (*models.StorageQuota, error) {
	return &models.StorageQuota{RepositoryID: repoID, WarnThresholdPercent: 80}, nil
}

func (d *prDB) UpdateStorageQuota(context.Context, *models.StorageQuota) error { return nil }

// prFixture is the real RBAC middleware in front of the repository, cleanup and
// quota handlers. Project acme allows hosted npm/maven repositories named
// acme-*, at most 2; project beta has no name prefix and allows two types.
type prFixture struct {
	*pzFixture

	db   *prDB
	eval *auth.Evaluator
}

func newPRFixture(t *testing.T) *prFixture {
	t.Helper()

	base := newPZFixture(t)
	db := &prDB{pzDB: base.db, repos: map[string]*models.Repository{}, extraTargets: map[string][]*models.PermissionTarget{}}

	acme, beta := db.projects[pzAcme], db.projects[pzBeta]
	acme.AllowedFormats = []string{"npm", "maven"}
	acme.AllowedRepoTypes = []string{"hosted"}
	acme.RepoNamePrefix = "acme-"
	acme.MaxRepositories = 2
	beta.AllowedRepoTypes = []string{"hosted", "group"}
	beta.MaxRepositories = 3
	beta.AllowPublicRepos = false

	db.members[pzBeta][prOlivia] = "owner" // olivia owns both projects
	db.members[pzBeta][prMark] = "viewer"  // mark: maintainer of acme, viewer of beta
	db.members[pzBeta][prNina] = "maintainer"

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	eval := auth.NewEvaluator(db, log, false)

	router := chi.NewRouter()
	router.Use(middleware.RBACMiddleware(middleware.RBACConfig{}, db, eval, log))
	api.NewRepositoriesHandler(db, log, eval).RegisterAuthed(router)
	api.NewCleanupHandler(db, log, eval, nil).RegisterAuthed(router)
	api.NewQuotasHandler(log, db, eval).RegisterRoutes(router, "/api/v1/repositories/{name}")

	base.router = router

	return &prFixture{pzFixture: base, db: db, eval: eval}
}

func (f *prFixture) seed(name, projectID, format string, public bool) {
	f.db.insert(&models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &projectID, Public: public})
	f.db.created = nil
}

const npmBody = `{"name":"acme-web","format":"npm"}`

func TestProjectRepos_Create(t *testing.T) {
	tests := []struct {
		name   string
		user   string
		path   string
		body   string
		want   int
		seeded int
	}{
		{"maintainer creates npm repo", prMark, "/api/v1/projects/acme/repositories", npmBody, http.StatusCreated, 0},
		{"owner creates it too", prOlivia, "/api/v1/projects/acme/repositories", npmBody, http.StatusCreated, 0},
		{"admin creates it", prAdmin, "/api/v1/projects/acme/repositories", npmBody, http.StatusCreated, 0},
		{"developer is forbidden", prDana, "/api/v1/projects/acme/repositories", npmBody, http.StatusForbidden, 0},
		{"viewer is forbidden", prVic, "/api/v1/projects/acme/repositories", npmBody, http.StatusForbidden, 0},
		{"non-member sees 404", prOtto, "/api/v1/projects/acme/repositories", npmBody, http.StatusNotFound, 0},
		{"maintainer of beta on acme sees 404", prNina, "/api/v1/projects/acme/repositories", npmBody, http.StatusNotFound, 0},
		{"unknown project is 404", prMark, "/api/v1/projects/nope/repositories", npmBody, http.StatusNotFound, 0},
		{"format outside allowed_formats", prMark, "/api/v1/projects/acme/repositories", `{"name":"acme-img","format":"docker"}`, http.StatusBadRequest, 0},
		{"proxy type outside allowed_repo_types", prMark, "/api/v1/projects/acme/repositories", `{"name":"acme-px","format":"npm","repo_type":"proxy"}`, http.StatusBadRequest, 0},
		{"name without the prefix", prMark, "/api/v1/projects/acme/repositories", `{"name":"web","format":"npm"}`, http.StatusBadRequest, 0},
		{"public without allow_public_repos", prMark, "/api/v1/projects/acme/repositories", `{"name":"acme-pub","format":"npm","public":true}`, http.StatusForbidden, 0},
		{"project field in the body is ignored", prMark, "/api/v1/projects/acme/repositories", `{"name":"acme-web","format":"npm","project":"beta"}`, http.StatusCreated, 0},
		{"repository cap reached", prMark, "/api/v1/projects/acme/repositories", npmBody, http.StatusConflict, 2},
		{"admin bypasses the policy", prAdmin, "/api/v1/projects/acme/repositories", `{"name":"anything","format":"docker","public":true}`, http.StatusCreated, 0},
		{"unreadable member_repos", prNina, "/api/v1/projects/beta/repositories", `{"name":"grp","format":"npm","repo_type":"group","member_repos":["acme-secret"]}`, http.StatusForbidden, 0},
		{"readable member_repos", prNina, "/api/v1/projects/beta/repositories", `{"name":"grp","format":"npm","repo_type":"group","member_repos":["beta-lib"]}`, http.StatusCreated, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPRFixture(t)
			f.seed("acme-secret", pzAcme, "npm", false)
			f.seed("beta-lib", pzBeta, "npm", false)

			if tt.seeded > 0 {
				f.seed("acme-a", pzAcme, "npm", false)
			}

			out := f.expect(tt.user, http.MethodPost, tt.path, tt.body, tt.want)

			if tt.want == http.StatusCreated && out["project"] != projectNameOf(tt.path) {
				t.Fatalf("created in project %v, want %v", out["project"], projectNameOf(tt.path))
			}
		})
	}
}

func projectNameOf(path string) string {
	return strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")[0]
}

func TestProjectRepos_DelegatedRoutes(t *testing.T) {
	tests := []struct {
		name   string
		user   string
		method string
		path   string
		body   string
		want   int
	}{
		{"maintainer updates description", prMark, http.MethodPatch, "/api/v1/repository/acme-web", `{"description":"x"}`, http.StatusOK},
		{"owner updates description", prOlivia, http.MethodPatch, "/api/v1/repository/acme-web", `{"description":"x"}`, http.StatusOK},
		{"developer cannot update", prDana, http.MethodPatch, "/api/v1/repository/acme-web", `{"description":"x"}`, http.StatusForbidden},
		{"viewer cannot update", prVic, http.MethodPatch, "/api/v1/repository/acme-web", `{"description":"x"}`, http.StatusForbidden},
		{"non-member sees 404 on update", prOtto, http.MethodPatch, "/api/v1/repository/acme-web", `{"description":"x"}`, http.StatusNotFound},
		{"maintainer of another project sees 404 on update", prNina, http.MethodPatch, "/api/v1/repository/acme-web", `{"description":"x"}`, http.StatusNotFound},
		{"maintainer cannot move the repository", prMark, http.MethodPatch, "/api/v1/repository/acme-web", `{"project":"beta"}`, http.StatusForbidden},
		{"maintainer cannot block it", prMark, http.MethodPatch, "/api/v1/repository/acme-web", `{"blocked":true}`, http.StatusForbidden},
		{"maintainer cannot set presign", prMark, http.MethodPatch, "/api/v1/repository/acme-web", `{"presign_downloads":true}`, http.StatusForbidden},
		{"maintainer cannot make it public", prMark, http.MethodPatch, "/api/v1/repository/acme-web", `{"public":true}`, http.StatusForbidden},
		{"admin moves it", prAdmin, http.MethodPatch, "/api/v1/repository/acme-web", `{"project":"beta"}`, http.StatusOK},
		{"maintainer lists cleanup policies", prMark, http.MethodGet, "/api/v1/repositories/acme-web/cleanup-policies", "", http.StatusOK},
		{"developer cannot list cleanup policies", prDana, http.MethodGet, "/api/v1/repositories/acme-web/cleanup-policies", "", http.StatusForbidden},
		{"other project maintainer sees 404 on cleanup", prNina, http.MethodGet, "/api/v1/repositories/acme-web/cleanup-policies", "", http.StatusNotFound},
		{"maintainer sets quota", prMark, http.MethodPut, "/api/v1/repositories/acme-web/quota", `{"max_size_bytes":1048576}`, http.StatusOK},
		{"developer cannot set quota", prDana, http.MethodPut, "/api/v1/repositories/acme-web/quota", `{"max_size_bytes":1}`, http.StatusForbidden},
		{"maintainer cannot reconcile quota", prMark, http.MethodPost, "/api/v1/repositories/acme-web/quota/reconcile", "", http.StatusForbidden},
		{"maintainer deletes the repository", prMark, http.MethodDelete, "/api/v1/repository/acme-web?confirm=acme-web", "", http.StatusNoContent},
		{"developer cannot delete it", prDana, http.MethodDelete, "/api/v1/repository/acme-web?confirm=acme-web", "", http.StatusForbidden},
		{"other project maintainer sees 404 on delete", prNina, http.MethodDelete, "/api/v1/repository/acme-web?confirm=acme-web", "", http.StatusNotFound},
		{"maintainer cannot touch a system-project repo", prMark, http.MethodPatch, "/api/v1/repository/sys-repo", `{"description":"x"}`, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPRFixture(t)
			f.seed("acme-web", pzAcme, "npm", false)
			f.seed("sys-repo", models.SystemProjectID, "npm", true)

			f.expect(tt.user, tt.method, tt.path, tt.body, tt.want)
		})
	}
}

func TestProjectRepos_ScopedManageTargetDoesNotDelegate(t *testing.T) {
	f := newPRFixture(t)
	f.seed("acme-web", pzAcme, "npm", false)

	// otto holds repo-scoped manage (and read) on acme-web through a permission
	// target, with no project role. That never made him a repo admin.
	f.db.extraTargets[prOtto] = []*models.PermissionTarget{{
		ID: "t-scoped", RepositoryFilter: `^acme-web$`, PathPattern: `.*`, Actions: []string{"read", "manage"},
	}}

	f.expect(prOtto, http.MethodPatch, "/api/v1/repository/acme-web", `{"description":"x"}`, http.StatusForbidden)
	f.expect(prOtto, http.MethodGet, "/api/v1/repositories/acme-web/cleanup-policies", "", http.StatusForbidden)
	f.expect(prOtto, http.MethodPut, "/api/v1/repositories/acme-web/quota", `{"max_size_bytes":1}`, http.StatusForbidden)

	otto := auth.Principal{UserID: new(prOtto), Username: "otto"}

	admin, err := f.eval.EvaluateRepoAdmin(t.Context(), otto, "acme-web")
	if err != nil || admin {
		t.Fatalf("EvaluateRepoAdmin(scoped manage target) = %v, %v; want false", admin, err)
	}

	if ok, err := f.eval.Evaluate(t.Context(), otto, "acme-web", "", "manage"); err != nil || !ok {
		t.Fatalf("scoped manage target must keep its own manage right: %v, %v", ok, err)
	}

	mark := auth.Principal{UserID: new(prMark), Username: "mark"}

	if ok, err := f.eval.Evaluate(t.Context(), mark, "", "", "manage"); err != nil || ok {
		t.Fatalf("a project maintainer passed Evaluate(manage): %v, %v", ok, err)
	}

	if ok, err := f.eval.Evaluate(t.Context(), mark, "acme-web", "", "manage"); err != nil || ok {
		t.Fatalf("a project maintainer passed Evaluate(manage) on its repo: %v, %v", ok, err)
	}

	if ok, err := f.eval.EvaluateRepoAdmin(t.Context(), mark, "acme-web"); err != nil || !ok {
		t.Fatalf("EvaluateRepoAdmin(project maintainer) = %v, %v; want true", ok, err)
	}
}

func TestProjectRepos_PermissionsAdmin(t *testing.T) {
	tests := []struct {
		user string
		want bool
	}{
		{prAdmin, true}, {prOlivia, true}, {prMark, true}, {prDana, false}, {prVic, false},
	}

	for _, tt := range tests {
		f := newPRFixture(t)
		f.seed("acme-web", pzAcme, "npm", false)

		out := f.expect(tt.user, http.MethodGet, "/api/v1/repository/acme-web", "", http.StatusOK)

		perms, _ := out["permissions"].(map[string]any)
		if perms == nil || perms["admin"] != tt.want {
			t.Errorf("user %s: permissions = %v, want admin=%v", tt.user, perms, tt.want)
		}
	}
}

func TestProjectRepos_Transfer(t *testing.T) {
	const path = "/api/v1/projects/acme/repositories/acme-web/transfer"

	tests := []struct {
		name  string
		user  string
		body  string
		setup func(f *prFixture)
		want  int
	}{
		{"owner of both moves it", prOlivia, `{"to_project":"beta"}`, nil, http.StatusOK},
		{"admin moves it", prAdmin, `{"to_project":"beta"}`, nil, http.StatusOK},
		{"maintainer of source is forbidden", prMark, `{"to_project":"beta"}`, nil, http.StatusForbidden},
		{"owner of source only: target is hidden", prOlivia, `{"to_project":"beta"}`, func(f *prFixture) { delete(f.db.members[pzBeta], prOlivia) }, http.StatusNotFound},
		{"owner of source, maintainer of target is forbidden", prOlivia, `{"to_project":"beta"}`, func(f *prFixture) { f.db.members[pzBeta][prOlivia] = "maintainer" }, http.StatusForbidden},
		{"owner of target only: source is hidden", prVic, `{"to_project":"beta"}`, func(f *prFixture) { delete(f.db.members[pzAcme], prVic) }, http.StatusNotFound},
		{"viewer of source, owner of target is forbidden", prVic, `{"to_project":"beta"}`, nil, http.StatusForbidden},
		{"unknown target is hidden", prOlivia, `{"to_project":"nope"}`, nil, http.StatusNotFound},
		{"same project is invalid", prOlivia, `{"to_project":"acme"}`, nil, http.StatusBadRequest},
		{"missing target is invalid", prOlivia, `{}`, nil, http.StatusBadRequest},
		{"target cap reached", prOlivia, `{"to_project":"beta"}`, func(f *prFixture) {
			for _, n := range []string{"b1", "b2", "b3"} {
				f.seed(n, pzBeta, "npm", false)
			}
		}, http.StatusConflict},
		{"target prefix must fit", prOlivia, `{"to_project":"beta"}`, func(f *prFixture) { f.db.projects[pzBeta].RepoNamePrefix = "beta-" }, http.StatusBadRequest},
		{"target format must be allowed", prOlivia, `{"to_project":"beta"}`, func(f *prFixture) { f.db.projects[pzBeta].AllowedFormats = []string{"go"} }, http.StatusBadRequest},
		{"public repo into a private-only target", prOlivia, `{"to_project":"beta"}`, func(f *prFixture) { f.db.repos["acme-web"].Public = true }, http.StatusForbidden},
		{"repository of another project via this URL", prOlivia, `{"to_project":"beta"}`, func(f *prFixture) { f.db.repos["acme-web"].ProjectID = new(pzBeta) }, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPRFixture(t)
			f.seed("acme-web", pzAcme, "npm", false)

			if tt.setup != nil {
				tt.setup(f)
			}

			out := f.expect(tt.user, http.MethodPost, path, tt.body, tt.want)

			if tt.want == http.StatusOK {
				if out["project"] != "beta" || *f.db.repos["acme-web"].ProjectID != pzBeta {
					t.Fatalf("not moved: %v / %v", out["project"], *f.db.repos["acme-web"].ProjectID)
				}
			}
		})
	}
}

// TestProjectRepos_MemberReadsAfterCreate checks the member who created a
// repository can use it as a project member immediately (no permission target).
func TestProjectRepos_MemberReadsAfterCreate(t *testing.T) {
	f := newPRFixture(t)
	f.expect(prMark, http.MethodPost, "/api/v1/projects/acme/repositories", npmBody, http.StatusCreated)
	f.expect(prMark, http.MethodGet, "/api/v1/repository/acme-web", "", http.StatusOK)
	f.expect(prNina, http.MethodGet, "/api/v1/repository/acme-web", "", http.StatusForbidden)
}
