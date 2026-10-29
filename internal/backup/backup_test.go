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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/backup/backuptest"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeDB embeds the engine's store interfaces (nil) and overrides only the
// handful of methods this test actually drives. A method it does not drive
// panics unless a stub answers "not implemented", which every backup/restore
// helper here treats as "log a warning and skip".
type fakeDB struct {
	testEngineStore

	salt   string
	nextID int64

	repos           map[string]*models.Repository
	users           map[string]*models.User
	groups          map[string]*models.Group
	jobs            map[string]*models.BackupJob
	pkgRows         map[string]map[string][]string      // exported package rows: repository id -> table -> JSON rows
	restoredRows    []restoredBatch                     // package rows a restore handed to RestorePackageRows
	dockerManifests map[string][]*models.DockerManifest // keyed by repository id
	extra           *r2State
	access          *accessState

	blobs       []database.BlobRow // the blobs table
	snapshotErr error              // fails BeginBackupSnapshot
	snapshots   int                // snapshots taken
	released    int                // snapshots released
}

var errNotImplemented = errors.New("not implemented")

// ReconcileAllStorageUsage is not exercised by the tests.
func (*fakeDB) ReconcileAllStorageUsage(context.Context) (int64, error) { return 0, errNotImplemented }

func newFakeDB(salt string) *fakeDB {
	return &fakeDB{
		salt:            salt,
		repos:           map[string]*models.Repository{},
		users:           map[string]*models.User{},
		groups:          map[string]*models.Group{},
		pkgRows:         map[string]map[string][]string{},
		jobs:            map[string]*models.BackupJob{},
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

// addBlobRow records a blobs-table row; a full backup exports exactly these.
func (f *fakeDB) addBlobRow(sha string, size int64) {
	f.blobs = append(f.blobs, database.BlobRow{SHA256: sha, SizeBytes: size})
	sort.Slice(f.blobs, func(i, j int) bool { return f.blobs[i].SHA256 < f.blobs[j].SHA256 })
}

// BeginBackupSnapshot hands out the fake itself as the snapshot (an in-memory
// fake is trivially consistent) over the recorded blobs rows.
func (f *fakeDB) BeginBackupSnapshot(context.Context, time.Duration) (database.SnapshotReader, error) {
	return f.snapshotOf(f)
}

// snapshotOf is BeginBackupSnapshot for a type that embeds the fake and
// overrides some reads: the snapshot must read through the outer type.
func (f *fakeDB) snapshotOf(reader database.SnapshotReads) (database.SnapshotReader, error) {
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}

	f.snapshots++

	return &backuptest.LiveSnapshot{
		SnapshotReads: reader,
		Blobs:         slices.Clone(f.blobs),
		OnRelease:     func() { f.released++ },
	}, nil
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

// UpsertBlob mirrors the real content-addressed contract: the blob store
// key/id and the blobs-table dedup key are both the sha256.
func (f *fakeDB) UpsertBlob(_ context.Context, sha256Hex, _ string, _ int64) (string, error) {
	return sha256Hex, nil
}

// restoredBatch is one RestorePackageRows call.
type restoredBatch struct {
	repoID string
	table  string
	rows   []string
}

// ExportPackageRows streams the rows seeded in pkgRows.
func (f *fakeDB) ExportPackageRows(_ context.Context, set database.PackageRowSet, repoID string, fn func([]byte) error) error {
	for _, row := range f.pkgRows[repoID][set.Name()] {
		if err := fn([]byte(row)); err != nil {
			return err
		}
	}

	return nil
}

// RestorePackageRows records the batch.
func (f *fakeDB) RestorePackageRows(_ context.Context, set database.PackageRowSet, repoID string, rows []json.RawMessage) (int, error) {
	batch := restoredBatch{repoID: repoID, table: set.Name()}
	for _, r := range rows {
		batch.rows = append(batch.rows, string(r))
	}

	f.restoredRows = append(f.restoredRows, batch)

	return 0, nil
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

// testEngineStore is the part of the database newTestEngine needs.
type testEngineStore interface {
	EngineStore
	database.SnapshotReads // read through the in-memory snapshot
}

func newTestEngine(t *testing.T, db testEngineStore) (*Engine, storage.BlobStore) {
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

// TestBackupRestore_PackageRowsAndBlobsRoundTrip verifies that a "full" backup
// carries every table of the format's descriptor, in the descriptor's restore
// order, under the repository's NEW id, and its blob.
func TestBackupRestore_PackageRowsAndBlobsRoundTrip(t *testing.T) {
	const blobContent = "package bytes"

	ctx := context.Background()
	sum := sha256.Sum256([]byte(blobContent))
	sha := hex.EncodeToString(sum[:])

	srcDB := newFakeDB("src")
	srcEngine, srcBlobStore := newTestEngine(t, srcDB)

	if _, err := srcBlobStore.Put(ctx, strings.NewReader(blobContent), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("failed to seed blob: %v", err)
	}

	srcDB.addBlobRow(sha, int64(len(blobContent)))

	repo := &models.Repository{Name: "myrepo-deb", Format: "deb", RepoType: "hosted"}
	if err := srcDB.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("failed to seed repository: %v", err)
	}

	srcDB.pkgRows[repo.ID] = map[string][]string{
		"debian_distributions": {`{"id":"d1","name":"stable"}`},
		"packages":             {`{"id":"p1","name":"mypkg"}`},
		"package_files":        {`{"id":"f1","blob_sha256":"` + sha + `"}`, `{"id":"f2"}`},
		"deb_files":            {`{"id":"f1","distribution_id":"d1"}`},
		"package_tags":         {`{"id":"t1"}`},
		"package_versions":     {`{"id":"v1","package_id":"p1"}`},
	}

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

	manifest, _ := readArchive(t, finished.StoragePath)
	if manifest.Version != ArchiveVersion || manifest.TableCounts["package_files"] != 2 ||
		manifest.TableCounts["debian_distributions"] != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}

	dstDB := newFakeDB("dst")
	dstEngine, dstBlobStore := newTestEngine(t, dstDB)

	if err := dstEngine.RunRestore(ctx, finished.StoragePath); err != nil {
		t.Fatalf("RunRestore failed: %v", err)
	}

	var newRepoID string

	for id := range dstDB.repos {
		newRepoID = id
	}

	if newRepoID == "" || newRepoID == repo.ID {
		t.Fatalf("repository did not restore under a new server-generated id: %q", newRepoID)
	}

	var got []string

	for _, b := range dstDB.restoredRows {
		if b.repoID != newRepoID {
			t.Fatalf("rows of %s restored into %s, want the new repository %s", b.table, b.repoID, newRepoID)
		}

		got = append(got, b.table+"="+strings.Join(b.rows, ","))
	}

	want := []string{
		`debian_distributions={"id":"d1","name":"stable"}`,
		`packages={"id":"p1","name":"mypkg"}`,
		`package_versions={"id":"v1","package_id":"p1"}`,
		`package_files={"id":"f1","blob_sha256":"` + sha + `"},{"id":"f2"}`,
		`deb_files={"id":"f1","distribution_id":"d1"}`,
		`package_tags={"id":"t1"}`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("restored rows =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	blob, err := dstBlobStore.Get(ctx, sha)
	if err != nil {
		t.Fatalf("restored blob store Get(%q) failed: %v", sha, err)
	}

	data, err := io.ReadAll(blob)
	_ = blob.Close()

	if err != nil || string(data) != blobContent {
		t.Fatalf("restored blob content = %q (err %v), want %q", string(data), err, blobContent)
	}
}

// TestBackupRestore_DockerManifestRoundTrip verifies that docker, which keeps
// its dedicated tables, still archives its manifests (so docker_tags do not
// point at missing manifests) and restores them under the new repository id
// with the blob content intact.
func TestBackupRestore_DockerManifestRoundTrip(t *testing.T) {
	const dockerBlob = "docker manifest json bytes"

	ctx := context.Background()
	dockerSum := sha256.Sum256([]byte(dockerBlob))
	dockerSHA := hex.EncodeToString(dockerSum[:])

	srcDB := newFakeDB("src")
	srcEngine, srcBlobStore := newTestEngine(t, srcDB)

	if _, err := srcBlobStore.Put(ctx, strings.NewReader(dockerBlob), map[string]string{"sha256": dockerSHA}); err != nil {
		t.Fatalf("failed to seed docker blob: %v", err)
	}

	srcDB.addBlobRow(dockerSHA, int64(len(dockerBlob)))

	dockerRepo := &models.Repository{Name: "myrepo-docker", Format: "docker", RepoType: "hosted"}
	if err := srcDB.CreateRepository(ctx, dockerRepo); err != nil {
		t.Fatalf("failed to seed docker repository: %v", err)
	}

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

	dstDB := newFakeDB("dst")
	dstEngine, dstBlobStore := newTestEngine(t, dstDB)

	if err := dstEngine.RunRestore(ctx, finished.StoragePath); err != nil {
		t.Fatalf("RunRestore failed: %v", err)
	}

	var newDockerRepoID string

	for id, r := range dstDB.repos {
		if r.Name == "myrepo-docker" {
			newDockerRepoID = id
		}
	}

	if newDockerRepoID == "" || newDockerRepoID == dockerRepo.ID {
		t.Fatalf("docker repository did not restore under a new server-generated id: %q", newDockerRepoID)
	}

	assertRestoredDocker(t, dstDB, dstBlobStore, newDockerRepoID, dockerSHA, dockerBlob)
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

// fakeBlobStore is a minimal in-memory storage.BlobStore for the blob export
// tests; every method the export doesn't call returns an error if invoked.
type fakeBlobStore struct {
	data map[string][]byte
	keys []string
}

func newFakeBlobStore(n int) *fakeBlobStore {
	fb := &fakeBlobStore{data: map[string][]byte{}}

	for i := range n {
		content := []byte(fmt.Sprintf("blob number %d", i))
		sum := sha256.Sum256(content)
		id := hex.EncodeToString(sum[:])
		fb.data[id] = content
		fb.keys = append(fb.keys, id)
	}

	sort.Strings(fb.keys)

	return fb
}

// rows are the blobs-table rows describing the store's content.
func (f *fakeBlobStore) rows() []database.BlobRow {
	out := make([]database.BlobRow, 0, len(f.keys))

	for _, k := range f.keys {
		out = append(out, database.BlobRow{SHA256: k, SizeBytes: int64(len(f.data[k]))})
	}

	return out
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

func (d *fakeDB) ListVEXStatements(context.Context, string, string) ([]*models.VEXStatement, error) {
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
