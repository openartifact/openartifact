// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package formattest provides in-memory fakes shared by the format handler
// tests: a content-addressed blob store, a recording webhook publisher and a
// quarantine lookup.
package formattest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errNoBlob = errors.New("formattest: blob not found")

// BlobStore is a minimal content-addressed in-memory storage.BlobStore. Blobs
// are keyed by the "sha256" metadata entry given to Put.
type BlobStore struct {
	mu    sync.Mutex
	Blobs map[string][]byte
}

// NewBlobStore returns an empty BlobStore.
func NewBlobStore() *BlobStore { return &BlobStore{Blobs: map[string][]byte{}} }

// Put stores data under metadata["sha256"].
func (m *BlobStore) Put(_ context.Context, data io.Reader, metadata map[string]string) (string, error) {
	b, err := io.ReadAll(data)
	if err != nil {
		return "", err
	}

	id := metadata["sha256"]

	m.mu.Lock()
	m.Blobs[id] = b
	m.mu.Unlock()

	return id, nil
}

// DiscardBlobStore is a BlobStore that reads and drops what it is given, for
// benchmarks that must measure a handler rather than the fake's own copy of
// every blob.
type DiscardBlobStore struct{ *BlobStore }

// NewDiscardBlobStore returns an empty DiscardBlobStore.
func NewDiscardBlobStore() *DiscardBlobStore { return &DiscardBlobStore{NewBlobStore()} }

// Put drains data and returns metadata["sha256"] without keeping anything.
func (m *DiscardBlobStore) Put(_ context.Context, data io.Reader, metadata map[string]string) (string, error) {
	if _, err := io.Copy(io.Discard, data); err != nil {
		return "", err
	}

	return metadata["sha256"], nil
}

// Get returns the blob stored under id.
func (m *BlobStore) Get(_ context.Context, id string) (io.ReadCloser, error) {
	m.mu.Lock()
	b, ok := m.Blobs[id]
	m.mu.Unlock()

	if !ok {
		return nil, errNoBlob
	}

	return io.NopCloser(bytes.NewReader(b)), nil
}

// Delete removes a blob.
func (m *BlobStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.Blobs, id)
	m.mu.Unlock()

	return nil
}

// Exists reports whether a blob exists.
func (m *BlobStore) Exists(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	_, ok := m.Blobs[id]
	m.mu.Unlock()

	return ok, nil
}

// GetMetadata returns no metadata.
func (m *BlobStore) GetMetadata(_ context.Context, _ string) (map[string]string, error) {
	return nil, nil //nolint:nilnil // no metadata is modelled
}

// List returns nothing.
func (m *BlobStore) List(_ context.Context, _ string, _ int) ([]string, string, error) {
	return nil, "", nil
}

// GetSize returns the blob size.
func (m *BlobStore) GetSize(_ context.Context, id string) (int64, error) {
	m.mu.Lock()
	b, ok := m.Blobs[id]
	m.mu.Unlock()

	if !ok {
		return 0, errNoBlob
	}

	return int64(len(b)), nil
}

// ExistsBySHA256 reports whether a blob with the digest exists.
func (m *BlobStore) ExistsBySHA256(_ context.Context, sha256 string) (string, bool, error) {
	m.mu.Lock()
	_, ok := m.Blobs[sha256]
	m.mu.Unlock()

	return sha256, ok, nil
}

// PresignURL is unsupported.
func (m *BlobStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil
}

// Event is one published webhook event.
type Event struct {
	RepoID  string
	Type    string
	Payload map[string]any
}

// Artifact returns the "artifact" object of the payload.
func (e Event) Artifact() map[string]any {
	a, _ := e.Payload["artifact"].(map[string]any)
	return a
}

// Publisher is a webhook.Publisher recording every event.
type Publisher struct {
	mu     sync.Mutex
	events []Event
}

// Dispatch records the event.
func (p *Publisher) Dispatch(_ context.Context, repoID, eventType string, payload map[string]any) {
	p.mu.Lock()
	p.events = append(p.events, Event{RepoID: repoID, Type: eventType, Payload: payload})
	p.mu.Unlock()
}

// Events returns a copy of the recorded events.
func (p *Publisher) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.events)
}

// Quarantine implements IsArtifactQuarantined over a set of quarantined keys;
// embed it in a database mock.
type Quarantine struct {
	Keys map[string]bool
}

// IsArtifactQuarantined reports whether any key is in the quarantined set.
func (q *Quarantine) IsArtifactQuarantined(_ context.Context, _ string, keys []string) (bool, error) {
	for _, k := range keys {
		if q.Keys[k] {
			return true, nil
		}
	}

	return false, nil
}

// StatusOf returns a function that GETs a path through register (mounted at
// /repos/{name}) and reports the response status.
func StatusOf(register func(chi.Router)) func(path string) int {
	return func(path string) int {
		return Do(register, Request(http.MethodGet, path, nil)).Code
	}
}

// AssertQuarantinedByID checks the quarantine contract of a download handler for
// a scan anchored on a row id alone: the identifier the package listing shows
// and the UI, the license policy and the OSV worker key scans by, never a path
// or file name. get returns the status of a GET of a path. Both blocked and
// sibling are served while nothing is quarantined; with only id quarantined,
// blocked is refused with 403 and sibling is still served.
func AssertQuarantinedByID(tb testing.TB, q *Quarantine, id string, get func(path string) int, blocked, sibling string) {
	tb.Helper()

	if id == "" {
		tb.Fatal("the fixture has no row id to quarantine by")
	}

	q.Keys = map[string]bool{}

	for _, p := range []string{blocked, sibling} {
		if c := get(p); c != http.StatusOK {
			tb.Fatalf("GET %s = %d before the quarantine, want 200", p, c)
		}
	}

	q.Keys = map[string]bool{id: true}

	if c := get(blocked); c != http.StatusForbidden {
		tb.Errorf("GET %s with row id %q quarantined = %d, want 403", blocked, id, c)
	}

	if c := get(sibling); c != http.StatusOK {
		tb.Errorf("GET %s next to the quarantined row = %d, want 200", sibling, c)
	}
}

// formatStore is what DB is used as.
type formatStore interface {
	ArtifactExists(ctx context.Context, sha256 string, repositoryID string) (bool, error)
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CargoIndexStamp(ctx context.Context, repoID string, name string) (string, error)
	CondaRepodataStamp(ctx context.Context, repoID string, platform string) (string, error)
	CreateAnsibleCollection(ctx context.Context, c *models.AnsibleCollection) error
	CreateCargoCrate(ctx context.Context, crate *models.CargoCrate) error
	CreateComposerPackage(ctx context.Context, p *models.ComposerPackage) error
	CreateConanPackage(ctx context.Context, pkg *models.ConanPackage) error
	CreateCondaPackage(ctx context.Context, pkg *models.CondaPackage) error
	CreateDebPackage(ctx context.Context, pkg *models.Component, distID string, compID string, control models.DebControl) error
	CreateGoModule(ctx context.Context, mod *models.GoModule) error
	CreateHelmChart(ctx context.Context, chart *models.HelmChart) error
	CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error
	CreateNPMPackage(ctx context.Context, p *models.NPMPackage) (*models.NPMPackage, error)
	CreateNPMVersion(ctx context.Context, v *models.NPMVersion) (*models.NPMVersion, error)
	CreateNixPath(ctx context.Context, p *models.NixPath) error
	CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error
	CreatePacmanPackage(ctx context.Context, pkg *models.PacmanPackage) error
	CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error
	CreateRubyGem(ctx context.Context, gem *models.RubyGem) error
	CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error
	CreateTerraformModule(ctx context.Context, mod *models.TerraformModule) error
	CreateTerraformProvider(ctx context.Context, prov *models.TerraformProvider) error
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	DeleteAnsibleCollection(ctx context.Context, repoID string, id string) error
	DeleteComponent(ctx context.Context, componentID string) error
	DeleteComposerPackage(ctx context.Context, repoID string, id string) error
	DeleteConanPackage(ctx context.Context, id string) error
	DeleteCondaPackage(ctx context.Context, id string) error
	DeleteHelmChart(ctx context.Context, repoID string, name string, version string) error
	DeleteIvyArtifact(ctx context.Context, id string) error
	DeleteNPMDistTag(ctx context.Context, packageID string, tag string) error
	DeleteNPMPackage(ctx context.Context, packageID string) error
	DeleteNPMVersion(ctx context.Context, packageID string, version string) error
	DeleteNixPath(ctx context.Context, repoID string, id string) error
	DeleteNuGetPackage(ctx context.Context, repoID string, packageID string, version string) error
	DeletePacmanPackage(ctx context.Context, repoID string, id string) error
	DeletePyPIPackage(ctx context.Context, repoID string, name string, version string, filename string) error
	DeleteRawArtifact(ctx context.Context, repoID string, path string) error
	DeleteSBTArtifact(ctx context.Context, id string) error
	DeleteTerraformModule(ctx context.Context, repoID string, id string) error
	DeleteTerraformProvider(ctx context.Context, repoID string, id string) error
	FindConanPackageBySHA1(ctx context.Context, repoID string, sha1 string) (*models.ConanPackage, error)
	GetAnsibleCollection(
		ctx context.Context,
		repoID string,
		namespace string,
		name string,
		version string,
	) (*models.AnsibleCollection, error)
	GetAnsibleCollectionByID(ctx context.Context, repoID string, id string) (*models.AnsibleCollection, error)
	GetCargoCrate(ctx context.Context, repoID string, name string, version string) (*models.CargoCrate, error)
	GetComponentByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error)
	GetComposerPackage(ctx context.Context, repoID string, name string, versionNormalized string) (*models.ComposerPackage, error)
	GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error)
	GetCondaPackageFile(
		ctx context.Context,
		repoID string,
		name string,
		version string,
		buildString string,
		platform string,
		extension string,
	) (*models.CondaPackage, error)
	GetDebBlobInfo(ctx context.Context, blobID string) (string, int64, error)
	GetDebPackageByID(ctx context.Context, id string) (*models.DebPackage, error)
	GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error)
	GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error)
	GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error)
	GetDebianPackageByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error)
	GetGoModule(ctx context.Context, repoID string, modulePath string, version string) (*models.GoModule, error)
	GetHelmChart(ctx context.Context, repoID string, name string, version string) (*models.HelmChart, error)
	GetIvyArtifact(
		ctx context.Context,
		repoID string,
		organisation string,
		module string,
		revision string,
		artifactName string,
		artifactExt string,
	) (*models.IvyArtifact, error)
	GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error)
	GetNPMPackageByName(ctx context.Context, repoID string, fullName string) (*models.NPMPackage, error)
	GetNPMVersion(ctx context.Context, packageID string, version string) (*models.NPMVersion, error)
	GetNPMVersionsByPackage(ctx context.Context, packageID string) ([]*models.NPMVersion, error)
	GetNixPathByHash(ctx context.Context, repoID string, storeHash string) (*models.NixPath, error)
	GetNixPathByNarFile(ctx context.Context, repoID string, narFile string) (*models.NixPath, error)
	GetNixPendingNar(ctx context.Context, repoID string, sha256 string) (*models.NixPendingNar, error)
	GetNuGetPackage(ctx context.Context, repoID string, packageID string, version string) (*models.NuGetPackage, error)
	GetOrCreateDebianArchitecture(ctx context.Context, distID string, name string) (*models.DebianArchitecture, error)
	GetOrCreateDebianComponent(ctx context.Context, distID string, name string) (*models.DebianComponent, error)
	GetOrCreateDebianDistribution(ctx context.Context, repoID string, name string) (*models.DebianDistribution, error)
	GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error)
	GetPacmanPackageByFilename(ctx context.Context, repoID string, filename string) (*models.PacmanPackage, error)
	GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error)
	GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error)
	GetProjectByName(ctx context.Context, name string) (*models.Project, error)
	GetPyPIPackageByFilename(ctx context.Context, repoID string, filename string) (*models.PyPIPackage, error)
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error)
	GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	GetRubyGem(ctx context.Context, repoID string, name string, version string, platform string) (*models.RubyGem, error)
	GetSBTArtifact(
		ctx context.Context,
		repoID string,
		groupID string,
		artifactID string,
		version string,
		filename string,
	) (*models.SBTArtifact, error)
	GetSessionByTokenHash(ctx context.Context, hash string) (*models.Session, error)
	GetTerraformModule(ctx context.Context, repoID string, name string, system string, version string) (*models.TerraformModule, error)
	GetTerraformProvider(
		ctx context.Context,
		repoID string,
		typ string,
		version string,
		osName string,
		arch string,
	) (*models.TerraformProvider, error)
	GetTokenByHash(ctx context.Context, hash string) (*models.Token, error)
	GetUserByID(ctx context.Context, id string) (*models.User, error)
	HelmIndexStamp(ctx context.Context, repoID string) (string, error)
	IncrementCargoDownloadCount(ctx context.Context, repoID string, name string, version string) error
	IncrementNixDownloadCount(ctx context.Context, id string, repoID string) error
	IncrementNuGetDownloadCount(ctx context.Context, repoID string, packageID string, version string) error
	IncrementTerraformModuleDownloadCount(ctx context.Context, repoID string, id string) error
	IncrementTerraformProviderDownloadCount(ctx context.Context, repoID string, id string) error
	ListAnsibleCollectionNames(ctx context.Context, repoID string, limit int, offset int) ([]*models.AnsibleCollectionName, int, error)
	ListAnsibleCollectionVersions(ctx context.Context, repoID string, namespace string, name string) ([]*models.AnsibleCollection, error)
	ListCargoCrateVersions(ctx context.Context, repoID string, name string) ([]*models.CargoCrate, error)
	ListCargoIndexVersions(ctx context.Context, repoID string, name string) ([]*models.CargoCrate, error)
	ListComposerPackageNames(ctx context.Context, repoID string) ([]*models.ComposerPackageName, error)
	ListComposerPackageVersions(ctx context.Context, repoID string, name string) ([]*models.ComposerPackage, error)
	ListConanRecipeFiles(
		ctx context.Context,
		repoID string,
		name string,
		version string,
		userChannel string,
	) ([]*models.ConanPackage, error)
	ListConanRecipes(ctx context.Context, repoID string) ([]models.ConanRecipe, error)
	ListCondaPackages(ctx context.Context, repoID string, platform string) ([]*models.CondaPackage, error)
	ListDebPackageEntries(ctx context.Context, repoID string, dist string, comp string, arch string) ([]models.DebPackageEntry, error)
	ListGoModuleVersions(ctx context.Context, repoID string, modulePath string) ([]*models.GoModule, error)
	ListGroupsByUser(ctx context.Context, userID string) ([]*models.Group, error)
	ListHelmCharts(ctx context.Context, repoID string) ([]*models.HelmChart, error)
	ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error)
	ListNuGetPackageVersions(ctx context.Context, repoID string, packageID string) ([]*models.NuGetPackage, error)
	ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error)
	ListPublicRepositoryNames(ctx context.Context) ([]string, error)
	ListPyPIPackageNames(ctx context.Context, repoID string) ([]string, error)
	ListPyPIPackageVersions(ctx context.Context, repoID string, name string) ([]*models.PyPIPackage, error)
	ListRawArtifacts(ctx context.Context, repoID string, prefix string) ([]*models.RawArtifact, error)
	ListRubyGemSpecs(ctx context.Context, repoID string) ([]*models.RubyGem, error)
	ListRubyGemVersions(ctx context.Context, repoID string, name string) ([]*models.RubyGem, error)
	ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error)
	ListTerraformModuleVersions(ctx context.Context, repoID string, name string, system string) ([]*models.TerraformModule, error)
	ListTerraformProviderPlatforms(ctx context.Context, repoID string, typ string) ([]*models.TerraformProvider, error)
	ListTerraformProviderVersionPlatforms(
		ctx context.Context,
		repoID string,
		typ string,
		version string,
	) ([]*models.TerraformProvider, error)
	MarkPackumentStale(ctx context.Context, packageID string) error
	PutNixPendingNar(ctx context.Context, n *models.NixPendingNar) error
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	RubyGemsSpecsStamp(ctx context.Context, repoID string) (string, error)
	SearchCargoCrateSummaries(ctx context.Context, repoID string, query string, limit int) ([]*models.CargoCrateSummary, int, error)
	SearchNuGetPackages(ctx context.Context, repoID string, query string, skip int, take int) ([]*models.NuGetPackage, int, error)
	SearchRubyGems(ctx context.Context, repoID string, query string) ([]*models.RubyGem, error)
	SetConanPackageDigests(ctx context.Context, id string, md5 string, sha1 string) error
	SetNuGetDependencyGroups(ctx context.Context, repoID string, packageID string, version string, groups []byte) error
	SetPacmanSignature(ctx context.Context, repoID string, id string, signature string, source string, key string) error
	TerraformModuleStamp(ctx context.Context, repoID string, name string, system string) (string, error)
	TerraformProviderStamp(ctx context.Context, repoID string, typ string) (string, error)
	UpdateNPMVersionManifest(ctx context.Context, packageID string, version string, manifest []byte) error
	UpdateSessionLastSeen(ctx context.Context, sessionID string, expiresAt time.Time) error
	UpdateTokenLastUsed(ctx context.Context, tokenID string) error
	UpsertBlob(ctx context.Context, sha256 string, blobKey string, sizeBytes int64) (string, error)
	UpsertNPMDistTag(ctx context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error)
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
	YankCargoCrate(ctx context.Context, repoID string, name string, version string, yanked bool) error
	YankRubyGem(ctx context.Context, repoID string, name string, version string, platform string, yanked bool) error
}

// DB is a database fake serving one repository, with quarantine
// lookups and a zero blob reference count (so SafeDeleteBlob removes blobs).
// Embed it in a per-format mock that adds the format's package methods.
type DB struct {
	formatStore
	Quarantine

	Repo *models.Repository
}

// NewDB returns a DB serving repository "myrepo" (ID "repo-1").
func NewDB() *DB {
	d := &DB{Repo: &models.Repository{ID: "repo-1", Name: "myrepo"}}
	d.Keys = map[string]bool{}

	return d
}

// GetRepositoryByName returns the repository when the name matches.
func (d *DB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if d.Repo != nil && d.Repo.Name == name {
		return d.Repo, nil
	}

	return nil, nil //nolint:nilnil // not found is (nil, nil) in the store contract
}

// BlobReferenceCount always reports no references.
func (d *DB) BlobReferenceCount(_ context.Context, _ string) (int, error) { return 0, nil }

// NewLogger returns a quiet logger.
func NewLogger(tb testing.TB) *logger.Logger {
	tb.Helper()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		tb.Fatalf("logger.New: %v", err)
	}

	return log
}

// Do routes req through a chi router mounted at /repos/{name} using register
// and returns the recorded response.
func Do(register func(chi.Router), req *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Route("/repos/{name}", register)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w
}

// Request builds a request with a background context.
func Request(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), method, target, body)
}

// MavenGARefsFromArtifacts derives what ListMavenGARefs returns (versions in
// string order, snapshot files for -SNAPSHOT versions) from full artifact rows,
// for in-memory test databases.
func MavenGARefsFromArtifacts(arts []*models.MavenArtifact) *models.MavenGARefs {
	refs := &models.MavenGARefs{}

	for _, a := range arts {
		refs.Versions = append(refs.Versions, a.Version)

		if strings.HasSuffix(a.Version, "-SNAPSHOT") {
			refs.Snapshots = append(refs.Snapshots, models.MavenSnapshotRef{
				Version:     a.Version,
				Classifier:  a.Classifier,
				Extension:   a.Extension,
				Timestamp:   a.SnapshotTimestamp,
				BuildNumber: a.SnapshotBuildNumber,
			})
		}
	}

	slices.Sort(refs.Versions)
	refs.Versions = slices.Compact(refs.Versions)

	return refs
}
