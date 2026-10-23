// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type provMockDB struct {
	database.BaseMockDB

	usersByExtID   map[string]*models.User
	usersByEmail   map[string]*models.User
	usersByID      map[string]*models.User
	groupsByName   map[string]*models.Group
	groupsByID     map[string]*models.Group
	createUserFn   func(*models.User) (*models.User, error)
	updateUserFn   func(*models.User) error
	fullRowUpdates int
	updateSSOFn    func(userID, extID, slug string) error
	syncGroupsFn   func(userID string, groupIDs []string) error
	syncGroupsErr  error
	createdUsers   []*models.User
	updatedUsers   []*models.User
	syncGroupsArgs []struct {
		userID   string
		groupIDs []string
	}
	updateSSOCalled bool

	targetsByName      map[string]*models.PermissionTarget
	assignments        []string
	deletedAssignments []string
}

func newProvMockDB() *provMockDB {
	return &provMockDB{
		usersByExtID: make(map[string]*models.User),
		usersByEmail: make(map[string]*models.User),
		usersByID:    make(map[string]*models.User),
		groupsByName: make(map[string]*models.Group),
		groupsByID:   make(map[string]*models.Group),
	}
}

func (m *provMockDB) addUser(u *models.User) {
	if u.ExternalID != nil && u.ProviderSlug != nil {
		key := *u.ExternalID + "|" + *u.ProviderSlug
		m.usersByExtID[key] = u
	}

	if u.Email != nil {
		m.usersByEmail[*u.Email] = u
	}

	m.usersByID[u.ID] = u
}

func (m *provMockDB) addGroup(g *models.Group) {
	m.groupsByName[g.Name] = g
	m.groupsByID[g.ID] = g
}

func (m *provMockDB) GetUserByExternalID(_ context.Context, externalID, providerSlug string) (*models.User, error) {
	return m.usersByExtID[externalID+"|"+providerSlug], nil
}

func (m *provMockDB) GetUserByEmail(_ context.Context, email string) (*models.User, error) {
	return m.usersByEmail[email], nil
}

func (m *provMockDB) CreateUser(_ context.Context, u *models.User) (*models.User, error) {
	if m.createUserFn != nil {
		return m.createUserFn(u)
	}

	if u.ID == "" {
		u.ID = fmt.Sprintf("user-%d", len(m.usersByID)+1)
	}

	m.usersByID[u.ID] = u
	m.createdUsers = append(m.createdUsers, u)

	return u, nil
}

func (m *provMockDB) UpdateUser(_ context.Context, u *models.User) error {
	m.fullRowUpdates++

	if m.updateUserFn != nil {
		return m.updateUserFn(u)
	}

	m.usersByID[u.ID] = u
	m.updatedUsers = append(m.updatedUsers, u)

	return nil
}

func (m *provMockDB) UpdateUserSSOProfile(_ context.Context, id string, email, fullName *string, at time.Time) error {
	u := m.usersByID[id]
	if u == nil {
		return nil
	}

	updated := *u
	if email != nil {
		updated.Email = email
	}

	if fullName != nil {
		updated.FullName = fullName
	}

	updated.LastLoginAt = &at

	if m.updateUserFn != nil {
		return m.updateUserFn(&updated)
	}

	m.usersByID[id] = &updated
	m.updatedUsers = append(m.updatedUsers, &updated)

	return nil
}

func (m *provMockDB) UpdateUserSSO(_ context.Context, userID, externalID, providerSlug string) error {
	m.updateSSOCalled = true
	if m.updateSSOFn != nil {
		return m.updateSSOFn(userID, externalID, providerSlug)
	}

	if u, ok := m.usersByID[userID]; ok {
		u.ExternalID = &externalID
		u.ProviderSlug = &providerSlug
	}

	return nil
}

func (m *provMockDB) GetGroupByName(_ context.Context, name string) (*models.Group, error) {
	return m.groupsByName[name], nil
}

func (m *provMockDB) GetOrCreateGroup(_ context.Context, name string) (*models.Group, error) {
	if g, ok := m.groupsByName[name]; ok {
		return g, nil
	}
	// Auto-create the group if it doesn't exist
	g := &models.Group{
		ID:   fmt.Sprintf("grp-%d", len(m.groupsByID)+1),
		Name: name,
	}
	m.addGroup(g)

	return g, nil
}

func (m *provMockDB) AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error {
	return nil
}

func (m *provMockDB) CreateBundlePromotion(ctx context.Context, promotion *models.BundlePromotion) error {
	return nil
}

func (m *provMockDB) CreatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *provMockDB) CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *provMockDB) RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error {
	return nil
}

func (m *provMockDB) ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *provMockDB) GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *provMockDB) ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error) {
	return nil, nil
}

func (m *provMockDB) GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *provMockDB) GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *provMockDB) UpdatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *provMockDB) DeletePromotionPipeline(ctx context.Context, id string) error {
	return nil
}

func (m *provMockDB) ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *provMockDB) GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *provMockDB) GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *provMockDB) UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *provMockDB) DeleteReleaseBundle(ctx context.Context, id string) error {
	return nil
}

func (m *provMockDB) ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error) {
	return nil, 0, nil
}

func (m *provMockDB) UpdateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error {
	return nil
}

func (m *provMockDB) SyncSSOGroups(_ context.Context, userID string, groupIDs []string) error {
	if m.syncGroupsErr != nil {
		return m.syncGroupsErr
	}

	if m.syncGroupsFn != nil {
		return m.syncGroupsFn(userID, groupIDs)
	}

	m.syncGroupsArgs = append(m.syncGroupsArgs, struct {
		userID   string
		groupIDs []string
	}{userID, groupIDs})

	return nil
}

func (m *provMockDB) GetDB() *sql.DB                                          { return nil }
func (m *provMockDB) Close() error                                            { return nil }
func (m *provMockDB) Ping(_ context.Context) error                            { return nil }
func (m *provMockDB) BeginTx(_ context.Context) (database.Transaction, error) { return nil, nil }
func (m *provMockDB) Migrate(_ context.Context) error                         { return nil }
func (m *provMockDB) GetRepositoryByName(_ context.Context, _ string) (*models.Repository, error) {
	return nil, nil
}
func (m *provMockDB) CreateRepository(_ context.Context, _ *models.Repository) error { return nil }
func (m *provMockDB) ListRepositories(_ context.Context, _, _ int, _ string) ([]*models.Repository, error) {
	return nil, nil
}
func (m *provMockDB) CountRepositories(_ context.Context, _ string) (int, error)     { return 0, nil }
func (m *provMockDB) UpdateRepository(_ context.Context, _ *models.Repository) error { return nil }
func (m *provMockDB) DeleteRepository(_ context.Context, _ string) error             { return nil }
func (m *provMockDB) ListComponentsByRepo(_ context.Context, _, _ string, _, _ int) ([]*models.Component, int, error) {
	return nil, 0, nil
}
func (m *provMockDB) GetRPMRepo(_ context.Context, _ string) (*models.RPMRepo, error) {
	return nil, nil
}
func (m *provMockDB) CreateRPMRepo(_ context.Context, _ *models.RPMRepo) error     { return nil }
func (m *provMockDB) MarkRepoStale(_ context.Context, _ string) error              { return nil }
func (m *provMockDB) CreateComponent(_ context.Context, _ *models.Component) error { return nil }
func (m *provMockDB) GetComponentByID(_ context.Context, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *provMockDB) GetComponentByFilename(_ context.Context, _, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *provMockDB) GetComponentsByRepo(_ context.Context, _ string) ([]models.Component, error) {
	return nil, nil
}
func (m *provMockDB) DeleteComponent(_ context.Context, _ string) error { return nil }
func (m *provMockDB) GetRepoMetadataByPath(_ context.Context, _, _ string) (*models.RepoMetadata, error) {
	return nil, nil
}
func (m *provMockDB) GetRepoMetadataByRepo(_ context.Context, _ string) ([]models.RepoMetadata, error) {
	return nil, nil
}
func (m *provMockDB) UpsertRepoMetadata(_ context.Context, _ *models.RepoMetadata) error {
	return nil
}
func (m *provMockDB) CreateDebianDistribution(_ context.Context, _ *models.DebianDistribution) error {
	return nil
}
func (m *provMockDB) GetDebianDistribution(_ context.Context, _, _ string) (*models.DebianDistribution, error) {
	return nil, nil
}
func (m *provMockDB) GetDebianDistributionsByRepo(_ context.Context, _ string) ([]models.DebianDistribution, error) {
	return nil, nil
}
func (m *provMockDB) CreateDebianComponent(_ context.Context, _ *models.DebianComponent) error {
	return nil
}
func (m *provMockDB) GetDebianComponentsByDist(_ context.Context, _ string) ([]models.DebianComponent, error) {
	return nil, nil
}
func (m *provMockDB) CreateDebianArchitecture(_ context.Context, _ *models.DebianArchitecture) error {
	return nil
}
func (m *provMockDB) GetDebianArchitecturesByDist(_ context.Context, _ string) ([]models.DebianArchitecture, error) {
	return nil, nil
}
func (m *provMockDB) GetDebianPackageByFilename(_ context.Context, _, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *provMockDB) GetDebianPackagesByDistComp(_ context.Context, _, _, _, _ string) ([]models.Component, error) {
	return nil, nil
}
func (m *provMockDB) CreateMavenArtifact(_ context.Context, _ *models.MavenArtifact) error {
	return nil
}
func (m *provMockDB) GetMavenArtifactByPath(_ context.Context, _, _ string) (*models.MavenArtifact, error) {
	return nil, nil
}
func (m *provMockDB) GetMavenArtifactsByGAV(_ context.Context, _, _, _, _ string) ([]*models.MavenArtifact, error) {
	return nil, nil
}
func (m *provMockDB) GetMavenArtifactsByGA(_ context.Context, _, _, _ string) ([]*models.MavenArtifact, error) {
	return nil, nil
}
func (m *provMockDB) GetLatestSnapshot(_ context.Context, _, _, _, _ string) (*models.MavenArtifact, error) {
	return nil, nil
}
func (m *provMockDB) DeleteMavenArtifact(_ context.Context, _ string) error { return nil }
func (m *provMockDB) CreateNPMPackage(_ context.Context, _ *models.NPMPackage) (*models.NPMPackage, error) {
	return nil, nil
}
func (m *provMockDB) GetNPMPackageByName(_ context.Context, _, _ string) (*models.NPMPackage, error) {
	return nil, nil
}
func (m *provMockDB) DeleteNPMPackage(_ context.Context, _ string) error   { return nil }
func (m *provMockDB) MarkPackumentStale(_ context.Context, _ string) error { return nil }
func (m *provMockDB) UpdateNPMPackageLatestVersion(_ context.Context, _, _ string) error {
	return nil
}
func (m *provMockDB) CreateNPMVersion(_ context.Context, _ *models.NPMVersion) (*models.NPMVersion, error) {
	return nil, nil
}
func (m *provMockDB) GetNPMVersion(_ context.Context, _, _ string) (*models.NPMVersion, error) {
	return nil, nil
}
func (m *provMockDB) GetNPMVersionsByPackage(_ context.Context, _ string) ([]*models.NPMVersion, error) {
	return nil, nil
}
func (m *provMockDB) DeleteNPMVersion(_ context.Context, _, _ string) error { return nil }
func (m *provMockDB) UpsertNPMDistTag(_ context.Context, _ *models.NPMDistTag) (*models.NPMDistTag, error) {
	return nil, nil
}
func (m *provMockDB) GetNPMDistTags(_ context.Context, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *provMockDB) DeleteNPMDistTag(_ context.Context, _, _ string) error { return nil }
func (m *provMockDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return m.usersByID[id], nil
}
func (m *provMockDB) GetUserByUsername(_ context.Context, name string) (*models.User, error) {
	for _, u := range m.usersByID {
		if u.Username == name {
			return u, nil
		}
	}

	return nil, nil
}
func (m *provMockDB) ListUsers(_ context.Context, _, _ int) ([]*models.User, int, error) {
	return nil, 0, nil
}
func (m *provMockDB) CountUsers(_ context.Context) (int, error) { return len(m.usersByID), nil }
func (m *provMockDB) CreateGroup(_ context.Context, _ *models.Group) (*models.Group, error) {
	return nil, nil
}
func (m *provMockDB) GetGroupByID(_ context.Context, id string) (*models.Group, error) {
	return m.groupsByID[id], nil
}
func (m *provMockDB) UpdateGroup(_ context.Context, _ *models.Group) error     { return nil }
func (m *provMockDB) DeleteGroup(_ context.Context, _ string) error            { return nil }
func (m *provMockDB) ListGroups(_ context.Context) ([]*models.Group, error)    { return nil, nil }
func (m *provMockDB) AddUserToGroup(_ context.Context, _, _ string) error      { return nil }
func (m *provMockDB) RemoveUserFromGroup(_ context.Context, _, _ string) error { return nil }
func (m *provMockDB) ListGroupsByUser(_ context.Context, _ string) ([]*models.Group, error) {
	return nil, nil
}
func (m *provMockDB) ListUsersByGroup(_ context.Context, _ string) ([]*models.User, error) {
	return nil, nil
}
func (m *provMockDB) CreateSession(_ context.Context, _ *models.Session) (*models.Session, error) {
	return nil, nil
}
func (m *provMockDB) GetSessionByTokenHash(_ context.Context, _ string) (*models.Session, error) {
	return nil, nil
}
func (m *provMockDB) DeleteSession(_ context.Context, _ string) error { return nil }
func (m *provMockDB) UpdateSessionLastSeen(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (m *provMockDB) CreateToken(_ context.Context, _ *models.Token) (*models.Token, error) {
	return nil, nil
}
func (m *provMockDB) GetTokenByHash(_ context.Context, _ string) (*models.Token, error) {
	return nil, nil
}
func (m *provMockDB) GetTokenByID(_ context.Context, _ string) (*models.Token, error) {
	return nil, nil
}
func (m *provMockDB) RevokeToken(_ context.Context, _ string) error         { return nil }
func (m *provMockDB) UpdateTokenLastUsed(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListTokensByUser(_ context.Context, _ string) ([]*models.Token, error) {
	return nil, nil
}
func (m *provMockDB) CreatePermissionTarget(_ context.Context, _ *models.PermissionTarget) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *provMockDB) GetPermissionTargetByID(_ context.Context, _ string) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *provMockDB) GetPermissionTargetByName(_ context.Context, name string) (*models.PermissionTarget, error) {
	return m.targetsByName[name], nil
}
func (m *provMockDB) UpdatePermissionTarget(_ context.Context, _ *models.PermissionTarget) error {
	return nil
}
func (m *provMockDB) DeletePermissionTarget(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListPermissionTargets(_ context.Context) ([]*models.PermissionTarget, error) {
	return nil, nil
}
func (m *provMockDB) CreatePermissionAssignment(_ context.Context, _ *models.PermissionAssignment) error {
	return nil
}
func (m *provMockDB) DeletePermissionAssignment(_ context.Context, targetID, principalType, principalID string) error {
	m.deletedAssignments = append(m.deletedAssignments, targetID+"|"+principalType+"|"+principalID)
	return nil
}
func (m *provMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return nil, nil
}
func (m *provMockDB) GetOrCreatePermissionAssignment(_ context.Context, targetID, principalType, principalID string) error {
	m.assignments = append(m.assignments, targetID+"|"+principalType+"|"+principalID)
	return nil
}
func (m *provMockDB) SyncSSOPermissionsForGroup(_ context.Context, _ string, _ []string) error {
	return nil
}
func (m *provMockDB) CreateSSOProvider(_ context.Context, _ *models.SSOProvider) error { return nil }
func (m *provMockDB) GetSSOProviderBySlug(_ context.Context, _ string) (*models.SSOProvider, error) {
	return nil, nil
}
func (m *provMockDB) GetSSOProviderByID(_ context.Context, _ string) (*models.SSOProvider, error) {
	return nil, nil
}
func (m *provMockDB) UpdateSSOProvider(_ context.Context, _ *models.SSOProvider) error { return nil }
func (m *provMockDB) DeleteSSOProvider(_ context.Context, _ string) error              { return nil }
func (m *provMockDB) ListEnabledSSOProviders(_ context.Context) ([]*models.SSOProvider, error) {
	return nil, nil
}
func (m *provMockDB) ListAllSSOProviders(_ context.Context) ([]*models.SSOProvider, error) {
	return nil, nil
}

func (m *provMockDB) ArtifactExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (m *provMockDB) ArtifactExistsBySHA256(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}

func (m *provMockDB) CreatePackageDelta(_ context.Context, _ *models.PackageDelta) error {
	return nil
}

func (m *provMockDB) GetPackageDelta(_ context.Context, _, _, _ string) (*models.PackageDelta, error) {
	return nil, nil
}

func (m *provMockDB) ListPackageDeltas(_ context.Context, _, _ string) ([]*models.PackageDelta, error) {
	return nil, nil
}

func (m *provMockDB) DeletePackageDelta(_ context.Context, _ string) error {
	return nil
}

func (m *provMockDB) InsertAuditLog(_ context.Context, _ *models.AuditLog) error {
	return nil
}

func (m *provMockDB) ListAuditLogs(_ context.Context, _ models.AuditListParams) (*models.AuditPage, error) {
	return &models.AuditPage{}, nil
}

func (m *provMockDB) ListAllTokens(_ context.Context, _, _ int) ([]*models.Token, int, error) {
	return nil, 0, nil
}

// Docker Registry methods
func (m *provMockDB) CreateDockerBlob(_ context.Context, _ *models.DockerBlob) error { return nil }
func (m *provMockDB) GetDockerBlob(_ context.Context, _, _ string) (*models.DockerBlob, error) {
	return nil, nil
}
func (m *provMockDB) DeleteDockerBlob(_ context.Context, _, _ string) error { return nil }
func (m *provMockDB) ListDockerBlobs(_ context.Context, _ string) ([]*models.DockerBlob, error) {
	return nil, nil
}

func (m *provMockDB) CreateDockerUpload(_ context.Context, _ *models.DockerUpload) error { return nil }
func (m *provMockDB) GetDockerUpload(_ context.Context, _, _ string) (*models.DockerUpload, error) {
	return nil, nil
}
func (m *provMockDB) UpdateDockerUploadOffset(_ context.Context, _, _ string, _, _ int64) (bool, error) {
	return true, nil
}
func (m *provMockDB) DeleteDockerUpload(_ context.Context, _, _ string) error { return nil }

func (m *provMockDB) CreateDockerManifest(_ context.Context, _ *models.DockerManifest) error {
	return nil
}
func (m *provMockDB) GetDockerManifest(_ context.Context, _, _, _ string) (*models.DockerManifest, error) {
	return nil, nil
}
func (m *provMockDB) DeleteDockerManifest(_ context.Context, _, _, _ string) error { return nil }

func (m *provMockDB) UpsertDockerTag(_ context.Context, _ *models.DockerTag) error { return nil }
func (m *provMockDB) GetDockerTag(_ context.Context, _, _, _ string) (*models.DockerTag, error) {
	return nil, nil
}
func (m *provMockDB) DeleteDockerTag(_ context.Context, _, _, _ string) error          { return nil }
func (m *provMockDB) DeleteDockerTagsByDigest(_ context.Context, _, _, _ string) error { return nil }
func (m *provMockDB) ListDockerTagsByRepo(_ context.Context, _ string, _ int) ([]*models.DockerTag, error) {
	return nil, nil
}
func (m *provMockDB) ListDockerTags(_ context.Context, _, _, _ string, _ int) ([]*models.DockerTag, error) {
	return nil, nil
}

// Proxy/Group repository methods
func (m *provMockDB) AddGroupMember(_ context.Context, _, _ string, _ int) error { return nil }
func (m *provMockDB) RemoveGroupMember(_ context.Context, _, _ string) error     { return nil }
func (m *provMockDB) ListGroupMembers(_ context.Context, _ string) ([]*models.RepositoryGroupMember, error) {
	return nil, nil
}
func (m *provMockDB) GetProxyCacheEntry(_ context.Context, _, _ string) (*models.ProxyCacheEntry, error) {
	return nil, nil
}
func (m *provMockDB) UpsertProxyCacheEntry(_ context.Context, _ *models.ProxyCacheEntry) error {
	return nil
}
func (m *provMockDB) DeleteProxyCacheEntry(_ context.Context, _, _ string) error { return nil }
func (m *provMockDB) DeleteExpiredProxyCacheEntries(_ context.Context) (int64, error) {
	return 0, nil
}

// Helm methods
func (m *provMockDB) CreateHelmChart(_ context.Context, _ *models.HelmChart) error { return nil }
func (m *provMockDB) GetHelmChart(_ context.Context, _, _, _ string) (*models.HelmChart, error) {
	return nil, nil
}
func (m *provMockDB) DeleteHelmChart(_ context.Context, _, _, _ string) error { return nil }
func (m *provMockDB) ListHelmCharts(_ context.Context, _ string) ([]*models.HelmChart, error) {
	return nil, nil
}
func (m *provMockDB) ListHelmChartVersions(_ context.Context, _, _ string) ([]*models.HelmChart, error) {
	return nil, nil
}

// PyPI methods
func (m *provMockDB) CreatePyPIPackage(_ context.Context, _ *models.PyPIPackage) error { return nil }
func (m *provMockDB) GetPyPIPackage(_ context.Context, _, _, _, _ string) (*models.PyPIPackage, error) {
	return nil, nil
}
func (m *provMockDB) DeletePyPIPackage(_ context.Context, _, _, _, _ string) error { return nil }
func (m *provMockDB) ListPyPIPackages(_ context.Context, _ string) ([]*models.PyPIPackage, error) {
	return nil, nil
}
func (m *provMockDB) ListPyPIPackageVersions(_ context.Context, _, _ string) ([]*models.PyPIPackage, error) {
	return nil, nil
}

// NuGet methods
func (m *provMockDB) CreateNuGetPackage(_ context.Context, _ *models.NuGetPackage) error { return nil }
func (m *provMockDB) GetNuGetPackage(_ context.Context, _, _, _ string) (*models.NuGetPackage, error) {
	return nil, nil
}
func (m *provMockDB) DeleteNuGetPackage(_ context.Context, _, _, _ string) error { return nil }
func (m *provMockDB) ListNuGetPackages(_ context.Context, _ string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *provMockDB) ListNuGetPackageVersions(_ context.Context, _, _ string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *provMockDB) SearchNuGetPackages(_ context.Context, _, _ string, _, _ int) ([]*models.NuGetPackage, int, error) {
	return nil, 0, nil
}
func (m *provMockDB) IncrementNuGetDownloadCount(_ context.Context, _, _, _ string) error { return nil }

// Go Module Proxy methods
func (m *provMockDB) CreateGoModule(_ context.Context, _ *models.GoModule) error { return nil }
func (m *provMockDB) GetGoModule(_ context.Context, _, _, _ string) (*models.GoModule, error) {
	return nil, nil
}
func (m *provMockDB) DeleteGoModule(_ context.Context, _, _, _ string) error { return nil }
func (m *provMockDB) ListGoModuleVersions(_ context.Context, _, _ string) ([]*models.GoModule, error) {
	return nil, nil
}

// Cleanup policy methods
func (m *provMockDB) CreateCleanupPolicy(_ context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (m *provMockDB) GetCleanupPolicy(_ context.Context, _ string) (*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *provMockDB) UpdateCleanupPolicy(_ context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (m *provMockDB) DeleteCleanupPolicy(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListCleanupPolicies(_ context.Context, _ string) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *provMockDB) ListEnabledCleanupPolicies(_ context.Context) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *provMockDB) UpdateCleanupPolicyRunResult(_ context.Context, _ string, _ int, _ error) error {
	return nil
}

// Webhook methods
func (m *provMockDB) CreateWebhook(_ context.Context, _ *models.Webhook) error { return nil }
func (m *provMockDB) GetWebhook(_ context.Context, _ string) (*models.Webhook, error) {
	return nil, nil
}
func (m *provMockDB) UpdateWebhook(_ context.Context, _ *models.Webhook) error { return nil }
func (m *provMockDB) DeleteWebhook(_ context.Context, _ string) error          { return nil }
func (m *provMockDB) ListWebhooks(_ context.Context, _ string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *provMockDB) ListWebhooksForEvent(_ context.Context, _, _ string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *provMockDB) CreateWebhookDelivery(_ context.Context, _ *models.WebhookDelivery) error {
	return nil
}
func (m *provMockDB) UpdateWebhookDelivery(_ context.Context, _ *models.WebhookDelivery) error {
	return nil
}
func (m *provMockDB) ListWebhookDeliveries(_ context.Context, _ string, _ int) ([]*models.WebhookDelivery, error) {
	return nil, nil
}

// Backup methods
func (m *provMockDB) CreateBackupJob(_ context.Context, _ *models.BackupJob) error { return nil }
func (m *provMockDB) GetBackupJob(_ context.Context, _ string) (*models.BackupJob, error) {
	return nil, nil
}
func (m *provMockDB) UpdateBackupJob(_ context.Context, _ *models.BackupJob) error { return nil }
func (m *provMockDB) ListBackupJobs(_ context.Context, _ int) ([]*models.BackupJob, error) {
	return nil, nil
}

// Cargo methods
func (m *provMockDB) CreateCargoCrate(_ context.Context, _ *models.CargoCrate) error { return nil }
func (m *provMockDB) GetCargoCrate(_ context.Context, _, _, _ string) (*models.CargoCrate, error) {
	return nil, nil
}
func (m *provMockDB) DeleteCargoCrate(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListCargoCrateVersions(_ context.Context, _, _ string) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *provMockDB) SearchCargoCrates(_ context.Context, _, _ string, _ int) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *provMockDB) YankCargoCrate(_ context.Context, _, _, _ string, _ bool) error      { return nil }
func (m *provMockDB) IncrementCargoDownloadCount(_ context.Context, _, _, _ string) error { return nil }

// RubyGems methods
func (m *provMockDB) CreateRubyGem(_ context.Context, _ *models.RubyGem) error { return nil }
func (m *provMockDB) GetRubyGem(_ context.Context, _, _, _, _ string) (*models.RubyGem, error) {
	return nil, nil
}
func (m *provMockDB) DeleteRubyGem(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListRubyGems(_ context.Context, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *provMockDB) ListRubyGemVersions(_ context.Context, _, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *provMockDB) SearchRubyGems(_ context.Context, _, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *provMockDB) YankRubyGem(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

// Raw artifact methods
func (m *provMockDB) UpsertRawArtifact(_ context.Context, _ *models.RawArtifact) error { return nil }
func (m *provMockDB) GetRawArtifact(_ context.Context, _, _ string) (*models.RawArtifact, error) {
	return nil, nil
}
func (m *provMockDB) DeleteRawArtifact(_ context.Context, _, _ string) error { return nil }
func (m *provMockDB) ListRawArtifacts(_ context.Context, _, _ string) ([]*models.RawArtifact, error) {
	return nil, nil
}

// SBOM methods
func (m *provMockDB) CreateSBOMReport(_ context.Context, _ *models.SBOMReport) error { return nil }
func (m *provMockDB) GetSBOMReport(_ context.Context, _, _, _, _ string) (*models.SBOMReport, error) {
	return nil, nil
}
func (m *provMockDB) ListSBOMReports(_ context.Context, _ string) ([]*models.SBOMReport, error) {
	return nil, nil
}
func (m *provMockDB) DeleteSBOMReport(_ context.Context, _ string) error { return nil }

// License compliance methods
func (m *provMockDB) CreateLicenseScanResult(_ context.Context, _ *models.LicenseScanResult) error {
	return nil
}
func (m *provMockDB) GetLicenseScanResult(_ context.Context, _, _ string) (*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *provMockDB) UpdateLicenseScanResult(_ context.Context, _ *models.LicenseScanResult) error {
	return nil
}
func (m *provMockDB) ListLicenseScanResults(_ context.Context, _ string) ([]*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *provMockDB) CreateLicensePolicy(_ context.Context, _ *models.LicensePolicy) error {
	return nil
}
func (m *provMockDB) GetLicensePolicy(_ context.Context, _ string) (*models.LicensePolicy, error) {
	return nil, nil
}
func (m *provMockDB) UpdateLicensePolicy(_ context.Context, _ *models.LicensePolicy) error {
	return nil
}
func (m *provMockDB) DeleteLicensePolicy(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListLicensePolicies(_ context.Context, _ string) ([]*models.LicensePolicy, error) {
	return nil, nil
}

// Secret detection methods
func (m *provMockDB) CreateSecretScanResult(_ context.Context, _ *models.SecretScanResult) error {
	return nil
}
func (m *provMockDB) GetSecretScanResult(_ context.Context, _, _ string) (*models.SecretScanResult, error) {
	return nil, nil
}
func (m *provMockDB) UpdateSecretScanResult(_ context.Context, _ *models.SecretScanResult) error {
	return nil
}
func (m *provMockDB) ListSecretScanResults(_ context.Context, _ string, _ bool) ([]*models.SecretScanResult, error) {
	return nil, nil
}
func (m *provMockDB) QuarantineArtifact(_ context.Context, _, _ string, _ bool) error { return nil }

// Vulnerability scanning methods
func (m *provMockDB) CreateVulnScanResult(_ context.Context, _ *models.VulnScanResult) error {
	return nil
}
func (m *provMockDB) GetVulnScanResult(_ context.Context, _, _ string) (*models.VulnScanResult, error) {
	return nil, nil
}
func (m *provMockDB) UpdateVulnScanResult(_ context.Context, _ *models.VulnScanResult) error {
	return nil
}
func (m *provMockDB) ListVulnScanResults(_ context.Context, _ string, _ bool) ([]*models.VulnScanResult, error) {
	return nil, nil
}
func (m *provMockDB) QuarantineVulnArtifact(_ context.Context, _, _ string, _ bool) error { return nil }

// Storage quota methods
func (m *provMockDB) GetOrCreateStorageQuota(_ context.Context, _ string) (*models.StorageQuota, error) {
	return nil, nil
}
func (m *provMockDB) UpdateStorageQuota(_ context.Context, _ *models.StorageQuota) error { return nil }
func (m *provMockDB) IncrementStorageUsage(_ context.Context, _ string, _ int64, _ int) error {
	return nil
}
func (m *provMockDB) DecrementStorageUsage(_ context.Context, _ string, _ int64, _ int) error {
	return nil
}

// Artifact properties methods
func (m *provMockDB) SetArtifactProperty(_ context.Context, _, _, _, _ string) error { return nil }
func (m *provMockDB) GetArtifactProperty(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}
func (m *provMockDB) DeleteArtifactProperty(_ context.Context, _, _, _ string) error { return nil }
func (m *provMockDB) ListArtifactProperties(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *provMockDB) SearchByProperty(_ context.Context, _, _, _ string) ([]string, error) {
	return nil, nil
}

// Organization methods
func (m *provMockDB) CreateProject(_ context.Context, _ *models.Project) error { return nil }
func (m *provMockDB) GetProject(_ context.Context, _ string) (*models.Project, error) {
	return nil, nil
}
func (m *provMockDB) GetProjectByName(_ context.Context, _ string) (*models.Project, error) {
	return nil, nil
}
func (m *provMockDB) UpdateProject(_ context.Context, _ *models.Project) error { return nil }
func (m *provMockDB) DeleteProject(_ context.Context, _ string) error          { return nil }
func (m *provMockDB) ListProjects(_ context.Context, _ string) ([]*models.Project, error) {
	return nil, nil
}
func (m *provMockDB) AddProjectMember(_ context.Context, _, _, _ string) error { return nil }
func (m *provMockDB) ListProjectMembers(_ context.Context, _ string) ([]*models.ProjectMember, error) {
	return nil, nil
}
func (m *provMockDB) GetProjectMember(_ context.Context, _, _ string) (*models.ProjectMember, error) {
	return nil, nil
}

// System settings methods
func (m *provMockDB) GetSystemSetting(_ context.Context, _ string) (string, error) { return "", nil }
func (m *provMockDB) SetSystemSetting(_ context.Context, _, _ string) error        { return nil }

// Conda methods
func (m *provMockDB) CreateCondaPackage(_ context.Context, _ *models.CondaPackage) error { return nil }
func (m *provMockDB) GetCondaPackageFile(_ context.Context, _, _, _, _, _, _ string) (*models.CondaPackage, error) {
	return nil, nil
}
func (m *provMockDB) DeleteCondaPackage(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListCondaPackages(_ context.Context, _, _ string) ([]*models.CondaPackage, error) {
	return nil, nil
}

// Conan methods
func (m *provMockDB) CreateConanPackage(_ context.Context, _ *models.ConanPackage) error { return nil }
func (m *provMockDB) GetConanPackage(_ context.Context, _ string, _ models.ConanFileKey) (*models.ConanPackage, error) {
	return nil, nil
}
func (m *provMockDB) ListConanRecipeFiles(_ context.Context, _, _, _, _ string) ([]*models.ConanPackage, error) {
	return nil, nil
}
func (m *provMockDB) ListConanRecipes(_ context.Context, _ string) ([]models.ConanRecipe, error) {
	return nil, nil
}
func (m *provMockDB) DeleteConanPackage(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListConanPackages(_ context.Context, _ string) ([]*models.ConanPackage, error) {
	return nil, nil
}

// Ivy methods
func (m *provMockDB) CreateIvyArtifact(_ context.Context, _ *models.IvyArtifact) error { return nil }
func (m *provMockDB) GetIvyArtifact(_ context.Context, _, _, _, _, _, _ string) (*models.IvyArtifact, error) {
	return nil, nil
}
func (m *provMockDB) DeleteIvyArtifact(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListIvyArtifacts(_ context.Context, _ string) ([]*models.IvyArtifact, error) {
	return nil, nil
}

// SBT methods
func (m *provMockDB) CreateSBTArtifact(_ context.Context, _ *models.SBTArtifact) error { return nil }
func (m *provMockDB) GetSBTArtifact(_ context.Context, _, _, _, _, _ string) (*models.SBTArtifact, error) {
	return nil, nil
}
func (m *provMockDB) DeleteSBTArtifact(_ context.Context, _ string) error { return nil }
func (m *provMockDB) ListSBTArtifacts(_ context.Context, _ string) ([]*models.SBTArtifact, error) {
	return nil, nil
}

// Storage backend methods
func (m *provMockDB) CreateStorageBackend(_ context.Context, _ *models.StorageBackend) error {
	return nil
}
func (m *provMockDB) ListStorageBackends(_ context.Context) ([]*models.StorageBackend, error) {
	return nil, nil
}
func (m *provMockDB) GetStorageBackend(_ context.Context, _ string) (*models.StorageBackend, error) {
	return nil, nil
}
func (m *provMockDB) UpdateStorageBackend(_ context.Context, _ *models.StorageBackend) error {
	return nil
}
func (m *provMockDB) DeleteStorageBackend(_ context.Context, _ string) error { return nil }
func (m *provMockDB) UpdateStorageBackendStatus(_ context.Context, _, _ string, _ *string) error {
	return nil
}

func newTestProvisioner(db *provMockDB) *Provisioner {
	log, _ := logger.New("error", "json", "stderr")
	return NewProvisioner(db, log)
}

func testProvider(autoProvision, linkByEmail bool, groupMapping map[string]string) *models.SSOProvider {
	if groupMapping == nil {
		groupMapping = map[string]string{}
	}

	return &models.SSOProvider{
		ID:                 "provider-1",
		Slug:               "okta",
		Kind:               "oidc",
		AutoProvisionUsers: autoProvision,
		LinkByEmail:        linkByEmail,
		GroupMapping:       groupMapping,
	}
}

func testClaims(extID, email, username string, groups []string) SSOClaims {
	return SSOClaims{
		ExternalID:   extID,
		ProviderSlug: "okta",
		Username:     username,
		Email:        email,
		FullName:     "Test User",
		Groups:       groups,
		// The IdP sent a groups set (possibly empty) unless a test says
		// otherwise.
		GroupsPresent: true,
		// The IdP verified the email unless a test says otherwise.
		EmailVerified: true,
	}
}

func TestProvisioner_NewUserProvisioned(t *testing.T) {
	db := newProvMockDB()
	p := newTestProvisioner(db)
	provider := testProvider(true, false, nil)
	claims := testClaims("sub|123", "new@example.com", "newuser", nil)

	user, err := p.Provision(context.Background(), provider, claims)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(db.createdUsers) != 1 {
		t.Fatalf("expected 1 created user, got %d", len(db.createdUsers))
	}

	if user.Username != "newuser" {
		t.Errorf("expected username=newuser, got %s", user.Username)
	}

	if user.ExternalID == nil || *user.ExternalID != "sub|123" {
		t.Errorf("expected ExternalID=sub|123, got %v", user.ExternalID)
	}

	if user.ProviderSlug == nil || *user.ProviderSlug != "okta" {
		t.Errorf("expected ProviderSlug=okta, got %v", user.ProviderSlug)
	}

	if user.PasswordHash != nil {
		t.Error("expected PasswordHash=nil for SSO user")
	}
}

func TestProvisioner_AutoProvisionDisabledRejects(t *testing.T) {
	db := newProvMockDB()
	p := newTestProvisioner(db)
	provider := testProvider(false, false, nil)
	claims := testClaims("sub|999", "nobody@example.com", "nobody", nil)

	_, err := p.Provision(context.Background(), provider, claims)
	if !errors.Is(err, ErrUserNotProvisioned) {
		t.Errorf("expected ErrUserNotProvisioned, got %v", err)
	}
}

func TestProvisioner_DisabledUserBlocked(t *testing.T) {
	db := newProvMockDB()
	extID := "sub|disabled"
	slug := "okta"
	db.addUser(&models.User{
		ID:           "user-disabled",
		Username:     "disabled-user",
		Disabled:     true,
		ExternalID:   &extID,
		ProviderSlug: &slug,
	})

	p := newTestProvisioner(db)
	provider := testProvider(true, false, nil)
	claims := testClaims("sub|disabled", "disabled@example.com", "disabled-user", nil)

	_, err := p.Provision(context.Background(), provider, claims)
	if !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("expected ErrAccountDisabled, got %v", err)
	}
}

func TestProvisioner_EmailUpdatedOnReLogin(t *testing.T) {
	db := newProvMockDB()
	extID := "sub|returning"
	slug := "okta"
	oldEmail := "old@example.com"
	db.addUser(&models.User{
		ID:           "user-returning",
		Username:     "returning",
		Disabled:     false,
		ExternalID:   &extID,
		ProviderSlug: &slug,
		Email:        &oldEmail,
	})

	p := newTestProvisioner(db)
	provider := testProvider(true, false, nil)
	claims := testClaims("sub|returning", "new@example.com", "returning", nil)

	_, err := p.Provision(context.Background(), provider, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(db.updatedUsers) == 0 {
		t.Fatal("expected UpdateUser to be called")
	}

	updated := db.updatedUsers[len(db.updatedUsers)-1]
	if updated.Email == nil || *updated.Email != "new@example.com" {
		t.Errorf("expected email=new@example.com, got %v", updated.Email)
	}
}

// A login must never write the whole auth_users row from its stale snapshot:
// that would resurrect a user an admin disabled (or restore a replaced
// password hash) between the lookup and the write.
func TestProvisioner_ReLoginUsesNarrowUpdate(t *testing.T) {
	db := newProvMockDB()
	extID := "sub|narrow"
	slug := "okta"
	db.addUser(&models.User{ID: "user-narrow", Username: "narrow", ExternalID: &extID, ProviderSlug: &slug})

	p := newTestProvisioner(db)

	if _, err := p.Provision(context.Background(), testProvider(true, false, nil), testClaims("sub|narrow", "n@example.com", "narrow", nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if db.fullRowUpdates != 0 {
		t.Fatalf("Provision wrote the full user row %d time(s)", db.fullRowUpdates)
	}

	if len(db.updatedUsers) == 0 {
		t.Fatal("expected the narrow profile update")
	}
}

func TestProvisioner_GroupSyncReplaces(t *testing.T) {
	db := newProvMockDB()
	extID := "sub|grpuser"
	slug := "okta"
	db.addUser(&models.User{
		ID:           "user-grp",
		Username:     "grpuser",
		ExternalID:   &extID,
		ProviderSlug: &slug,
	})
	db.addGroup(&models.Group{ID: "grp-a-id", Name: "local-a"})
	db.addGroup(&models.Group{ID: "grp-b-id", Name: "local-b"})

	p := newTestProvisioner(db)
	provider := testProvider(true, false, map[string]string{"a": "local-a", "b": "local-b"})
	claims := testClaims("sub|grpuser", "grpuser@example.com", "grpuser", []string{"a", "b"})

	_, err := p.Provision(context.Background(), provider, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(db.syncGroupsArgs) != 1 {
		t.Fatalf("expected 1 SyncSSOGroups call, got %d", len(db.syncGroupsArgs))
	}

	got := db.syncGroupsArgs[0].groupIDs
	if len(got) != 2 {
		t.Errorf("expected 2 group IDs, got %v", got)
	}

	idSet := map[string]bool{got[0]: true, got[1]: true}
	if !idSet["grp-a-id"] || !idSet["grp-b-id"] {
		t.Errorf("expected group IDs [grp-a-id, grp-b-id], got %v", got)
	}
}

func TestProvisioner_UnmappedGroupsIgnored(t *testing.T) {
	db := newProvMockDB()
	extID := "sub|unmapped"
	slug := "okta"
	db.addUser(&models.User{
		ID:           "user-unmapped",
		Username:     "unmapped",
		ExternalID:   &extID,
		ProviderSlug: &slug,
	})
	db.addGroup(&models.Group{ID: "grp-a-id", Name: "local-a"})

	p := newTestProvisioner(db)
	provider := testProvider(true, false, map[string]string{"a": "local-a"})
	claims := testClaims("sub|unmapped", "unmapped@example.com", "unmapped", []string{"a", "unknown"})

	_, err := p.Provision(context.Background(), provider, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(db.syncGroupsArgs) != 1 {
		t.Fatalf("expected 1 SyncSSOGroups call, got %d", len(db.syncGroupsArgs))
	}

	got := db.syncGroupsArgs[0].groupIDs
	if len(got) != 1 || got[0] != "grp-a-id" {
		t.Errorf("expected [grp-a-id], got %v", got)
	}
}

func TestProvisioner_LinkByEmailMatch(t *testing.T) {
	db := newProvMockDB()
	email := "alice@example.com"
	db.addUser(&models.User{
		ID:       "user-alice",
		Username: "alice",
		Email:    &email,
		Disabled: false,
	})

	p := newTestProvisioner(db)
	provider := testProvider(false, true, nil)
	claims := testClaims("sub|alice-sso", "alice@example.com", "alice", nil)

	user, err := p.Provision(context.Background(), provider, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !db.updateSSOCalled {
		t.Error("expected UpdateUserSSO to be called")
	}

	if user.ID != "user-alice" {
		t.Errorf("expected user-alice, got %s", user.ID)
	}
}

func TestProvisioner_LinkByEmailDisabledMatch(t *testing.T) {
	db := newProvMockDB()
	email := "disabled@example.com"
	db.addUser(&models.User{
		ID:       "user-disabled-email",
		Username: "disabled-email",
		Email:    &email,
		Disabled: true,
	})

	p := newTestProvisioner(db)
	provider := testProvider(true, true, nil)
	claims := testClaims("sub|disabled-sso", "disabled@example.com", "disabled-email", nil)

	_, err := p.Provision(context.Background(), provider, claims)
	if !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("expected ErrAccountDisabled, got %v", err)
	}
}

func TestProvisioner_GroupSyncErrorDoesNotBlock(t *testing.T) {
	db := newProvMockDB()
	extID := "sub|syncerr"
	slug := "okta"
	db.addUser(&models.User{
		ID:           "user-syncerr",
		Username:     "syncerr",
		ExternalID:   &extID,
		ProviderSlug: &slug,
	})
	db.syncGroupsErr = errors.New("transient db error")

	p := newTestProvisioner(db)
	provider := testProvider(true, false, nil)
	claims := testClaims("sub|syncerr", "syncerr@example.com", "syncerr", nil)

	user, err := p.Provision(context.Background(), provider, claims)
	if err != nil {
		t.Errorf("group sync error must not block login, got: %v", err)
	}

	if user == nil {
		t.Error("expected non-nil user when group sync fails")
	}
}

func TestProvisioner_LocalGroupMissingAutoCreated(t *testing.T) {
	db := newProvMockDB()
	extID := "sub|missinggrp"
	slug := "okta"
	db.addUser(&models.User{
		ID:           "user-missinggrp",
		Username:     "missinggrp",
		ExternalID:   &extID,
		ProviderSlug: &slug,
	})

	p := newTestProvisioner(db)
	provider := testProvider(true, false, map[string]string{"a": "missing-group"})
	claims := testClaims("sub|missinggrp", "missinggrp@example.com", "missinggrp", []string{"a"})

	_, err := p.Provision(context.Background(), provider, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(db.syncGroupsArgs) != 1 {
		t.Fatalf("expected 1 SyncSSOGroups call, got %d", len(db.syncGroupsArgs))
	}
	// With auto-create, the missing group should be created and included
	if len(db.syncGroupsArgs[0].groupIDs) != 1 {
		t.Errorf("expected 1 groupID for auto-created group, got %v", db.syncGroupsArgs[0].groupIDs)
	}
}

func TestProvisioner_DuplicateEmailRejected(t *testing.T) {
	db := newProvMockDB()
	email := "taken@example.com"
	db.addUser(&models.User{
		ID:       "user-existing",
		Username: "existing",
		Email:    &email,
	})

	p := newTestProvisioner(db)
	provider := testProvider(true, false, nil)
	claims := testClaims("sub|newcomer", "taken@example.com", "newcomer", nil)

	_, err := p.Provision(context.Background(), provider, claims)
	if !errors.Is(err, ErrEmailAlreadyInUse) {
		t.Errorf("expected ErrEmailAlreadyInUse, got %v", err)
	}
}

func (m *provMockDB) UpsertAIAnalysis(_ context.Context, _ *models.AIAnalysis) error { return nil }
func (m *provMockDB) GetAIAnalysis(_ context.Context, _, _ string) (*models.AIAnalysis, error) {
	return nil, nil
}
func (m *provMockDB) ListAIAnalyses(_ context.Context, _ string, _ string, _ bool, _, _ int) ([]*models.AIAnalysis, int, error) {
	return nil, 0, nil
}
func (m *provMockDB) DeleteAIAnalysis(_ context.Context, _, _ string) error { return nil }
func (m *provMockDB) ListPendingAIAnalyses(_ context.Context, _ int) ([]*models.AIAnalysis, error) {
	return nil, nil
}

func TestProvisioner_LinkByEmail_UnverifiedEmailNotLinked(t *testing.T) {
	db := newProvMockDB()
	email := "admin@example.com"
	db.addUser(&models.User{ID: "user-admin", Username: "admin", Email: &email})

	p := newTestProvisioner(db)
	provider := testProvider(true, true, nil)
	claims := testClaims("sub|attacker", "admin@example.com", "attacker", nil)
	claims.EmailVerified = false

	_, err := p.Provision(context.Background(), provider, claims)
	if !errors.Is(err, ErrEmailAlreadyInUse) {
		t.Fatalf("expected ErrEmailAlreadyInUse for unverified email, got %v", err)
	}

	if db.updateSSOCalled {
		t.Error("UpdateUserSSO must not be called for an unverified email")
	}

	if len(db.createdUsers) != 0 {
		t.Errorf("no user may be created, got %d", len(db.createdUsers))
	}
}

func TestProvisioner_LinkByEmail_AlreadyLinkedUserConflicts(t *testing.T) {
	cases := map[string]struct{ extID, slug *string }{
		"other subject":  {new("sub|someone-else"), new("okta")},
		"other provider": {new("sub|alice-sso"), new("azure")},
		"provider only":  {nil, new("azure")},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := newProvMockDB()
			email := "alice@example.com"
			// Registered by email only so the external-id lookup misses.
			u := &models.User{ID: "user-alice", Username: "alice", Email: &email, ExternalID: tc.extID, ProviderSlug: tc.slug}
			db.usersByEmail[email] = u
			db.usersByID[u.ID] = u

			p := newTestProvisioner(db)
			provider := testProvider(true, true, nil)
			claims := testClaims("sub|alice-sso", "alice@example.com", "alice", nil)

			_, err := p.Provision(context.Background(), provider, claims)
			if !errors.Is(err, ErrIdentityConflict) {
				t.Fatalf("expected ErrIdentityConflict, got %v", err)
			}

			if db.updateSSOCalled {
				t.Error("UpdateUserSSO must not re-bind an already linked account")
			}
		})
	}
}

func TestProvisioner_UnverifiedEmailNotWrittenOnReLogin(t *testing.T) {
	db := newProvMockDB()
	extID, slug, oldEmail := "sub|returning", "okta", "old@example.com"
	db.addUser(&models.User{ID: "user-returning", Username: "returning", ExternalID: &extID, ProviderSlug: &slug, Email: &oldEmail})

	p := newTestProvisioner(db)
	claims := testClaims("sub|returning", "new@example.com", "returning", nil)
	claims.EmailVerified = false

	if _, err := p.Provision(context.Background(), testProvider(true, false, nil), claims); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := db.updatedUsers[len(db.updatedUsers)-1]
	if updated.Email == nil || *updated.Email != oldEmail {
		t.Errorf("unverified email must not overwrite the stored one, got %v", updated.Email)
	}
}

func permSyncFixture() (*provMockDB, *models.SSOProvider) {
	db := newProvMockDB()
	db.addGroup(&models.Group{ID: "grp-admins-id", Name: "admins"})
	db.targetsByName = map[string]*models.PermissionTarget{"deploy-all": {ID: "target-deploy"}}

	provider := testProvider(true, false, map[string]string{"idp-admins": "admins"})
	provider.PermissionMapping = map[string]string{"admins": "deploy-all"}

	return db, provider
}

func TestProvisioner_PermissionSync_NeverDeletesAssignments(t *testing.T) {
	db, provider := permSyncFixture()
	p := newTestProvisioner(db)

	// User A is in the IdP group: the group gets the target.
	if err := p.SyncSSOPermissionsFromGroups(context.Background(), provider, []string{"idp-admins"}); err != nil {
		t.Fatalf("sync A: %v", err)
	}

	want := "target-deploy|group|grp-admins-id"
	if len(db.assignments) != 1 || db.assignments[0] != want {
		t.Fatalf("expected assignment %q, got %v", want, db.assignments)
	}

	// User B is not in the IdP group (or sends no group claim at all): nothing
	// may be removed from the shared group.
	for _, groups := range [][]string{nil, {"other-group"}} {
		if err := p.SyncSSOPermissionsFromGroups(context.Background(), provider, groups); err != nil {
			t.Fatalf("sync B: %v", err)
		}
	}

	if len(db.deletedAssignments) != 0 {
		t.Errorf("login must never delete group assignments, got %v", db.deletedAssignments)
	}
}

func TestProvisioner_Login_WithoutGroupClaimKeepsOthersAssignments(t *testing.T) {
	db, provider := permSyncFixture()
	p := newTestProvisioner(db)

	if _, err := p.Provision(context.Background(), provider, testClaims("sub|b", "b@example.com", "b", nil)); err != nil {
		t.Fatalf("provision: %v", err)
	}

	if len(db.deletedAssignments) != 0 {
		t.Errorf("login without group claim deleted assignments: %v", db.deletedAssignments)
	}
}

func TestProvisioner_UsernameCollisionGetsSuffix(t *testing.T) {
	db := newProvMockDB()
	db.addUser(&models.User{ID: "local-1", Username: "newuser"})
	p := newTestProvisioner(db)

	user, err := p.Provision(context.Background(), testProvider(true, false, nil), testClaims("sub|9", "n@example.com", "newuser", nil))
	if err != nil {
		t.Fatalf("collision must not fail provisioning, got %v", err)
	}

	if user.Username == "newuser" || !strings.HasPrefix(user.Username, "newuser-") {
		t.Errorf("expected suffixed username, got %q", user.Username)
	}
}
