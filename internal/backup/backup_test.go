// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeDB embeds database.BaseMockDB (stub-everything) and overrides only the
// handful of methods this test actually drives. Every other Database method
// returns BaseMockDB's "not implemented" error, which every backup/restore
// helper here already treats as "log a warning and skip" — exactly the
// caveat behavior described in the assignment.
type fakeDB struct {
	database.BaseMockDB

	salt   string
	nextID int64

	repos           map[string]*models.Repository
	users           map[string]*models.User
	groups          map[string]*models.Group
	rpmComponents   map[string][]models.Component // keyed by repository id
	jobs            map[string]*models.BackupJob
	npmPackages     map[string][]*models.NPMPackage     // keyed by repository id
	npmVersions     map[string][]*models.NPMVersion     // keyed by package id
	npmDistTags     map[string][]*models.NPMDistTag     // keyed by package id
	dockerManifests map[string][]*models.DockerManifest // keyed by repository id
	extra           *r2State
	access          *accessState
}

func newFakeDB(salt string) *fakeDB {
	return &fakeDB{
		salt:            salt,
		repos:           map[string]*models.Repository{},
		users:           map[string]*models.User{},
		groups:          map[string]*models.Group{},
		rpmComponents:   map[string][]models.Component{},
		jobs:            map[string]*models.BackupJob{},
		npmPackages:     map[string][]*models.NPMPackage{},
		npmVersions:     map[string][]*models.NPMVersion{},
		npmDistTags:     map[string][]*models.NPMDistTag{},
		dockerManifests: map[string][]*models.DockerManifest{},
	}
}

// newID generates an id unique within this fakeDB instance AND across
// instances (the salt keeps two independent fakeDB's — e.g. a backup source
// and a restore target in the same test — from ever coincidentally minting
// the same id, which would mask a missing id-remap bug as a false pass).
func (f *fakeDB) newID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%s-%d", f.salt, prefix, f.nextID)
}

func (f *fakeDB) CreateBackupJob(_ context.Context, job *models.BackupJob) error {
	if job.ID == "" {
		job.ID = f.newID("job")
	}

	f.jobs[job.ID] = job

	return nil
}

func (f *fakeDB) GetBackupJob(_ context.Context, id string) (*models.BackupJob, error) {
	return f.jobs[id], nil
}

func (f *fakeDB) UpdateBackupJob(_ context.Context, job *models.BackupJob) error {
	f.jobs[job.ID] = job
	return nil
}

func (f *fakeDB) ListRepositories(_ context.Context, _, _ int, _ string) ([]*models.Repository, error) {
	out := make([]*models.Repository, 0, len(f.repos))
	for _, r := range f.repos {
		out = append(out, r)
	}

	return out, nil
}

func (f *fakeDB) CreateRepository(_ context.Context, repo *models.Repository) error {
	repo.ID = f.newID("repo")
	repo.CreatedAt = time.Now()
	repo.UpdatedAt = time.Now()
	f.repos[repo.ID] = repo

	return nil
}

func (f *fakeDB) ListUsers(_ context.Context, _, _ int) ([]*models.User, int, error) {
	out := make([]*models.User, 0, len(f.users))
	for _, u := range f.users {
		out = append(out, u)
	}

	return out, len(out), nil
}

func (f *fakeDB) CreateUser(_ context.Context, user *models.User) (*models.User, error) {
	user.ID = f.newID("user")
	user.CreatedAt = time.Now()
	user.UpdatedAt = time.Now()
	f.users[user.ID] = user

	return user, nil
}

func (f *fakeDB) ListGroups(_ context.Context) ([]*models.Group, error) {
	out := make([]*models.Group, 0, len(f.groups))
	for _, g := range f.groups {
		out = append(out, g)
	}

	return out, nil
}

func (f *fakeDB) CreateGroup(_ context.Context, group *models.Group) (*models.Group, error) {
	group.ID = f.newID("group")
	f.groups[group.ID] = group

	return group, nil
}

func (f *fakeDB) GetComponentsByRepo(_ context.Context, repoID string) ([]models.Component, error) {
	return f.rpmComponents[repoID], nil
}

// UpsertBlob mirrors the real content-addressed contract: the blob store
// key/id and the blobs-table dedup key are both the sha256.
func (f *fakeDB) UpsertBlob(_ context.Context, sha256Hex, _ string, _ int64) (string, error) {
	return sha256Hex, nil
}

func (f *fakeDB) CreateRpmPackage(_ context.Context, pkg *models.Component) error {
	pkg.ID = f.newID("rpmpkg")
	f.rpmComponents[pkg.RepositoryID] = append(f.rpmComponents[pkg.RepositoryID], *pkg)

	return nil
}

func (f *fakeDB) ListAllNPMPackages(_ context.Context, repoID string) ([]*models.NPMPackage, error) {
	return f.npmPackages[repoID], nil
}

func (f *fakeDB) CreateNPMPackage(_ context.Context, pkg *models.NPMPackage) (*models.NPMPackage, error) {
	pkg.ID = f.newID("npmpkg")
	f.npmPackages[pkg.RepositoryID] = append(f.npmPackages[pkg.RepositoryID], pkg)

	return pkg, nil
}

func (f *fakeDB) ListNPMVersionsByRepo(_ context.Context, repoID string) ([]*models.NPMVersion, error) {
	var out []*models.NPMVersion
	for _, pkg := range f.npmPackages[repoID] {
		out = append(out, f.npmVersions[pkg.ID]...)
	}

	return out, nil
}

func (f *fakeDB) CreateNPMVersion(_ context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	v.ID = f.newID("npmver")
	f.npmVersions[v.PackageID] = append(f.npmVersions[v.PackageID], v)

	return v, nil
}

func (f *fakeDB) GetNPMDistTags(_ context.Context, packageID string) (map[string]string, error) {
	tags := map[string]string{}
	for _, t := range f.npmDistTags[packageID] {
		tags[t.Tag] = t.Version
	}

	return tags, nil
}

func (f *fakeDB) UpsertNPMDistTag(_ context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	t.ID = f.newID("npmtag")
	f.npmDistTags[t.PackageID] = append(f.npmDistTags[t.PackageID], t)

	return t, nil
}

func (f *fakeDB) ListDockerManifestsByRepo(_ context.Context, repoID string) ([]*models.DockerManifest, error) {
	return f.dockerManifests[repoID], nil
}

func (f *fakeDB) CreateDockerManifest(_ context.Context, m *models.DockerManifest) error {
	m.ID = f.newID("manifest")
	f.dockerManifests[m.RepositoryID] = append(f.dockerManifests[m.RepositoryID], m)

	return nil
}

// EnsureBlob mirrors UpsertBlob's simplified content-addressed contract.
func (f *fakeDB) EnsureBlob(_ context.Context, sha256Hex, _ string, _ int64) (string, error) {
	return sha256Hex, nil
}

func newTestEngine(t *testing.T, db database.Database) (*Engine, storage.BlobStore) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	dir := t.TempDir()

	blobStore, err := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("failed to create blob store: %v", err)
	}

	return NewEngine(db, blobStore, log, filepath.Join(dir, "backups")), blobStore
}

// TestBackupRestore_RpmPackageRoundTrips verifies that a "full" backup
// includes package rows and their blobs, not just repositories/users/groups.
// This exercises: seed one rpm package + its blob -> RunBackup -> RunRestore
// into a *fresh* DB/blob store -> the package and its blob content both land
// under the repository's NEW server-generated id.
func TestBackupRestore_RpmPackageRoundTrips(t *testing.T) {
	const blobContent = "rpm package bytes"

	ctx := context.Background()
	sum := sha256.Sum256([]byte(blobContent))
	sha := hex.EncodeToString(sum[:])

	// --- Source side: seed one rpm repo with one package. ---
	srcDB := newFakeDB("src")
	srcEngine, srcBlobStore := newTestEngine(t, srcDB)

	if _, err := srcBlobStore.Put(ctx, strings.NewReader(blobContent), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("failed to seed blob: %v", err)
	}

	repo := &models.Repository{Name: "myrepo-rpm", Format: "rpm", RepoType: "hosted"}
	if err := srcDB.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("failed to seed repository: %v", err)
	}

	if _, err := srcDB.CreateUser(ctx, &models.User{Username: "alice"}); err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	srcDB.rpmComponents[repo.ID] = []models.Component{{
		RepositoryID: repo.ID,
		SHA256:       sha,
		Format:       "rpm",
		Name:         "mypkg",
		Version:      "1.0",
		Filename:     "mypkg-1.0.rpm",
		PackageSize:  int64(len(blobContent)),
	}}

	job := &models.BackupJob{ID: "job1", Status: "pending", Type: "full"}
	if err := srcDB.CreateBackupJob(ctx, job); err != nil {
		t.Fatalf("failed to seed backup job: %v", err)
	}

	if err := srcEngine.RunBackup(ctx, job.ID, "full"); err != nil {
		t.Fatalf("RunBackup failed: %v", err)
	}

	finished := srcDB.jobs[job.ID]
	if finished.Status != "completed" {
		t.Fatalf("backup job status = %q, want completed (error: %s)", finished.Status, finished.ErrorMessage)
	}

	// --- Target side: fresh DB + fresh blob store, restore into it. ---
	dstDB := newFakeDB("dst")
	dstEngine, dstBlobStore := newTestEngine(t, dstDB)

	if err := dstEngine.RunRestore(ctx, finished.StoragePath); err != nil {
		t.Fatalf("RunRestore failed: %v", err)
	}

	if len(dstDB.repos) != 1 {
		t.Fatalf("expected 1 restored repository, got %d", len(dstDB.repos))
	}

	var newRepoID string

	for id, r := range dstDB.repos {
		newRepoID = id

		if r.Name != "myrepo-rpm" || r.Format != "rpm" {
			t.Fatalf("restored repo mismatch: %+v", r)
		}
	}

	if newRepoID == repo.ID {
		t.Fatal("restored repository reused the source id instead of getting a new server-generated one")
	}

	pkgs := dstDB.rpmComponents[newRepoID]
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 restored rpm package under the new repository id, got %d", len(pkgs))
	}

	if pkgs[0].SHA256 != sha || pkgs[0].Filename != "mypkg-1.0.rpm" {
		t.Fatalf("restored rpm package mismatch: %+v", pkgs[0])
	}

	if len(dstDB.users) != 1 {
		t.Fatalf("expected 1 restored user, got %d", len(dstDB.users))
	}

	// The blob content itself must have made the round trip too — otherwise
	// even a correctly-restored DB row would point at nothing.
	blob, err := dstBlobStore.Get(ctx, sha)
	if err != nil {
		t.Fatalf("restored blob store Get(%q) failed: %v", sha, err)
	}

	data, err := io.ReadAll(blob)
	_ = blob.Close()

	if err != nil {
		t.Fatalf("failed to read restored blob: %v", err)
	}

	if string(data) != blobContent {
		t.Fatalf("restored blob content = %q, want %q", string(data), blobContent)
	}
}

// TestBackupRestore_NPMAndDockerManifestRoundTrip verifies that a "full"
// backup includes npm packages/versions and docker manifests (so docker_tags
// do not point at missing manifests). This exercises: seed one npm package+version+
// dist-tag and one docker manifest, each with its own blob -> RunBackup ->
// RunRestore into a *fresh* DB/blob store -> everything lands under the
// repositories' NEW server-generated ids, with blob content intact and the
// npm version's PackageID correctly remapped to the new package id.
func TestBackupRestore_NPMAndDockerManifestRoundTrip(t *testing.T) {
	const (
		npmTarball = "npm tarball bytes"
		dockerBlob = "docker manifest json bytes"
	)

	ctx := context.Background()
	npmSum := sha256.Sum256([]byte(npmTarball))
	npmSHA := hex.EncodeToString(npmSum[:])
	dockerSum := sha256.Sum256([]byte(dockerBlob))
	dockerSHA := hex.EncodeToString(dockerSum[:])

	// --- Source side ---
	srcDB := newFakeDB("src")
	srcEngine, srcBlobStore := newTestEngine(t, srcDB)

	if _, err := srcBlobStore.Put(ctx, strings.NewReader(npmTarball), map[string]string{"sha256": npmSHA}); err != nil {
		t.Fatalf("failed to seed npm blob: %v", err)
	}

	if _, err := srcBlobStore.Put(ctx, strings.NewReader(dockerBlob), map[string]string{"sha256": dockerSHA}); err != nil {
		t.Fatalf("failed to seed docker blob: %v", err)
	}

	npmRepo := &models.Repository{Name: "myrepo-npm", Format: "npm", RepoType: "hosted"}
	if err := srcDB.CreateRepository(ctx, npmRepo); err != nil {
		t.Fatalf("failed to seed npm repository: %v", err)
	}

	dockerRepo := &models.Repository{Name: "myrepo-docker", Format: "docker", RepoType: "hosted"}
	if err := srcDB.CreateRepository(ctx, dockerRepo); err != nil {
		t.Fatalf("failed to seed docker repository: %v", err)
	}

	npmPkg := &models.NPMPackage{RepositoryID: npmRepo.ID, Name: "mypkg", FullName: "mypkg", LatestVersion: "1.0.0"}
	srcDB.npmPackages[npmRepo.ID] = []*models.NPMPackage{npmPkg}
	srcDB.npmVersions[npmPkg.ID] = []*models.NPMVersion{{
		PackageID:       npmPkg.ID,
		Version:         "1.0.0",
		TarballFilename: "mypkg-1.0.0.tgz",
		BlobSHA256:      npmSHA,
		BlobSizeBytes:   int64(len(npmTarball)),
	}}
	srcDB.npmDistTags[npmPkg.ID] = []*models.NPMDistTag{{PackageID: npmPkg.ID, Tag: "latest", Version: "1.0.0"}}

	srcDB.dockerManifests[dockerRepo.ID] = []*models.DockerManifest{{
		RepositoryID: dockerRepo.ID,
		Digest:       "sha256:" + dockerSHA,
		MediaType:    "application/vnd.docker.distribution.manifest.v2+json",
		Size:         int64(len(dockerBlob)),
		BlobSHA256:   dockerSHA,
	}}

	job := &models.BackupJob{ID: "job1", Status: "pending", Type: "full"}
	if err := srcDB.CreateBackupJob(ctx, job); err != nil {
		t.Fatalf("failed to seed backup job: %v", err)
	}

	if err := srcEngine.RunBackup(ctx, job.ID, "full"); err != nil {
		t.Fatalf("RunBackup failed: %v", err)
	}

	finished := srcDB.jobs[job.ID]
	if finished.Status != "completed" {
		t.Fatalf("backup job status = %q, want completed (error: %s)", finished.Status, finished.ErrorMessage)
	}

	// --- Target side: fresh DB + fresh blob store, restore into it. ---
	dstDB := newFakeDB("dst")
	dstEngine, dstBlobStore := newTestEngine(t, dstDB)

	if err := dstEngine.RunRestore(ctx, finished.StoragePath); err != nil {
		t.Fatalf("RunRestore failed: %v", err)
	}

	if len(dstDB.repos) != 2 {
		t.Fatalf("expected 2 restored repositories, got %d", len(dstDB.repos))
	}

	var newNPMRepoID, newDockerRepoID string

	for id, r := range dstDB.repos {
		switch r.Name {
		case "myrepo-npm":
			newNPMRepoID = id
		case "myrepo-docker":
			newDockerRepoID = id
		}
	}

	if newNPMRepoID == "" || newNPMRepoID == npmRepo.ID {
		t.Fatalf("npm repository did not restore under a new server-generated id: %q", newNPMRepoID)
	}

	if newDockerRepoID == "" || newDockerRepoID == dockerRepo.ID {
		t.Fatalf("docker repository did not restore under a new server-generated id: %q", newDockerRepoID)
	}

	assertRestoredNPM(t, dstDB, dstBlobStore, newNPMRepoID, npmPkg.ID, npmSHA, npmTarball)
	assertRestoredDocker(t, dstDB, dstBlobStore, newDockerRepoID, dockerSHA, dockerBlob)
}

func assertRestoredNPM(t *testing.T, dstDB *fakeDB, dstBlobStore storage.BlobStore, newNPMRepoID, srcPkgID, npmSHA, npmTarball string) {
	t.Helper()

	ctx := context.Background()

	// --- npm package/version/dist-tag ---
	restoredPkgs := dstDB.npmPackages[newNPMRepoID]
	if len(restoredPkgs) != 1 {
		t.Fatalf("expected 1 restored npm package, got %d", len(restoredPkgs))
	}

	newPkg := restoredPkgs[0]
	if newPkg.FullName != "mypkg" || newPkg.ID == srcPkgID {
		t.Fatalf("restored npm package mismatch or reused source id: %+v", newPkg)
	}

	restoredVersions := dstDB.npmVersions[newPkg.ID]
	if len(restoredVersions) != 1 {
		t.Fatalf("expected 1 restored npm version under the new package id, got %d", len(restoredVersions))
	}

	if restoredVersions[0].Version != "1.0.0" || restoredVersions[0].PackageID != newPkg.ID {
		t.Fatalf("restored npm version mismatch: %+v", restoredVersions[0])
	}

	if restoredVersions[0].BlobID != npmSHA {
		t.Fatalf("restored npm version blob id = %q, want %q (fakeDB.UpsertBlob is identity on sha256)", restoredVersions[0].BlobID, npmSHA)
	}

	restoredTags := dstDB.npmDistTags[newPkg.ID]
	if len(restoredTags) != 1 || restoredTags[0].Tag != "latest" || restoredTags[0].Version != "1.0.0" {
		t.Fatalf("expected 1 restored npm dist tag under the new package id, got %+v", restoredTags)
	}

	npmBlob, err := dstBlobStore.Get(ctx, npmSHA)
	if err != nil {
		t.Fatalf("restored npm blob store Get(%q) failed: %v", npmSHA, err)
	}

	npmData, err := io.ReadAll(npmBlob)
	_ = npmBlob.Close()

	if err != nil || string(npmData) != npmTarball {
		t.Fatalf("restored npm blob content = %q (err %v), want %q", string(npmData), err, npmTarball)
	}
}

func assertRestoredDocker(t *testing.T, dstDB *fakeDB, dstBlobStore storage.BlobStore, newDockerRepoID, dockerSHA, dockerBlob string) {
	t.Helper()

	ctx := context.Background()

	// --- docker manifest ---
	restoredManifests := dstDB.dockerManifests[newDockerRepoID]
	if len(restoredManifests) != 1 {
		t.Fatalf("expected 1 restored docker manifest, got %d", len(restoredManifests))
	}

	if restoredManifests[0].Digest != "sha256:"+dockerSHA || restoredManifests[0].BlobID != dockerSHA {
		t.Fatalf("restored docker manifest mismatch: %+v", restoredManifests[0])
	}

	dockerBlobData, err := dstBlobStore.Get(ctx, dockerSHA)
	if err != nil {
		t.Fatalf("restored docker blob store Get(%q) failed: %v", dockerSHA, err)
	}

	dockerData, err := io.ReadAll(dockerBlobData)
	_ = dockerBlobData.Close()

	if err != nil || string(dockerData) != dockerBlob {
		t.Fatalf("restored docker blob content = %q (err %v), want %q", string(dockerData), err, dockerBlob)
	}
}

// TestExportBlobs_PaginatesUntilExhausted proves exportBlobs pages through
// blobStore.List (offset increasing by blobPageSize each call) rather than
// a single fixed-limit call that would silently truncate any store with
// more blobs than the limit. A store with slightly more than one page's worth
// forces at least two List calls; the exported count must cover all of them.
func TestExportBlobs_PaginatesUntilExhausted(t *testing.T) {
	const total = blobPageSize + 5

	fb := newFakeBlobStore(total)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	e := NewEngine(nil, fb, log, t.TempDir())

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	manifest := &BackupManifest{TableCounts: map[string]int{}}

	if err := e.exportBlobs(context.Background(), tw, manifest); err != nil {
		t.Fatalf("exportBlobs failed: %v", err)
	}

	_ = tw.Close()

	if manifest.TableCounts["blobs"] != total {
		t.Fatalf("exported blob count = %d, want %d (old single-page code would truncate at the page size)",
			manifest.TableCounts["blobs"], total)
	}

	if fb.listCalls < 2 {
		t.Fatalf("blobStore.List called %d time(s), want >= 2 — exportBlobs must page through the store, not stop after one call", fb.listCalls)
	}
}

// fakeBlobStore is a minimal in-memory storage.BlobStore used only to prove
// exportBlobs' pagination loop; every method exportBlobs doesn't call
// returns an error if invoked.
type fakeBlobStore struct {
	data      map[string][]byte
	keys      []string
	listCalls int
}

func newFakeBlobStore(n int) *fakeBlobStore {
	fb := &fakeBlobStore{data: map[string][]byte{}}

	for i := range n {
		id := fmt.Sprintf("blob-%05d", i)
		fb.data[id] = []byte("x")
		fb.keys = append(fb.keys, id)
	}

	return fb
}

func (f *fakeBlobStore) Put(context.Context, io.Reader, map[string]string) (string, error) {
	return "", errors.New("fakeBlobStore.Put not implemented")
}

func (f *fakeBlobStore) Get(_ context.Context, id string) (io.ReadCloser, error) {
	data, ok := f.data[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", storage.ErrBlobNotFound, id)
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeBlobStore) Delete(context.Context, string) error {
	return errors.New("fakeBlobStore.Delete not implemented")
}

func (f *fakeBlobStore) Exists(_ context.Context, id string) (bool, error) {
	_, ok := f.data[id]
	return ok, nil
}

func (f *fakeBlobStore) GetMetadata(context.Context, string) (map[string]string, error) {
	return nil, errors.New("fakeBlobStore.GetMetadata not implemented")
}

func (f *fakeBlobStore) List(_ context.Context, token string, limit int) ([]string, string, error) {
	f.listCalls++

	start := 0
	if token != "" {
		start = sort.SearchStrings(f.keys, token)
		if start < len(f.keys) && f.keys[start] == token {
			start++
		}
	}

	if start >= len(f.keys) {
		return nil, "", nil
	}

	end := min(start+limit, len(f.keys))
	if end == len(f.keys) {
		return f.keys[start:end], "", nil
	}

	return f.keys[start:end], f.keys[end-1], nil
}

func (f *fakeBlobStore) GetSize(_ context.Context, id string) (int64, error) {
	data, ok := f.data[id]
	if !ok {
		return 0, fmt.Errorf("%w: %s", storage.ErrBlobNotFound, id)
	}

	return int64(len(data)), nil
}

func (f *fakeBlobStore) ExistsBySHA256(context.Context, string) (string, bool, error) {
	return "", false, errors.New("fakeBlobStore.ExistsBySHA256 not implemented")
}

func (f *fakeBlobStore) PresignURL(context.Context, string, time.Duration) (string, error) {
	return "", storage.ErrPresignNotSupported
}

func TestRestoreBlob_RejectsTraversalAndDigestMismatch(t *testing.T) {
	ctx := context.Background()
	eng, store := newTestEngine(t, newFakeDB("x"))

	content := "good bytes"
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	for _, name := range []string{"blobs/../../evil", "blobs/ab", "blobs/" + sha + "/../x", "blobs/" + strings.Repeat("0", 64)} {
		err := eng.restoreBlob(ctx, strings.NewReader(content), &tar.Header{Name: name, Size: int64(len(content))})
		if err == nil {
			t.Errorf("restoreBlob(%q) succeeded, want error", name)
		}
	}

	if ok, _ := store.Exists(ctx, strings.Repeat("0", 64)); ok {
		t.Error("mismatched-digest blob was stored")
	}

	if err := eng.restoreBlob(ctx, strings.NewReader(content), &tar.Header{Name: "blobs/" + sha, Size: int64(len(content))}); err != nil {
		t.Fatalf("valid blob rejected: %v", err)
	}

	if ok, _ := store.Exists(ctx, sha); !ok {
		t.Error("valid blob not stored")
	}
}

// The tables below are not under test: an empty list stands in for them, since
// a failing list now fails the export instead of being skipped.
func (d *fakeDB) ListWebhooks(context.Context, string) ([]*models.Webhook, error) { return nil, nil }

func (d *fakeDB) ListCleanupPolicies(context.Context, string) ([]*models.CleanupPolicy, error) {
	return nil, nil
}

func (d *fakeDB) ListSBOMReports(context.Context, string) ([]*models.SBOMReport, error) {
	return nil, nil
}

func (d *fakeDB) ListLicenseScanResults(context.Context, string) ([]*models.LicenseScanResult, error) {
	return nil, nil
}

func (d *fakeDB) ListSecretScanResults(context.Context, string, bool) ([]*models.SecretScanResult, error) {
	return nil, nil
}

func (d *fakeDB) ListVulnScanResults(context.Context, string, bool) ([]*models.VulnScanResult, error) {
	return nil, nil
}

func (d *fakeDB) ListAIAnalyses(context.Context, string, string, bool, int, int) ([]*models.AIAnalysis, int, error) {
	return nil, 0, nil
}

func (d *fakeDB) ListDockerBlobs(context.Context, string) ([]*models.DockerBlob, error) {
	return nil, nil
}

func (d *fakeDB) ListDockerTagsByRepo(context.Context, string, int) ([]*models.DockerTag, error) {
	return nil, nil
}

func (d *fakeDB) ListAllSSOProviders(context.Context) ([]*models.SSOProvider, error) { return nil, nil }

func (d *fakeDB) ListAllTokens(context.Context, int, int) ([]*models.Token, int, error) {
	return nil, 0, nil
}

func (d *fakeDB) ListStorageBackends(context.Context) ([]*models.StorageBackend, error) {
	return nil, nil
}

func (d *fakeDB) ListProjects(context.Context, string) ([]*models.Project, error) {
	return nil, nil
}

func (d *fakeDB) ListPromotionPipelines(context.Context) ([]*models.PromotionPipeline, error) {
	return nil, nil
}

func (d *fakeDB) ListReleaseBundles(context.Context, string, string, int, int) ([]*models.ReleaseBundle, int, error) {
	return nil, 0, nil
}

func (d *fakeDB) ListAllProjects(context.Context) ([]*models.Project, error) { return nil, nil }
