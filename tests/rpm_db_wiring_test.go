// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type MockDatabase struct {
	database.BaseMockDB

	mu                      sync.Mutex
	repositories            map[string]*models.Repository
	rpmRepos                map[string]*models.RPMRepo
	components              map[string]*models.Component
	repoMetadata            map[string]map[string]*models.RepoMetadata
	debianDistributions     map[string]*models.DebianDistribution
	debianComponents        map[string]*models.DebianComponent
	debianArchitectures     map[string]*models.DebianArchitecture
	mavenArtifacts          map[string]*models.MavenArtifact
	npmPackages             map[string]*models.NPMPackage
	npmVersions             map[string]*models.NPMVersion
	npmDistTags             map[string]map[string]string
	authUsers               map[string]*models.User
	authUsersByName         map[string]*models.User
	authGroups              map[string]*models.Group
	authUserGroups          map[string]map[string]bool
	authUserGroupsSSO       map[string]map[string]bool
	authSessions            map[string]*models.Session
	authTokens              map[string]*models.Token
	authTokensByID          map[string]*models.Token
	permissionTargets       map[string]*models.PermissionTarget
	permissionTargetsByName map[string]*models.PermissionTarget
	permissionAssignments   []models.PermissionAssignment
	ssoProviders            map[string]*models.SSOProvider
	ssoProvidersBySlug      map[string]*models.SSOProvider
	userExternalIDs         map[string]*models.User
	storageBackends         map[string]*models.StorageBackend
	AuditLogs               []*models.AuditLog

	// Quota fakes shared by the upload tests. Guarded by mu.
	quotaExceeded     bool  // ReserveStorageUsage reports "over quota" while set
	quotaReserved     int64 // bytes currently reserved (reserve - release)
	quotaArtifacts    int   // artifacts currently reserved
	quotaReserveCalls int   // successful reservations ever made
	quotaReleaseCalls int   // releases ever made
}

func newMockDatabase() *MockDatabase {
	now := time.Now()
	adminAllTarget := &models.PermissionTarget{
		ID:               "981948bd-de8f-5c65-b90b-23d72cf37aab",
		Name:             "admin-all",
		RepositoryFilter: ".*",
		PathPattern:      ".*",
		Actions:          []string{"read", "write", "delete", "manage", "annotate"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	anonReadTarget := &models.PermissionTarget{
		ID:               "seed-anon-read",
		Name:             "anonymous-read",
		RepositoryFilter: ".*",
		PathPattern:      ".*",
		Actions:          []string{"read"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	return &MockDatabase{
		repositories:        make(map[string]*models.Repository),
		rpmRepos:            make(map[string]*models.RPMRepo),
		components:          make(map[string]*models.Component),
		repoMetadata:        make(map[string]map[string]*models.RepoMetadata),
		debianDistributions: make(map[string]*models.DebianDistribution),
		debianComponents:    make(map[string]*models.DebianComponent),
		debianArchitectures: make(map[string]*models.DebianArchitecture),
		mavenArtifacts:      make(map[string]*models.MavenArtifact),
		npmPackages:         make(map[string]*models.NPMPackage),
		npmVersions:         make(map[string]*models.NPMVersion),
		npmDistTags:         make(map[string]map[string]string),
		authUsers:           make(map[string]*models.User),
		authUsersByName:     make(map[string]*models.User),
		authGroups:          make(map[string]*models.Group),
		authUserGroups:      make(map[string]map[string]bool),
		authUserGroupsSSO:   make(map[string]map[string]bool),
		authSessions:        make(map[string]*models.Session),
		authTokens:          make(map[string]*models.Token),
		authTokensByID:      make(map[string]*models.Token),
		permissionTargets: map[string]*models.PermissionTarget{
			adminAllTarget.ID: adminAllTarget,
			anonReadTarget.ID: anonReadTarget,
		},
		permissionTargetsByName: map[string]*models.PermissionTarget{
			adminAllTarget.Name: adminAllTarget,
			anonReadTarget.Name: anonReadTarget,
		},
		permissionAssignments: []models.PermissionAssignment{},
		ssoProviders:          make(map[string]*models.SSOProvider),
		ssoProvidersBySlug:    make(map[string]*models.SSOProvider),
		userExternalIDs:       make(map[string]*models.User),
		storageBackends:       make(map[string]*models.StorageBackend),
	}
}

func (m *MockDatabase) GetDB() *sql.DB {
	return nil
}

func (m *MockDatabase) Close() error {
	return nil
}

func (m *MockDatabase) Ping(ctx context.Context) error {
	return nil
}

func (m *MockDatabase) BeginTx(ctx context.Context) (database.Transaction, error) {
	return nil, nil
}

func (m *MockDatabase) Migrate(ctx context.Context) error {
	return nil
}

func (m *MockDatabase) GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error) {
	for _, repo := range m.repositories {
		if repo.Name == name {
			return repo, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) CreateRepository(ctx context.Context, repo *models.Repository) error {
	if repo.ID == "" {
		repo.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	m.repositories[repo.ID] = repo

	return nil
}

func (m *MockDatabase) CreateRepositoryWithMembers(ctx context.Context, repo *models.Repository, _ []string) error {
	return m.CreateRepository(ctx, repo)
}

func (m *MockDatabase) CountBlobsOnStorageBackend(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (m *MockDatabase) ListRepositories(ctx context.Context, page, limit int, formatFilter string) ([]*models.Repository, error) {
	var result []*models.Repository

	for _, r := range m.repositories {
		if formatFilter == "" || r.Format == formatFilter {
			result = append(result, r)
		}
	}

	offset := (page - 1) * limit
	if offset >= len(result) {
		return []*models.Repository{}, nil
	}

	end := min(offset+limit, len(result))

	return result[offset:end], nil
}

func (m *MockDatabase) CountRepositories(ctx context.Context, formatFilter string) (int, error) {
	count := 0

	for _, r := range m.repositories {
		if formatFilter == "" || r.Format == formatFilter {
			count++
		}
	}

	return count, nil
}

func (m *MockDatabase) UpdateRepository(ctx context.Context, repo *models.Repository) error {
	m.repositories[repo.ID] = repo
	return nil
}

func (m *MockDatabase) DeleteRepository(ctx context.Context, id string) error {
	delete(m.repositories, id)
	return nil
}

func (m *MockDatabase) ListComponentsByRepo(ctx context.Context, repoID, formatFilter string, page, limit int) ([]*models.Component, int, error) {
	var all []*models.Component

	for _, c := range m.components {
		if c.RepositoryID == repoID && (formatFilter == "" || c.Format == formatFilter) {
			all = append(all, c)
		}
	}

	total := len(all)

	offset := (page - 1) * limit
	if offset >= total {
		return []*models.Component{}, total, nil
	}

	end := min(offset+limit, total)

	return all[offset:end], total, nil
}

func (m *MockDatabase) GetRPMRepo(ctx context.Context, repositoryID string) (*models.RPMRepo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rpmRepo, ok := m.rpmRepos[repositoryID]
	if !ok {
		return nil, nil
	}

	return rpmRepo, nil
}

func (m *MockDatabase) CreateRPMRepo(ctx context.Context, repo *models.RPMRepo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if repo.ID == "" {
		repo.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	m.rpmRepos[repo.RepositoryID] = repo

	return nil
}

func (m *MockDatabase) MarkRepoStale(ctx context.Context, repositoryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rpmRepo, ok := m.rpmRepos[repositoryID]
	if ok {
		rpmRepo.MetadataStale = true
	}

	return nil
}

func (m *MockDatabase) CreateComponent(ctx context.Context, component *models.Component) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if component.SHA256 != "" {
		for _, c := range m.components {
			if c.SHA256 == component.SHA256 && c.RepositoryID == component.RepositoryID {
				return nil
			}
		}
	}

	if component.ID == "" {
		component.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	m.components[component.ID] = component

	return nil
}

func (m *MockDatabase) ArtifactExists(ctx context.Context, sha256 string, repositoryID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.components {
		if c.SHA256 == sha256 && c.RepositoryID == repositoryID {
			return true, nil
		}
	}

	return false, nil
}

func (m *MockDatabase) ArtifactExistsBySHA256(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}

func (m *MockDatabase) CreatePackageDelta(_ context.Context, _ *models.PackageDelta) error {
	return nil
}

func (m *MockDatabase) GetPackageDelta(_ context.Context, _, _, _ string) (*models.PackageDelta, error) {
	return nil, nil
}

func (m *MockDatabase) ListPackageDeltas(_ context.Context, _, _ string) ([]*models.PackageDelta, error) {
	return nil, nil
}

func (m *MockDatabase) DeletePackageDelta(_ context.Context, _ string) error {
	return nil
}

func (m *MockDatabase) GetComponentByID(ctx context.Context, id string) (*models.Component, error) {
	return m.components[id], nil
}

func (m *MockDatabase) GetComponentByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, comp := range m.components {
		if comp.RepositoryID == repoID && comp.Filename == filename {
			return comp, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) GetComponentsByRepo(ctx context.Context, repoID string) ([]models.Component, error) {
	var result []models.Component

	for _, comp := range m.components {
		if comp.RepositoryID == repoID {
			result = append(result, *comp)
		}
	}

	return result, nil
}

func (m *MockDatabase) DeleteComponent(ctx context.Context, componentID string) error {
	delete(m.components, componentID)
	return nil
}

func (m *MockDatabase) GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if meta, ok := m.repoMetadata[repositoryID]; ok {
		if metadata, ok := meta[path]; ok {
			return metadata, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) GetRepoMetadataByRepo(ctx context.Context, repositoryID string) ([]models.RepoMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []models.RepoMetadata

	if meta, ok := m.repoMetadata[repositoryID]; ok {
		for _, metadata := range meta {
			result = append(result, *metadata)
		}
	}

	return result, nil
}

func (m *MockDatabase) UpsertRepoMetadata(ctx context.Context, meta *models.RepoMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if meta.ID == "" {
		meta.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	if _, ok := m.repoMetadata[meta.RepositoryID]; !ok {
		m.repoMetadata[meta.RepositoryID] = make(map[string]*models.RepoMetadata)
	}

	m.repoMetadata[meta.RepositoryID][meta.Path] = meta

	return nil
}

func (m *MockDatabase) CreateDebianDistribution(ctx context.Context, dist *models.DebianDistribution) error {
	if dist.ID == "" {
		dist.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	m.debianDistributions[dist.ID] = dist

	return nil
}

func (m *MockDatabase) GetDebianDistribution(ctx context.Context, repoID string, name string) (*models.DebianDistribution, error) {
	for _, dist := range m.debianDistributions {
		if dist.RepositoryID == repoID && dist.Name == name {
			return dist, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error) {
	var result []models.DebianDistribution

	for _, dist := range m.debianDistributions {
		if dist.RepositoryID == repoID {
			result = append(result, *dist)
		}
	}

	return result, nil
}

func (m *MockDatabase) CreateDebianComponent(ctx context.Context, comp *models.DebianComponent) error {
	if comp.ID == "" {
		comp.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	m.debianComponents[comp.ID] = comp

	return nil
}

func (m *MockDatabase) GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error) {
	var result []models.DebianComponent

	for _, comp := range m.debianComponents {
		if comp.DistributionID == distID {
			result = append(result, *comp)
		}
	}

	return result, nil
}

func (m *MockDatabase) CreateDebianArchitecture(ctx context.Context, arch *models.DebianArchitecture) error {
	if arch.ID == "" {
		arch.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	m.debianArchitectures[arch.ID] = arch

	return nil
}

func (m *MockDatabase) GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error) {
	var result []models.DebianArchitecture

	for _, arch := range m.debianArchitectures {
		if arch.DistributionID == distID {
			result = append(result, *arch)
		}
	}

	return result, nil
}

func (m *MockDatabase) GetDebianPackageByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	return nil, nil
}

func (m *MockDatabase) GetDebianPackagesByDistComp(ctx context.Context, repoID string, dist string, comp string, arch string) ([]models.Component, error) {
	return []models.Component{}, nil
}

func (m *MockDatabase) CreateMavenArtifact(ctx context.Context, a *models.MavenArtifact) error {
	if a.ID == "" {
		a.ID = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}

	m.mavenArtifacts[a.ID] = a

	return nil
}

func (m *MockDatabase) GetMavenArtifactByPath(ctx context.Context, repoID, path string) (*models.MavenArtifact, error) {
	for _, a := range m.mavenArtifacts {
		if a.RepositoryID == repoID && a.RepoPath == path {
			return a, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) GetMavenArtifactsByGAV(ctx context.Context, repoID, groupID, artifactID, version string) ([]*models.MavenArtifact, error) {
	var result []*models.MavenArtifact

	for _, a := range m.mavenArtifacts {
		if a.RepositoryID == repoID && a.GroupID == groupID && a.ArtifactCoord == artifactID && a.Version == version {
			result = append(result, a)
		}
	}

	return result, nil
}

func (m *MockDatabase) GetMavenArtifactsByGA(ctx context.Context, repoID, groupID, artifactID string) ([]*models.MavenArtifact, error) {
	var result []*models.MavenArtifact

	for _, a := range m.mavenArtifacts {
		if a.RepositoryID == repoID && a.GroupID == groupID && a.ArtifactCoord == artifactID {
			result = append(result, a)
		}
	}

	return result, nil
}

func (m *MockDatabase) GetLatestSnapshot(ctx context.Context, repoID, groupID, artifactID, version string) (*models.MavenArtifact, error) {
	var latest *models.MavenArtifact

	for _, a := range m.mavenArtifacts {
		if a.RepositoryID == repoID && a.GroupID == groupID && a.ArtifactCoord == artifactID && a.Version == version && a.IsSnapshot {
			if latest == nil || a.SnapshotTimestamp > latest.SnapshotTimestamp {
				latest = a
			}
		}
	}

	return latest, nil
}

func (m *MockDatabase) DeleteMavenArtifact(ctx context.Context, id string) error {
	delete(m.mavenArtifacts, id)
	return nil
}

func (m *MockDatabase) CreateNPMPackage(ctx context.Context, p *models.NPMPackage) (*models.NPMPackage, error) {
	if p.ID == "" {
		p.ID = fmt.Sprintf("npm-pkg-%d", len(m.npmPackages)+1)
	}

	m.npmPackages[p.ID] = p

	return p, nil
}

func (m *MockDatabase) DeleteNPMPackage(ctx context.Context, packageID string) error {
	delete(m.npmPackages, packageID)
	return nil
}

func (m *MockDatabase) GetNPMPackageByName(ctx context.Context, repoID, fullName string) (*models.NPMPackage, error) {
	for _, p := range m.npmPackages {
		if p.RepositoryID == repoID && p.FullName == fullName {
			return p, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) MarkPackumentStale(ctx context.Context, packageID string) error {
	if p, ok := m.npmPackages[packageID]; ok {
		p.PackumentStale = true
	}

	return nil
}

func (m *MockDatabase) CreateNPMVersion(ctx context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	key := v.PackageID + ":" + v.Version
	if m.npmVersions[key] != nil {
		return nil, fmt.Errorf("npm version already exists: %s@%s", v.PackageID, v.Version)
	}

	if v.ID == "" {
		v.ID = fmt.Sprintf("npm-ver-%d", len(m.npmVersions)+1)
	}

	m.npmVersions[key] = v

	return v, nil
}

func (m *MockDatabase) GetNPMVersion(ctx context.Context, packageID, version string) (*models.NPMVersion, error) {
	return m.npmVersions[packageID+":"+version], nil
}

func (m *MockDatabase) GetNPMVersionsByPackage(ctx context.Context, packageID string) ([]*models.NPMVersion, error) {
	var result []*models.NPMVersion

	for _, v := range m.npmVersions {
		if v.PackageID == packageID {
			result = append(result, v)
		}
	}

	return result, nil
}

func (m *MockDatabase) GetNPMVersionStates(ctx context.Context, packageID string) ([]models.NPMVersionState, error) {
	versions, _ := m.GetNPMVersionsByPackage(ctx, packageID)

	return npmVersionStates(versions), nil
}

// npmVersionStates is what GetNPMVersionStates reads in SQL: the version and
// whether its manifest has a non-empty string "deprecated".
func npmVersionStates(versions []*models.NPMVersion) []models.NPMVersionState {
	states := make([]models.NPMVersionState, 0, len(versions))

	for _, v := range versions {
		var manifest struct {
			Deprecated any `json:"deprecated"`
		}

		_ = json.Unmarshal(v.ManifestJSON, &manifest)
		msg, _ := manifest.Deprecated.(string)

		states = append(states, models.NPMVersionState{Version: v.Version, Deprecated: msg != ""})
	}

	return states
}

func (m *MockDatabase) UpdateNPMVersionManifest(ctx context.Context, packageID, version string, manifest []byte) error {
	if v := m.npmVersions[packageID+":"+version]; v != nil {
		v.ManifestJSON = manifest
	}

	return nil
}

func (m *MockDatabase) DeleteNPMVersion(ctx context.Context, packageID, version string) error {
	delete(m.npmVersions, packageID+":"+version)
	return nil
}

func (m *MockDatabase) UpsertNPMDistTag(ctx context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	if t.ID == "" {
		t.ID = fmt.Sprintf("npm-tag-%d", len(m.npmDistTags)+1)
	}

	if m.npmDistTags[t.PackageID] == nil {
		m.npmDistTags[t.PackageID] = make(map[string]string)
	}

	m.npmDistTags[t.PackageID][t.Tag] = t.Version

	return t, nil
}

func (m *MockDatabase) GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error) {
	tags := m.npmDistTags[packageID]
	if tags == nil {
		return make(map[string]string), nil
	}

	result := make(map[string]string, len(tags))
	maps.Copy(result, tags)

	return result, nil
}

func (m *MockDatabase) DeleteNPMDistTag(ctx context.Context, packageID, tag string) error {
	if tags, ok := m.npmDistTags[packageID]; ok {
		delete(tags, tag)
	}

	return nil
}

func (m *MockDatabase) UpdateNPMPackageLatestVersion(ctx context.Context, packageID, latestVersion string) error {
	if p, ok := m.npmPackages[packageID]; ok {
		p.LatestVersion = latestVersion
		p.PackumentStale = false
	}

	return nil
}

func (m *MockDatabase) CreateUser(ctx context.Context, user *models.User) (*models.User, error) {
	if user.ID == "" {
		user.ID = uuid.NewString()
	}

	m.authUsers[user.ID] = user
	m.authUsersByName[user.Username] = user

	return user, nil
}

func (m *MockDatabase) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	return m.authUsers[id], nil
}

func (m *MockDatabase) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	return m.authUsersByName[username], nil
}

func (m *MockDatabase) UpdateUser(ctx context.Context, user *models.User) error {
	if _, ok := m.authUsers[user.ID]; !ok {
		return nil
	}

	m.authUsers[user.ID] = user
	m.authUsersByName[user.Username] = user

	return nil
}

func (m *MockDatabase) UpdateUserSSOProfile(ctx context.Context, id string, email, fullName *string, at time.Time) error {
	if u, ok := m.authUsers[id]; ok {
		if email != nil {
			u.Email = email
		}

		if fullName != nil {
			u.FullName = fullName
		}

		u.LastLoginAt = &at
	}

	return nil
}

func (m *MockDatabase) UpdateUserLastLogin(ctx context.Context, id string, at time.Time) error {
	if u, ok := m.authUsers[id]; ok {
		u.LastLoginAt = &at
	}

	return nil
}

func (m *MockDatabase) UpdateUserPassword(ctx context.Context, id, passwordHash string, mustChange bool) error {
	if u, ok := m.authUsers[id]; ok {
		u.PasswordHash = &passwordHash
		u.MustChangePassword = mustChange
	}

	return nil
}

func (m *MockDatabase) ListUsers(ctx context.Context, page, perPage int) ([]*models.User, int, error) {
	var users []*models.User
	for _, u := range m.authUsers {
		users = append(users, u)
	}

	total := len(users)

	start := (page - 1) * perPage
	if start >= total {
		return []*models.User{}, total, nil
	}

	end := min(start+perPage, total)

	return users[start:end], total, nil
}

func (m *MockDatabase) CountUsers(ctx context.Context) (int, error) {
	return len(m.authUsers), nil
}

func (m *MockDatabase) CreateGroup(ctx context.Context, group *models.Group) (*models.Group, error) {
	if group.ID == "" {
		group.ID = uuid.NewString()
	}

	m.authGroups[group.ID] = group

	return group, nil
}

func (m *MockDatabase) GetGroupByID(ctx context.Context, id string) (*models.Group, error) {
	return m.authGroups[id], nil
}

func (m *MockDatabase) UpdateGroup(ctx context.Context, group *models.Group) error {
	m.authGroups[group.ID] = group
	return nil
}

func (m *MockDatabase) DeleteGroup(ctx context.Context, id string) error {
	delete(m.authGroups, id)
	return nil
}

func (m *MockDatabase) ListGroups(ctx context.Context) ([]*models.Group, error) {
	var groups []*models.Group
	for _, g := range m.authGroups {
		groups = append(groups, g)
	}

	return groups, nil
}

func (m *MockDatabase) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	if m.authUserGroups[userID] == nil {
		m.authUserGroups[userID] = make(map[string]bool)
	}

	m.authUserGroups[userID][groupID] = true

	return nil
}

func (m *MockDatabase) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	if m.authUserGroups[userID] != nil {
		delete(m.authUserGroups[userID], groupID)
	}

	return nil
}

func (m *MockDatabase) ListGroupsByUser(ctx context.Context, userID string) ([]*models.Group, error) {
	var groups []*models.Group

	for groupID := range m.authUserGroups[userID] {
		if g, ok := m.authGroups[groupID]; ok {
			groups = append(groups, g)
		}
	}

	return groups, nil
}

func (m *MockDatabase) ListUsersByGroup(ctx context.Context, groupID string) ([]*models.User, error) {
	var users []*models.User

	for userID, groupSet := range m.authUserGroups {
		if groupSet[groupID] {
			if u, ok := m.authUsers[userID]; ok {
				users = append(users, u)
			}
		}
	}

	return users, nil
}

func (m *MockDatabase) CreateSession(ctx context.Context, session *models.Session) (*models.Session, error) {
	if session.ID == "" {
		session.ID = fmt.Sprintf("session-%d", len(m.authSessions)+1)
	}

	m.authSessions[session.TokenHash] = session

	return session, nil
}

func (m *MockDatabase) GetSessionByTokenHash(ctx context.Context, hash string) (*models.Session, error) {
	return m.authSessions[hash], nil
}

func (m *MockDatabase) DeleteSession(ctx context.Context, tokenHash string) error {
	delete(m.authSessions, tokenHash)
	return nil
}

func (m *MockDatabase) DeleteSessionsByUser(ctx context.Context, userID string) error {
	for hash, s := range m.authSessions {
		if s.UserID == userID {
			delete(m.authSessions, hash)
		}
	}

	return nil
}

func (m *MockDatabase) DeleteOtherSessionsByUser(ctx context.Context, userID, keepTokenHash string) error {
	for hash, s := range m.authSessions {
		if s.UserID == userID && hash != keepTokenHash {
			delete(m.authSessions, hash)
		}
	}

	return nil
}

func (m *MockDatabase) UpdateSessionLastSeen(ctx context.Context, sessionID string, expiresAt time.Time) error {
	for _, s := range m.authSessions {
		if s.ID == sessionID {
			s.ExpiresAt = expiresAt
			s.LastSeenAt = time.Now()
		}
	}

	return nil
}

func (m *MockDatabase) CreateToken(ctx context.Context, token *models.Token) (*models.Token, error) {
	if token.ID == "" {
		token.ID = uuid.NewString()
	}

	m.authTokens[token.TokenHash] = token
	m.authTokensByID[token.ID] = token

	return token, nil
}

func (m *MockDatabase) GetTokenByHash(ctx context.Context, hash string) (*models.Token, error) {
	return m.authTokens[hash], nil
}

func (m *MockDatabase) GetTokenByID(ctx context.Context, tokenID string) (*models.Token, error) {
	return m.authTokensByID[tokenID], nil
}

func (m *MockDatabase) RevokeToken(ctx context.Context, tokenID string) error {
	if t, ok := m.authTokensByID[tokenID]; ok {
		now := time.Now()
		t.RevokedAt = &now
	}

	return nil
}

func (m *MockDatabase) UpdateTokenLastUsed(ctx context.Context, tokenID string) error {
	if t, ok := m.authTokensByID[tokenID]; ok {
		now := time.Now()
		t.LastUsedAt = &now
	}

	return nil
}

func (m *MockDatabase) ListTokensByUser(ctx context.Context, userID string) ([]*models.Token, error) {
	var tokens []*models.Token

	for _, t := range m.authTokensByID {
		if t.UserID == userID {
			tokens = append(tokens, t)
		}
	}

	return tokens, nil
}

func (m *MockDatabase) CreatePermissionTarget(ctx context.Context, target *models.PermissionTarget) (*models.PermissionTarget, error) {
	if target.ID == "" {
		target.ID = uuid.NewString()
	}

	m.permissionTargets[target.ID] = target
	m.permissionTargetsByName[target.Name] = target

	return target, nil
}

func (m *MockDatabase) GetPermissionTargetByID(ctx context.Context, id string) (*models.PermissionTarget, error) {
	return m.permissionTargets[id], nil
}

func (m *MockDatabase) GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error) {
	return m.permissionTargetsByName[name], nil
}

func (m *MockDatabase) UpdatePermissionTarget(ctx context.Context, target *models.PermissionTarget) error {
	m.permissionTargets[target.ID] = target
	m.permissionTargetsByName[target.Name] = target

	return nil
}

func (m *MockDatabase) DeletePermissionTarget(ctx context.Context, id string) error {
	if t, ok := m.permissionTargets[id]; ok {
		delete(m.permissionTargetsByName, t.Name)
		delete(m.permissionTargets, id)
	}

	return nil
}

func (m *MockDatabase) ListPermissionTargets(ctx context.Context) ([]*models.PermissionTarget, error) {
	var targets []*models.PermissionTarget
	for _, t := range m.permissionTargets {
		targets = append(targets, t)
	}

	return targets, nil
}

func (m *MockDatabase) CreatePermissionAssignment(ctx context.Context, assignment *models.PermissionAssignment) error {
	for _, a := range m.permissionAssignments {
		if a.PermissionTargetID == assignment.PermissionTargetID &&
			a.PrincipalType == assignment.PrincipalType &&
			a.PrincipalID == assignment.PrincipalID {
			return nil
		}
	}

	if assignment.ID == "" {
		assignment.ID = fmt.Sprintf("pa-%d", len(m.permissionAssignments)+1)
	}

	m.permissionAssignments = append(m.permissionAssignments, *assignment)

	return nil
}

func (m *MockDatabase) DeletePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error {
	var kept []models.PermissionAssignment

	for _, a := range m.permissionAssignments {
		if a.PermissionTargetID == targetID && a.PrincipalType == principalType && a.PrincipalID == principalID {
			continue
		}

		kept = append(kept, a)
	}

	m.permissionAssignments = kept

	return nil
}

func (m *MockDatabase) GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error) {
	groupSet := make(map[string]bool, len(groupIDs))
	for _, gid := range groupIDs {
		groupSet[gid] = true
	}

	seen := make(map[string]bool)

	var targets []*models.PermissionTarget

	for _, a := range m.permissionAssignments {
		matched := (a.PrincipalType == "user" && a.PrincipalID == userID) ||
			(a.PrincipalType == "group" && groupSet[a.PrincipalID])
		if matched && !seen[a.PermissionTargetID] {
			if t, ok := m.permissionTargets[a.PermissionTargetID]; ok {
				targets = append(targets, t)
				seen[a.PermissionTargetID] = true
			}
		}
	}

	return targets, nil
}

func (m *MockDatabase) GetOrCreatePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error {
	return nil
}

func (m *MockDatabase) SyncSSOPermissionsForGroup(ctx context.Context, groupID string, targetIDs []string) error {
	return nil
}

func (m *MockDatabase) CreateSSOProvider(ctx context.Context, provider *models.SSOProvider) error {
	if provider.ID == "" {
		provider.ID = fmt.Sprintf("sso-%d", len(m.ssoProviders)+1)
	}

	now := time.Now()
	provider.CreatedAt = now

	provider.UpdatedAt = now
	if provider.GroupMapping == nil {
		provider.GroupMapping = make(map[string]string)
	}

	m.ssoProviders[provider.ID] = provider
	m.ssoProvidersBySlug[provider.Slug] = provider

	return nil
}

func (m *MockDatabase) GetSSOProviderBySlug(ctx context.Context, slug string) (*models.SSOProvider, error) {
	return m.ssoProvidersBySlug[slug], nil
}

func (m *MockDatabase) GetSSOProviderByID(ctx context.Context, id string) (*models.SSOProvider, error) {
	return m.ssoProviders[id], nil
}

func (m *MockDatabase) UpdateSSOProvider(ctx context.Context, provider *models.SSOProvider) error {
	existing, ok := m.ssoProviders[provider.ID]
	if !ok {
		return nil
	}

	if existing.Slug != provider.Slug {
		delete(m.ssoProvidersBySlug, existing.Slug)
	}

	provider.UpdatedAt = time.Now()
	if provider.GroupMapping == nil {
		provider.GroupMapping = make(map[string]string)
	}

	m.ssoProviders[provider.ID] = provider
	m.ssoProvidersBySlug[provider.Slug] = provider

	return nil
}

func (m *MockDatabase) DeleteSSOProvider(ctx context.Context, id string) error {
	if p, ok := m.ssoProviders[id]; ok {
		delete(m.ssoProvidersBySlug, p.Slug)
		delete(m.ssoProviders, id)
	}

	return nil
}

func (m *MockDatabase) ListEnabledSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	var result []*models.SSOProvider

	for _, p := range m.ssoProviders {
		if p.Enabled {
			result = append(result, p)
		}
	}

	return result, nil
}

func (m *MockDatabase) ListAllSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	var result []*models.SSOProvider
	for _, p := range m.ssoProviders {
		result = append(result, p)
	}

	return result, nil
}

func (m *MockDatabase) UpdateUserSSO(ctx context.Context, userID string, externalID, providerSlug string) error {
	u, ok := m.authUsers[userID]
	if !ok {
		return nil
	}

	u.ExternalID = &externalID
	u.ProviderSlug = &providerSlug
	m.userExternalIDs[externalID+":"+providerSlug] = u

	return nil
}

func (m *MockDatabase) SyncSSOGroups(ctx context.Context, userID string, groupIDs []string) error {
	m.authUserGroupsSSO[userID] = make(map[string]bool)
	for _, gid := range groupIDs {
		m.authUserGroupsSSO[userID][gid] = true
		if m.authUserGroups[userID] == nil {
			m.authUserGroups[userID] = make(map[string]bool)
		}

		m.authUserGroups[userID][gid] = true
	}

	return nil
}

func (m *MockDatabase) GetUserByExternalID(ctx context.Context, externalID, providerSlug string) (*models.User, error) {
	return m.userExternalIDs[externalID+":"+providerSlug], nil
}

func (m *MockDatabase) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	for _, u := range m.authUsers {
		if u.Email != nil && *u.Email == email {
			return u, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) GetGroupByName(ctx context.Context, name string) (*models.Group, error) {
	for _, g := range m.authGroups {
		if g.Name == name {
			return g, nil
		}
	}

	return nil, nil
}

func (m *MockDatabase) GetOrCreateGroup(ctx context.Context, name string) (*models.Group, error) {
	if g, err := m.GetGroupByName(ctx, name); err == nil && g != nil {
		return g, nil
	}

	return m.CreateGroup(ctx, &models.Group{Name: name, CreatedAt: time.Now(), UpdatedAt: time.Now()})
}

func (m *MockDatabase) AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error {
	return nil
}

func (m *MockDatabase) CreateBundlePromotion(ctx context.Context, promotion *models.BundlePromotion) error {
	return nil
}

func (m *MockDatabase) CreatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *MockDatabase) CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *MockDatabase) RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error {
	return nil
}

func (m *MockDatabase) ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *MockDatabase) GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *MockDatabase) ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error) {
	return nil, nil
}

func (m *MockDatabase) GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *MockDatabase) GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *MockDatabase) UpdatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *MockDatabase) DeletePromotionPipeline(ctx context.Context, id string) error {
	return nil
}

func (m *MockDatabase) ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *MockDatabase) GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *MockDatabase) GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *MockDatabase) UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *MockDatabase) DeleteReleaseBundle(ctx context.Context, id string) error {
	return nil
}

func (m *MockDatabase) ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error) {
	return nil, 0, nil
}

func (m *MockDatabase) UpdateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error {
	return nil
}

func (m *MockDatabase) InsertAuditLog(ctx context.Context, event *models.AuditLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.AuditLogs = append(m.AuditLogs, event)

	return nil
}

func (m *MockDatabase) ListAuditLogs(ctx context.Context, params models.AuditListParams) (*models.AuditPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var filtered []*models.AuditLog

	for _, log := range m.AuditLogs {
		if params.Actor != "" && (log.ActorID == nil || *log.ActorID != params.Actor) {
			continue
		}

		if params.Action != "" && log.Action != params.Action {
			continue
		}

		filtered = append(filtered, log)
	}

	total := len(filtered)
	start := min(params.Offset, total)

	if params.After != nil {
		start = total

		for i, log := range filtered {
			if log.ID == params.After.ID {
				start = i + 1
			}
		}
	}

	end := min(start+params.Limit, total)
	page := &models.AuditPage{Logs: filtered[start:end], Total: total, TotalExact: true}

	if end < total && end > start {
		last := filtered[end-1]
		page.Next = &models.AuditCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	return page, nil
}

func (m *MockDatabase) ListAllTokens(ctx context.Context, page, perPage int) ([]*models.Token, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var tokens []*models.Token
	for _, t := range m.authTokensByID {
		tokens = append(tokens, t)
	}

	total := len(tokens)
	start := (page - 1) * perPage

	end := start + perPage
	if start > total {
		start = total
	}

	if end > total {
		end = total
	}

	return tokens[start:end], total, nil
}

// Docker Registry methods
func (m *MockDatabase) CreateDockerBlob(ctx context.Context, blob *models.DockerBlob) error {
	return nil
}
func (m *MockDatabase) GetDockerBlob(ctx context.Context, repoID, digest string) (*models.DockerBlob, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteDockerBlob(ctx context.Context, repoID, digest string) error { return nil }
func (m *MockDatabase) ListDockerBlobs(ctx context.Context, repoID string) ([]*models.DockerBlob, error) {
	return nil, nil
}

func (m *MockDatabase) CreateDockerUpload(ctx context.Context, upload *models.DockerUpload) error {
	return nil
}
func (m *MockDatabase) GetDockerUpload(ctx context.Context, repositoryID, uploadUUID string) (*models.DockerUpload, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateDockerUploadOffset(ctx context.Context, repositoryID, uploadUUID string, oldOffset, newOffset int64) (bool, error) {
	return true, nil
}
func (m *MockDatabase) DeleteDockerUpload(ctx context.Context, repositoryID, uploadUUID string) error {
	return nil
}

func (m *MockDatabase) CreateDockerManifest(ctx context.Context, manifest *models.DockerManifest) error {
	return nil
}
func (m *MockDatabase) GetDockerManifest(ctx context.Context, repoID, image, digest string) (*models.DockerManifest, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteDockerManifest(ctx context.Context, repoID, image, digest string) error {
	return nil
}

func (m *MockDatabase) UpsertDockerTag(ctx context.Context, tag *models.DockerTag) error { return nil }
func (m *MockDatabase) GetDockerTag(ctx context.Context, repoID, image, name string) (*models.DockerTag, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteDockerTag(ctx context.Context, repoID, image, name string) error {
	return nil
}
func (m *MockDatabase) DeleteDockerTagsByDigest(ctx context.Context, repoID, image, digest string) error {
	return nil
}
func (m *MockDatabase) ListDockerTagsByRepo(ctx context.Context, repoID string, limit int) ([]*models.DockerTag, error) {
	return nil, nil
}
func (m *MockDatabase) ListDockerTags(ctx context.Context, repoID, image, last string, n int) ([]*models.DockerTag, error) {
	return nil, nil
}

// Proxy/Group repository methods
func (m *MockDatabase) AddGroupMember(ctx context.Context, groupRepoID, memberRepoID string, priority int) error {
	return nil
}
func (m *MockDatabase) RemoveGroupMember(ctx context.Context, groupRepoID, memberRepoID string) error {
	return nil
}
func (m *MockDatabase) ListGroupMembers(ctx context.Context, groupRepoID string) ([]*models.RepositoryGroupMember, error) {
	return nil, nil
}
func (m *MockDatabase) GetProxyCacheEntry(ctx context.Context, repoID, cacheKey string) (*models.ProxyCacheEntry, error) {
	return nil, nil
}
func (m *MockDatabase) UpsertProxyCacheEntry(ctx context.Context, entry *models.ProxyCacheEntry) error {
	return nil
}
func (m *MockDatabase) DeleteProxyCacheEntry(ctx context.Context, repoID, cacheKey string) error {
	return nil
}
func (m *MockDatabase) DeleteExpiredProxyCacheEntries(ctx context.Context) (int64, error) {
	return 0, nil
}

// Helm methods
func (m *MockDatabase) CreateHelmChart(ctx context.Context, chart *models.HelmChart) error {
	return nil
}
func (m *MockDatabase) GetHelmChart(ctx context.Context, repoID, name, version string) (*models.HelmChart, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteHelmChart(ctx context.Context, repoID, name, version string) error {
	return nil
}
func (m *MockDatabase) ListHelmCharts(ctx context.Context, repoID string) ([]*models.HelmChart, error) {
	return nil, nil
}
func (m *MockDatabase) ListHelmChartVersions(ctx context.Context, repoID, name string) ([]*models.HelmChart, error) {
	return nil, nil
}

// PyPI methods
func (m *MockDatabase) CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error {
	return nil
}
func (m *MockDatabase) GetPyPIPackage(ctx context.Context, repoID, name, version, filename string) (*models.PyPIPackage, error) {
	return nil, nil
}
func (m *MockDatabase) DeletePyPIPackage(ctx context.Context, repoID, name, version, filename string) error {
	return nil
}
func (m *MockDatabase) ListPyPIPackages(ctx context.Context, repoID string) ([]*models.PyPIPackage, error) {
	return nil, nil
}
func (m *MockDatabase) ListPyPIPackageVersions(ctx context.Context, repoID, name string) ([]*models.PyPIPackage, error) {
	return nil, nil
}

// NuGet methods
func (m *MockDatabase) CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error {
	return nil
}
func (m *MockDatabase) GetNuGetPackage(ctx context.Context, repoID, packageID, version string) (*models.NuGetPackage, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteNuGetPackage(ctx context.Context, repoID, packageID, version string) error {
	return nil
}
func (m *MockDatabase) ListNuGetPackages(ctx context.Context, repoID string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *MockDatabase) ListNuGetPackageVersions(ctx context.Context, repoID, packageID string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *MockDatabase) SearchNuGetPackages(ctx context.Context, repoID, query string, skip, take int) ([]*models.NuGetPackage, int, error) {
	return nil, 0, nil
}
func (m *MockDatabase) IncrementNuGetDownloadCount(ctx context.Context, repoID, packageID, version string) error {
	return nil
}

// Go Module Proxy methods
func (m *MockDatabase) CreateGoModule(ctx context.Context, mod *models.GoModule) error { return nil }
func (m *MockDatabase) GetGoModule(ctx context.Context, repoID, modulePath, version string) (*models.GoModule, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteGoModule(ctx context.Context, repoID, modulePath, version string) error {
	return nil
}
func (m *MockDatabase) ListGoModuleVersions(ctx context.Context, repoID, modulePath string) ([]*models.GoModule, error) {
	return nil, nil
}

// Cleanup policy methods
func (m *MockDatabase) CreateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	return nil
}
func (m *MockDatabase) GetCleanupPolicy(ctx context.Context, id string) (*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	return nil
}
func (m *MockDatabase) DeleteCleanupPolicy(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListCleanupPolicies(ctx context.Context, repoID string) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *MockDatabase) ListEnabledCleanupPolicies(ctx context.Context) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateCleanupPolicyRunResult(ctx context.Context, id string, deletedCount int, runErr error) error {
	return nil
}

// Webhook methods
func (m *MockDatabase) CreateWebhook(ctx context.Context, wh *models.Webhook) error { return nil }
func (m *MockDatabase) GetWebhook(ctx context.Context, id string) (*models.Webhook, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateWebhook(ctx context.Context, wh *models.Webhook) error { return nil }
func (m *MockDatabase) DeleteWebhook(ctx context.Context, id string) error          { return nil }
func (m *MockDatabase) ListWebhooks(ctx context.Context, repoID string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *MockDatabase) ListWebhooksForEvent(ctx context.Context, repoID, eventType string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *MockDatabase) CreateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	return nil
}
func (m *MockDatabase) UpdateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	return nil
}
func (m *MockDatabase) ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]*models.WebhookDelivery, error) {
	return nil, nil
}
func (m *MockDatabase) ClaimPendingWebhookDeliveries(ctx context.Context, limit int, lease time.Duration) ([]*models.WebhookDelivery, error) {
	return nil, nil
}

// Backup methods
func (m *MockDatabase) CreateBackupJob(ctx context.Context, job *models.BackupJob) error { return nil }
func (m *MockDatabase) GetBackupJob(ctx context.Context, id string) (*models.BackupJob, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateBackupJob(ctx context.Context, job *models.BackupJob) error { return nil }
func (m *MockDatabase) ListBackupJobs(ctx context.Context, limit int) ([]*models.BackupJob, error) {
	return nil, nil
}

// Cargo methods
func (m *MockDatabase) CreateCargoCrate(ctx context.Context, crate *models.CargoCrate) error {
	return nil
}
func (m *MockDatabase) GetCargoCrate(ctx context.Context, repoID, name, version string) (*models.CargoCrate, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteCargoCrate(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListCargoCrateVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *MockDatabase) SearchCargoCrates(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *MockDatabase) YankCargoCrate(ctx context.Context, repoID, name, version string, yanked bool) error {
	return nil
}
func (m *MockDatabase) IncrementCargoDownloadCount(ctx context.Context, repoID, name, version string) error {
	return nil
}

// RubyGems methods
func (m *MockDatabase) CreateRubyGem(ctx context.Context, gem *models.RubyGem) error { return nil }
func (m *MockDatabase) GetRubyGem(ctx context.Context, repoID, name, version, platform string) (*models.RubyGem, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteRubyGem(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *MockDatabase) ListRubyGemVersions(ctx context.Context, repoID, name string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *MockDatabase) SearchRubyGems(ctx context.Context, repoID, query string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *MockDatabase) YankRubyGem(ctx context.Context, repoID, name, version, platform string, yanked bool) error {
	return nil
}

// Raw artifact methods
func (m *MockDatabase) UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error {
	return nil
}
func (m *MockDatabase) GetRawArtifact(ctx context.Context, repoID, path string) (*models.RawArtifact, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteRawArtifact(ctx context.Context, repoID, path string) error { return nil }
func (m *MockDatabase) ListRawArtifacts(ctx context.Context, repoID, prefix string) ([]*models.RawArtifact, error) {
	return nil, nil
}

// SBOM methods
func (m *MockDatabase) CreateSBOMReport(ctx context.Context, report *models.SBOMReport) error {
	return nil
}
func (m *MockDatabase) GetSBOMReport(ctx context.Context, repoID, artifactID, format, specVersion string) (*models.SBOMReport, error) {
	return nil, nil
}
func (m *MockDatabase) ListSBOMReports(ctx context.Context, repoID string) ([]*models.SBOMReport, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteSBOMReport(ctx context.Context, id string) error { return nil }

// License compliance methods
func (m *MockDatabase) CreateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error {
	return nil
}
func (m *MockDatabase) GetLicenseScanResult(ctx context.Context, repoID, artifactID string) (*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error {
	return nil
}
func (m *MockDatabase) ListLicenseScanResults(ctx context.Context, repoID string) ([]*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *MockDatabase) CreateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	return nil
}
func (m *MockDatabase) GetLicensePolicy(ctx context.Context, id string) (*models.LicensePolicy, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	return nil
}
func (m *MockDatabase) DeleteLicensePolicy(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error) {
	return nil, nil
}

// Secret detection methods
func (m *MockDatabase) CreateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error {
	return nil
}
func (m *MockDatabase) GetSecretScanResult(ctx context.Context, repoID, artifactID string) (*models.SecretScanResult, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error {
	return nil
}
func (m *MockDatabase) ListSecretScanResults(ctx context.Context, repoID string, flaggedOnly bool) ([]*models.SecretScanResult, error) {
	return nil, nil
}
func (m *MockDatabase) QuarantineArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	return nil
}

// Vulnerability scanning methods
func (m *MockDatabase) CreateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	return nil
}
func (m *MockDatabase) GetVulnScanResult(ctx context.Context, repoID, artifactID string) (*models.VulnScanResult, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	return nil
}
func (m *MockDatabase) ListVulnScanResults(ctx context.Context, repoID string, vulnerableOnly bool) ([]*models.VulnScanResult, error) {
	return nil, nil
}
func (m *MockDatabase) QuarantineVulnArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	return nil
}

// Storage quota methods
func (m *MockDatabase) GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateStorageQuota(ctx context.Context, quota *models.StorageQuota) error {
	return nil
}
func (m *MockDatabase) IncrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	return nil
}
func (m *MockDatabase) DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.quotaReserved -= sizeBytes
	m.quotaArtifacts -= artifactCount
	m.quotaReleaseCalls++

	return nil
}

// ReserveStorageUsage models the atomic quota reservation: it succeeds unless
// setQuotaExceeded(true) was called.
func (m *MockDatabase) ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.quotaExceeded {
		return false, nil
	}

	m.quotaReserved += sizeBytes
	m.quotaArtifacts += artifactCount
	m.quotaReserveCalls++

	return true, nil
}

func (m *MockDatabase) setQuotaExceeded(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.quotaExceeded = v
}

// quotaState returns the net reserved bytes/artifacts and the call counters.
func (m *MockDatabase) quotaState() (int64, int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.quotaReserved, m.quotaArtifacts, m.quotaReserveCalls, m.quotaReleaseCalls
}

// Artifact properties methods
func (m *MockDatabase) SetArtifactProperty(ctx context.Context, repoID, artifactID, key, value string) error {
	return nil
}
func (m *MockDatabase) GetArtifactProperty(ctx context.Context, repoID, artifactID, key string) (string, error) {
	return "", nil
}
func (m *MockDatabase) DeleteArtifactProperty(ctx context.Context, repoID, artifactID, key string) error {
	return nil
}
func (m *MockDatabase) ListArtifactProperties(ctx context.Context, repoID, artifactID string) (map[string]string, error) {
	return nil, nil
}
func (m *MockDatabase) SearchByProperty(ctx context.Context, repoID, key, value string) ([]string, error) {
	return nil, nil
}

// Organization methods
func (m *MockDatabase) CreateProject(ctx context.Context, org *models.Project) error {
	return nil
}
func (m *MockDatabase) GetProject(ctx context.Context, id string) (*models.Project, error) {
	return nil, nil
}
func (m *MockDatabase) GetProjectByName(ctx context.Context, name string) (*models.Project, error) {
	return nil, nil
}
func (m *MockDatabase) UpdateProject(ctx context.Context, org *models.Project) error {
	return nil
}
func (m *MockDatabase) DeleteProject(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListProjects(ctx context.Context, userID string) ([]*models.Project, error) {
	return nil, nil
}
func (m *MockDatabase) AddProjectMember(ctx context.Context, orgID, userID, role string) error {
	return nil
}
func (m *MockDatabase) ListProjectMembers(ctx context.Context, orgID string) ([]*models.ProjectMember, error) {
	return nil, nil
}
func (m *MockDatabase) GetProjectMember(ctx context.Context, orgID, userID string) (*models.ProjectMember, error) {
	return nil, nil
}
func (m *MockDatabase) ChangeProjectMember(ctx context.Context, projectID, userID, newRole string, guard database.ProjectRoleGuard) error {
	return nil
}
func (m *MockDatabase) ChangeProjectGroup(ctx context.Context, projectID, groupID, newRole string, createdBy *string, guard database.ProjectRoleGuard) error {
	return nil
}
func (m *MockDatabase) ListProjectRepositories(ctx context.Context, projectID string, page, limit int) ([]*models.Repository, int, error) {
	return nil, 0, nil
}
func (m *MockDatabase) CountRepositoriesByProject(ctx context.Context, projectIDs []string) (map[string]int, error) {
	return nil, nil
}

// System settings methods
func (m *MockDatabase) GetSystemSetting(ctx context.Context, key string) (string, error) {
	return "", nil
}
func (m *MockDatabase) SetSystemSetting(ctx context.Context, key, value string) error { return nil }

// Conda methods
func (m *MockDatabase) CreateCondaPackage(ctx context.Context, pkg *models.CondaPackage) error {
	return nil
}
func (m *MockDatabase) GetCondaPackageFile(ctx context.Context, repoID, name, version, buildString, platform, extension string) (*models.CondaPackage, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteCondaPackage(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListCondaPackages(ctx context.Context, repoID, platform string) ([]*models.CondaPackage, error) {
	return nil, nil
}

// Conan methods
func (m *MockDatabase) CreateConanPackage(ctx context.Context, pkg *models.ConanPackage) error {
	return nil
}
func (m *MockDatabase) GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error) {
	return nil, nil
}
func (m *MockDatabase) ListConanRecipeFiles(ctx context.Context, repoID, name, version, userChannel string) ([]*models.ConanPackage, error) {
	return nil, nil
}
func (m *MockDatabase) ListConanRecipes(ctx context.Context, repoID string) ([]models.ConanRecipe, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteConanPackage(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListConanPackages(ctx context.Context, repoID string) ([]*models.ConanPackage, error) {
	return nil, nil
}

// Ivy methods
func (m *MockDatabase) CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error {
	return nil
}
func (m *MockDatabase) GetIvyArtifact(ctx context.Context, repoID, organisation, module, revision, artifactName, artifactExt string) (*models.IvyArtifact, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteIvyArtifact(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListIvyArtifacts(ctx context.Context, repoID string) ([]*models.IvyArtifact, error) {
	return nil, nil
}

// SBT methods
func (m *MockDatabase) CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error {
	return nil
}
func (m *MockDatabase) GetSBTArtifact(ctx context.Context, repoID, groupID, artifactID, version, filename string) (*models.SBTArtifact, error) {
	return nil, nil
}
func (m *MockDatabase) DeleteSBTArtifact(ctx context.Context, id string) error { return nil }
func (m *MockDatabase) ListSBTArtifacts(ctx context.Context, repoID string) ([]*models.SBTArtifact, error) {
	return nil, nil
}

// Storage backend methods
func (m *MockDatabase) CreateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.storageBackends[backend.Name] = backend

	return nil
}

func (m *MockDatabase) ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	backends := make([]*models.StorageBackend, 0, len(m.storageBackends))
	for _, b := range m.storageBackends {
		backends = append(backends, b)
	}

	return backends, nil
}

func (m *MockDatabase) GetStorageBackend(ctx context.Context, name string) (*models.StorageBackend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.storageBackends[name], nil
}

func (m *MockDatabase) UpdateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.storageBackends[backend.Name] = backend

	return nil
}

func (m *MockDatabase) DeleteStorageBackend(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Find by ID and delete
	for name, b := range m.storageBackends {
		if b.ID == id {
			delete(m.storageBackends, name)
			return nil
		}
	}

	return nil
}

func (m *MockDatabase) UpdateStorageBackendStatus(ctx context.Context, id, status string, message *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, b := range m.storageBackends {
		if b.ID == id {
			b.Status = status
			b.StatusMessage = message
			now := time.Now()
			b.LastCheckedAt = &now

			return nil
		}
	}

	return nil
}

type MockBlobStore struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	sha256Map map[string]string
}

func newMockBlobStore() *MockBlobStore {
	return &MockBlobStore{
		blobs:     make(map[string][]byte),
		sha256Map: make(map[string]string),
	}
}

func (m *MockBlobStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	id := fmt.Sprintf("blob-%d", time.Now().UnixNano())
	b, _ := io.ReadAll(data)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.blobs[id] = b
	if sha, ok := metadata["sha256"]; ok {
		m.sha256Map[sha] = id
	}

	return id, nil
}

func (m *MockBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.blobs[id]
	m.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("blob not found: %s", id)
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *MockBlobStore) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.blobs, id)

	return nil
}

func (m *MockBlobStore) Exists(ctx context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.blobs[id]

	return ok, nil
}

func (m *MockBlobStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	return nil, nil
}

func (m *MockBlobStore) List(_ context.Context, _ string, _ int) ([]string, string, error) {
	return nil, "", nil
}

func (m *MockBlobStore) GetSize(ctx context.Context, id string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, ok := m.blobs[id]
	if ok {
		return int64(len(data)), nil
	}

	return 0, nil
}

func (m *MockBlobStore) ExistsBySHA256(_ context.Context, sha256 string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id, ok := m.sha256Map[sha256]; ok {
		return id, true, nil
	}

	return "", false, nil
}

func (m *MockBlobStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", storage.ErrPresignNotSupported
}

var _ storage.BlobStore = (*MockBlobStore)(nil)

func TestGetRPMRepo_Found(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	rpmRepo := &models.RPMRepo{
		ID:            "rpm-1",
		RepositoryID:  "repo-1",
		BaseArch:      "x86_64",
		MetadataStale: false,
	}
	mockDB.rpmRepos["repo-1"] = rpmRepo

	result, err := mockDB.GetRPMRepo(ctx, "repo-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.ID != "rpm-1" {
		t.Errorf("expected ID 'rpm-1', got %s", result.ID)
	}

	if result.BaseArch != "x86_64" {
		t.Errorf("expected BaseArch 'x86_64', got %s", result.BaseArch)
	}
}

func TestGetRPMRepo_NotFound(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	result, err := mockDB.GetRPMRepo(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != nil {
		t.Fatal("expected nil result")
	}
}

func TestCreateRPMRepo(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	rpmRepo := &models.RPMRepo{
		RepositoryID:  "repo-1",
		BaseArch:      "x86_64",
		MetadataStale: false,
	}

	err := mockDB.CreateRPMRepo(ctx, rpmRepo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rpmRepo.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	retrieved, err := mockDB.GetRPMRepo(ctx, "repo-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if retrieved == nil {
		t.Fatal("expected non-nil result")
	}

	if retrieved.BaseArch != "x86_64" {
		t.Errorf("expected BaseArch 'x86_64', got %s", retrieved.BaseArch)
	}
}

func TestMarkRepoStale(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	rpmRepo := &models.RPMRepo{
		ID:            "rpm-1",
		RepositoryID:  "repo-1",
		BaseArch:      "x86_64",
		MetadataStale: false,
	}
	mockDB.rpmRepos["repo-1"] = rpmRepo

	err := mockDB.MarkRepoStale(ctx, "repo-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	retrieved, err := mockDB.GetRPMRepo(ctx, "repo-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !retrieved.MetadataStale {
		t.Fatal("expected MetadataStale to be true")
	}
}

func TestCreateComponent(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	component := &models.Component{
		RepositoryID: "repo-1",
		ArtifactID:   "artifact-1",
		Name:         "test-package",
		Version:      "1.0.0",
		Release:      "1",
		Arch:         "x86_64",
		Filename:     "test-package-1.0.0-1.x86_64.rpm",
	}

	err := mockDB.CreateComponent(ctx, component)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if component.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	if _, ok := mockDB.components[component.ID]; !ok {
		t.Fatal("expected component to be stored")
	}
}

func TestGetComponentByFilename_Found(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	component := &models.Component{
		ID:           "comp-1",
		RepositoryID: "repo-1",
		ArtifactID:   "artifact-1",
		Name:         "test-package",
		Version:      "1.0.0",
		Release:      "1",
		Arch:         "x86_64",
		Filename:     "test-package-1.0.0-1.x86_64.rpm",
	}
	mockDB.components["comp-1"] = component

	result, err := mockDB.GetComponentByFilename(ctx, "repo-1", "test-package-1.0.0-1.x86_64.rpm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.ID != "comp-1" {
		t.Errorf("expected ID 'comp-1', got %s", result.ID)
	}
}

func TestGetComponentByFilename_NotFound(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	result, err := mockDB.GetComponentByFilename(ctx, "repo-1", "nonexistent.rpm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != nil {
		t.Fatal("expected nil result")
	}
}

func TestGetComponentsByRepo(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	comp1 := &models.Component{
		ID:           "comp-1",
		RepositoryID: "repo-1",
		Filename:     "pkg1.rpm",
	}
	comp2 := &models.Component{
		ID:           "comp-2",
		RepositoryID: "repo-1",
		Filename:     "pkg2.rpm",
	}
	comp3 := &models.Component{
		ID:           "comp-3",
		RepositoryID: "repo-2",
		Filename:     "pkg3.rpm",
	}

	mockDB.components["comp-1"] = comp1
	mockDB.components["comp-2"] = comp2
	mockDB.components["comp-3"] = comp3

	result, err := mockDB.GetComponentsByRepo(ctx, "repo-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 components, got %d", len(result))
	}
}

func TestDeleteComponent(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	component := &models.Component{
		ID:           "comp-1",
		RepositoryID: "repo-1",
		Filename:     "test.rpm",
	}
	mockDB.components["comp-1"] = component

	err := mockDB.DeleteComponent(ctx, "comp-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := mockDB.components["comp-1"]; ok {
		t.Fatal("expected component to be deleted")
	}
}

func TestGetRepoMetadataByPath_Found(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	metadata := &models.RepoMetadata{
		ID:           "meta-1",
		RepositoryID: "repo-1",
		Path:         "repodata/repomd.xml",
		BlobSHA256:   "abc123",
		BlobSize:     1024,
	}

	mockDB.repoMetadata["repo-1"] = make(map[string]*models.RepoMetadata)
	mockDB.repoMetadata["repo-1"]["repodata/repomd.xml"] = metadata

	result, err := mockDB.GetRepoMetadataByPath(ctx, "repo-1", "repodata/repomd.xml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.BlobSHA256 != "abc123" {
		t.Errorf("expected BlobSHA256 'abc123', got %s", result.BlobSHA256)
	}
}

func TestGetRepoMetadataByPath_NotFound(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	result, err := mockDB.GetRepoMetadataByPath(ctx, "repo-1", "nonexistent/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != nil {
		t.Fatal("expected nil result")
	}
}

func TestGetRepoMetadataByRepo(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	meta1 := &models.RepoMetadata{
		ID:           "meta-1",
		RepositoryID: "repo-1",
		Path:         "repodata/repomd.xml",
		BlobSHA256:   "abc123",
	}
	meta2 := &models.RepoMetadata{
		ID:           "meta-2",
		RepositoryID: "repo-1",
		Path:         "repodata/primary.xml.gz",
		BlobSHA256:   "def456",
	}

	mockDB.repoMetadata["repo-1"] = make(map[string]*models.RepoMetadata)
	mockDB.repoMetadata["repo-1"]["repodata/repomd.xml"] = meta1
	mockDB.repoMetadata["repo-1"]["repodata/primary.xml.gz"] = meta2

	result, err := mockDB.GetRepoMetadataByRepo(ctx, "repo-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 metadata entries, got %d", len(result))
	}
}

func TestUpsertRepoMetadata(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	metadata := &models.RepoMetadata{
		RepositoryID: "repo-1",
		Path:         "repodata/repomd.xml",
		BlobSHA256:   "abc123",
		BlobSize:     1024,
	}

	err := mockDB.UpsertRepoMetadata(ctx, metadata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if metadata.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	retrieved, err := mockDB.GetRepoMetadataByPath(ctx, "repo-1", "repodata/repomd.xml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if retrieved == nil {
		t.Fatal("expected non-nil result")
	}

	if retrieved.BlobSHA256 != "abc123" {
		t.Errorf("expected BlobSHA256 'abc123', got %s", retrieved.BlobSHA256)
	}
}

func TestGetRepositoryByName_Found(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	repo := &models.Repository{
		ID:     "repo-1",
		Name:   "test-repo",
		Format: "rpm",
	}
	mockDB.repositories["repo-1"] = repo

	result, err := mockDB.GetRepositoryByName(ctx, "test-repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.ID != "repo-1" {
		t.Errorf("expected ID 'repo-1', got %s", result.ID)
	}
}

func TestGetRepositoryByName_NotFound(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	result, err := mockDB.GetRepositoryByName(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != nil {
		t.Fatal("expected nil result")
	}
}

func TestHTTPHandler_ServeRepomdXML(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()
	mockBlobStore := newMockBlobStore()

	repo := &models.Repository{
		ID:     "repo-1",
		Name:   "test-repo",
		Format: "rpm",
	}
	mockDB.repositories["repo-1"] = repo

	xmlContent := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary">
    <location href="repodata/primary.xml.gz"/>
  </data>
</repomd>`)

	blobID, err := mockBlobStore.Put(ctx, bytes.NewReader(xmlContent), nil)
	if err != nil {
		t.Fatalf("failed to store blob: %v", err)
	}

	metadata := &models.RepoMetadata{
		ID:           "meta-1",
		RepositoryID: "repo-1",
		Path:         "repodata/repomd.xml",
		BlobSHA256:   blobID,
		BlobSize:     int64(len(xmlContent)),
	}
	mockDB.repoMetadata["repo-1"] = make(map[string]*models.RepoMetadata)
	mockDB.repoMetadata["repo-1"]["repodata/repomd.xml"] = metadata

	log, _ := logger.New("info", "json", "stdout")
	_ = rpm.NewHTTPHandler(log, mockDB, mockBlobStore)

	router := chi.NewRouter()
	router.HandleFunc("/repos/{name}/repodata/repomd.xml", func(w http.ResponseWriter, r *http.Request) {
		repoID, err := mockDB.GetRepositoryByName(r.Context(), chi.URLParam(r, "name"))
		if err != nil || repoID == nil {
			http.NotFound(w, r)
			return
		}

		meta, err := mockDB.GetRepoMetadataByPath(r.Context(), repoID.ID, "repodata/repomd.xml")
		if err != nil || meta == nil {
			http.NotFound(w, r)
			return
		}

		reader, err := mockBlobStore.Get(r.Context(), meta.BlobSHA256)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		defer func() { _ = reader.Close() }()

		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.Copy(w, reader)
	})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/test-repo/repodata/repomd.xml", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("repomd")) {
		t.Errorf("expected response to contain 'repomd', got %s", w.Body.String())
	}
}

func TestHTTPHandler_ServeRepomdXML_NotFound(t *testing.T) {
	mockDB := newMockDatabase()
	mockBlobStore := newMockBlobStore()

	log, _ := logger.New("info", "json", "stdout")
	_ = rpm.NewHTTPHandler(log, mockDB, mockBlobStore)

	router := chi.NewRouter()
	router.HandleFunc("/repos/{name}/repodata/repomd.xml", func(w http.ResponseWriter, r *http.Request) {
		repoID, err := mockDB.GetRepositoryByName(r.Context(), chi.URLParam(r, "name"))
		if err != nil || repoID == nil {
			http.NotFound(w, r)
			return
		}

		meta, err := mockDB.GetRepoMetadataByPath(r.Context(), repoID.ID, "repodata/repomd.xml")
		if err != nil || meta == nil {
			http.NotFound(w, r)
			return
		}

		reader, err := mockBlobStore.Get(r.Context(), meta.BlobSHA256)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		defer func() { _ = reader.Close() }()

		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.Copy(w, reader)
	})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/nonexistent-repo/repodata/repomd.xml", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func (m *MockDatabase) UpsertAIAnalysis(_ context.Context, _ *models.AIAnalysis) error {
	return nil
}

func (m *MockDatabase) GetAIAnalysis(_ context.Context, _, _ string) (*models.AIAnalysis, error) {
	return nil, nil
}

func (m *MockDatabase) ListAIAnalyses(_ context.Context, _ string, _ string, _ bool, _, _ int) ([]*models.AIAnalysis, int, error) {
	return nil, 0, nil
}

func (m *MockDatabase) DeleteAIAnalysis(_ context.Context, _, _ string) error {
	return nil
}

func (m *MockDatabase) ListPendingAIAnalyses(_ context.Context, _ int) ([]*models.AIAnalysis, error) {
	return nil, nil
}
