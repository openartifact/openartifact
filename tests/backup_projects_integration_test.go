// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newBackupEngine(t *testing.T, db database.Database) *backup.Engine {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	blobs, err := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	return backup.NewEngine(db, blobs, log, filepath.Join(dir, "backups"))
}

// realProjectsSeed is what TestBackupRestore_ProjectsRealDB puts in the source.
type realProjectsSeed struct {
	acme    *models.Project
	team    *models.Group
	owner   *models.User
	formats []string
}

func seedRealProjects(t *testing.T, src *database.PostgresDB) realProjectsSeed {
	t.Helper()

	ctx := context.Background()

	owner, err := src.CreateUser(ctx, &models.User{Username: "bk-owner"})
	if err != nil {
		t.Fatal(err)
	}

	dev, err := src.CreateUser(ctx, &models.User{Username: "bk-dev"})
	if err != nil {
		t.Fatal(err)
	}

	team, err := src.CreateGroup(ctx, &models.Group{Name: "bk-team"})
	if err != nil {
		t.Fatal(err)
	}

	formats := []string{"raw", "npm"}
	acme := &models.Project{
		Name: "bk-acme", OwnerID: &owner.ID, AllowedFormats: formats,
		AllowedRepoTypes: []string{"hosted", "proxy"}, RepoNamePrefix: "bk-acme-",
		AllowPublicRepos: true, MaxRepositories: 7, TokenMaxTTLDays: 30,
	}
	if err := src.CreateProject(ctx, acme); err != nil {
		t.Fatal(err)
	}

	if err := src.AddProjectMember(ctx, acme.ID, dev.ID, models.ProjectRoleDeveloper); err != nil {
		t.Fatal(err)
	}

	if err := src.SetProjectGroup(ctx, acme.ID, team.ID, models.ProjectRoleMaintainer, &owner.ID); err != nil {
		t.Fatal(err)
	}

	repo := &models.Repository{Name: "bk-acme-raw", Format: "raw", RepoType: "hosted", ProjectID: &acme.ID}
	if err := src.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	return realProjectsSeed{acme: acme, team: team, owner: owner, formats: formats}
}

func backupAndRestore(t *testing.T, src, dst *database.PostgresDB) {
	t.Helper()

	ctx := context.Background()

	job := &models.BackupJob{Status: "pending", Type: "full"}
	if err := src.CreateBackupJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := newBackupEngine(t, src).RunBackup(ctx, job.ID, "full"); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	done, err := src.GetBackupJob(ctx, job.ID)
	if err != nil || done.Status != "completed" {
		t.Fatalf("backup job = %+v, %v", done, err)
	}

	if err := newBackupEngine(t, dst).RunRestore(ctx, done.StoragePath); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}
}

func assertRealProjectPolicy(t *testing.T, dst *database.PostgresDB, seed realProjectsSeed) (*models.Project, *models.User) {
	t.Helper()

	ctx := context.Background()

	got, err := dst.GetProjectByName(ctx, "bk-acme")
	if err != nil || got == nil {
		t.Fatalf("project not restored: %v", err)
	}

	if got.ID == seed.acme.ID || got.Slug != "bk-acme" || !slices.Equal(got.AllowedFormats, seed.formats) ||
		!slices.Equal(got.AllowedRepoTypes, []string{"hosted", "proxy"}) || got.RepoNamePrefix != "bk-acme-" ||
		!got.AllowPublicRepos || got.MaxRepositories != 7 || got.TokenMaxTTLDays != 30 {
		t.Fatalf("restored project = %+v", got)
	}

	newOwner, err := dst.GetUserByUsername(ctx, "bk-owner")
	if err != nil || newOwner == nil || got.OwnerID == nil || *got.OwnerID != newOwner.ID {
		t.Fatalf("owner not remapped: %v (project owner %v)", err, got.OwnerID)
	}

	return got, newOwner
}

func assertRealProjectAccess(t *testing.T, dst *database.PostgresDB, got *models.Project, newOwner *models.User, seed realProjectsSeed) {
	t.Helper()

	ctx := context.Background()

	newDev, err := dst.GetUserByUsername(ctx, "bk-dev")
	if err != nil || newDev == nil {
		t.Fatalf("dev user not restored: %v", err)
	}

	member, err := dst.GetProjectMember(ctx, got.ID, newDev.ID)
	if err != nil || member == nil || member.Role != models.ProjectRoleDeveloper {
		t.Fatalf("member = %+v, %v; want developer", member, err)
	}

	groups, err := dst.ListProjectGroups(ctx, got.ID)
	if err != nil || len(groups) != 1 || groups[0].Role != models.ProjectRoleMaintainer || groups[0].GroupName != "bk-team" || groups[0].GroupID == seed.team.ID {
		t.Fatalf("project groups = %+v, %v; want bk-team maintainer with a new group id", groups, err)
	}

	if groups[0].CreatedBy == nil || *groups[0].CreatedBy != newOwner.ID {
		t.Fatalf("project group created_by = %v, want remapped owner %s", groups[0].CreatedBy, newOwner.ID)
	}

	restored, err := dst.GetRepositoryByName(ctx, "bk-acme-raw")
	if err != nil || restored == nil || restored.ProjectID == nil || *restored.ProjectID != got.ID {
		t.Fatalf("repository = %+v, %v; want moved into the restored project", restored, err)
	}
}

// TestBackupRestore_ProjectsRealDB backs up projects with policy fields,
// members, group attachments and repositories on one real database and
// restores them into another one: ids are remapped, nothing is lost.
func TestBackupRestore_ProjectsRealDB(t *testing.T) {
	src := freshPostgres(t, 5)
	dst := freshPostgres(t, 5)

	seed := seedRealProjects(t, src)
	backupAndRestore(t, src, dst)

	got, newOwner := assertRealProjectPolicy(t, dst, seed)
	assertRealProjectAccess(t, dst, got, newOwner, seed)
}

type aclSeed struct {
	user   *models.User
	group  *models.Group
	target *models.PermissionTarget
	repo   *models.Repository
}

func seedACL(t *testing.T, src *database.PostgresDB) aclSeed {
	t.Helper()

	ctx := context.Background()

	user, err := src.CreateUser(ctx, &models.User{Username: "bk-acl-user"})
	if err != nil {
		t.Fatal(err)
	}

	group, err := src.CreateGroup(ctx, &models.Group{Name: "bk-acl-group"})
	if err != nil {
		t.Fatal(err)
	}

	target, err := src.CreatePermissionTarget(ctx, &models.PermissionTarget{Name: "bk-deploy", RepositoryFilter: "bk-*", PathPattern: "**", Actions: []string{"read", "write"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range [][2]string{{"user", user.ID}, {"group", group.ID}} {
		if err := src.GetOrCreatePermissionAssignment(ctx, target.ID, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := src.AddUserToGroup(ctx, user.ID, group.ID); err != nil {
		t.Fatal(err)
	}

	sysProject := models.SystemProjectID
	repo := &models.Repository{Name: "bk-acl-repo", Format: "raw", RepoType: "hosted", ProjectID: &sysProject}

	if err := src.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	if _, err := src.GetOrCreateStorageQuota(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	if err := src.UpdateStorageQuota(ctx, &models.StorageQuota{RepositoryID: repo.ID, MaxSizeBytes: 2048, MaxArtifactCount: 3, WarnThresholdPercent: 60}); err != nil {
		t.Fatal(err)
	}

	return aclSeed{user: user, group: group, target: target, repo: repo}
}

func assertTargetsKeptOnce(t *testing.T, dst *database.PostgresDB) *models.PermissionTarget {
	t.Helper()

	ctx := context.Background()

	targets, err := dst.ListPermissionTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for _, tg := range targets {
		seen[tg.Name]++
	}

	if seen["bk-deploy"] != 1 || seen["admin-all"] != 1 || seen["anonymous-read"] != 1 {
		t.Fatalf("targets after restore = %v, want each of bk-deploy/admin-all/anonymous-read once", seen)
	}

	newTarget, err := dst.GetPermissionTargetByName(ctx, "bk-deploy")
	if err != nil || newTarget == nil {
		t.Fatalf("bk-deploy missing: %v", err)
	}

	return newTarget
}

func assertGrantsAndMemberships(t *testing.T, dst *database.PostgresDB, newTarget *models.PermissionTarget) {
	t.Helper()

	ctx := context.Background()

	newUser, err := dst.GetUserByUsername(ctx, "bk-acl-user")
	if err != nil || newUser == nil {
		t.Fatalf("user missing: %v", err)
	}

	assigns, err := dst.ListPermissionAssignments(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var userGrant, groupGrant bool

	for _, a := range assigns {
		if a.PermissionTargetID != newTarget.ID {
			continue
		}

		userGrant = userGrant || (a.PrincipalType == "user" && a.PrincipalID == newUser.ID)
		groupGrant = groupGrant || a.PrincipalType == "group"
	}

	if !userGrant || !groupGrant {
		t.Fatalf("assignments for bk-deploy = %+v, want a user and a group grant", assigns)
	}

	members, err := dst.ListUserGroupMemberships(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(members, func(m *models.UserGroup) bool { return m.UserID == newUser.ID }) {
		t.Fatalf("memberships = %+v, want bk-acl-user in its group", members)
	}
}

func assertRestoredQuota(t *testing.T, dst *database.PostgresDB) {
	t.Helper()

	ctx := context.Background()

	newRepo, err := dst.GetRepositoryByName(ctx, "bk-acl-repo")
	if err != nil || newRepo == nil {
		t.Fatalf("repository missing: %v", err)
	}

	quotas, err := dst.ListStorageQuotas(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(quotas, func(q *models.StorageQuota) bool {
		return q.RepositoryID == newRepo.ID && q.MaxSizeBytes == 2048 && q.MaxArtifactCount == 3 && q.WarnThresholdPercent == 60
	}) {
		t.Fatalf("quotas = %+v, want 2048/3/60 on the restored repository", quotas)
	}
}

// TestBackupRestore_AccessControlAndQuotasRealDB restores permission targets,
// assignments, memberships and quota limits on a real database, next to the
// seeded built-in targets that must not be duplicated.
func TestBackupRestore_AccessControlAndQuotasRealDB(t *testing.T) {
	src := freshPostgres(t, 5)
	dst := freshPostgres(t, 5)

	seedACL(t, src)
	backupAndRestore(t, src, dst)

	assertGrantsAndMemberships(t, dst, assertTargetsKeptOnce(t, dst))
	assertRestoredQuota(t, dst)
}
