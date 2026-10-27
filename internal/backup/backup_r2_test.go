// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// r2State is the extra fake state the round-two backup tests need (fakeDB.extra).
type r2State struct {
	rpmRepos  map[string]*models.RPMRepo
	stale     map[string]bool
	members   map[string][]*models.RepositoryGroupMember
	goModules map[string][]*models.GoModule
	maven     map[string][]*models.MavenArtifact
}

func r2(d *fakeDB) *r2State {
	s := d.extra
	if s == nil {
		s = &r2State{
			rpmRepos:  map[string]*models.RPMRepo{},
			stale:     map[string]bool{},
			members:   map[string][]*models.RepositoryGroupMember{},
			goModules: map[string][]*models.GoModule{},
			maven:     map[string][]*models.MavenArtifact{},
		}
		d.extra = s
	}

	return s
}

func (d *fakeDB) GetRPMRepo(_ context.Context, repoID string) (*models.RPMRepo, error) {
	return r2(d).rpmRepos[repoID], nil
}

func (d *fakeDB) CreateRPMRepo(_ context.Context, repo *models.RPMRepo) error {
	r2(d).rpmRepos[repo.RepositoryID] = repo
	return nil
}

func (d *fakeDB) MarkRepoStale(_ context.Context, repoID string) error {
	r2(d).stale[repoID] = true
	return nil
}

func (d *fakeDB) ListGroupMembers(_ context.Context, groupID string) ([]*models.RepositoryGroupMember, error) {
	return r2(d).members[groupID], nil
}

func (d *fakeDB) AddGroupMember(_ context.Context, groupID, memberID string, priority int) error {
	s := r2(d)
	s.members[groupID] = append(
		s.members[groupID],
		&models.RepositoryGroupMember{GroupRepoID: groupID, MemberRepoID: memberID, Priority: priority},
	)

	return nil
}

func (d *fakeDB) ListGoModulesByRepo(_ context.Context, repoID string) ([]*models.GoModule, error) {
	return r2(d).goModules[repoID], nil
}

func (d *fakeDB) CreateGoModule(_ context.Context, m *models.GoModule) error {
	s := r2(d)
	s.goModules[m.RepositoryID] = append(s.goModules[m.RepositoryID], m)

	return nil
}

func (d *fakeDB) ListMavenArtifactsByRepo(_ context.Context, repoID string) ([]*models.MavenArtifact, error) {
	return r2(d).maven[repoID], nil
}

func (d *fakeDB) CreateMavenArtifact(_ context.Context, a *models.MavenArtifact) error {
	s := r2(d)
	s.maven[a.RepositoryID] = append(s.maven[a.RepositoryID], a)

	return nil
}

func (d *fakeDB) GetDebianDistributionsByRepo(context.Context, string) ([]models.DebianDistribution, error) {
	return nil, nil
}

// recordingRegen records the metadata refreshes a restore asks for.
type recordingRegen struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRegen) add(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recordingRegen) NotifyDebStale(repoID string) { r.add("deb:" + repoID) }
func (r *recordingRegen) NotifyRPMStale(repoID string) { r.add("rpm:" + repoID) }
func (r *recordingRegen) NotifyApkStale(repoID string) { r.add("apk:" + repoID) }
func (r *recordingRegen) NotifyMavenStale(repoID, g, a string) {
	r.add("maven:" + repoID + ":" + g + ":" + a)
}

// backupAndRestore runs a full backup of src and restores it into a fresh
// target engine, returning the target db.
func backupAndRestore(t *testing.T, src *fakeDB, configure func(*Engine)) *fakeDB {
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

	finished := src.jobs[job.ID]
	if finished.Status != "completed" {
		t.Fatalf("backup status = %q (%s)", finished.Status, finished.ErrorMessage)
	}

	dst := newFakeDB("dst")
	dstEngine, _ := newTestEngine(t, dst)

	if configure != nil {
		configure(dstEngine)
	}

	if err := dstEngine.RunRestore(ctx, finished.StoragePath); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	return dst
}

func repoByName(t *testing.T, d *fakeDB, name string) *models.Repository {
	t.Helper()

	for _, r := range d.repos {
		if r.Name == name {
			return r
		}
	}

	t.Fatalf("repository %q not restored", name)

	return nil
}

// BKP-3: go repositories were skipped because the export switch only knew
// "gomod", while repositories.format stores "go" (as do package listings).
func TestBackupRestore_GoModulesRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	repo := &models.Repository{Name: "gomods", Format: "go", RepoType: "hosted"}
	if err := src.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	r2(src).goModules[repo.ID] = []*models.GoModule{
		{
			RepositoryID: repo.ID,
			ModulePath:   "example.com/m",
			Version:      "v1.0.0",
			GoModContent: "module example.com/m",
			ZipHash:      strings.Repeat("c", 64),
			ZipBlobKey:   strings.Repeat("c", 64),
		},
	}

	dst := backupAndRestore(t, src, nil)

	mods := r2(dst).goModules[repoByName(t, dst, "gomods").ID]
	if len(mods) != 1 || mods[0].ModulePath != "example.com/m" || mods[0].Version != "v1.0.0" {
		t.Fatalf("restored go modules = %+v, want example.com/m@v1.0.0", mods)
	}
}

// BKP-5: group repositories lost their members (and so all their content) on a
// restore.
func TestBackupRestore_GroupMembersRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	a := &models.Repository{Name: "a", Format: "go", RepoType: "hosted"}
	b := &models.Repository{Name: "b", Format: "go", RepoType: "hosted"}
	g := &models.Repository{Name: "grp", Format: "go", RepoType: "group"}

	for _, r := range []*models.Repository{a, b, g} {
		if err := src.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	_ = src.AddGroupMember(ctx, g.ID, b.ID, 0)
	_ = src.AddGroupMember(ctx, g.ID, a.ID, 1)

	dst := backupAndRestore(t, src, nil)

	got := append([]*models.RepositoryGroupMember(nil), r2(dst).members[repoByName(t, dst, "grp").ID]...)
	sort.Slice(got, func(i, j int) bool { return got[i].Priority < got[j].Priority })

	if len(got) != 2 || got[0].MemberRepoID != repoByName(t, dst, "b").ID || got[1].MemberRepoID != repoByName(t, dst, "a").ID {
		t.Fatalf("restored members = %+v, want [b a] under the new ids", got)
	}
}

// BKP-4: restoring rpm/deb/maven package rows leaves repodata/dists/
// maven-metadata.xml missing; the restore must schedule their regeneration.
func TestBackupRestore_RegeneratesGeneratedMetadata(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	rpm := &models.Repository{Name: "r", Format: "rpm", RepoType: "hosted"}
	deb := &models.Repository{Name: "d", Format: "deb", RepoType: "hosted"}
	mvn := &models.Repository{Name: "m", Format: "maven", RepoType: "hosted"}

	for _, r := range []*models.Repository{rpm, deb, mvn} {
		if err := src.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	sha := "0000000000000000000000000000000000000000000000000000000000000001"
	r2(src).maven[mvn.ID] = []*models.MavenArtifact{
		{RepositoryID: mvn.ID, GroupID: "com.x", ArtifactCoord: "app", Version: "1.0", Extension: "jar", BlobSHA256: sha, BlobSizeBytes: 1},
	}

	regen := &recordingRegen{}
	dst := backupAndRestore(t, src, func(e *Engine) { e.SetRegenerator(regen) })

	rpmID, debID, mvnID := repoByName(t, dst, "r").ID, repoByName(t, dst, "d").ID, repoByName(t, dst, "m").ID

	if !r2(dst).stale[rpmID] || r2(dst).rpmRepos[rpmID] == nil {
		t.Errorf("rpm repo not created+marked stale after restore (rpm_repos=%v stale=%v)", r2(dst).rpmRepos[rpmID], r2(dst).stale[rpmID])
	}

	sort.Strings(regen.calls)

	want := []string{"deb:" + debID, "maven:" + mvnID + ":com.x:app", "rpm:" + rpmID}
	if len(regen.calls) != len(want) {
		t.Fatalf("regeneration calls = %v, want %v", regen.calls, want)
	}

	for i := range want {
		if regen.calls[i] != want[i] {
			t.Fatalf("regeneration calls = %v, want %v", regen.calls, want)
		}
	}
}
