// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// projectFilterWorld is three projects with one raw repository each, holding a file
// readme.md, plus a repository in the system project, and these callers:
//
//	alice  owner of acme, viewer of sara; reads "loose" (system project) and globex-docs
//	       through permission targets, without a role in globex
//	bob    no project role; reads acme-docs through a permission target only
//	carol  developer of globex through the group "reviewers"
//	boss   global admin (admin-all), no project role
//	svc    service account of acme (developer), also listed as a viewer of sara
//
// and alice again through a scoped token (scope: her "loose" target) and a deploy
// token, boss through a deploy token, a legacy API key principal and an anonymous caller.
type projectFilterWorld struct {
	handler   *Handler
	pg        *database.PostgresDB
	evaluator *auth.Evaluator
	log       *logger.Logger
	users     map[string]*auth.Principal
}

func newProjectFilterWorld(t *testing.T) *projectFilterWorld {
	t.Helper()

	ctx := context.Background()
	pg := newMigratedSearchPG(t)

	user := func(name string) *models.User {
		u, err := pg.CreateUser(ctx, &models.User{Username: name})
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}

		return u
	}
	alice, bob, carol, boss := user("alice"), user("bob"), user("carol"), user("boss")

	svc, err := pg.CreateUser(ctx, &models.User{Username: "acme-ci", Kind: models.UserKindServiceAccount})
	must(t, err)

	project := func(name string, owner *models.User) string {
		p := &models.Project{Name: name}
		if owner != nil {
			p.OwnerID = &owner.ID
		}

		if err := pg.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject(%s): %v", name, err)
		}

		return p.ID
	}
	acme, globex, sara := project("acme", alice), project("globex", nil), project("sara", nil)

	must(t, pg.AddProjectMember(ctx, acme, alice.ID, models.ProjectRoleOwner))
	must(t, pg.AddProjectMember(ctx, sara, alice.ID, models.ProjectRoleViewer))
	must(t, pg.AddProjectMember(ctx, acme, svc.ID, models.ProjectRoleDeveloper))
	must(t, pg.AddProjectMember(ctx, sara, svc.ID, models.ProjectRoleViewer))

	reviewers, err := pg.CreateGroup(ctx, &models.Group{Name: "reviewers"})
	must(t, err)
	must(t, pg.AddUserToGroup(ctx, carol.ID, reviewers.ID))
	must(t, pg.SetProjectGroup(ctx, globex, reviewers.ID, models.ProjectRoleDeveloper, nil))

	system := models.SystemProjectID
	for name, projectID := range map[string]*string{
		"acme-docs": &acme, "globex-docs": &globex, "sara-docs": &sara, "loose": &system,
	} {
		seedProjectRepo(t, pg, name, projectID)
	}

	grantRead(t, pg, "bob-acme", "acme-docs", bob.ID)
	aliceLoose := grantRead(t, pg, "alice-loose", "loose", alice.ID)
	grantRead(t, pg, "alice-globex", "globex-docs", alice.ID)

	adminAll, err := pg.GetPermissionTargetByName(ctx, "admin-all")
	must(t, err)
	must(t, pg.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: adminAll.ID, PrincipalType: "user", PrincipalID: boss.ID,
	}))

	log, err := logger.New("error", "json", "stderr")
	must(t, err)

	principal := func(u *models.User, groups ...string) *auth.Principal {
		return &auth.Principal{UserID: &u.ID, Username: u.Username, GroupIDs: groups, Credential: auth.CredentialSession}
	}

	token := func(u *models.User, kind string, scopes ...string) *auth.Principal {
		p := principal(u)
		p.Credential, p.TokenKind, p.TokenScopes = auth.CredentialBearerToken, kind, scopes

		return p
	}

	service := principal(svc)
	service.Credential, service.TokenKind, service.ServiceProjectID = auth.CredentialBearerToken, auth.TokenKindAccess, acme

	ev := auth.NewEvaluator(pg, log, false)

	return &projectFilterWorld{
		handler: NewHandler(pg.GetDB(), ev, log), pg: pg, evaluator: ev, log: log,
		users: map[string]*auth.Principal{
			"alice": principal(alice), "bob": principal(bob), "carol": principal(carol, reviewers.ID),
			"boss": principal(boss), "anonymous": {IsAnonymous: true},
			"alice-scoped": token(alice, auth.TokenKindAccess, aliceLoose),
			"alice-deploy": token(alice, auth.TokenKindDeploy),
			"boss-deploy":  token(boss, auth.TokenKindDeploy),
			"svc":          service,
			"legacy":       {IsLegacy: true, Username: "api-key", Credential: auth.CredentialLegacy},
		},
	}
}

func seedProjectRepo(t *testing.T, pg *database.PostgresDB, name string, projectID *string) {
	t.Helper()

	ctx := context.Background()
	repo := &models.Repository{Name: name, Format: "raw", RepoType: "hosted", ProjectID: projectID}
	must(t, pg.CreateRepository(ctx, repo))

	digest := sha256.Sum256([]byte(name))
	sum := hex.EncodeToString(digest[:])
	must(t, pg.UpsertRawArtifact(ctx, &models.RawArtifact{
		ID: uuid.NewString(), RepositoryID: repo.ID, Path: "readme.md", ContentType: "text/plain",
		SHA256: sum, BlobKey: sum, SizeBytes: 1,
	}))
}

// grantRead gives userID read on repo through a new permission target and returns its id.
func grantRead(t *testing.T, pg *database.PostgresDB, name, repo, userID string) string {
	t.Helper()

	ctx := context.Background()
	target, err := pg.CreatePermissionTarget(ctx, &models.PermissionTarget{
		Name: name, RepositoryFilter: repo, PathPattern: ".*", Actions: []string{"read"},
	})
	must(t, err)
	must(t, pg.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: target.ID, PrincipalType: "user", PrincipalID: userID,
	}))

	return target.ID
}

func must(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

// search runs GET /api/v1/search?<query> as who and returns the repositories of the
// package hits and the repository hits, sorted, plus the raw body.
func (w *projectFilterWorld) search(t *testing.T, who, query string) ([]string, []string, string) {
	t.Helper()

	var pkgs, repos []string

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/search?"+query, nil)
	r = r.WithContext(middleware.WithPrincipal(r.Context(), w.users[who]))
	rw := httptest.NewRecorder()
	w.handler.ServeHTTP(rw, r)

	if rw.Code != http.StatusOK {
		t.Fatalf("%s %s: status %d %s", who, query, rw.Code, rw.Body.String())
	}

	var resp struct {
		Packages []struct {
			Repository string `json:"repository"`
		} `json:"packages"`
		Repositories []struct {
			Name string `json:"name"`
		} `json:"repositories"`
	}
	must(t, json.Unmarshal(rw.Body.Bytes(), &resp))

	for _, p := range resp.Packages {
		pkgs = append(pkgs, p.Repository)
	}

	for _, r := range resp.Repositories {
		repos = append(repos, r.Name)
	}

	slices.Sort(pkgs)
	slices.Sort(repos)

	return pkgs, repos, rw.Body.String()
}

func TestSearchProjectFilters(t *testing.T) {
	w := newProjectFilterWorld(t)

	for _, tc := range []struct {
		name, who, query string
		want             []string // repositories of the package hits for q=readme
	}{
		{"no filter: everything alice can read", "alice", "q=readme", []string{"acme-docs", "globex-docs", "loose", "sara-docs"}},
		{"readable through a permission target, but no role there", "alice", "q=readme&project=globex", nil},
		{"project", "alice", "q=readme&project=acme", []string{"acme-docs"}},
		{"project qualifier", "alice", "q=readme+project:sara", []string{"sara-docs"}},
		{"two projects", "alice", "q=readme&project=acme&project=sara", []string{"acme-docs", "sara-docs"}},
		{"shared with me", "alice", "q=readme&project_role=viewer", []string{"sara-docs"}},
		{"project_role qualifier", "alice", "q=readme+project_role:owner", []string{"acme-docs"}},
		{"mine", "alice", "q=readme&project_role=developer&project_role=maintainer&project_role=owner", []string{"acme-docs"}},
		{"both filters must hold", "alice", "q=readme&project=acme&project_role=viewer", nil},
		{"system project never matches", "alice", "q=readme&project=system", nil},
		{"permission target only: no project role", "bob", "q=readme&project=acme", nil},
		{"permission target only: not shared", "bob", "q=readme&project_role=viewer", nil},
		{"group grant", "carol", "q=readme&project_role=developer", []string{"globex-docs"}},
		{"group grant by project", "carol", "q=readme&project=globex", []string{"globex-docs"}},
		{"admin is owner everywhere", "boss", "q=readme&project_role=owner", []string{"acme-docs", "globex-docs", "sara-docs"}},
		{"admin: viewer finds nothing, deliberately", "boss", "q=readme&project_role=viewer", nil},
		{"admin: any project", "boss", "q=readme&project=globex", []string{"globex-docs"}},
		{"anonymous", "anonymous", "q=readme&project=acme", nil},
		{"scoped token: unfiltered, only its scope", "alice-scoped", "q=readme", []string{"loose"}},
		{"scoped token: no project role", "alice-scoped", "q=readme&project_role=owner", nil},
		{"scoped token: by project", "alice-scoped", "q=readme&project=acme", nil},
		{"deploy token: no project role", "alice-deploy", "q=readme&project_role=owner", nil},
		{"deploy token: by project", "alice-deploy", "q=readme&project=acme", nil},
		{"deploy token of an admin: not an owner anywhere", "boss-deploy", "q=readme&project_role=owner", nil},
		{"service account: its own project", "svc", "q=readme&project_role=developer", []string{"acme-docs"}},
		{"service account: other memberships ignored", "svc", "q=readme&project_role=viewer", nil},
		{"service account: other project", "svc", "q=readme&project=sara", nil},
		{"legacy API key: owner everywhere", "legacy", "q=readme&project_role=owner", []string{"acme-docs", "globex-docs", "sara-docs"}},
		{"legacy API key: viewer finds nothing", "legacy", "q=readme&project_role=viewer", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkgs, _, body := w.search(t, tc.who, tc.query)
			if !slices.Equal(pkgs, tc.want) {
				t.Errorf("package hits in %v, want %v (%s)", pkgs, tc.want, body)
			}
		})
	}
}

// TestSearchProjectFilterRepositoryHits: the repository hits honour the same filters.
func TestSearchProjectFilterRepositoryHits(t *testing.T) {
	w := newProjectFilterWorld(t)

	_, repos, _ := w.search(t, "alice", "q=docs&project_role=viewer")
	if !slices.Equal(repos, []string{"sara-docs"}) {
		t.Errorf("repository hits %v, want [sara-docs]", repos)
	}

	_, repos, _ = w.search(t, "alice", "q=docs")
	if !slices.Equal(repos, []string{"acme-docs", "globex-docs", "sara-docs"}) {
		t.Errorf("unfiltered repository hits %v, want [acme-docs globex-docs sara-docs]", repos)
	}
}

// TestSearchUnknownAndForeignProjectLookTheSame: a project that does not exist and one
// the caller has no role in give the same answer, so the filter can't probe projects.
func TestSearchUnknownAndForeignProjectLookTheSame(t *testing.T) {
	w := newProjectFilterWorld(t)

	_, _, foreign := w.search(t, "alice", "q=readme&project=globex")
	_, _, missing := w.search(t, "alice", "q=readme&project=does-not-exist")

	if foreign != missing {
		t.Errorf("project without a role answered %s, missing project %s; want the same", foreign, missing)
	}

	if foreign != `{"packages":[],"repositories":[],"total":0}`+"\n" {
		t.Errorf("answer %s, want empty lists", foreign)
	}
}

// TestProjectScopeUsesProjects: project:<project> is suggested on query_too_broad only
// to callers that hold a project role.
func TestProjectScopeUsesProjects(t *testing.T) {
	w := newProjectFilterWorld(t)
	scope := NewProjectScope(w.pg.GetDB(), w.evaluator)

	for who, want := range map[string]bool{
		"alice": true, "carol": true, "boss": true, "svc": true, "legacy": true,
		"bob": false, "anonymous": false, "alice-deploy": false, "boss-deploy": false, "alice-scoped": false,
	} {
		got, err := scope.UsesProjects(middleware.WithPrincipal(context.Background(), w.users[who]))
		if err != nil || got != want {
			t.Errorf("%s: UsesProjects = %v, %v; want %v", who, got, err, want)
		}
	}
}

// TestSearchProjectFilterKeepsCachedReadableSet: the readable set is cached per
// principal and shared between requests; a filtered request must not shrink it for
// the next unfiltered one.
func TestSearchProjectFilterKeepsCachedReadableSet(t *testing.T) {
	w := newProjectFilterWorld(t)
	w.handler = NewHandler(w.pg.GetDB(), w.evaluator, w.log, WithAuthCache(&fakeAuthCache{ttl: time.Hour}))

	all := []string{"acme-docs", "globex-docs", "loose", "sara-docs"}

	if pkgs, _, _ := w.search(t, "alice", "q=readme"); !slices.Equal(pkgs, all) {
		t.Fatalf("first unfiltered search %v, want %v", pkgs, all)
	}

	if pkgs, _, _ := w.search(t, "alice", "q=readme&project_role=viewer"); !slices.Equal(pkgs, []string{"sara-docs"}) {
		t.Fatalf("filtered search %v, want [sara-docs]", pkgs)
	}

	if pkgs, _, _ := w.search(t, "alice", "q=readme"); !slices.Equal(pkgs, all) {
		t.Errorf("unfiltered search after a filtered one %v, want %v: the cached set was modified", pkgs, all)
	}
}
