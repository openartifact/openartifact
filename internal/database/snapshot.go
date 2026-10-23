// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Scopes of a queued blob deletion (BlobDeletion.Scope). Any other value is the
// storage_backends id of the tier whose copy a placement flip retired.
const (
	// BlobDeletionScopeAll deletes every copy of the blob, on every tier: the
	// blob itself was deleted.
	BlobDeletionScopeAll = "*"
	// BlobDeletionScopePrimary deletes the copy on the primary (default) store.
	BlobDeletionScopePrimary = ""
)

// BlobRow is one blobs row as a backup reads it.
type BlobRow struct {
	SHA256    string
	SizeBytes int64
}

// BlobDeletion is a queued deletion of blob bytes (see EnqueueBlobDeletion).
type BlobDeletion struct {
	SHA256    string
	Scope     string
	QueuedAt  time.Time
	NotBefore time.Time
	Attempts  int
}

// BlobDeletionStore is the deferred-deletion queue the blob deletion sweeper
// works through; *PostgresDB implements it. See docs/operations/storage.md.
type BlobDeletionStore interface {
	// ClaimBlobDeletions hands out up to limit deletions that are due: past
	// their grace period and not needed by any running backup. A claimed row is
	// invisible to other callers for claimFor.
	ClaimBlobDeletions(ctx context.Context, limit int, claimFor time.Duration) ([]BlobDeletion, error)
	// BlobDeletionDue re-checks, under the per-blob lock, that the deletion is
	// still due.
	BlobDeletionDue(ctx context.Context, sha256, scope string) (bool, error)
	// DeleteBlobDeletion removes a queued deletion.
	DeleteBlobDeletion(ctx context.Context, sha256, scope string) error
	// CountBlobDeletions returns the queue length.
	CountBlobDeletions(ctx context.Context) (int64, error)
	// PurgeExpiredBackupLeases removes leases of backups that stopped renewing.
	PurgeExpiredBackupLeases(ctx context.Context) (int64, error)
}

// SnapshotReader reads the database as of one instant: every call sees the same
// committed state, however long the backup takes and whatever is written
// meanwhile. It is read-only by construction (only read methods are listed, and
// the underlying transactions are READ ONLY).
//
// A snapshot also holds a lease: while it is open, the sweeper deletes no blob
// bytes the snapshot can still see (a blob deleted, or a tier copy retired,
// after the snapshot was taken stays readable). Release ends both; a crashed
// holder's lease expires on its own.
type SnapshotReader interface {
	GetComponentsByRepo(ctx context.Context, repoID string) ([]models.Component, error)
	GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error)
	GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error)
	GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error)
	GetDebianPackagesByDistComp(ctx context.Context, repoID string, dist string, comp string, arch string) ([]models.Component, error)
	GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error)
	GetProjectQuota(ctx context.Context, projectID string) (*models.ProjectQuota, error)
	ListAIAnalyses(ctx context.Context, repoID string, status string, licenseReviewOnly bool, limit, offset int) ([]*models.AIAnalysis, int, error)
	ListAllCondaPackages(ctx context.Context, repoID string) ([]*models.CondaPackage, error)
	ListAllNPMPackages(ctx context.Context, repoID string) ([]*models.NPMPackage, error)
	ListAllProjects(ctx context.Context) ([]*models.Project, error)
	ListAllSSOProviders(ctx context.Context) ([]*models.SSOProvider, error)
	ListAllTokens(ctx context.Context, page, perPage int) ([]*models.Token, int, error)
	ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error)
	ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error)
	ListCleanupPolicies(ctx context.Context, repoID string) ([]*models.CleanupPolicy, error)
	ListConanPackages(ctx context.Context, repoID string) ([]*models.ConanPackage, error)
	ListDockerBlobs(ctx context.Context, repoID string) ([]*models.DockerBlob, error)
	ListDockerManifestsByRepo(ctx context.Context, repoID string) ([]*models.DockerManifest, error)
	ListDockerTagsByRepo(ctx context.Context, repoID string, limit int) ([]*models.DockerTag, error)
	ListGoModulesByRepo(ctx context.Context, repoID string) ([]*models.GoModule, error)
	ListGroupMembers(ctx context.Context, groupRepoID string) ([]*models.RepositoryGroupMember, error)
	ListGroups(ctx context.Context) ([]*models.Group, error)
	ListHelmCharts(ctx context.Context, repoID string) ([]*models.HelmChart, error)
	ListIvyArtifacts(ctx context.Context, repoID string) ([]*models.IvyArtifact, error)
	ListLicenseScanResults(ctx context.Context, repoID string) ([]*models.LicenseScanResult, error)
	ListMavenArtifactsByRepo(ctx context.Context, repoID string) ([]*models.MavenArtifact, error)
	ListNPMVersionsByRepo(ctx context.Context, repoID string) ([]*models.NPMVersion, error)
	ListNuGetPackages(ctx context.Context, repoID string) ([]*models.NuGetPackage, error)
	ListPermissionAssignments(ctx context.Context) ([]*models.PermissionAssignment, error)
	ListPermissionTargets(ctx context.Context) ([]*models.PermissionTarget, error)
	ListProjectCleanupPolicies(ctx context.Context, projectID string) ([]*models.CleanupPolicy, error)
	ListProjectGroups(ctx context.Context, projectID string) ([]*models.ProjectGroup, error)
	ListProjectMembers(ctx context.Context, projectID string) ([]*models.ProjectMember, error)
	ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error)
	ListPyPIPackages(ctx context.Context, repoID string) ([]*models.PyPIPackage, error)
	ListRawArtifacts(ctx context.Context, repoID, prefix string) ([]*models.RawArtifact, error)
	ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error)
	ListRepositories(ctx context.Context, page, limit int, formatFilter string) ([]*models.Repository, error)
	ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error)
	ListSBOMReports(ctx context.Context, repoID string) ([]*models.SBOMReport, error)
	ListVEXStatements(ctx context.Context, repoID, artifactID string) ([]*models.VEXStatement, error)
	ListSBTArtifacts(ctx context.Context, repoID string) ([]*models.SBTArtifact, error)
	ListSecretScanResults(ctx context.Context, repoID string, flaggedOnly bool) ([]*models.SecretScanResult, error)
	ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error)
	ListStorageQuotas(ctx context.Context) ([]*models.StorageQuota, error)
	ListUserGroupMemberships(ctx context.Context) ([]*models.UserGroup, error)
	ListUsers(ctx context.Context, page, perPage int) ([]*models.User, int, error)
	ListVulnScanResults(ctx context.Context, repoID string, vulnerableOnly bool) ([]*models.VulnScanResult, error)
	ListWebhooks(ctx context.Context, repoID string) ([]*models.Webhook, error) // repoID="" = global
	SearchCargoCrates(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrate, error)

	// ListBlobsAfter returns up to limit stored objects with sha256 > after, in
	// sha256 order: blobs rows plus the generated metadata and deltas that
	// have no blobs row.
	ListBlobsAfter(ctx context.Context, after string, limit int) ([]BlobRow, error)
	// CountBlobs returns the number of objects ListBlobsAfter pages through.
	CountBlobs(ctx context.Context) (int64, error)

	// Release ends the snapshot and removes its lease. It is idempotent.
	Release()
}
