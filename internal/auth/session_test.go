// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

type sessionMockDB struct {
	database.BaseMockDB

	sessions map[string]*models.Session
}

func newSessionMockDB() *sessionMockDB {
	return &sessionMockDB{sessions: make(map[string]*models.Session)}
}

func (m *sessionMockDB) GetDB() *sql.DB                                          { return nil }
func (m *sessionMockDB) Close() error                                            { return nil }
func (m *sessionMockDB) Ping(_ context.Context) error                            { return nil }
func (m *sessionMockDB) BeginTx(_ context.Context) (database.Transaction, error) { return nil, nil }
func (m *sessionMockDB) Migrate(_ context.Context) error                         { return nil }

func (m *sessionMockDB) CreateSession(_ context.Context, s *models.Session) (*models.Session, error) {
	if s.ID == "" {
		s.ID = fmt.Sprintf("sess-%d", len(m.sessions)+1)
	}

	m.sessions[s.TokenHash] = s

	return s, nil
}

func (m *sessionMockDB) GetSessionByTokenHash(_ context.Context, hash string) (*models.Session, error) {
	return m.sessions[hash], nil
}

func (m *sessionMockDB) DeleteSession(_ context.Context, tokenHash string) error {
	delete(m.sessions, tokenHash)
	return nil
}

func (m *sessionMockDB) UpdateSessionLastSeen(_ context.Context, sessionID string, expiresAt time.Time) error {
	for _, s := range m.sessions {
		if s.ID == sessionID {
			s.ExpiresAt = expiresAt
			s.LastSeenAt = time.Now()
		}
	}

	return nil
}

func (m *sessionMockDB) GetRepositoryByName(_ context.Context, _ string) (*models.Repository, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateRepository(_ context.Context, _ *models.Repository) error { return nil }
func (m *sessionMockDB) ListRepositories(_ context.Context, _, _ int, _ string) ([]*models.Repository, error) {
	return nil, nil
}
func (m *sessionMockDB) CountRepositories(_ context.Context, _ string) (int, error)     { return 0, nil }
func (m *sessionMockDB) UpdateRepository(_ context.Context, _ *models.Repository) error { return nil }
func (m *sessionMockDB) DeleteRepository(_ context.Context, _ string) error             { return nil }
func (m *sessionMockDB) ListComponentsByRepo(_ context.Context, _, _ string, _, _ int) ([]*models.Component, int, error) {
	return nil, 0, nil
}
func (m *sessionMockDB) GetRPMRepo(_ context.Context, _ string) (*models.RPMRepo, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateRPMRepo(_ context.Context, _ *models.RPMRepo) error { return nil }
func (m *sessionMockDB) MarkRepoStale(_ context.Context, _ string) error          { return nil }
func (m *sessionMockDB) GetComponentByID(_ context.Context, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *sessionMockDB) GetComponentByFilename(_ context.Context, _, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *sessionMockDB) GetComponentsByRepo(_ context.Context, _ string) ([]models.Component, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteComponent(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) GetRepoMetadataByPath(_ context.Context, _, _ string) (*models.RepoMetadata, error) {
	return nil, nil
}
func (m *sessionMockDB) GetRepoMetadataByRepo(_ context.Context, _ string) ([]models.RepoMetadata, error) {
	return nil, nil
}
func (m *sessionMockDB) UpsertRepoMetadata(_ context.Context, _ *models.RepoMetadata) error {
	return nil
}
func (m *sessionMockDB) CreateDebianDistribution(_ context.Context, _ *models.DebianDistribution) error {
	return nil
}
func (m *sessionMockDB) GetDebianDistributionsByRepo(_ context.Context, _ string) ([]models.DebianDistribution, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateDebianComponent(_ context.Context, _ *models.DebianComponent) error {
	return nil
}
func (m *sessionMockDB) GetDebianComponentsByDist(_ context.Context, _ string) ([]models.DebianComponent, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateDebianArchitecture(_ context.Context, _ *models.DebianArchitecture) error {
	return nil
}
func (m *sessionMockDB) GetDebianArchitecturesByDist(_ context.Context, _ string) ([]models.DebianArchitecture, error) {
	return nil, nil
}
func (m *sessionMockDB) GetDebianPackageByFilename(_ context.Context, _, _ string) (*models.Component, error) {
	return nil, nil
}
func (m *sessionMockDB) GetDebianPackagesByDistComp(_ context.Context, _, _, _, _ string) ([]models.Component, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateMavenArtifact(_ context.Context, _ *models.MavenArtifact) error {
	return nil
}
func (m *sessionMockDB) GetMavenArtifactByPath(_ context.Context, _, _ string) (*models.MavenArtifact, error) {
	return nil, nil
}
func (m *sessionMockDB) GetMavenArtifactsByGAV(_ context.Context, _, _, _, _ string) ([]*models.MavenArtifact, error) {
	return nil, nil
}
func (m *sessionMockDB) ListMavenGARefs(_ context.Context, _, _, _ string) (*models.MavenGARefs, error) {
	return nil, nil
}
func (m *sessionMockDB) GetLatestSnapshot(_ context.Context, _, _, _, _ string) (*models.MavenArtifact, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteMavenArtifact(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) CreateNPMPackage(_ context.Context, _ *models.NPMPackage) (*models.NPMPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) GetNPMPackageByName(_ context.Context, _, _ string) (*models.NPMPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteNPMPackage(_ context.Context, _ string) error   { return nil }
func (m *sessionMockDB) MarkPackumentStale(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) UpdateNPMPackageLatestVersion(_ context.Context, _, _ string) error {
	return nil
}
func (m *sessionMockDB) CreateNPMVersion(_ context.Context, _ *models.NPMVersion) (*models.NPMVersion, error) {
	return nil, nil
}
func (m *sessionMockDB) GetNPMVersion(_ context.Context, _, _ string) (*models.NPMVersion, error) {
	return nil, nil
}
func (m *sessionMockDB) GetNPMVersionsByPackage(_ context.Context, _ string) ([]*models.NPMVersion, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteNPMVersion(_ context.Context, _, _ string) error { return nil }
func (m *sessionMockDB) UpsertNPMDistTag(_ context.Context, _ *models.NPMDistTag) (*models.NPMDistTag, error) {
	return nil, nil
}
func (m *sessionMockDB) GetNPMDistTags(_ context.Context, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteNPMDistTag(_ context.Context, _, _ string) error { return nil }

func (m *sessionMockDB) CreateUser(_ context.Context, _ *models.User) (*models.User, error) {
	return nil, nil
}
func (m *sessionMockDB) GetUserByID(_ context.Context, _ string) (*models.User, error) {
	return nil, nil
}
func (m *sessionMockDB) GetUserByUsername(_ context.Context, _ string) (*models.User, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateUser(_ context.Context, _ *models.User) error { return nil }
func (m *sessionMockDB) ListUsers(_ context.Context, _, _ int) ([]*models.User, int, error) {
	return nil, 0, nil
}
func (m *sessionMockDB) CountUsers(_ context.Context) (int, error) { return 0, nil }
func (m *sessionMockDB) CreateGroup(_ context.Context, _ *models.Group) (*models.Group, error) {
	return nil, nil
}
func (m *sessionMockDB) GetGroupByID(_ context.Context, _ string) (*models.Group, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateGroup(_ context.Context, _ *models.Group) error     { return nil }
func (m *sessionMockDB) DeleteGroup(_ context.Context, _ string) error            { return nil }
func (m *sessionMockDB) ListGroups(_ context.Context) ([]*models.Group, error)    { return nil, nil }
func (m *sessionMockDB) AddUserToGroup(_ context.Context, _, _ string) error      { return nil }
func (m *sessionMockDB) RemoveUserFromGroup(_ context.Context, _, _ string) error { return nil }
func (m *sessionMockDB) ListGroupsByUser(_ context.Context, _ string) ([]*models.Group, error) {
	return nil, nil
}
func (m *sessionMockDB) ListUsersByGroup(_ context.Context, _ string) ([]*models.User, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateToken(_ context.Context, _ *models.Token) (*models.Token, error) {
	return nil, nil
}
func (m *sessionMockDB) GetTokenByHash(_ context.Context, _ string) (*models.Token, error) {
	return nil, nil
}
func (m *sessionMockDB) GetTokenByID(_ context.Context, _ string) (*models.Token, error) {
	return nil, nil
}
func (m *sessionMockDB) RevokeToken(_ context.Context, _ string) error         { return nil }
func (m *sessionMockDB) UpdateTokenLastUsed(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListTokensByUser(_ context.Context, _ string) ([]*models.Token, error) {
	return nil, nil
}
func (m *sessionMockDB) CreatePermissionTarget(_ context.Context, _ *models.PermissionTarget) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *sessionMockDB) GetPermissionTargetByID(_ context.Context, _ string) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *sessionMockDB) GetPermissionTargetByName(_ context.Context, _ string) (*models.PermissionTarget, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdatePermissionTarget(_ context.Context, _ *models.PermissionTarget) error {
	return nil
}
func (m *sessionMockDB) DeletePermissionTarget(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListPermissionTargets(_ context.Context) ([]*models.PermissionTarget, error) {
	return nil, nil
}
func (m *sessionMockDB) CreatePermissionAssignment(_ context.Context, _ *models.PermissionAssignment) error {
	return nil
}
func (m *sessionMockDB) DeletePermissionAssignment(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *sessionMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return nil, nil
}

func (m *sessionMockDB) GetOrCreatePermissionAssignment(_ context.Context, _, _, _ string) error {
	return nil
}

// Docker Registry methods
func (m *sessionMockDB) CreateDockerBlob(_ context.Context, _ *models.DockerBlob) error { return nil }
func (m *sessionMockDB) GetDockerBlob(_ context.Context, _, _ string) (*models.DockerBlob, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteDockerBlob(_ context.Context, _, _ string) error { return nil }
func (m *sessionMockDB) ListDockerBlobs(_ context.Context, _ string) ([]*models.DockerBlob, error) {
	return nil, nil
}

func (m *sessionMockDB) CreateDockerUpload(_ context.Context, _ *models.DockerUpload) error {
	return nil
}
func (m *sessionMockDB) GetDockerUpload(_ context.Context, _, _ string) (*models.DockerUpload, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateDockerUploadOffset(_ context.Context, _, _ string, _, _ int64) (bool, error) {
	return true, nil
}
func (m *sessionMockDB) DeleteDockerUpload(_ context.Context, _, _ string) error { return nil }

func (m *sessionMockDB) CreateDockerManifest(_ context.Context, _ *models.DockerManifest) error {
	return nil
}
func (m *sessionMockDB) GetDockerManifest(_ context.Context, _, _, _ string) (*models.DockerManifest, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteDockerManifest(_ context.Context, _, _, _ string) error { return nil }

func (m *sessionMockDB) UpsertDockerTag(_ context.Context, _ *models.DockerTag) error { return nil }
func (m *sessionMockDB) GetDockerTag(_ context.Context, _, _, _ string) (*models.DockerTag, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteDockerTag(_ context.Context, _, _, _ string) error          { return nil }
func (m *sessionMockDB) DeleteDockerTagsByDigest(_ context.Context, _, _, _ string) error { return nil }
func (m *sessionMockDB) ListDockerTagsByRepo(_ context.Context, _ string, _ int) ([]*models.DockerTag, error) {
	return nil, nil
}
func (m *sessionMockDB) ListDockerTags(_ context.Context, _, _, _ string, _ int) ([]*models.DockerTag, error) {
	return nil, nil
}

// Proxy/Group repository methods
func (m *sessionMockDB) AddGroupMember(_ context.Context, _, _ string, _ int) error { return nil }
func (m *sessionMockDB) ListGroupMembers(_ context.Context, _ string) ([]*models.RepositoryGroupMember, error) {
	return nil, nil
}
func (m *sessionMockDB) GetProxyCacheEntry(_ context.Context, _, _ string) (*models.ProxyCacheEntry, error) {
	return nil, nil
}
func (m *sessionMockDB) UpsertProxyCacheEntry(_ context.Context, _ *models.ProxyCacheEntry) error {
	return nil
}
func (m *sessionMockDB) DeleteProxyCacheEntry(_ context.Context, _, _ string) error { return nil }
func (m *sessionMockDB) DeleteExpiredProxyCacheEntries(_ context.Context) (int64, error) {
	return 0, nil
}

// Helm methods
func (m *sessionMockDB) CreateHelmChart(_ context.Context, _ *models.HelmChart) error { return nil }
func (m *sessionMockDB) GetHelmChart(_ context.Context, _, _, _ string) (*models.HelmChart, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteHelmChart(_ context.Context, _, _, _ string) error { return nil }
func (m *sessionMockDB) ListHelmCharts(_ context.Context, _ string) ([]*models.HelmChart, error) {
	return nil, nil
}

// PyPI methods
func (m *sessionMockDB) CreatePyPIPackage(_ context.Context, _ *models.PyPIPackage) error { return nil }
func (m *sessionMockDB) DeletePyPIPackage(_ context.Context, _, _, _, _ string) error     { return nil }
func (m *sessionMockDB) ListPyPIPackages(_ context.Context, _ string) ([]*models.PyPIPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) ListPyPIPackageVersions(_ context.Context, _, _ string) ([]*models.PyPIPackage, error) {
	return nil, nil
}

// NuGet methods
func (m *sessionMockDB) CreateNuGetPackage(_ context.Context, _ *models.NuGetPackage) error {
	return nil
}
func (m *sessionMockDB) GetNuGetPackage(_ context.Context, _, _, _ string) (*models.NuGetPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteNuGetPackage(_ context.Context, _, _, _ string) error { return nil }
func (m *sessionMockDB) ListNuGetPackages(_ context.Context, _ string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) ListNuGetPackageVersions(_ context.Context, _, _ string) ([]*models.NuGetPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) SearchNuGetPackages(_ context.Context, _, _ string, _, _ int) ([]*models.NuGetPackage, int, error) {
	return nil, 0, nil
}
func (m *sessionMockDB) IncrementNuGetDownloadCount(_ context.Context, _, _, _ string) error {
	return nil
}

// Go Module Proxy methods
func (m *sessionMockDB) CreateGoModule(_ context.Context, _ *models.GoModule) error { return nil }
func (m *sessionMockDB) GetGoModule(_ context.Context, _, _, _ string) (*models.GoModule, error) {
	return nil, nil
}
func (m *sessionMockDB) ListGoModuleVersions(_ context.Context, _, _ string) ([]*models.GoModule, error) {
	return nil, nil
}

// Cleanup policy methods
func (m *sessionMockDB) CreateCleanupPolicy(_ context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (m *sessionMockDB) GetCleanupPolicy(_ context.Context, _ string) (*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateCleanupPolicy(_ context.Context, _ *models.CleanupPolicy) error {
	return nil
}
func (m *sessionMockDB) DeleteCleanupPolicy(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListCleanupPolicies(_ context.Context, _ string) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *sessionMockDB) ListEnabledCleanupPolicies(_ context.Context) ([]*models.CleanupPolicy, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateCleanupPolicyRunResult(_ context.Context, _ string, _ int, _ error) error {
	return nil
}

// Webhook methods
func (m *sessionMockDB) CreateWebhook(_ context.Context, _ *models.Webhook) error { return nil }
func (m *sessionMockDB) GetWebhook(_ context.Context, _ string) (*models.Webhook, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateWebhook(_ context.Context, _ *models.Webhook) error { return nil }
func (m *sessionMockDB) DeleteWebhook(_ context.Context, _ string) error          { return nil }
func (m *sessionMockDB) ListWebhooks(_ context.Context, _ string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *sessionMockDB) ListWebhooksForEvent(_ context.Context, _, _ string) ([]*models.Webhook, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateWebhookDelivery(_ context.Context, _ *models.WebhookDelivery) error {
	return nil
}
func (m *sessionMockDB) UpdateWebhookDelivery(_ context.Context, _ *models.WebhookDelivery) error {
	return nil
}
func (m *sessionMockDB) ListWebhookDeliveries(_ context.Context, _ string, _ int) ([]*models.WebhookDelivery, error) {
	return nil, nil
}

// Backup methods
func (m *sessionMockDB) CreateBackupJob(_ context.Context, _ *models.BackupJob) error { return nil }
func (m *sessionMockDB) GetBackupJob(_ context.Context, _ string) (*models.BackupJob, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateBackupJob(_ context.Context, _ *models.BackupJob) error { return nil }
func (m *sessionMockDB) ListBackupJobs(_ context.Context, _ int) ([]*models.BackupJob, error) {
	return nil, nil
}

// Cargo methods
func (m *sessionMockDB) CreateCargoCrate(_ context.Context, _ *models.CargoCrate) error { return nil }
func (m *sessionMockDB) GetCargoCrate(_ context.Context, _, _, _ string) (*models.CargoCrate, error) {
	return nil, nil
}
func (m *sessionMockDB) ListCargoCrateVersions(_ context.Context, _, _ string) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *sessionMockDB) SearchCargoCrates(_ context.Context, _, _ string, _ int) ([]*models.CargoCrate, error) {
	return nil, nil
}
func (m *sessionMockDB) YankCargoCrate(_ context.Context, _, _, _ string, _ bool) error { return nil }
func (m *sessionMockDB) IncrementCargoDownloadCount(_ context.Context, _, _, _ string) error {
	return nil
}

// RubyGems methods
func (m *sessionMockDB) CreateRubyGem(_ context.Context, _ *models.RubyGem) error { return nil }
func (m *sessionMockDB) GetRubyGem(_ context.Context, _, _, _, _ string) (*models.RubyGem, error) {
	return nil, nil
}
func (m *sessionMockDB) ListRubyGems(_ context.Context, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *sessionMockDB) ListRubyGemVersions(_ context.Context, _, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *sessionMockDB) SearchRubyGems(_ context.Context, _, _ string) ([]*models.RubyGem, error) {
	return nil, nil
}
func (m *sessionMockDB) YankRubyGem(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

// Raw artifact methods
func (m *sessionMockDB) UpsertRawArtifact(_ context.Context, _ *models.RawArtifact) error { return nil }
func (m *sessionMockDB) GetRawArtifact(_ context.Context, _, _ string) (*models.RawArtifact, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteRawArtifact(_ context.Context, _, _ string) error { return nil }
func (m *sessionMockDB) ListRawArtifacts(_ context.Context, _, _ string) ([]*models.RawArtifact, error) {
	return nil, nil
}

// SBOM methods
func (m *sessionMockDB) CreateSBOMReport(_ context.Context, _ *models.SBOMReport) error { return nil }
func (m *sessionMockDB) ListSBOMReports(_ context.Context, _ string) ([]*models.SBOMReport, error) {
	return nil, nil
}

// License compliance methods
func (m *sessionMockDB) CreateLicenseScanResult(_ context.Context, _ *models.LicenseScanResult) error {
	return nil
}
func (m *sessionMockDB) GetLicenseScanResult(_ context.Context, _, _ string) (*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *sessionMockDB) ListLicenseScanResults(_ context.Context, _ string) ([]*models.LicenseScanResult, error) {
	return nil, nil
}
func (m *sessionMockDB) CreateLicensePolicy(_ context.Context, _ *models.LicensePolicy) error {
	return nil
}
func (m *sessionMockDB) GetLicensePolicy(_ context.Context, _ string) (*models.LicensePolicy, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateLicensePolicy(_ context.Context, _ *models.LicensePolicy) error {
	return nil
}
func (m *sessionMockDB) DeleteLicensePolicy(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListLicensePolicies(_ context.Context, _ string) ([]*models.LicensePolicy, error) {
	return nil, nil
}

// Secret detection methods
func (m *sessionMockDB) CreateSecretScanResult(_ context.Context, _ *models.SecretScanResult) error {
	return nil
}
func (m *sessionMockDB) GetSecretScanResult(_ context.Context, _, _ string) (*models.SecretScanResult, error) {
	return nil, nil
}
func (m *sessionMockDB) ListSecretScanResults(_ context.Context, _ string, _ bool) ([]*models.SecretScanResult, error) {
	return nil, nil
}
func (m *sessionMockDB) QuarantineArtifact(_ context.Context, _, _ string, _ bool) error { return nil }

// Vulnerability scanning methods
func (m *sessionMockDB) CreateVulnScanResult(_ context.Context, _ *models.VulnScanResult) error {
	return nil
}
func (m *sessionMockDB) GetVulnScanResult(_ context.Context, _, _ string) (*models.VulnScanResult, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateVulnScanResult(_ context.Context, _ *models.VulnScanResult) error {
	return nil
}
func (m *sessionMockDB) ListVulnScanResults(_ context.Context, _ string, _ bool) ([]*models.VulnScanResult, error) {
	return nil, nil
}
func (m *sessionMockDB) QuarantineVulnArtifact(_ context.Context, _, _ string, _ bool) error {
	return nil
}

// Storage quota methods
func (m *sessionMockDB) GetOrCreateStorageQuota(_ context.Context, _ string) (*models.StorageQuota, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateStorageQuota(_ context.Context, _ *models.StorageQuota) error {
	return nil
}
func (m *sessionMockDB) DecrementStorageUsage(_ context.Context, _ string, _ int64, _ int) error {
	return nil
}

func (m *sessionMockDB) DeleteArtifactProperty(_ context.Context, _, _, _ string) error { return nil }
func (m *sessionMockDB) ListArtifactProperties(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *sessionMockDB) SearchByProperty(_ context.Context, _, _, _ string) ([]string, error) {
	return nil, nil
}

// Organization methods
func (m *sessionMockDB) CreateProject(_ context.Context, _ *models.Project) error {
	return nil
}
func (m *sessionMockDB) GetProject(_ context.Context, _ string) (*models.Project, error) {
	return nil, nil
}
func (m *sessionMockDB) GetProjectByName(_ context.Context, _ string) (*models.Project, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateProject(_ context.Context, _ *models.Project) error {
	return nil
}
func (m *sessionMockDB) DeleteProject(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListProjects(_ context.Context, _ string) ([]*models.Project, error) {
	return nil, nil
}
func (m *sessionMockDB) AddProjectMember(_ context.Context, _, _, _ string) error { return nil }
func (m *sessionMockDB) ListProjectMembers(_ context.Context, _ string) ([]*models.ProjectMember, error) {
	return nil, nil
}
func (m *sessionMockDB) GetProjectMember(_ context.Context, _, _ string) (*models.ProjectMember, error) {
	return nil, nil
}

// System settings methods
func (m *sessionMockDB) GetSystemSetting(_ context.Context, _ string) (string, error) { return "", nil }
func (m *sessionMockDB) SetSystemSetting(_ context.Context, _, _ string) error        { return nil }

// Conda methods
func (m *sessionMockDB) CreateCondaPackage(_ context.Context, _ *models.CondaPackage) error {
	return nil
}
func (m *sessionMockDB) GetCondaPackageFile(_ context.Context, _, _, _, _, _, _ string) (*models.CondaPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteCondaPackage(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListCondaPackages(_ context.Context, _, _ string) ([]*models.CondaPackage, error) {
	return nil, nil
}

// Conan methods
func (m *sessionMockDB) CreateConanPackage(_ context.Context, _ *models.ConanPackage) error {
	return nil
}
func (m *sessionMockDB) GetConanPackage(_ context.Context, _ string, _ models.ConanFileKey) (*models.ConanPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) ListConanRecipeFiles(_ context.Context, _, _, _, _ string) ([]*models.ConanPackage, error) {
	return nil, nil
}
func (m *sessionMockDB) ListConanRecipes(_ context.Context, _ string) ([]models.ConanRecipe, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteConanPackage(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListConanPackages(_ context.Context, _ string) ([]*models.ConanPackage, error) {
	return nil, nil
}

// Ivy methods
func (m *sessionMockDB) CreateIvyArtifact(_ context.Context, _ *models.IvyArtifact) error { return nil }
func (m *sessionMockDB) GetIvyArtifact(_ context.Context, _, _, _, _, _, _ string) (*models.IvyArtifact, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteIvyArtifact(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListIvyArtifacts(_ context.Context, _ string) ([]*models.IvyArtifact, error) {
	return nil, nil
}

// SBT methods
func (m *sessionMockDB) CreateSBTArtifact(_ context.Context, _ *models.SBTArtifact) error { return nil }
func (m *sessionMockDB) GetSBTArtifact(_ context.Context, _, _, _, _, _ string) (*models.SBTArtifact, error) {
	return nil, nil
}
func (m *sessionMockDB) DeleteSBTArtifact(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) ListSBTArtifacts(_ context.Context, _ string) ([]*models.SBTArtifact, error) {
	return nil, nil
}

// Storage backend methods
func (m *sessionMockDB) CreateStorageBackend(_ context.Context, _ *models.StorageBackend) error {
	return nil
}
func (m *sessionMockDB) ListStorageBackends(_ context.Context) ([]*models.StorageBackend, error) {
	return nil, nil
}
func (m *sessionMockDB) GetStorageBackend(_ context.Context, _ string) (*models.StorageBackend, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateStorageBackend(_ context.Context, _ *models.StorageBackend) error {
	return nil
}
func (m *sessionMockDB) DeleteStorageBackend(_ context.Context, _ string) error { return nil }
func (m *sessionMockDB) UpdateStorageBackendStatus(_ context.Context, _, _ string, _ *string) error {
	return nil
}

func (m *sessionMockDB) CreateSSOProvider(_ context.Context, _ *models.SSOProvider) error {
	return nil
}
func (m *sessionMockDB) GetSSOProviderBySlug(_ context.Context, _ string) (*models.SSOProvider, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateSSOProvider(_ context.Context, _ *models.SSOProvider) error { return nil }
func (m *sessionMockDB) DeleteSSOProvider(_ context.Context, _ string) error              { return nil }
func (m *sessionMockDB) ListEnabledSSOProviders(_ context.Context) ([]*models.SSOProvider, error) {
	return nil, nil
}
func (m *sessionMockDB) ListAllSSOProviders(_ context.Context) ([]*models.SSOProvider, error) {
	return nil, nil
}
func (m *sessionMockDB) UpdateUserSSO(_ context.Context, _, _, _ string) error { return nil }
func (m *sessionMockDB) SyncSSOGroups(_ context.Context, _ string, _ []string) error {
	return nil
}
func (m *sessionMockDB) GetUserByExternalID(_ context.Context, _, _ string) (*models.User, error) {
	return nil, nil
}
func (m *sessionMockDB) GetUserByEmail(_ context.Context, _ string) (*models.User, error) {
	return nil, nil
}
func (m *sessionMockDB) GetGroupByName(_ context.Context, _ string) (*models.Group, error) {
	return nil, nil
}

func (m *sessionMockDB) GetOrCreateGroup(_ context.Context, _ string) (*models.Group, error) {
	return nil, nil
}

func (m *sessionMockDB) AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error {
	return nil
}

func (m *sessionMockDB) CreateBundlePromotion(ctx context.Context, promotion *models.BundlePromotion) error {
	return nil
}

func (m *sessionMockDB) CreatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *sessionMockDB) CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *sessionMockDB) RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error {
	return nil
}

func (m *sessionMockDB) ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *sessionMockDB) GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (m *sessionMockDB) ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error) {
	return nil, nil
}

func (m *sessionMockDB) GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *sessionMockDB) GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *sessionMockDB) UpdatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return nil
}

func (m *sessionMockDB) DeletePromotionPipeline(ctx context.Context, id string) error {
	return nil
}

func (m *sessionMockDB) ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error) {
	return nil, nil
}

func (m *sessionMockDB) GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *sessionMockDB) GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error) {
	return nil, nil
}

func (m *sessionMockDB) UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return nil
}

func (m *sessionMockDB) DeleteReleaseBundle(ctx context.Context, id string) error {
	return nil
}

func (m *sessionMockDB) ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error) {
	return nil, 0, nil
}

func (m *sessionMockDB) ArtifactExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (m *sessionMockDB) CreatePackageDelta(_ context.Context, _ *models.PackageDelta) error {
	return nil
}

func (m *sessionMockDB) GetPackageDelta(_ context.Context, _, _, _ string) (*models.PackageDelta, error) {
	return nil, nil
}

func (m *sessionMockDB) ListPackageDeltas(_ context.Context, _, _ string) ([]*models.PackageDelta, error) {
	return nil, nil
}

func (m *sessionMockDB) InsertAuditLog(_ context.Context, _ *models.AuditLog) error {
	return nil
}

func (m *sessionMockDB) ListAuditLogs(_ context.Context, _ models.AuditListParams) (*models.AuditPage, error) {
	return &models.AuditPage{}, nil
}

func (m *sessionMockDB) ListAllTokens(_ context.Context, _, _ int) ([]*models.Token, int, error) {
	return nil, 0, nil
}

func TestCreateAndValidateSession(t *testing.T) {
	db := newSessionMockDB()
	ctx := context.Background()

	cookie, sess, err := CreateSession(ctx, db, "user-1", "127.0.0.1", "Go-test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if cookie == "" {
		t.Fatal("expected non-empty cookie")
	}

	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}

	got, err := ValidateSession(ctx, db, cookie)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}

	if got == nil {
		t.Fatal("expected valid session")
	}

	if got.UserID != "user-1" {
		t.Errorf("expected UserID user-1, got %s", got.UserID)
	}
}

func TestValidateSession_InvalidCookie(t *testing.T) {
	db := newSessionMockDB()
	ctx := context.Background()

	// Use a valid hex string that does not match any session
	got, err := ValidateSession(ctx, db, "deadbeefcafebabe0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nil {
		t.Error("expected nil for unknown cookie")
	}
}

func TestValidateSession_Expired(t *testing.T) {
	db := newSessionMockDB()
	ctx := context.Background()

	old := SessionDuration

	ConfigureSessionDuration(-1 * time.Hour)
	defer ConfigureSessionDuration(old)

	cookie, _, err := CreateSession(ctx, db, "user-2", "", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := ValidateSession(ctx, db, cookie)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}

	if got != nil {
		t.Error("expected nil for expired session")
	}
}

func TestExtendSession(t *testing.T) {
	db := newSessionMockDB()
	ctx := context.Background()

	_, sess, err := CreateSession(ctx, db, "user-3", "", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	before := sess.ExpiresAt
	if err := ExtendSession(ctx, db, sess.ID); err != nil {
		t.Fatalf("ExtendSession: %v", err)
	}

	if !sess.ExpiresAt.After(before) {
		t.Error("expected ExpiresAt to be extended")
	}
}

func TestDeleteSession(t *testing.T) {
	db := newSessionMockDB()
	ctx := context.Background()

	cookie, _, err := CreateSession(ctx, db, "user-4", "", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := DeleteSession(ctx, db, cookie); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	got, err := ValidateSession(ctx, db, cookie)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}

	if got != nil {
		t.Error("expected nil after delete")
	}
}

func (m *sessionMockDB) UpsertAIAnalysis(_ context.Context, _ *models.AIAnalysis) error {
	return nil
}

func (m *sessionMockDB) GetAIAnalysis(_ context.Context, _, _ string) (*models.AIAnalysis, error) {
	return nil, nil
}

func (m *sessionMockDB) ListAIAnalyses(_ context.Context, _ string, _ string, _ bool, _, _ int) ([]*models.AIAnalysis, int, error) {
	return nil, 0, nil
}
