// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// orgAuthzFixture runs against real Postgres with a cache TTL (a minute) far
// longer than any test, so only the invalidation hooks can make a membership
// change visible.
type orgAuthzFixture struct {
	pg    *database.PostgresDB
	eval  *auth.Evaluator
	owner *models.User
	alice *models.User
	org   *models.Organization
	repo  *models.Repository
}

func newOrgAuthzFixture(t *testing.T) *orgAuthzFixture {
	t.Helper()

	pg := freshPostgresCfg(t, 5, func(c *database.DatabaseConfig) { c.AuthCacheTTLMillis = 60_000 })
	ctx := context.Background()

	owner, err := pg.CreateUser(ctx, &models.User{Username: "org-owner"})
	if err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}

	alice, err := pg.CreateUser(ctx, &models.User{Username: "org-alice"})
	if err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}

	org := &models.Organization{Name: "acme", OwnerID: &owner.ID, MemberRole: models.OrgAccessRead}
	if err := pg.CreateOrganization(ctx, org); err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}

	repo := &models.Repository{Name: "acme-raw", Format: "raw", RepoType: "hosted", OrgID: &org.ID}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	return &orgAuthzFixture{pg: pg, eval: auth.NewEvaluator(pg, nil, false), owner: owner, alice: alice, org: org, repo: repo}
}

func (f *orgAuthzFixture) principal(u *models.User) auth.Principal {
	id := u.ID

	return auth.Principal{UserID: &id, Username: u.Username, Credential: auth.CredentialSession}
}

// actions returns the subset of read/write/delete/manage u holds on repoName.
func (f *orgAuthzFixture) actions(t *testing.T, u *models.User, repoName string) string {
	t.Helper()

	var got strings.Builder

	for _, a := range []string{"read", "write", "delete", "manage"} {
		ok, err := f.eval.Evaluate(context.Background(), f.principal(u), repoName, "", a)
		if err != nil {
			t.Fatalf("Evaluate(%s): %v", a, err)
		}

		if ok {
			got.WriteString(a[:1])
		}
	}

	return got.String() // letters: r, w, d, m
}

func (f *orgAuthzFixture) expect(t *testing.T, step string, u *models.User, want string) {
	t.Helper()

	if got := f.actions(t, u, f.repo.Name); got != want {
		t.Fatalf("%s: actions = %q, want %q", step, got, want)
	}
}

func TestOrgAuthz_MigrationApplies(t *testing.T) {
	f := newOrgAuthzFixture(t)
	ctx := context.Background()

	got, err := f.pg.GetOrganizationByName(ctx, "acme")
	if err != nil || got == nil || got.MemberRole != models.OrgAccessRead {
		t.Fatalf("organization = %+v, %v; want member_role read", got, err)
	}

	sys, err := f.pg.GetOrganization(ctx, models.SystemOrgID)
	if err != nil || sys == nil || sys.MemberRole != models.OrgAccessRead {
		t.Fatalf("system organization = %+v, %v; want member_role read default", sys, err)
	}

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE organizations SET member_role = 'owner' WHERE id = $1`, f.org.ID); err == nil {
		t.Fatal("member_role CHECK accepted an invalid level")
	}

	// Repository names are global: the same name in another organization is rejected.
	dup := &models.Repository{Name: f.repo.Name, Format: "raw", RepoType: "hosted", OrgID: ptr(models.SystemOrgID)}
	if err := f.pg.CreateRepository(ctx, dup); !database.IsUniqueViolation(err) {
		t.Fatalf("duplicate repository name across organizations: err = %v, want unique violation", err)
	}
}

func ptr[T any](v T) *T { return new(v) }

func TestOrgAuthz_MembershipLifecycle(t *testing.T) {
	f := newOrgAuthzFixture(t)
	ctx := context.Background()

	f.expect(t, "non-member", f.alice, "")
	f.expect(t, "recorded owner without membership row", f.owner, "rwd")

	if err := f.pg.AddOrgMember(ctx, f.org.ID, f.alice.ID, "member"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	f.expect(t, "member with member_role read", f.alice, "r")

	f.org.MemberRole = models.OrgAccessWrite
	if err := f.pg.UpdateOrganization(ctx, f.org); err != nil {
		t.Fatalf("UpdateOrganization: %v", err)
	}

	f.expect(t, "member_role changed to write", f.alice, "rw")

	if err := f.pg.UpdateOrgMemberRole(ctx, f.org.ID, f.alice.ID, "admin"); err != nil {
		t.Fatalf("UpdateOrgMemberRole: %v", err)
	}

	f.expect(t, "org admin role", f.alice, "rwd")

	if err := f.pg.UpdateOrgMemberRole(ctx, f.org.ID, f.alice.ID, "member"); err != nil {
		t.Fatalf("UpdateOrgMemberRole: %v", err)
	}

	f.expect(t, "demoted to member (write)", f.alice, "rw")

	if err := f.pg.RemoveOrgMember(ctx, f.org.ID, f.alice.ID); err != nil {
		t.Fatalf("RemoveOrgMember: %v", err)
	}

	f.expect(t, "removed member", f.alice, "")
}

// TestOrgAuthz_RemovedMemberStaleWithoutInvalidation shows the cache would
// keep serving a removed member (writes bypassing the PostgresDB hooks are
// seen only after the TTL) and that the hooked write drops it.
func TestOrgAuthz_RemovedMemberStaleWithoutInvalidation(t *testing.T) {
	f := newOrgAuthzFixture(t)
	ctx := context.Background()

	if err := f.pg.AddOrgMember(ctx, f.org.ID, f.alice.ID, "member"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	f.expect(t, "member (warms the cache)", f.alice, "r")

	if _, err := f.pg.GetDB().ExecContext(ctx, `DELETE FROM organization_members WHERE org_id = $1 AND user_id = $2`, f.org.ID, f.alice.ID); err != nil {
		t.Fatalf("raw delete: %v", err)
	}

	f.expect(t, "raw delete without the invalidation hook (cached)", f.alice, "r")

	// Any hooked org write drops every cached lookup.
	if err := f.pg.UpdateOrganization(ctx, f.org); err != nil {
		t.Fatalf("UpdateOrganization: %v", err)
	}

	f.expect(t, "after a hooked write", f.alice, "")
}

func TestOrgAuthz_SystemOrgGrantsNothing(t *testing.T) {
	f := newOrgAuthzFixture(t)
	ctx := context.Background()

	sysRepo := &models.Repository{Name: "sys-raw", Format: "raw", RepoType: "hosted", OrgID: ptr(models.SystemOrgID)}
	if err := f.pg.CreateRepository(ctx, sysRepo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	if err := f.pg.AddOrgMember(ctx, models.SystemOrgID, f.alice.ID, "owner"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	if got := f.actions(t, f.alice, sysRepo.Name); got != "" {
		t.Fatalf("system org owner holds %q on a system-org repository, want none", got)
	}
}

func TestOrgAuthz_RepositoryListFilter(t *testing.T) {
	f := newOrgAuthzFixture(t)
	ctx := context.Background()

	sysRepo := &models.Repository{Name: "sys-raw", Format: "raw", RepoType: "hosted", OrgID: ptr(models.SystemOrgID)}
	if err := f.pg.CreateRepository(ctx, sysRepo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	visible := func() map[string]bool {
		filter, err := f.eval.RepoReadFilter(ctx, f.principal(f.alice))
		if err != nil {
			t.Fatalf("RepoReadFilter: %v", err)
		}

		repos, err := f.pg.ListRepositories(ctx, 1, 100, "")
		if err != nil {
			t.Fatalf("ListRepositories: %v", err)
		}

		out := map[string]bool{}

		for _, r := range repos {
			if ok, err := filter(r); err != nil {
				t.Fatalf("filter: %v", err)
			} else if ok {
				out[r.Name] = true
			}
		}

		return out
	}

	if got := visible(); len(got) != 0 {
		t.Fatalf("non-member sees %v", got)
	}

	if err := f.pg.AddOrgMember(ctx, f.org.ID, f.alice.ID, "member"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	if got := visible(); len(got) != 1 || !got["acme-raw"] {
		t.Fatalf("member sees %v, want only acme-raw", got)
	}
}

func TestOrgAuthz_RepositoryMoveAndOrgDelete(t *testing.T) {
	f := newOrgAuthzFixture(t)
	ctx := context.Background()

	if err := f.pg.AddOrgMember(ctx, f.org.ID, f.alice.ID, "member"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	f.expect(t, "member", f.alice, "r")

	// Delete is blocked while the organization owns a repository.
	if err := f.pg.DeleteOrganization(ctx, f.org.ID); !database.IsForeignKeyViolation(err) {
		t.Fatalf("DeleteOrganization with repositories: err = %v, want foreign key violation", err)
	}

	f.expect(t, "after blocked delete", f.alice, "r")

	// Moving the repository to the system organization removes the access.
	f.repo.OrgID = ptr(models.SystemOrgID)
	if err := f.pg.UpdateRepository(ctx, f.repo); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	f.expect(t, "repository moved to the system org", f.alice, "")

	// And back; then deleting the repository and the organization.
	f.repo.OrgID = &f.org.ID
	if err := f.pg.UpdateRepository(ctx, f.repo); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	f.expect(t, "repository moved back", f.alice, "r")

	if err := f.pg.DeleteRepository(ctx, f.repo.ID); err != nil {
		t.Fatalf("DeleteRepository: %v", err)
	}

	if err := f.pg.DeleteOrganization(ctx, f.org.ID); err != nil {
		t.Fatalf("DeleteOrganization: %v", err)
	}

	grants, err := f.pg.ListOrgGrantsByUser(ctx, f.alice.ID)
	if err != nil || len(grants) != 0 {
		t.Fatalf("grants after org delete = %v, %v", grants, err)
	}
}

// TestOrgAuthz_CreateOrganizationOverHTTP is the regression for POST
// /api/v1/orgs answering 500 on the real schema (slug NOT NULL was never set).
func TestOrgAuthz_CreateOrganizationOverHTTP(t *testing.T) {
	f := newOrgAuthzFixture(t)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	api.NewOrgsHandler(f.pg, log).RegisterRoutes(router)

	post := func(body string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/orgs", strings.NewReader(body))
		p := f.principal(f.owner)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &p))

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)

		return rw.Code
	}

	tests := []struct {
		name string
		body string
		want int
	}{
		{"first create", `{"name":"My_Org","member_role":"write"}`, http.StatusCreated},
		{"same name again", `{"name":"My_Org"}`, http.StatusConflict},
		{"slug-only collision", `{"name":"my-org"}`, http.StatusConflict},
		{"another org", `{"name":"other"}`, http.StatusCreated},
		{"bad name", `{"name":"no spaces"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		if got := post(tt.body); got != tt.want {
			t.Fatalf("%s: status = %d, want %d", tt.name, got, tt.want)
		}
	}

	org, err := f.pg.GetOrganizationByName(context.Background(), "My_Org")
	if err != nil || org == nil || org.Slug != "my-org" || org.MemberRole != models.OrgAccessWrite {
		t.Fatalf("stored organization = %+v, %v", org, err)
	}
}
