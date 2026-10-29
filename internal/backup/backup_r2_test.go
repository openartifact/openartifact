// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
)

// r2State is the extra fake state the round-two backup tests need (fakeDB.extra).
type r2State struct {
	rpmRepos map[string]*models.RPMRepo
	stale    map[string]bool
	members  map[string][]*models.RepositoryGroupMember
}

func r2(d *fakeDB) *r2State {
	s := d.extra
	if s == nil {
		s = &r2State{
			rpmRepos: map[string]*models.RPMRepo{},
			stale:    map[string]bool{},
			members:  map[string][]*models.RepositoryGroupMember{},
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

// recordingWorker is the metadata worker the recording hooks find bound for
// their format.
type recordingWorker struct {
	r    *recordingRegen
	name string
}

func (w recordingWorker) NotifyStale(repoID, group, artifact string) {
	if group == "" && artifact == "" {
		w.r.add(w.name + ":" + repoID)

		return
	}

	w.r.add(w.name + ":" + repoID + ":" + group + ":" + artifact)
}

// staleNotifier is what the recording hooks call; the backup package cannot
// import the plugin layer that registers the real ones.
type staleNotifier interface {
	NotifyStale(repoID, group, artifact string)
}

var registerRecordingHooks sync.Once

// workers binds a recording worker per format with generated metadata and, once
// per process, registers the Changed hooks that forward to it.
func (r *recordingRegen) workers() *lifecycle.Workers {
	registerRecordingHooks.Do(func() {
		for _, name := range []string{"deb", "rpm", "pacman", "apk", "maven"} {
			lifecycle.Register(name, lifecycle.Hooks{Changed: func(env *lifecycle.Env, c lifecycle.Change) {
				if n, ok := lifecycle.Worker[staleNotifier](env.Workers, name); ok {
					n.NotifyStale(c.RepoID, c.GroupID, c.ArtifactID)
				}
			}})
		}
	})

	w := lifecycle.NewWorkers()
	for _, name := range []string{"deb", "rpm", "pacman", "apk", "maven"} {
		w.Bind(name, recordingWorker{r, name})
	}

	return w
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

// BKP-5: group repositories lost their members (and so all their content) on a
// restore.
func TestBackupRestore_GroupMembersRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	a := &models.Repository{Name: "a", Format: "raw", RepoType: "hosted"}
	b := &models.Repository{Name: "b", Format: "raw", RepoType: "hosted"}
	g := &models.Repository{Name: "grp", Format: "raw", RepoType: "group"}

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

// BKP-4: restoring deb/maven/rpm package rows leaves repodata/dists/
// maven-metadata.xml missing; the restore must schedule their regeneration,
// per the Regen kind of each format's descriptor (not per format name).
func TestBackupRestore_RegeneratesGeneratedMetadata(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	rpm := &models.Repository{Name: "r", Format: "rpm", RepoType: "hosted"}
	deb := &models.Repository{Name: "d", Format: "deb", RepoType: "hosted"}
	mvn := &models.Repository{Name: "m", Format: "maven", RepoType: "hosted"}
	plain := &models.Repository{Name: "p", Format: "raw", RepoType: "hosted"}
	proxy := &models.Repository{Name: "px", Format: "deb", RepoType: "proxy"}

	for _, r := range []*models.Repository{deb, mvn, rpm, plain, proxy} {
		if err := src.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	src.pkgRows[mvn.ID] = map[string][]string{
		"packages": {
			`{"id":"p1","attrs":{"group_id":"com.x","artifact_id":"app"}}`,
			`{"id":"p2","attrs":{"group_id":"com.x","artifact_id":"app"}}`,
			`{"id":"p3","attrs":{}}`,
		},
	}

	regen := &recordingRegen{}
	dst := backupAndRestore(t, src, func(e *Engine) { e.SetWorkers(regen.workers()) })

	rpmID, debID, mvnID := repoByName(t, dst, "r").ID, repoByName(t, dst, "d").ID, repoByName(t, dst, "m").ID

	if !r2(dst).stale[rpmID] || r2(dst).rpmRepos[rpmID] == nil {
		t.Errorf("rpm repo not created+marked stale after restore (rpm_repos=%v stale=%v)", r2(dst).rpmRepos[rpmID], r2(dst).stale[rpmID])
	}

	sort.Strings(regen.calls)

	want := []string{"deb:" + debID, "maven:" + mvnID + ":com.x:app", "rpm:" + rpmID}
	if !slices.Equal(regen.calls, want) {
		t.Fatalf("regeneration calls = %v, want %v", regen.calls, want)
	}
}
