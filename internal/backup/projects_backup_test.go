// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// projectFakeDB adds the project tables to fakeDB.
type projectFakeDB struct {
	*fakeDB

	projects []*models.Project
	members  []*models.ProjectMember
	pgroups  []*models.ProjectGroup
	quotas   map[string]*models.ProjectQuota
	policies []*models.CleanupPolicy
}

func newProjectFakeDB(salt string) *projectFakeDB {
	return &projectFakeDB{fakeDB: newFakeDB(salt)}
}

func (f *projectFakeDB) CreateProject(_ context.Context, p *models.Project) error {
	p.ID = f.newID("project")
	f.projects = append(f.projects, p)

	return nil
}

func (f *projectFakeDB) ListAllProjects(context.Context) ([]*models.Project, error) {
	return f.projects, nil
}

func (f *projectFakeDB) AddProjectMember(_ context.Context, projectID, userID, role string) error {
	f.members = append(f.members, &models.ProjectMember{ProjectID: projectID, UserID: userID, Role: role})

	return nil
}

func (f *projectFakeDB) ListProjectMembers(_ context.Context, projectID string) ([]*models.ProjectMember, error) {
	var out []*models.ProjectMember

	for _, m := range f.members {
		if m.ProjectID == projectID {
			out = append(out, m)
		}
	}

	return out, nil
}

func (f *projectFakeDB) SetProjectGroup(_ context.Context, projectID, groupID, role string, createdBy *string) error {
	f.pgroups = append(f.pgroups, &models.ProjectGroup{ProjectID: projectID, GroupID: groupID, Role: role, CreatedBy: createdBy})

	return nil
}

func (f *projectFakeDB) ListProjectGroups(_ context.Context, projectID string) ([]*models.ProjectGroup, error) {
	var out []*models.ProjectGroup

	for _, g := range f.pgroups {
		if g.ProjectID == projectID {
			out = append(out, g)
		}
	}

	return out, nil
}

func (f *projectFakeDB) GetProjectQuota(_ context.Context, projectID string) (*models.ProjectQuota, error) {
	if q, ok := f.quotas[projectID]; ok {
		return q, nil
	}

	return &models.ProjectQuota{ProjectID: projectID, WarnThresholdPercent: 80}, nil
}

func (f *projectFakeDB) SetProjectQuota(_ context.Context, projectID string, maxSize, maxCount int64, warn int) (*models.ProjectQuota, error) {
	if f.quotas == nil {
		f.quotas = map[string]*models.ProjectQuota{}
	}

	q := &models.ProjectQuota{ProjectID: projectID, MaxSizeBytes: maxSize, MaxArtifactCount: maxCount, WarnThresholdPercent: warn}
	f.quotas[projectID] = q

	return q, nil
}

func (f *projectFakeDB) CreateCleanupPolicy(_ context.Context, p *models.CleanupPolicy) error {
	f.policies = append(f.policies, p)

	return nil
}

func (f *projectFakeDB) ListProjectCleanupPolicies(_ context.Context, projectID string) ([]*models.CleanupPolicy, error) {
	var out []*models.CleanupPolicy

	for _, p := range f.policies {
		if p.ProjectID == projectID {
			out = append(out, p)
		}
	}

	return out, nil
}

func (f *projectFakeDB) UpdateRepository(_ context.Context, repo *models.Repository) error {
	f.repos[repo.ID] = repo

	return nil
}

func (f *projectFakeDB) projectByName(t *testing.T, name string) *models.Project {
	t.Helper()

	for _, p := range f.projects {
		if p.Name == name {
			return p
		}
	}

	t.Fatalf("project %q not restored; have %d projects", name, len(f.projects))

	return nil
}

func (f *projectFakeDB) roleOf(projectID, userID string) string {
	for _, m := range f.members {
		if m.ProjectID == projectID && m.UserID == userID {
			return m.Role
		}
	}

	return ""
}

func (f *projectFakeDB) userByName(t *testing.T, name string) string {
	t.Helper()

	for id, u := range f.users {
		if u.Username == name {
			return id
		}
	}

	t.Fatalf("user %q not restored", name)

	return ""
}

func (f *projectFakeDB) repoByName(t *testing.T, name string) *models.Repository {
	t.Helper()

	for _, r := range f.repos {
		if r.Name == name {
			return r
		}
	}

	t.Fatalf("repository %q not restored", name)

	return nil
}

// projectsSeed is what TestBackupRestore_ProjectsRoundTrip puts in the source.
type projectsSeed struct {
	acme    *models.Project
	team    *models.Group
	formats []string
}

func seedProjectsSource(t *testing.T, src *projectFakeDB) projectsSeed {
	t.Helper()

	ctx := context.Background()

	owner, _ := src.CreateUser(ctx, &models.User{Username: "owner"})
	dev, _ := src.CreateUser(ctx, &models.User{Username: "dev"})
	team, _ := src.CreateGroup(ctx, &models.Group{Name: "team"})

	formats := []string{"maven", "npm"}
	acme := &models.Project{
		Name: "acme", Slug: "acme", OwnerID: &owner.ID,
		AllowedFormats: formats, AllowedRepoTypes: []string{"hosted", "proxy"},
		RepoNamePrefix: "acme-", AllowPublicRepos: true, MaxRepositories: 7, TokenMaxTTLDays: 30,
	}
	// A project with no format restriction and unlimited quotas (0 = unlimited).
	open := &models.Project{Name: "open", Slug: "open", AllowedRepoTypes: []string{"hosted"}, RepoNamePrefix: "open-"}

	for _, p := range []*models.Project{acme, open} {
		if err := src.CreateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	_ = src.AddProjectMember(ctx, acme.ID, owner.ID, models.ProjectRoleOwner)
	_ = src.AddProjectMember(ctx, acme.ID, dev.ID, models.ProjectRoleDeveloper)
	_ = src.SetProjectGroup(ctx, acme.ID, team.ID, models.ProjectRoleMaintainer, &owner.ID)

	if _, err := src.SetProjectQuota(ctx, acme.ID, 1000, 5, 70); err != nil {
		t.Fatal(err)
	}

	keep := 3
	_ = src.CreateCleanupPolicy(ctx, &models.CleanupPolicy{ProjectID: acme.ID, Name: "default", Enabled: true, KeepLastN: &keep, ScheduleCron: "0 3 * * *"})

	sys := models.SystemProjectID
	_ = src.CreateRepository(ctx, &models.Repository{Name: "acme-libs", Format: "rpm", RepoType: "hosted", ProjectID: &acme.ID})
	_ = src.CreateRepository(ctx, &models.Repository{Name: "loose", Format: "rpm", RepoType: "hosted", ProjectID: &sys})

	return projectsSeed{acme: acme, team: team, formats: formats}
}

func assertRestoredProjectPolicy(t *testing.T, dst *projectFakeDB, seed projectsSeed) *models.Project {
	t.Helper()

	got := dst.projectByName(t, "acme")
	if got.ID == seed.acme.ID {
		t.Fatal("project kept its source id")
	}

	newOwner := dst.userByName(t, "owner")
	if got.OwnerID == nil || *got.OwnerID != newOwner {
		t.Fatalf("owner = %v, want remapped user %s", got.OwnerID, newOwner)
	}

	if !slices.Equal(got.AllowedFormats, seed.formats) || !slices.Equal(got.AllowedRepoTypes, []string{"hosted", "proxy"}) ||
		got.RepoNamePrefix != "acme-" || !got.AllowPublicRepos || got.MaxRepositories != 7 || got.TokenMaxTTLDays != 30 {
		t.Fatalf("policy not restored: %+v", got)
	}

	if o := dst.projectByName(t, "open"); o.AllowedFormats != nil || o.MaxRepositories != 0 || o.TokenMaxTTLDays != 0 {
		t.Fatalf("open project policy = %+v, want nil formats and zero limits", o)
	}

	return got
}

func assertRestoredProjectMembers(t *testing.T, dst *projectFakeDB, got *models.Project) {
	t.Helper()

	if r := dst.roleOf(got.ID, dst.userByName(t, "owner")); r != models.ProjectRoleOwner {
		t.Fatalf("owner member role = %q", r)
	}

	if r := dst.roleOf(got.ID, dst.userByName(t, "dev")); r != models.ProjectRoleDeveloper {
		t.Fatalf("dev member role = %q", r)
	}
}

func assertRestoredProjectGroup(t *testing.T, dst *projectFakeDB, got *models.Project, seed projectsSeed) {
	t.Helper()

	if len(dst.pgroups) != 1 {
		t.Fatalf("restored %d project groups, want 1", len(dst.pgroups))
	}

	pg := dst.pgroups[0]
	newOwner := dst.userByName(t, "owner")

	var newGroup string

	for id, g := range dst.groups {
		if g.Name == "team" {
			newGroup = id
		}
	}

	if pg.ProjectID != got.ID || pg.GroupID != newGroup || pg.GroupID == seed.team.ID || pg.Role != models.ProjectRoleMaintainer ||
		pg.CreatedBy == nil || *pg.CreatedBy != newOwner {
		t.Fatalf("project group = %+v, want project %s group %s (remapped) maintainer created by %s", pg, got.ID, newGroup, newOwner)
	}
}

func assertRestoredProjectQuotaAndPolicies(t *testing.T, dst *projectFakeDB, got *models.Project) {
	t.Helper()

	if q := dst.quotas[got.ID]; q == nil || q.MaxSizeBytes != 1000 || q.MaxArtifactCount != 5 || q.WarnThresholdPercent != 70 {
		t.Fatalf("project quota = %+v, want 1000 bytes / 5 artifacts / warn 70", dst.quotas[got.ID])
	}

	if len(dst.quotas) != 1 {
		t.Fatalf("restored %d project quotas, want only the limited one", len(dst.quotas))
	}

	if len(dst.policies) != 1 {
		t.Fatalf("restored %d project cleanup policies, want 1", len(dst.policies))
	}

	p := dst.policies[0]
	if p.ProjectID != got.ID || p.RepositoryID != "" || p.Name != "default" || p.KeepLastN == nil || *p.KeepLastN != 3 {
		t.Fatalf("project cleanup policy = %+v, want default/keep 3 on project %s", p, got.ID)
	}
}

func TestBackupRestore_ProjectsRoundTrip(t *testing.T) {
	ctx := context.Background()

	src := newProjectFakeDB("src")
	srcEngine, _ := newTestEngine(t, src)
	seed := seedProjectsSource(t, src)

	job := &models.BackupJob{ID: "job1", Status: "pending", Type: "full"}
	_ = src.CreateBackupJob(ctx, job)

	if err := srcEngine.RunBackup(ctx, job.ID, "full"); err != nil || src.jobs[job.ID].Status != "completed" {
		t.Fatalf("RunBackup: %v (%+v)", err, src.jobs[job.ID])
	}

	dst := newProjectFakeDB("dst")
	dstEngine, _ := newTestEngine(t, dst)

	if err := dstEngine.RunRestore(ctx, src.jobs[job.ID].StoragePath); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	got := assertRestoredProjectPolicy(t, dst, seed)
	assertRestoredProjectMembers(t, dst, got)
	assertRestoredProjectGroup(t, dst, got, seed)
	assertRestoredProjectQuotaAndPolicies(t, dst, got)

	if r := dst.repoByName(t, "acme-libs"); r.ProjectID == nil || *r.ProjectID != got.ID {
		t.Fatalf("acme-libs project = %v, want %s", r.ProjectID, got.ID)
	}

	if r := dst.repoByName(t, "loose"); r.ProjectID == nil || *r.ProjectID != models.SystemProjectID {
		t.Fatalf("loose project = %v, want system", r.ProjectID)
	}
}

// writeLegacyArchive writes a backup in the layout used before organizations
// became projects: the entries are written in the given order.
func writeLegacyArchive(t *testing.T, entries []legacyEntry) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "legacy.tar.gz")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	manifest := BackupManifest{Version: "1.0", CreatedAt: time.Now(), Type: "full", TableCounts: map[string]int{}}
	all := append([]legacyEntry{{"manifest.json", manifest}}, entries...)

	for _, e := range all {
		data, err := json.Marshal(e.value)
		if err != nil {
			t.Fatal(err)
		}

		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return path
}

type legacyEntry struct {
	name  string
	value any
}

func TestRestore_LegacyOrganizationArchive(t *testing.T) {
	const (
		oldOwner    = "u-owner"
		oldAdmin    = "u-admin"
		oldMember   = "u-member"
		oldOrgWrite = "org-write"
		oldOrgRead  = "org-read"
		oldOrgAdmin = "org-admin"
		oldRepo     = "repo-1"
		oldRepoSys  = "repo-sys"
	)

	users := legacyEntry{"users.jsonl", []map[string]any{
		{"ID": oldOwner, "Username": "owner"},
		{"ID": oldAdmin, "Username": "admin"},
		{"ID": oldMember, "Username": "member"},
	}}
	repos := legacyEntry{"repositories.jsonl", []map[string]any{
		{"ID": oldRepo, "Name": "legacy-repo", "Format": "raw", "RepoType": "hosted", "OrgID": oldOrgWrite},
		{"ID": oldRepoSys, "Name": "sys-repo", "Format": "raw", "RepoType": "hosted", "OrgID": models.SystemProjectID},
	}}
	orgs := legacyEntry{"organizations.jsonl", []map[string]any{
		{"ID": oldOrgWrite, "Name": "Write-Org", "Slug": "write-org", "OwnerID": oldOwner, "MemberRole": "write"},
		{"ID": oldOrgRead, "Name": "read-org", "Slug": "read-org", "OwnerID": oldOwner, "MemberRole": "read"},
		{"ID": oldOrgAdmin, "Name": "admin-org", "Slug": "", "OwnerID": oldOwner, "MemberRole": "admin"},
	}}
	members := legacyEntry{"org_members.jsonl", []map[string]any{
		{"OrgID": oldOrgWrite, "UserID": oldOwner, "Role": "owner"},
		{"OrgID": oldOrgWrite, "UserID": oldAdmin, "Role": "admin"},
		{"OrgID": oldOrgWrite, "UserID": oldMember, "Role": "member"},
		{"OrgID": oldOrgRead, "UserID": oldMember, "Role": "member"},
		{"OrgID": oldOrgAdmin, "UserID": oldMember, "Role": "member"},
	}}

	orders := map[string][]legacyEntry{
		"users first":               {users, repos, orgs, members},
		"repositories before users": {repos, users, orgs, members},
	}

	for name, entries := range orders {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dst := newProjectFakeDB("dst")
			engine, _ := newTestEngine(t, dst)

			if err := engine.RunRestore(ctx, writeLegacyArchive(t, entries)); err != nil {
				t.Fatalf("RunRestore: %v", err)
			}

			assertLegacyProjects(t, dst)
		})
	}
}

func assertLegacyProjects(t *testing.T, dst *projectFakeDB) {
	t.Helper()

	if len(dst.projects) != 3 {
		t.Fatalf("restored %d projects, want 3", len(dst.projects))
	}

	owner, admin, member := dst.userByName(t, "owner"), dst.userByName(t, "admin"), dst.userByName(t, "member")

	for _, tt := range []struct{ name, slug string }{
		{"Write-Org", "write-org"}, {"read-org", "read-org"}, {"admin-org", "admin-org"},
	} {
		p := dst.projectByName(t, tt.name)
		if p.Slug != tt.slug || p.RepoNamePrefix != tt.slug+"-" || p.MaxRepositories != 50 || p.TokenMaxTTLDays != 365 ||
			!slices.Equal(p.AllowedRepoTypes, []string{models.ProjectRepoTypeHosted}) || p.AllowedFormats != nil || p.AllowPublicRepos {
			t.Errorf("%s legacy defaults = %+v", tt.name, p)
		}

		if p.OwnerID == nil || *p.OwnerID != owner {
			t.Errorf("%s owner = %v, want %s", tt.name, p.OwnerID, owner)
		}
	}

	roles := []struct{ name, project, user, want string }{
		{"owner stays owner", "Write-Org", owner, models.ProjectRoleOwner},
		{"admin -> maintainer", "Write-Org", admin, models.ProjectRoleMaintainer},
		{"member + member_role write -> developer", "Write-Org", member, models.ProjectRoleDeveloper},
		{"member + member_role read -> viewer", "read-org", member, models.ProjectRoleViewer},
		{"member + member_role admin -> maintainer", "admin-org", member, models.ProjectRoleMaintainer},
	}

	for _, r := range roles {
		if got := dst.roleOf(dst.projectByName(t, r.project).ID, r.user); got != r.want {
			t.Errorf("%s: role = %q, want %q", r.name, got, r.want)
		}
	}

	repo := dst.repoByName(t, "legacy-repo")
	if want := dst.projectByName(t, "Write-Org").ID; repo.ProjectID == nil || *repo.ProjectID != want {
		t.Errorf("legacy-repo project = %v, want %s", repo.ProjectID, want)
	}

	if sys := dst.repoByName(t, "sys-repo"); sys.ProjectID == nil || *sys.ProjectID != models.SystemProjectID {
		t.Errorf("sys-repo project = %v, want system", sys.ProjectID)
	}
}
