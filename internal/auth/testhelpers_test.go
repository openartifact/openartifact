// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func sha256ForString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type tokenMockDB struct {
	database.BaseMockDB

	tokens     map[string]*models.Token
	tokensByID map[string]*models.Token
	users      map[string]*models.User
}

func newTokenMockDB() *tokenMockDB {
	return &tokenMockDB{
		tokens:     make(map[string]*models.Token),
		tokensByID: make(map[string]*models.Token),
		users:      make(map[string]*models.User),
	}
}

func (m *tokenMockDB) GetDB() *sql.DB                                          { return nil }
func (m *tokenMockDB) Close() error                                            { return nil }
func (m *tokenMockDB) Ping(_ context.Context) error                            { return nil }
func (m *tokenMockDB) BeginTx(_ context.Context) (database.Transaction, error) { return nil, nil }
func (m *tokenMockDB) Migrate(_ context.Context) error                         { return nil }
func (m *tokenMockDB) GetRepositoryByName(_ context.Context, _ string) (*models.Repository, error) {
	return nil, nil
}
func (m *tokenMockDB) CreateRepository(_ context.Context, _ *models.Repository) error { return nil }
func (m *tokenMockDB) ListRepositories(_ context.Context, _, _ int, _ string) ([]*models.Repository, error) {
	return nil, nil
}
func (m *tokenMockDB) CountRepositories(_ context.Context, _ string) (int, error)     { return 0, nil }
func (m *tokenMockDB) UpdateRepository(_ context.Context, _ *models.Repository) error { return nil }
func (m *tokenMockDB) DeleteRepository(_ context.Context, _ string) error             { return nil }
func (m *tokenMockDB) ListComponentsByRepo(_ context.Context, _, _ string, _, _ int) ([]*models.Component, int, error) {
	return nil, 0, nil
}
func (m *tokenMockDB) GetRPMRepo(_ context.Context, _ string) (*models.RPMRepo, error) {
	return nil, nil
}
func (m *tokenMockDB) CreateRPMRepo(_ context.Context, _ *models.RPMRepo) error     { return nil }
func (m *tokenMockDB) MarkRepoStale(_ context.Context, _ string) error              { return nil }
func (m *tokenMockDB) CreateComponent(_ context.Context, _ *models.Component) error { return nil }
func (m *tokenMockDB) GetComponentByID(_ context.Context, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *tokenMockDB) GetComponentByFilename(_ context.Context, _, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *tokenMockDB) GetComponentsByRepo(_ context.Context, _ string) ([]models.Component, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteComponent(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) GetRepoMetadataByPath(_ context.Context, _, _ string) (*models.RepoMetadata, error) {
	return nil, nil
}
func (m *tokenMockDB) GetRepoMetadataByRepo(_ context.Context, _ string) ([]models.RepoMetadata, error) {
	return nil, nil
}
func (m *tokenMockDB) UpsertRepoMetadata(_ context.Context, _ *models.RepoMetadata) error {
	return nil
}
func (m *tokenMockDB) CreateDebianDistribution(_ context.Context, _ *models.DebianDistribution) error {
	return nil
}
func (m *tokenMockDB) GetDebianDistribution(_ context.Context, _, _ string) (*models.DebianDistribution, error) {
	return nil, nil
}
func (m *tokenMockDB) GetDebianDistributionsByRepo(_ context.Context, _ string) ([]models.DebianDistribution, error) {
	return nil, nil
}
func (m *tokenMockDB) CreateDebianComponent(_ context.Context, _ *models.DebianComponent) error {
	return nil
}
func (m *tokenMockDB) GetDebianComponentsByDist(_ context.Context, _ string) ([]models.DebianComponent, error) {
	return nil, nil
}
func (m *tokenMockDB) CreateDebianArchitecture(_ context.Context, _ *models.DebianArchitecture) error {
	return nil
}
func (m *tokenMockDB) GetDebianArchitecturesByDist(_ context.Context, _ string) ([]models.DebianArchitecture, error) {
	return nil, nil
}
func (m *tokenMockDB) GetDebianPackageByFilename(_ context.Context, _, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *tokenMockDB) GetDebianPackagesByDistComp(_ context.Context, _, _, _, _ string) ([]models.Component, error) {
	return nil, nil
}
func (m *tokenMockDB) CreateMavenArtifact(_ context.Context, _ *models.MavenArtifact) error {
	return nil
}
func (m *tokenMockDB) GetMavenArtifactByPath(_ context.Context, _, _ string) (*models.MavenArtifact, error) {
	return nil, nil
}
func (m *tokenMockDB) GetMavenArtifactsByGAV(_ context.Context, _, _, _, _ string) ([]*models.MavenArtifact, error) {
	return nil, nil
}
func (m *tokenMockDB) ListMavenGARefs(_ context.Context, _, _, _ string) (*models.MavenGARefs, error) {
	return nil, nil
}
func (m *tokenMockDB) GetLatestSnapshot(_ context.Context, _, _, _, _ string) (*models.MavenArtifact, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteMavenArtifact(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) CreateNPMPackage(_ context.Context, _ *models.NPMPackage) (*models.NPMPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) GetNPMPackageByName(_ context.Context, _, _ string) (*models.NPMPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteNPMPackage(_ context.Context, _ string) error   { return nil }
func (m *tokenMockDB) MarkPackumentStale(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) UpdateNPMPackageLatestVersion(_ context.Context, _, _ string) error {
	return nil
}
func (m *tokenMockDB) CreateNPMVersion(_ context.Context, _ *models.NPMVersion) (*models.NPMVersion, error) {
	return nil, nil
}
func (m *tokenMockDB) GetNPMVersion(_ context.Context, _, _ string) (*models.NPMVersion, error) {
	return nil, nil
}
func (m *tokenMockDB) GetNPMVersionsByPackage(_ context.Context, _ string) ([]*models.NPMVersion, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteNPMVersion(_ context.Context, _, _ string) error { return nil }
func (m *tokenMockDB) UpsertNPMDistTag(_ context.Context, _ *models.NPMDistTag) (*models.NPMDistTag, error) {
	return nil, nil
}
func (m *tokenMockDB) GetNPMDistTags(_ context.Context, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteNPMDistTag(_ context.Context, _, _ string) error { return nil }

func (m *tokenMockDB) CreateUser(_ context.Context, u *models.User) (*models.User, error) {
	if u.ID == "" {
		u.ID = fmt.Sprintf("user-%d", len(m.users)+1)
	}

	m.users[u.ID] = u

	return u, nil
}
func (m *tokenMockDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return m.users[id], nil
}
func (m *tokenMockDB) GetUserByUsername(_ context.Context, _ string) (*models.User, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateUser(_ context.Context, _ *models.User) error { return nil }
func (m *tokenMockDB) ListUsers(_ context.Context, _, _ int) ([]*models.User, int, error) {
	return nil, 0, nil
}
func (m *tokenMockDB) CountUsers(_ context.Context) (int, error) { return len(m.users), nil }
func (m *tokenMockDB) CreateGroup(_ context.Context, _ *models.Group) (*models.Group, error) {
	return nil, nil
}
func (m *tokenMockDB) GetGroupByID(_ context.Context, _ string) (*models.Group, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateGroup(_ context.Context, _ *models.Group) error     { return nil }
func (m *tokenMockDB) DeleteGroup(_ context.Context, _ string) error            { return nil }
func (m *tokenMockDB) ListGroups(_ context.Context) ([]*models.Group, error)    { return nil, nil }
func (m *tokenMockDB) AddUserToGroup(_ context.Context, _, _ string) error      { return nil }
func (m *tokenMockDB) RemoveUserFromGroup(_ context.Context, _, _ string) error { return nil }
func (m *tokenMockDB) ListGroupsByUser(_ context.Context, _ string) ([]*models.Group, error) {
	return nil, nil
}
func (m *tokenMockDB) ListUsersByGroup(_ context.Context, _ string) ([]*models.User, error) {
	return nil, nil
}

func (m *tokenMockDB) CreateSession(_ context.Context, _ *models.Session) (*models.Session, error) {
	return nil, nil
}
func (m *tokenMockDB) GetSessionByTokenHash(_ context.Context, _ string) (*models.Session, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteSession(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) UpdateSessionLastSeen(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *tokenMockDB) CreateToken(_ context.Context, t *models.Token) (*models.Token, error) {
	if t.ID == "" {
		t.ID = fmt.Sprintf("tok-%d", len(m.tokensByID)+1)
	}

	m.tokens[t.TokenHash] = t
	m.tokensByID[t.ID] = t

	return t, nil
}
func (m *tokenMockDB) GetTokenByHash(_ context.Context, hash string) (*models.Token, error) {
	return m.tokens[hash], nil
}
func (m *tokenMockDB) GetTokenByID(_ context.Context, tokenID string) (*models.Token, error) {
	return m.tokensByID[tokenID], nil
}
func (m *tokenMockDB) RevokeToken(_ context.Context, tokenID string) error {
	if t, ok := m.tokensByID[tokenID]; ok {
		now := time.Now()
		t.RevokedAt = &now
	}

	return nil
}
func (m *tokenMockDB) UpdateTokenLastUsed(_ context.Context, tokenID string) error {
	if t, ok := m.tokensByID[tokenID]; ok {
		now := time.Now()
		t.LastUsedAt = &now
	}

	return nil
}
func (m *tokenMockDB) ListTokensByUser(_ context.Context, _ string) ([]*models.Token, error) {
	return nil, nil
}

func (m *tokenMockDB) CreatePermissionTarget(_ context.Context, _ *models.PermissionTarget) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *tokenMockDB) GetPermissionTargetByID(_ context.Context, _ string) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *tokenMockDB) GetPermissionTargetByName(_ context.Context, _ string) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdatePermissionTarget(_ context.Context, _ *models.PermissionTarget) error {
	return nil
}
func (m *tokenMockDB) DeletePermissionTarget(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListPermissionTargets(_ context.Context) ([]*models.PermissionTarget, error) {
	return nil, nil
}
func (m *tokenMockDB) CreatePermissionAssignment(_ context.Context, _ *models.PermissionAssignment) error {
	return nil
}
func (m *tokenMockDB) DeletePermissionAssignment(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *tokenMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return nil, nil
}

func (m *tokenMockDB) GetOrCreatePermissionAssignment(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *tokenMockDB) SyncSSOPermissionsForGroup(_ context.Context, _ string, _ []string) error {
	return nil
}

func (m *tokenMockDB) CreateSSOProvider(_ context.Context, _ *models.SSOProvider) error {
	return nil
}
func (m *tokenMockDB) GetSSOProviderBySlug(_ context.Context, _ string) (*models.SSOProvider, error) {
	return nil, nil
}
func (m *tokenMockDB) GetSSOProviderByID(_ context.Context, _ string) (*models.SSOProvider, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateSSOProvider(_ context.Context, _ *models.SSOProvider) error { return nil }
func (m *tokenMockDB) DeleteSSOProvider(_ context.Context, _ string) error              { return nil }
func (m *tokenMockDB) ListEnabledSSOProviders(_ context.Context) ([]*models.SSOProvider, error) {
	return nil, nil
}
func (m *tokenMockDB) ListAllSSOProviders(_ context.Context) ([]*models.SSOProvider, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateUserSSO(_ context.Context, _, _, _ string) error { return nil }
func (m *tokenMockDB) SyncSSOGroups(_ context.Context, _ string, _ []string) error {
	return nil
}
func (m *tokenMockDB) GetUserByExternalID(_ context.Context, _, _ string) (*models.User, error) {
	return nil, nil
}
func (m *tokenMockDB) GetUserByEmail(_ context.Context, _ string) (*models.User, error) {
	return nil, nil
}
func (m *tokenMockDB) GetGroupByName(_ context.Context, _ string) (*models.Group, error) {
	return nil, nil
}

func (m *tokenMockDB) GetOrCreateGroup(_ context.Context, _ string) (*models.Group, error) {
	return nil, nil
}

func (m *tokenMockDB) AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error {
	return nil
}

func (m *tokenMockDB) CreateBundlePromotion(ctx context.Context, promotion *models.BundlePromotion) error {
	return nil
}

func (m *tokenMockDB) CreatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *tokenMockDB) CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *tokenMockDB) RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error {
	return nil
}

func (m *tokenMockDB) ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *tokenMockDB) GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *tokenMockDB) ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error) {
	return nil, nil
}

func (m *tokenMockDB) GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *tokenMockDB) GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *tokenMockDB) UpdatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *tokenMockDB) DeletePromotionPipeline(ctx context.Context, id string) error {
	return nil
}

func (m *tokenMockDB) ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *tokenMockDB) GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *tokenMockDB) GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *tokenMockDB) UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *tokenMockDB) DeleteReleaseBundle(ctx context.Context, id string) error {
	return nil
}

func (m *tokenMockDB) ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error) {
	return nil, 0, nil
}

func (m *tokenMockDB) UpdateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error {
	return nil
}

func (m *tokenMockDB) ArtifactExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (m *tokenMockDB) ArtifactExistsBySHA256(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}

func (m *tokenMockDB) CreatePackageDelta(_ context.Context, _ *models.PackageDelta) error {
	return nil
}

func (m *tokenMockDB) GetPackageDelta(_ context.Context, _, _, _ string) (*models.PackageDelta, error) {
	return nil, nil
}

func (m *tokenMockDB) ListPackageDeltas(_ context.Context, _, _ string) ([]*models.PackageDelta, error) {
	return nil, nil
}

func (m *tokenMockDB) DeletePackageDelta(_ context.Context, _ string) error {
	return nil
}

func (m *tokenMockDB) InsertAuditLog(_ context.Context, _ *models.AuditLog) error {
	return nil
}

func (m *tokenMockDB) ListAuditLogs(_ context.Context, _ models.AuditListParams) (*models.AuditPage, error) {
	return &models.AuditPage{}, nil
}

func (m *tokenMockDB) ListAllTokens(_ context.Context, _, _ int) ([]*models.Token, int, error) {
	return nil, 0, nil
}

// Docker Registry methods
func (m *tokenMockDB) CreateDockerBlob(_ context.Context, _ *models.DockerBlob) error { return nil }
func (m *tokenMockDB) GetDockerBlob(_ context.Context, _, _ string) (*models.DockerBlob, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteDockerBlob(_ context.Context, _, _ string) error { return nil }
func (m *tokenMockDB) ListDockerBlobs(_ context.Context, _ string) ([]*models.DockerBlob, error) {
	return nil, nil
}

func (m *tokenMockDB) CreateDockerUpload(_ context.Context, _ *models.DockerUpload) error { return nil }
func (m *tokenMockDB) GetDockerUpload(_ context.Context, _, _ string) (*models.DockerUpload, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateDockerUploadOffset(_ context.Context, _, _ string, _, _ int64) (bool, error) {
	return true, nil
}
func (m *tokenMockDB) DeleteDockerUpload(_ context.Context, _, _ string) error { return nil }

func (m *tokenMockDB) CreateDockerManifest(_ context.Context, _ *models.DockerManifest) error {
	return nil
}
func (m *tokenMockDB) GetDockerManifest(_ context.Context, _, _, _ string) (*models.DockerManifest, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteDockerManifest(_ context.Context, _, _, _ string) error { return nil }

func (m *tokenMockDB) UpsertDockerTag(_ context.Context, _ *models.DockerTag) error { return nil }
func (m *tokenMockDB) GetDockerTag(_ context.Context, _, _, _ string) (*models.DockerTag, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteDockerTag(_ context.Context, _, _, _ string) error          { return nil }
func (m *tokenMockDB) DeleteDockerTagsByDigest(_ context.Context, _, _, _ string) error { return nil }
func (m *tokenMockDB) ListDockerTagsByRepo(_ context.Context, _ string, _ int) ([]*models.DockerTag, error) {
	return nil, nil
}
func (m *tokenMockDB) ListDockerTags(_ context.Context, _, _, _ string, _ int) ([]*models.DockerTag, error) {
	return nil, nil
}

// Proxy/Group repository methods
func (m *tokenMockDB) AddGroupMember(_ context.Context, _, _ string, _ int) error { return nil }
func (m *tokenMockDB) RemoveGroupMember(_ context.Context, _, _ string) error     { return nil }
func (m *tokenMockDB) ListGroupMembers(_ context.Context, _ string) ([]*models.RepositoryGroupMember, error) {
	return nil, nil
}
func (m *tokenMockDB) GetProxyCacheEntry(_ context.Context, _, _ string) (*models.ProxyCacheEntry, error) {
	return nil, nil
}
func (m *tokenMockDB) UpsertProxyCacheEntry(_ context.Context, _ *models.ProxyCacheEntry) error {
	return nil
}
func (m *tokenMockDB) DeleteProxyCacheEntry(_ context.Context, _, _ string) error { return nil }
func (m *tokenMockDB) DeleteExpiredProxyCacheEntries(_ context.Context) (int64, error) {
	return 0, nil
}

// Helm methods
func (m *tokenMockDB) CreateHelmChart(_ context.Context, _ *models.HelmChart) error { return nil }
func (m *tokenMockDB) GetHelmChart(_ context.Context, _, _, _ string) (*models.HelmChart, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteHelmChart(_ context.Context, _, _, _ string) error { return nil }
func (m *tokenMockDB) ListHelmCharts(_ context.Context, _ string) ([]*models.HelmChart, error) {
	return nil, nil
}
func (m *tokenMockDB) ListHelmChartVersions(_ context.Context, _, _ string) ([]*models.HelmChart, error) {
	return nil, nil
}

// PyPI methods
func (m *tokenMockDB) CreatePyPIPackage(_ context.Context, _ *models.PyPIPackage) error { return nil }
func (m *tokenMockDB) GetPyPIPackage(_ context.Context, _, _, _, _ string) (*models.PyPIPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) DeletePyPIPackage(_ context.Context, _, _, _, _ string) error { return nil }
func (m *tokenMockDB) ListPyPIPackages(_ context.Context, _ string) ([]*models.PyPIPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) ListPyPIPackageVersions(_ context.Context, _, _ string) ([]*models.PyPIPackage, error) {
	return nil, nil
}

// NuGet methods
func (m *tokenMockDB) CreateNuGetPackage(_ context.Context, _ *models.NuGetPackage) error { return nil }
func (m *tokenMockDB) GetNuGetPackage(_ context.Context, _, _, _ string) (*models.NuGetPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteNuGetPackage(_ context.Context, _, _, _ string) error { return nil }
func (m *tokenMockDB) ListNuGetPackages(_ context.Context, _ string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) ListNuGetPackageVersions(_ context.Context, _, _ string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) SearchNuGetPackages(_ context.Context, _, _ string, _, _ int) ([]*models.NuGetPackage, int, error) {
	return nil, 0, nil
}
func (m *tokenMockDB) IncrementNuGetDownloadCount(_ context.Context, _, _, _ string) error {
	return nil
}

// Go Module Proxy methods
func (m *tokenMockDB) CreateGoModule(_ context.Context, _ *models.GoModule) error { return nil }
func (m *tokenMockDB) GetGoModule(_ context.Context, _, _, _ string) (*models.GoModule, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteGoModule(_ context.Context, _, _, _ string) error { return nil }
func (m *tokenMockDB) ListGoModuleVersions(_ context.Context, _, _ string) ([]*models.GoModule, error) {
	return nil, nil
}

// Cleanup policy methods
func (m *tokenMockDB) CreateCleanupPolicy(_ context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (m *tokenMockDB) GetCleanupPolicy(_ context.Context, _ string) (*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateCleanupPolicy(_ context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (m *tokenMockDB) DeleteCleanupPolicy(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListCleanupPolicies(_ context.Context, _ string) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *tokenMockDB) ListEnabledCleanupPolicies(_ context.Context) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateCleanupPolicyRunResult(_ context.Context, _ string, _ int, _ error) error {
	return nil
}

// Webhook methods
func (m *tokenMockDB) CreateWebhook(_ context.Context, _ *models.Webhook) error { return nil }
func (m *tokenMockDB) GetWebhook(_ context.Context, _ string) (*models.Webhook, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateWebhook(_ context.Context, _ *models.Webhook) error { return nil }
func (m *tokenMockDB) DeleteWebhook(_ context.Context, _ string) error          { return nil }
func (m *tokenMockDB) ListWebhooks(_ context.Context, _ string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *tokenMockDB) ListWebhooksForEvent(_ context.Context, _, _ string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *tokenMockDB) CreateWebhookDelivery(_ context.Context, _ *models.WebhookDelivery) error {
	return nil
}
func (m *tokenMockDB) UpdateWebhookDelivery(_ context.Context, _ *models.WebhookDelivery) error {
	return nil
}
func (m *tokenMockDB) ListWebhookDeliveries(_ context.Context, _ string, _ int) ([]*models.WebhookDelivery, error) {
	return nil, nil
}

// Backup methods
func (m *tokenMockDB) CreateBackupJob(_ context.Context, _ *models.BackupJob) error { return nil }
func (m *tokenMockDB) GetBackupJob(_ context.Context, _ string) (*models.BackupJob, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateBackupJob(_ context.Context, _ *models.BackupJob) error { return nil }
func (m *tokenMockDB) ListBackupJobs(_ context.Context, _ int) ([]*models.BackupJob, error) {
	return nil, nil
}

// Cargo methods
func (m *tokenMockDB) CreateCargoCrate(_ context.Context, _ *models.CargoCrate) error { return nil }
func (m *tokenMockDB) GetCargoCrate(_ context.Context, _, _, _ string) (*models.CargoCrate, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteCargoCrate(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListCargoCrateVersions(_ context.Context, _, _ string) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *tokenMockDB) SearchCargoCrates(_ context.Context, _, _ string, _ int) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *tokenMockDB) YankCargoCrate(_ context.Context, _, _, _ string, _ bool) error { return nil }
func (m *tokenMockDB) IncrementCargoDownloadCount(_ context.Context, _, _, _ string) error {
	return nil
}

// RubyGems methods
func (m *tokenMockDB) CreateRubyGem(_ context.Context, _ *models.RubyGem) error { return nil }
func (m *tokenMockDB) GetRubyGem(_ context.Context, _, _, _, _ string) (*models.RubyGem, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteRubyGem(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListRubyGems(_ context.Context, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *tokenMockDB) ListRubyGemVersions(_ context.Context, _, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *tokenMockDB) SearchRubyGems(_ context.Context, _, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *tokenMockDB) YankRubyGem(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

// Raw artifact methods
func (m *tokenMockDB) UpsertRawArtifact(_ context.Context, _ *models.RawArtifact) error { return nil }
func (m *tokenMockDB) GetRawArtifact(_ context.Context, _, _ string) (*models.RawArtifact, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteRawArtifact(_ context.Context, _, _ string) error { return nil }
func (m *tokenMockDB) ListRawArtifacts(_ context.Context, _, _ string) ([]*models.RawArtifact, error) {
	return nil, nil
}

// SBOM methods
func (m *tokenMockDB) CreateSBOMReport(_ context.Context, _ *models.SBOMReport) error { return nil }
func (m *tokenMockDB) GetSBOMReport(_ context.Context, _, _, _, _ string) (*models.SBOMReport, error) {
	return nil, nil
}
func (m *tokenMockDB) ListSBOMReports(_ context.Context, _ string) ([]*models.SBOMReport, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteSBOMReport(_ context.Context, _ string) error { return nil }

// License compliance methods
func (m *tokenMockDB) CreateLicenseScanResult(_ context.Context, _ *models.LicenseScanResult) error {
	return nil
}
func (m *tokenMockDB) GetLicenseScanResult(_ context.Context, _, _ string) (*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateLicenseScanResult(_ context.Context, _ *models.LicenseScanResult) error {
	return nil
}
func (m *tokenMockDB) ListLicenseScanResults(_ context.Context, _ string) ([]*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *tokenMockDB) CreateLicensePolicy(_ context.Context, _ *models.LicensePolicy) error {
	return nil
}
func (m *tokenMockDB) GetLicensePolicy(_ context.Context, _ string) (*models.LicensePolicy, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateLicensePolicy(_ context.Context, _ *models.LicensePolicy) error {
	return nil
}
func (m *tokenMockDB) DeleteLicensePolicy(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListLicensePolicies(_ context.Context, _ string) ([]*models.LicensePolicy, error) {
	return nil, nil
}

// Secret detection methods
func (m *tokenMockDB) CreateSecretScanResult(_ context.Context, _ *models.SecretScanResult) error {
	return nil
}
func (m *tokenMockDB) GetSecretScanResult(_ context.Context, _, _ string) (*models.SecretScanResult, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateSecretScanResult(_ context.Context, _ *models.SecretScanResult) error {
	return nil
}
func (m *tokenMockDB) ListSecretScanResults(_ context.Context, _ string, _ bool) ([]*models.SecretScanResult, error) {
	return nil, nil
}
func (m *tokenMockDB) QuarantineArtifact(_ context.Context, _, _ string, _ bool) error { return nil }

// Vulnerability scanning methods
func (m *tokenMockDB) CreateVulnScanResult(_ context.Context, _ *models.VulnScanResult) error {
	return nil
}
func (m *tokenMockDB) GetVulnScanResult(_ context.Context, _, _ string) (*models.VulnScanResult, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateVulnScanResult(_ context.Context, _ *models.VulnScanResult) error {
	return nil
}
func (m *tokenMockDB) ListVulnScanResults(_ context.Context, _ string, _ bool) ([]*models.VulnScanResult, error) {
	return nil, nil
}
func (m *tokenMockDB) QuarantineVulnArtifact(_ context.Context, _, _ string, _ bool) error {
	return nil
}

// Storage quota methods
func (m *tokenMockDB) GetOrCreateStorageQuota(_ context.Context, _ string) (*models.StorageQuota, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateStorageQuota(_ context.Context, _ *models.StorageQuota) error { return nil }
func (m *tokenMockDB) IncrementStorageUsage(_ context.Context, _ string, _ int64, _ int) error {
	return nil
}
func (m *tokenMockDB) DecrementStorageUsage(_ context.Context, _ string, _ int64, _ int) error {
	return nil
}

// Artifact properties methods
func (m *tokenMockDB) SetArtifactProperty(_ context.Context, _, _, _, _ string) error { return nil }
func (m *tokenMockDB) GetArtifactProperty(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}
func (m *tokenMockDB) DeleteArtifactProperty(_ context.Context, _, _, _ string) error { return nil }
func (m *tokenMockDB) ListArtifactProperties(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *tokenMockDB) SearchByProperty(_ context.Context, _, _, _ string) ([]string, error) {
	return nil, nil
}

// Organization methods
func (m *tokenMockDB) CreateProject(_ context.Context, _ *models.Project) error { return nil }
func (m *tokenMockDB) GetProject(_ context.Context, _ string) (*models.Project, error) {
	return nil, nil
}
func (m *tokenMockDB) GetProjectByName(_ context.Context, _ string) (*models.Project, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateProject(_ context.Context, _ *models.Project) error { return nil }
func (m *tokenMockDB) DeleteProject(_ context.Context, _ string) error          { return nil }
func (m *tokenMockDB) ListProjects(_ context.Context, _ string) ([]*models.Project, error) {
	return nil, nil
}
func (m *tokenMockDB) AddProjectMember(_ context.Context, _, _, _ string) error { return nil }
func (m *tokenMockDB) ListProjectMembers(_ context.Context, _ string) ([]*models.ProjectMember, error) {
	return nil, nil
}
func (m *tokenMockDB) GetProjectMember(_ context.Context, _, _ string) (*models.ProjectMember, error) {
	return nil, nil
}

// System settings methods
func (m *tokenMockDB) GetSystemSetting(_ context.Context, _ string) (string, error) { return "", nil }
func (m *tokenMockDB) SetSystemSetting(_ context.Context, _, _ string) error        { return nil }

// Conda methods
func (m *tokenMockDB) CreateCondaPackage(_ context.Context, _ *models.CondaPackage) error { return nil }
func (m *tokenMockDB) GetCondaPackageFile(_ context.Context, _, _, _, _, _, _ string) (*models.CondaPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteCondaPackage(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListCondaPackages(_ context.Context, _, _ string) ([]*models.CondaPackage, error) {
	return nil, nil
}

// Conan methods
func (m *tokenMockDB) CreateConanPackage(_ context.Context, _ *models.ConanPackage) error { return nil }
func (m *tokenMockDB) GetConanPackage(_ context.Context, _ string, _ models.ConanFileKey) (*models.ConanPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) ListConanRecipeFiles(_ context.Context, _, _, _, _ string) ([]*models.ConanPackage, error) {
	return nil, nil
}
func (m *tokenMockDB) ListConanRecipes(_ context.Context, _ string) ([]models.ConanRecipe, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteConanPackage(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListConanPackages(_ context.Context, _ string) ([]*models.ConanPackage, error) {
	return nil, nil
}

// Ivy methods
func (m *tokenMockDB) CreateIvyArtifact(_ context.Context, _ *models.IvyArtifact) error { return nil }
func (m *tokenMockDB) GetIvyArtifact(_ context.Context, _, _, _, _, _, _ string) (*models.IvyArtifact, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteIvyArtifact(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListIvyArtifacts(_ context.Context, _ string) ([]*models.IvyArtifact, error) {
	return nil, nil
}

// SBT methods
func (m *tokenMockDB) CreateSBTArtifact(_ context.Context, _ *models.SBTArtifact) error { return nil }
func (m *tokenMockDB) GetSBTArtifact(_ context.Context, _, _, _, _, _ string) (*models.SBTArtifact, error) {
	return nil, nil
}
func (m *tokenMockDB) DeleteSBTArtifact(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) ListSBTArtifacts(_ context.Context, _ string) ([]*models.SBTArtifact, error) {
	return nil, nil
}

// Storage backend methods
func (m *tokenMockDB) CreateStorageBackend(_ context.Context, _ *models.StorageBackend) error {
	return nil
}
func (m *tokenMockDB) ListStorageBackends(_ context.Context) ([]*models.StorageBackend, error) {
	return nil, nil
}
func (m *tokenMockDB) GetStorageBackend(_ context.Context, _ string) (*models.StorageBackend, error) {
	return nil, nil
}
func (m *tokenMockDB) UpdateStorageBackend(_ context.Context, _ *models.StorageBackend) error {
	return nil
}
func (m *tokenMockDB) DeleteStorageBackend(_ context.Context, _ string) error { return nil }
func (m *tokenMockDB) UpdateStorageBackendStatus(_ context.Context, _, _ string, _ *string) error {
	return nil
}

func (m *tokenMockDB) UpsertAIAnalysis(_ context.Context, _ *models.AIAnalysis) error { return nil }
func (m *tokenMockDB) GetAIAnalysis(_ context.Context, _, _ string) (*models.AIAnalysis, error) {
	return nil, nil
}
func (m *tokenMockDB) ListAIAnalyses(_ context.Context, _ string, _ string, _ bool, _, _ int) ([]*models.AIAnalysis, int, error) {
	return nil, 0, nil
}
func (m *tokenMockDB) DeleteAIAnalysis(_ context.Context, _, _ string) error { return nil }
func (m *tokenMockDB) ListPendingAIAnalyses(_ context.Context, _ int) ([]*models.AIAnalysis, error) {
	return nil, nil
}
