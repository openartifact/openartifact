// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

var errGuardVeto = errors.New("guard veto")

// mustMember sets a member's role without a guard.
func (f *projectAuthzFixture) mustMember(t *testing.T, u *models.User, role string) {
	t.Helper()

	if err := f.pg.ChangeProjectMember(context.Background(), f.project.ID, u.ID, role, nil); err != nil {
		t.Fatalf("ChangeProjectMember(%s, %q): %v", u.Username, role, err)
	}
}

func (f *projectAuthzFixture) memberRole(t *testing.T, u *models.User) string {
	t.Helper()

	m, err := f.pg.GetProjectMember(context.Background(), f.project.ID, u.ID)
	if err != nil {
		t.Fatalf("GetProjectMember: %v", err)
	}

	if m == nil {
		return ""
	}

	return m.Role
}

func (f *projectAuthzFixture) countOwners(t *testing.T) int {
	t.Helper()

	var n int
	if err := f.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM project_members WHERE project_id = $1 AND role = 'owner'`, f.project.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

func TestProjectChange_MemberGuard(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	f.mustMember(t, f.alice, models.ProjectRoleDeveloper)

	// A veto leaves nothing changed and is returned unchanged.
	err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleOwner, func(string) error { return errGuardVeto })
	if !errors.Is(err, errGuardVeto) {
		t.Fatalf("err = %v, want guard veto", err)
	}

	if got := f.memberRole(t, f.alice); got != models.ProjectRoleDeveloper {
		t.Fatalf("role after veto = %q", got)
	}

	err = f.pg.ChangeProjectMember(ctx, f.project.ID, f.alice.ID, "", func(string) error { return errGuardVeto })
	if !errors.Is(err, errGuardVeto) || f.memberRole(t, f.alice) != models.ProjectRoleDeveloper {
		t.Fatalf("vetoed removal: err = %v, role = %q", err, f.memberRole(t, f.alice))
	}

	// The guard sees the current role: "" for a non-member.
	var seen []string

	record := func(cur string) error { seen = append(seen, cur); return nil }

	if err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.owner.ID, models.ProjectRoleOwner, record); err != nil {
		t.Fatal(err)
	}

	if err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleMaintainer, record); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 2 || seen[1] != models.ProjectRoleDeveloper {
		t.Fatalf("guard saw %q", seen)
	}

	if seen[0] != "" && seen[0] != models.ProjectRoleOwner {
		t.Fatalf("guard saw %q for the owner", seen[0])
	}

	if got := f.memberRole(t, f.alice); got != models.ProjectRoleMaintainer {
		t.Fatalf("role = %q", got)
	}
}

func TestProjectChange_OwnerInvariants(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	// The recorded owner holds an owner membership: locked even with another owner around.
	f.mustMember(t, f.owner, models.ProjectRoleOwner)

	for _, role := range []string{models.ProjectRoleMaintainer, ""} {
		err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.owner.ID, role, nil)
		if !errors.Is(err, database.ErrProjectOwnerLocked) {
			t.Fatalf("recorded owner -> %q: err = %v, want ErrProjectOwnerLocked", role, err)
		}
	}

	// A second owner can be demoted; the recorded owner stays the last owner.
	f.mustMember(t, f.alice, models.ProjectRoleOwner)
	f.mustMember(t, f.alice, models.ProjectRoleDeveloper)

	if got := f.countOwners(t); got != 1 {
		t.Fatalf("owners = %d", got)
	}

	// Re-asserting the same role is not a demotion.
	f.mustMember(t, f.owner, models.ProjectRoleOwner)
}

func TestProjectChange_LastOwnerProtected(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	// Detach the recorded owner so only the invariant on owner rows applies.
	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE projects SET owner_id = NULL WHERE id = $1`, f.project.ID); err != nil {
		t.Fatal(err)
	}

	f.mustMember(t, f.alice, models.ProjectRoleOwner)

	for _, role := range []string{models.ProjectRoleMaintainer, ""} {
		err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.alice.ID, role, nil)
		if !errors.Is(err, database.ErrLastProjectOwner) {
			t.Fatalf("last owner -> %q: err = %v, want ErrLastProjectOwner", role, err)
		}
	}

	if got := f.memberRole(t, f.alice); got != models.ProjectRoleOwner {
		t.Fatalf("role = %q", got)
	}

	// With a second owner the first one can go.
	f.mustMember(t, f.owner, models.ProjectRoleOwner)
	f.mustMember(t, f.alice, models.ProjectRoleViewer)
}

func TestProjectChange_ConcurrentDemotion(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE projects SET owner_id = NULL WHERE id = $1`, f.project.ID); err != nil {
		t.Fatal(err)
	}

	f.mustMember(t, f.owner, models.ProjectRoleOwner)
	f.mustMember(t, f.alice, models.ProjectRoleOwner)

	users := []*models.User{f.owner, f.alice}
	errs := make([]error, len(users))

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
	)

	for i, u := range users {
		wg.Go(func() {
			<-start

			errs[i] = f.pg.ChangeProjectMember(ctx, f.project.ID, u.ID, models.ProjectRoleViewer, nil)
		})
	}

	close(start)
	wg.Wait()

	ok := 0

	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, database.ErrLastProjectOwner):
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if ok != 1 || f.countOwners(t) != 1 {
		t.Fatalf("successful demotions = %d, owners left = %d; want 1 and 1", ok, f.countOwners(t))
	}
}

func TestProjectChange_UnknownProject(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	const missing = "99999999-9999-9999-9999-999999999999"

	if err := f.pg.ChangeProjectMember(
		ctx,
		missing,
		f.alice.ID,
		models.ProjectRoleViewer,
		nil,
	); !errors.Is(
		err,
		database.ErrProjectNotFound,
	) {
		t.Fatalf("member: err = %v", err)
	}

	g := f.newGroup(t, "ghosts")
	if err := f.pg.ChangeProjectGroup(
		ctx,
		missing,
		g.ID,
		models.ProjectRoleViewer,
		nil,
		nil,
	); !errors.Is(
		err,
		database.ErrProjectNotFound,
	) {
		t.Fatalf("group: err = %v", err)
	}
}

func TestProjectChange_Group(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()
	g := f.newGroup(t, "devs")

	var seen []string

	record := func(cur string) error { seen = append(seen, cur); return nil }

	steps := []struct {
		role string
		want string
	}{
		{models.ProjectRoleViewer, "r"},
		{models.ProjectRoleMaintainer, "rwd"},
		{"", ""},
	}

	for _, s := range steps {
		if err := f.pg.ChangeProjectGroup(ctx, f.project.ID, g.ID, s.role, &f.owner.ID, record); err != nil {
			t.Fatalf("ChangeProjectGroup(%q): %v", s.role, err)
		}

		f.expect(t, "group "+s.role, f.alice, s.want)
	}

	if fmt.Sprint(seen) != fmt.Sprint([]string{"", models.ProjectRoleViewer, models.ProjectRoleMaintainer}) {
		t.Fatalf("guard saw %q", seen)
	}

	if err := f.pg.ChangeProjectGroup(ctx, f.project.ID, g.ID, models.ProjectRoleViewer, nil, nil); err != nil {
		t.Fatal(err)
	}

	err := f.pg.ChangeProjectGroup(ctx, f.project.ID, g.ID, "", nil, func(string) error { return errGuardVeto })
	if !errors.Is(err, errGuardVeto) {
		t.Fatalf("err = %v", err)
	}

	pg, err := f.pg.GetProjectGroup(ctx, f.project.ID, g.ID)
	if err != nil || pg == nil || pg.Role != models.ProjectRoleViewer {
		t.Fatalf("group after veto = %+v, %v", pg, err)
	}
}

func TestProjectChange_Repositories(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	// acme-raw exists; add four more, trash one.
	for i := range 4 {
		r := &models.Repository{Name: fmt.Sprintf("acme-r%d", i), Format: "raw", RepoType: "hosted", ProjectID: &f.project.ID}
		if err := f.pg.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE repositories SET deleted_at = now() WHERE name = 'acme-r3'`); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}

	for page := 1; page <= 2; page++ {
		repos, total, err := f.pg.ListProjectRepositories(ctx, f.project.ID, page, 2)
		if err != nil {
			t.Fatal(err)
		}

		if total != 4 {
			t.Fatalf("total = %d, want 4 (trashed excluded)", total)
		}

		want := 2
		if len(repos) != want {
			t.Fatalf("page %d: %d repos", page, len(repos))
		}

		for _, r := range repos {
			if r.Name == "acme-r3" || seen[r.Name] {
				t.Fatalf("page %d: unexpected %s", page, r.Name)
			}

			seen[r.Name] = true
		}
	}

	if repos, total, err := f.pg.ListProjectRepositories(ctx, f.project.ID, 3, 2); err != nil || len(repos) != 0 || total != 4 {
		t.Fatalf("page 3 = %d repos, total %d, %v", len(repos), total, err)
	}

	counts, err := f.pg.CountRepositoriesByProject(ctx, []string{f.project.ID, models.SystemProjectID})
	if err != nil || counts[f.project.ID] != 4 || counts[models.SystemProjectID] != 0 {
		t.Fatalf("counts = %v, %v", counts, err)
	}

	if counts, err := f.pg.CountRepositoriesByProject(ctx, nil); err != nil || len(counts) != 0 {
		t.Fatalf("empty ids: %v, %v", counts, err)
	}
}

func TestProjectChange_MemberUsernames(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	f.mustMember(t, f.alice, models.ProjectRoleViewer)

	m, err := f.pg.GetProjectMember(ctx, f.project.ID, f.alice.ID)
	if err != nil || m == nil || m.Username != f.alice.Username {
		t.Fatalf("GetProjectMember = %+v, %v", m, err)
	}

	members, err := f.pg.ListProjectMembers(ctx, f.project.ID)
	if err != nil || len(members) == 0 {
		t.Fatalf("ListProjectMembers = %v, %v", members, err)
	}

	for _, mm := range members {
		if mm.Username == "" {
			t.Fatalf("member %s has no username", mm.UserID)
		}
	}
}
