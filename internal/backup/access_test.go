// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// accessState is the fake authorization and quota state (fakeDB.access).
type accessState struct {
	targets     []*models.PermissionTarget
	assignments []*models.PermissionAssignment
	memberships []*models.UserGroup
	quotas      map[string]*models.StorageQuota // by repository id
}

func acc(d *fakeDB) *accessState {
	if d.access == nil {
		d.access = &accessState{quotas: map[string]*models.StorageQuota{}}
	}

	return d.access
}

func (d *fakeDB) ListPermissionTargets(context.Context) ([]*models.PermissionTarget, error) {
	return acc(d).targets, nil
}

func (d *fakeDB) GetPermissionTargetByName(_ context.Context, name string) (*models.PermissionTarget, error) {
	for _, t := range acc(d).targets {
		if t.Name == name {
			return t, nil
		}
	}

	return nil, nil
}

func (d *fakeDB) CreatePermissionTarget(_ context.Context, t *models.PermissionTarget) (*models.PermissionTarget, error) {
	t.ID = d.newID("target")
	acc(d).targets = append(acc(d).targets, t)

	return t, nil
}

func (d *fakeDB) ListPermissionAssignments(context.Context) ([]*models.PermissionAssignment, error) {
	return acc(d).assignments, nil
}

func (d *fakeDB) GetOrCreatePermissionAssignment(_ context.Context, targetID, principalType, principalID string) error {
	for _, a := range acc(d).assignments {
		if a.PermissionTargetID == targetID && a.PrincipalType == principalType && a.PrincipalID == principalID {
			return nil
		}
	}

	acc(d).assignments = append(acc(d).assignments, &models.PermissionAssignment{
		ID: d.newID("assign"), PermissionTargetID: targetID, PrincipalType: principalType, PrincipalID: principalID,
	})

	return nil
}

func (d *fakeDB) ListUserGroupMemberships(context.Context) ([]*models.UserGroup, error) {
	return acc(d).memberships, nil
}

func (d *fakeDB) AddUserToGroup(_ context.Context, userID, groupID string) error {
	acc(d).memberships = append(acc(d).memberships, &models.UserGroup{UserID: userID, GroupID: groupID})

	return nil
}

func (d *fakeDB) ListStorageQuotas(context.Context) ([]*models.StorageQuota, error) {
	out := make([]*models.StorageQuota, 0, len(acc(d).quotas))
	for _, q := range acc(d).quotas {
		out = append(out, q)
	}

	return out, nil
}

func (d *fakeDB) GetOrCreateStorageQuota(_ context.Context, repoID string) (*models.StorageQuota, error) {
	q, ok := acc(d).quotas[repoID]
	if !ok {
		q = &models.StorageQuota{RepositoryID: repoID, WarnThresholdPercent: 80}
		acc(d).quotas[repoID] = q
	}

	c := *q

	return &c, nil
}

func (d *fakeDB) UpdateStorageQuota(_ context.Context, q *models.StorageQuota) error {
	c := *q
	acc(d).quotas[q.RepositoryID] = &c

	return nil
}

// Restoring must bring back who may do what and every repository limit, remapped
// to the new ids; the built-in targets must not be duplicated and SSO-managed
// memberships are left to the next SSO login.
func TestBackupRestore_AccessControlAndQuotasRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	repo := &models.Repository{Name: "limited", Format: "go", RepoType: "hosted"}
	if err := src.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	alice, _ := src.CreateUser(ctx, &models.User{Username: "alice"})
	bob, _ := src.CreateUser(ctx, &models.User{Username: "bob"})
	devs, _ := src.CreateGroup(ctx, &models.Group{Name: "devs"})

	adminAll, _ := src.CreatePermissionTarget(ctx, &models.PermissionTarget{Name: "admin-all", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read"}})
	deploy, _ := src.CreatePermissionTarget(ctx, &models.PermissionTarget{Name: "deploy", RepositoryFilter: "limited", PathPattern: ".*", Actions: []string{"read", "write"}})

	_ = src.GetOrCreatePermissionAssignment(ctx, adminAll.ID, "user", alice.ID)
	_ = src.GetOrCreatePermissionAssignment(ctx, deploy.ID, "user", alice.ID)
	_ = src.GetOrCreatePermissionAssignment(ctx, deploy.ID, "group", devs.ID)

	_ = src.AddUserToGroup(ctx, alice.ID, devs.ID)

	acc(src).memberships = append(acc(src).memberships, &models.UserGroup{UserID: bob.ID, GroupID: devs.ID, SSOManaged: true})
	acc(src).quotas[repo.ID] = &models.StorageQuota{RepositoryID: repo.ID, MaxSizeBytes: 1000, MaxArtifactCount: 5, WarnThresholdPercent: 70}

	dst := newFakeDB("dst")
	seeded, _ := dst.CreatePermissionTarget(ctx, &models.PermissionTarget{Name: "admin-all", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read"}})

	restoreInto(t, src, dst)

	newAlice, newBob := userByName(t, dst, "alice"), userByName(t, dst, "bob")
	newDevs := groupByName(t, dst, "devs")
	newRepo := repoByName(t, dst, "limited")

	var names []string
	for _, tg := range acc(dst).targets {
		names = append(names, tg.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"admin-all", "deploy"}) {
		t.Fatalf("targets after restore = %v, want admin-all (kept) and deploy", names)
	}

	newDeploy, _ := dst.GetPermissionTargetByName(ctx, "deploy")
	if newDeploy.Name != "deploy" || !slices.Equal(newDeploy.Actions, []string{"read", "write"}) || newDeploy.RepositoryFilter != "limited" {
		t.Fatalf("deploy target restored as %+v", newDeploy)
	}

	want := map[[3]string]bool{
		{seeded.ID, "user", newAlice.ID}:    true,
		{newDeploy.ID, "user", newAlice.ID}: true,
		{newDeploy.ID, "group", newDevs.ID}: true,
	}

	for _, a := range acc(dst).assignments {
		delete(want, [3]string{a.PermissionTargetID, a.PrincipalType, a.PrincipalID})
	}

	if len(want) != 0 {
		t.Fatalf("assignments missing after restore: %v (have %+v)", want, acc(dst).assignments)
	}

	if len(acc(dst).memberships) != 1 || acc(dst).memberships[0].UserID != newAlice.ID || acc(dst).memberships[0].GroupID != newDevs.ID {
		t.Fatalf("memberships = %+v, want only alice in devs (SSO-managed bob left to the next login; bob=%s)", acc(dst).memberships, newBob.ID)
	}

	q := acc(dst).quotas[newRepo.ID]
	if q == nil || q.MaxSizeBytes != 1000 || q.MaxArtifactCount != 5 || q.WarnThresholdPercent != 70 {
		t.Fatalf("quota after restore = %+v, want 1000 bytes / 5 artifacts / warn 70", q)
	}
}

// A failing list of authorization state must fail the backup, not write a
// partial archive that restores to an RBAC-less instance.
func TestBackup_FailsWhenAccessControlCannotBeListed(t *testing.T) {
	ctx := context.Background()
	src := &noAssignmentsDB{fakeDB: newFakeDB("src")}

	eng, _ := newTestEngine(t, src)

	job := &models.BackupJob{ID: "job1", Status: "pending", Type: "metadata-only"}
	_ = src.CreateBackupJob(ctx, job)

	if err := eng.RunBackup(ctx, job.ID, "metadata-only"); err == nil || src.jobs[job.ID].Status != "failed" {
		t.Fatalf("RunBackup = %v, job status %q; want a failed backup", err, src.jobs[job.ID].Status)
	}
}

type noAssignmentsDB struct{ *fakeDB }

var errListFailed = errors.New("list failed")

func (d *noAssignmentsDB) ListPermissionAssignments(context.Context) ([]*models.PermissionAssignment, error) {
	return nil, errListFailed
}

// Upstream credentials must not be written to the archive, and exporting must
// not blank the live repository.
func TestBackup_ExcludesUpstreamPassword(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	proxy := &models.Repository{Name: "px", Format: "go", RepoType: "proxy", UpstreamURL: "https://up.example", UpstreamUsername: "u", UpstreamPasswordEnc: "enc:v1:AAAA"}
	if err := src.CreateRepository(ctx, proxy); err != nil {
		t.Fatal(err)
	}

	dst := backupAndRestore(t, src, nil)

	got := repoByName(t, dst, "px")
	if got.UpstreamPasswordEnc != "" || got.UpstreamUsername != "u" || got.UpstreamURL != "https://up.example" {
		t.Fatalf("restored proxy = %+v, want credentials without the password", got)
	}

	if src.repos[proxy.ID].UpstreamPasswordEnc != "enc:v1:AAAA" {
		t.Fatal("exporting blanked the live repository's password")
	}
}

func userByName(t *testing.T, d *fakeDB, name string) *models.User {
	t.Helper()

	for _, u := range d.users {
		if u.Username == name {
			return u
		}
	}

	t.Fatalf("user %q not found", name)

	return nil
}

func groupByName(t *testing.T, d *fakeDB, name string) *models.Group {
	t.Helper()

	for _, g := range d.groups {
		if g.Name == name {
			return g
		}
	}

	t.Fatalf("group %q not found", name)

	return nil
}

// restoreInto backs up src and restores the archive into the given db.
func restoreInto(t *testing.T, src, dst *fakeDB) {
	t.Helper()

	ctx := context.Background()
	srcEngine, _ := newTestEngine(t, src)

	job := &models.BackupJob{ID: "job1", Status: "pending", Type: "full"}
	if err := src.CreateBackupJob(ctx, job); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	if err := srcEngine.RunBackup(ctx, job.ID, "full"); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	dstEngine, _ := newTestEngine(t, dst)
	if err := dstEngine.RunRestore(ctx, src.jobs[job.ID].StoragePath); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}
}
