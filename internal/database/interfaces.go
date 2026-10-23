// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// ErrDuplicate is returned by Create*Package when an insert hits the unique
// constraint (ON CONFLICT), so callers can distinguish an idempotent duplicate
// upload (map to HTTP 409) from a real failure. This closes the check-then-insert
// TOCTOU: concurrent identical uploads race the existence check, but exactly one
// INSERT wins and the losers observe ErrDuplicate.
var ErrDuplicate = errors.New("artifact already exists")

// ErrProjectRepoLimit is returned when creating or moving a repository would
// exceed the project's max_repositories.
var ErrProjectRepoLimit = errors.New("project repository limit reached")

// ErrRepositoryNotFound is returned when a repository to modify does not exist (or is trashed).
var ErrRepositoryNotFound = errors.New("repository not found")

// Database defines the interface for database operations.
type Database interface {
	// GetDB returns the underlying sql.DB connection
	GetDB() *sql.DB

	// Close closes the database connection
	Close() error

	// Ping checks if the database is accessible
	Ping(ctx context.Context) error

	// BeginTx starts a new transaction
	BeginTx(ctx context.Context) (Transaction, error)

	// Migrate runs database migrations
	Migrate(ctx context.Context) error

	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error)
	CreateRepository(ctx context.Context, repo *models.Repository) error
	// ListPublicRepositoryNames returns the names of all repositories flagged public.
	ListPublicRepositoryNames(ctx context.Context) ([]string, error)
	ListRepositories(ctx context.Context, page, limit int, formatFilter string) ([]*models.Repository, error)
	CountRepositories(ctx context.Context, formatFilter string) (int, error)
	UpdateRepository(ctx context.Context, repo *models.Repository) error
	// UpdateRepositoryAndMembers is UpdateRepository plus ReplaceGroupMembers in
	// one transaction.
	UpdateRepositoryAndMembers(ctx context.Context, repo *models.Repository, memberIDs []string) error
	DeleteRepository(ctx context.Context, id string) error
	ListComponentsByRepo(ctx context.Context, repoID, formatFilter string, page, limit int) ([]*models.Component, int, error)

	GetRPMRepo(ctx context.Context, repositoryID string) (*models.RPMRepo, error)
	CreateRPMRepo(ctx context.Context, repo *models.RPMRepo) error
	MarkRepoStale(ctx context.Context, repositoryID string) error

	CreateComponent(ctx context.Context, component *models.Component) error
	CreateDebPackage(ctx context.Context, pkg *models.Component, distID, compID string) error
	CreateRpmPackage(ctx context.Context, pkg *models.Component) error
	UpsertBlob(ctx context.Context, sha256, blobKey string, sizeBytes int64) (string, error)
	GetComponentByID(ctx context.Context, id string) (*models.Component, error)
	// GetRepoComponentBlob resolves a package-listing row id (any format) to its
	// blob, scoped to repoID. Returns nil, nil when the id is not in that repository.
	GetRepoComponentBlob(ctx context.Context, repoID, id string) (*models.ComponentBlob, error)
	GetComponentByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error)
	GetComponentsByRepo(ctx context.Context, repoID string) ([]models.Component, error)
	// GetComponentBySHA256 returns the live rpm or deb package in a repository whose blob has the given sha256, or nil.
	GetComponentBySHA256(ctx context.Context, repoID, sha256 string) (*models.Component, error)
	DeleteComponent(ctx context.Context, componentID string) error

	GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error)
	GetRepoMetadataByRepo(ctx context.Context, repositoryID string) ([]models.RepoMetadata, error)
	UpsertRepoMetadata(ctx context.Context, meta *models.RepoMetadata) error

	CreateDebianDistribution(ctx context.Context, dist *models.DebianDistribution) error
	GetDebianDistribution(ctx context.Context, repoID string, name string) (*models.DebianDistribution, error)
	GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error)
	CreateDebianComponent(ctx context.Context, comp *models.DebianComponent) error
	GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error)
	CreateDebianArchitecture(ctx context.Context, arch *models.DebianArchitecture) error
	GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error)
	GetDebianPackageByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error)
	GetDebianPackagesByDistComp(ctx context.Context, repoID string, dist string, comp string, arch string) ([]models.Component, error)
	GetDebPackageByID(ctx context.Context, id string) (*models.DebPackage, error)
	ListDebPackagesByRepo(ctx context.Context, repoID string) ([]*models.DebPackage, error)
	GetOrCreateDebianDistribution(ctx context.Context, repoID, name string) (*models.DebianDistribution, error)
	GetOrCreateDebianComponent(ctx context.Context, distID, name string) (*models.DebianComponent, error)
	GetOrCreateDebianArchitecture(ctx context.Context, distID, name string) (*models.DebianArchitecture, error)
	SetDebControlFields(ctx context.Context, packageID, maintainer, homepage string, fields map[string]string) error
	GetDebBlobInfo(ctx context.Context, blobID string) (string, int64, error)
	ListDebPackageEntries(ctx context.Context, repoID, dist, comp, arch string) ([]models.DebPackageEntry, error)
	GetRpmPackageByID(ctx context.Context, id string) (*models.RpmPackage, error)
	ListRpmPackagesByRepo(ctx context.Context, repoID string) ([]*models.RpmPackage, error)
	// RpmPackageExists reports whether the repo already has a package with the given NEVRA.
	RpmPackageExists(ctx context.Context, repoID, name, version, release string, epoch int, arch string) (bool, error)
	// ListRpmPackagesForMetadata returns every rpm package of a repo with
	// dependencies, files, changelogs and header range loaded.
	ListRpmPackagesForMetadata(ctx context.Context, repoID string) ([]pkgrpm.Package, error)
	// UpdateRpmPackageExtra replaces rpm_extra for the repo's package(s) whose
	// blob has the given sha256 (used to backfill weak dependencies).
	UpdateRpmPackageExtra(ctx context.Context, repoID, sha256 string, extra []byte) error

	CreateMavenArtifact(ctx context.Context, a *models.MavenArtifact) error
	GetMavenArtifactByPath(ctx context.Context, repoID, path string) (*models.MavenArtifact, error)
	GetMavenArtifactsByGAV(ctx context.Context, repoID, groupID, artifactID, version string) ([]*models.MavenArtifact, error)
	GetMavenArtifactsByGA(ctx context.Context, repoID, groupID, artifactID string) ([]*models.MavenArtifact, error)
	GetLatestSnapshot(ctx context.Context, repoID, groupID, artifactID, version string) (*models.MavenArtifact, error)
	// GetLatestSnapshotFile returns the newest snapshot build of the file with
	// the given classifier and extension.
	GetLatestSnapshotFile(ctx context.Context, repoID, groupID, artifactID, version, classifier, extension string) (*models.MavenArtifact, error)
	DeleteMavenArtifact(ctx context.Context, id string) error

	// ListMavenArtifactsByRepo is a bulk-list-by-repo method used only by
	// backup export (no GA/path filter required).
	ListMavenArtifactsByRepo(ctx context.Context, repoID string) ([]*models.MavenArtifact, error)

	CreateNPMPackage(ctx context.Context, p *models.NPMPackage) (*models.NPMPackage, error)
	GetNPMPackageByName(ctx context.Context, repoID, fullName string) (*models.NPMPackage, error)
	DeleteNPMPackage(ctx context.Context, packageID string) error
	MarkPackumentStale(ctx context.Context, packageID string) error
	UpdateNPMPackageLatestVersion(ctx context.Context, packageID, latestVersion string) error
	CreateNPMVersion(ctx context.Context, v *models.NPMVersion) (*models.NPMVersion, error)
	GetNPMVersion(ctx context.Context, packageID, version string) (*models.NPMVersion, error)
	GetNPMVersionsByPackage(ctx context.Context, packageID string) ([]*models.NPMVersion, error)
	// GetNPMVersionStates returns number and deprecation of every live version
	// of the package, without the manifests.
	GetNPMVersionStates(ctx context.Context, packageID string) ([]models.NPMVersionState, error)
	// UpdateNPMVersionManifest replaces the stored manifest of a live version
	// (used for `npm deprecate`).
	UpdateNPMVersionManifest(ctx context.Context, packageID, version string, manifest []byte) error
	DeleteNPMVersion(ctx context.Context, packageID, version string) error
	UpsertNPMDistTag(ctx context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error)
	GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error)
	DeleteNPMDistTag(ctx context.Context, packageID, tag string) error

	// ListAllNPMPackages/ListNPMVersionsByRepo are bulk-list-by-repo methods
	// used only by backup export (no name/GA/platform filter required).
	ListAllNPMPackages(ctx context.Context, repoID string) ([]*models.NPMPackage, error)
	ListNPMVersionsByRepo(ctx context.Context, repoID string) ([]*models.NPMVersion, error)

	// Docker Registry methods
	CreateDockerBlob(ctx context.Context, blob *models.DockerBlob) error
	GetDockerBlob(ctx context.Context, repoID, digest string) (*models.DockerBlob, error)
	DeleteDockerBlob(ctx context.Context, repoID, digest string) error
	ListDockerBlobs(ctx context.Context, repoID string) ([]*models.DockerBlob, error)

	CreateDockerUpload(ctx context.Context, upload *models.DockerUpload) error
	GetDockerUpload(ctx context.Context, repositoryID, uploadUUID string) (*models.DockerUpload, error)
	// UpdateDockerUploadOffset is a compare-and-set: it moves the offset from
	// oldOffset to newOffset and reports false when it no longer equals oldOffset.
	UpdateDockerUploadOffset(ctx context.Context, repositoryID, uploadUUID string, oldOffset, newOffset int64) (bool, error)
	DeleteDockerUpload(ctx context.Context, repositoryID, uploadUUID string) error

	CreateDockerManifest(ctx context.Context, m *models.DockerManifest) error
	GetDockerManifest(ctx context.Context, repoID, image, digest string) (*models.DockerManifest, error)
	DeleteDockerManifest(ctx context.Context, repoID, image, digest string) error

	// ListDockerManifestsByRepo is a bulk-list-by-repo method used only by
	// backup export (no digest/subject filter required).
	ListDockerManifestsByRepo(ctx context.Context, repoID string) ([]*models.DockerManifest, error)

	UpsertDockerTag(ctx context.Context, tag *models.DockerTag) error
	GetDockerTag(ctx context.Context, repoID, image, name string) (*models.DockerTag, error)
	DeleteDockerTag(ctx context.Context, repoID, image, name string) error
	// DeleteDockerTagsByDigest removes every tag of the image that points at digest.
	DeleteDockerTagsByDigest(ctx context.Context, repoID, image, digest string) error
	ListDockerTags(ctx context.Context, repoID, image, last string, n int) ([]*models.DockerTag, error)
	// ListDockerTagsByRepo lists up to limit tags across every image of the repository (backup export).
	ListDockerTagsByRepo(ctx context.Context, repoID string, limit int) ([]*models.DockerTag, error)

	// EnsureBlob ensures the global blobs table has a row for sha256
	// (creating it, or updating its media type, if needed) and returns its
	// blobs.id. Used by manifest handlers that need a blob_id FK without an
	// accompanying per-format registry row (unlike UpsertBlob's callers).
	EnsureBlob(ctx context.Context, sha256, mediaType string, size int64) (string, error)

	// ListDockerManifestsBySubject returns manifests in repoID whose OCI
	// `subject` field points at subjectDigest, for the Referrers API.
	ListDockerManifestsBySubject(ctx context.Context, repoID, image, subjectDigest string) ([]*models.DockerManifest, error)

	// Proxy/Group repository methods
	AddGroupMember(ctx context.Context, groupRepoID, memberRepoID string, priority int) error
	RemoveGroupMember(ctx context.Context, groupRepoID, memberRepoID string) error
	ListGroupMembers(ctx context.Context, groupRepoID string) ([]*models.RepositoryGroupMember, error)
	// ListGroupMemberRepositories returns the live member repositories of a
	// group in priority order (the order of ListGroupMembers) in one query.
	ListGroupMemberRepositories(ctx context.Context, groupRepoID string) ([]*models.Repository, error)
	// ListGroupsOfMember returns the live group repositories the repository is
	// a member of, oldest link first.
	ListGroupsOfMember(ctx context.Context, memberRepoID string) ([]*models.Repository, error)
	// Deb/rpm group metadata (docs/repositories/hosted-proxy-group.md).
	// MarkGroupMetadataStale flags a group's merged metadata for regeneration.
	MarkGroupMetadataStale(ctx context.Context, groupID string) error
	// GetGroupMetadataState returns a group's regeneration state, nil when it has none.
	GetGroupMetadataState(ctx context.Context, groupID string) (*models.GroupMetadataState, error)
	// ListMetadataGroupsOfMember returns the deb/rpm groups that list the repository as a member.
	ListMetadataGroupsOfMember(ctx context.Context, memberRepoID string) ([]string, error)
	// ListStaleMetadataGroups returns the deb/rpm groups that are stale or were never generated.
	ListStaleMetadataGroups(ctx context.Context) ([]string, error)
	// GetGroupPackageOwner returns the member owning a file the group's merged index lists.
	GetGroupPackageOwner(ctx context.Context, groupID, path string) (string, bool, error)
	// ReplaceGroupMembers atomically replaces a group's ordered member list (empty clears it).
	ReplaceGroupMembers(ctx context.Context, groupRepoID string, memberIDs []string) error
	// CreateRepositoryWithMembers creates a repository and its group members in one transaction.
	CreateRepositoryWithMembers(ctx context.Context, repo *models.Repository, memberIDs []string) error
	// CreateProjectRepository is CreateRepositoryWithMembers under the project's
	// max_repositories cap (ErrProjectRepoLimit), checked with the project row locked.
	CreateProjectRepository(ctx context.Context, repo *models.Repository, memberIDs []string, maxRepositories int) error
	// MoveRepositoryToProject reassigns a live repository to another project
	// under the target's cap when maxRepositories > 0 (ErrProjectRepoLimit).
	MoveRepositoryToProject(ctx context.Context, repoID, toProjectID string, maxRepositories int) error
	GetProxyCacheEntry(ctx context.Context, repoID, cacheKey string) (*models.ProxyCacheEntry, error)
	UpsertProxyCacheEntry(ctx context.Context, entry *models.ProxyCacheEntry) error
	DeleteProxyCacheEntry(ctx context.Context, repoID, cacheKey string) error
	DeleteExpiredProxyCacheEntries(ctx context.Context) (int64, error)

	// Helm methods
	CreateHelmChart(ctx context.Context, chart *models.HelmChart) error
	GetHelmChart(ctx context.Context, repoID, name, version string) (*models.HelmChart, error)
	DeleteHelmChart(ctx context.Context, repoID, name, version string) error
	ListHelmCharts(ctx context.Context, repoID string) ([]*models.HelmChart, error)
	// HelmIndexStamp is a cheap validator of a repository's index.yaml: it
	// changes exactly when a live chart is added or removed.
	HelmIndexStamp(ctx context.Context, repoID string) (string, error)
	ListHelmChartVersions(ctx context.Context, repoID, name string) ([]*models.HelmChart, error)

	// PyPI methods
	CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error
	GetPyPIPackage(ctx context.Context, repoID, name, version, filename string) (*models.PyPIPackage, error)
	// GetPyPIPackageByFilename returns the live file with this filename, nil
	// when there is none and ErrAmbiguousPyPIFilename when several packages of
	// the repository hold a file of that name.
	GetPyPIPackageByFilename(ctx context.Context, repoID, filename string) (*models.PyPIPackage, error)
	DeletePyPIPackage(ctx context.Context, repoID, name, version, filename string) error
	ListPyPIPackages(ctx context.Context, repoID string) ([]*models.PyPIPackage, error)
	// ListPyPIPackageNames returns the distinct stored project names of a
	// repository, sorted, without loading any file row.
	ListPyPIPackageNames(ctx context.Context, repoID string) ([]string, error)
	ListPyPIPackageVersions(ctx context.Context, repoID, name string) ([]*models.PyPIPackage, error)

	// NuGet methods
	CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error
	GetNuGetPackage(ctx context.Context, repoID, packageID, version string) (*models.NuGetPackage, error)
	DeleteNuGetPackage(ctx context.Context, repoID, packageID, version string) error
	// SetNuGetDependencyGroups records the dependency groups JSON of a package
	// that has none stored yet (published before they were persisted).
	SetNuGetDependencyGroups(ctx context.Context, repoID, packageID, version string, groups []byte) error
	ListNuGetPackages(ctx context.Context, repoID string) ([]*models.NuGetPackage, error)
	ListNuGetPackageVersions(ctx context.Context, repoID, packageID string) ([]*models.NuGetPackage, error)
	// SearchNuGetPackages pages over matching packages: skip/take count
	// packages, the total is the number of matching packages, and every live
	// version of each package on the page is returned.
	SearchNuGetPackages(ctx context.Context, repoID, query string, skip, take int) ([]*models.NuGetPackage, int, error)
	IncrementNuGetDownloadCount(ctx context.Context, repoID, packageID, version string) error

	CreateUser(ctx context.Context, user *models.User) (*models.User, error)
	GetUserByID(ctx context.Context, id string) (*models.User, error)
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)
	UpdateUser(ctx context.Context, user *models.User) error
	// UpdateUserLastLogin sets only last_login_at, so a login finishing from a
	// stale snapshot cannot undo a concurrent disable or password reset.
	UpdateUserLastLogin(ctx context.Context, id string, at time.Time) error
	// UpdateUserSSOProfile sets only email (when non-nil), full_name (when
	// non-nil) and last_login_at, so an SSO login finishing from a stale
	// snapshot cannot undo a concurrent disable or password reset.
	UpdateUserSSOProfile(ctx context.Context, id string, email, fullName *string, lastLogin time.Time) error
	// UpdateUserPassword sets only password_hash and must_change_password.
	UpdateUserPassword(ctx context.Context, id, passwordHash string, mustChange bool) error
	ListUsers(ctx context.Context, page, perPage int) ([]*models.User, int, error)
	CountUsers(ctx context.Context) (int, error)
	// CreateBootstrapAdmin atomically (advisory-locked transaction) creates the first
	// admin user and binds it to the admin-all permission target. It reports false
	// without creating anything when another replica already created a user.
	CreateBootstrapAdmin(ctx context.Context, username, passwordHash string) (bool, error)
	// RepairBootstrapAdmin re-binds the bootstrap admin to admin-all when no principal
	// holds that target (a previously interrupted bootstrap). Reports whether it repaired.
	RepairBootstrapAdmin(ctx context.Context, username string) (bool, error)

	CreateGroup(ctx context.Context, group *models.Group) (*models.Group, error)
	GetGroupByID(ctx context.Context, id string) (*models.Group, error)
	UpdateGroup(ctx context.Context, group *models.Group) error
	DeleteGroup(ctx context.Context, id string) error
	ListGroups(ctx context.Context) ([]*models.Group, error)
	AddUserToGroup(ctx context.Context, userID, groupID string) error
	RemoveUserFromGroup(ctx context.Context, userID, groupID string) error
	ListGroupsByUser(ctx context.Context, userID string) ([]*models.Group, error)
	ListUsersByGroup(ctx context.Context, groupID string) ([]*models.User, error)

	CreateSession(ctx context.Context, session *models.Session) (*models.Session, error)
	GetSessionByTokenHash(ctx context.Context, hash string) (*models.Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	UpdateSessionLastSeen(ctx context.Context, sessionID string, expiresAt time.Time) error
	// ConsumeSAMLAssertion records a SAML assertion ID as used; false means it was
	// already consumed (replay).
	ConsumeSAMLAssertion(ctx context.Context, providerSlug, assertionID string, expiresAt time.Time) (bool, error)
	// DeleteSessionsByUser revokes every session of a user (password change/reset, disable).
	DeleteSessionsByUser(ctx context.Context, userID string) error
	// DeleteOtherSessionsByUser revokes every session of a user except the one whose
	// token hash is keepTokenHash (used on self-service password change).
	DeleteOtherSessionsByUser(ctx context.Context, userID, keepTokenHash string) error

	CreateToken(ctx context.Context, token *models.Token) (*models.Token, error)
	GetTokenByHash(ctx context.Context, hash string) (*models.Token, error)
	GetTokenByID(ctx context.Context, tokenID string) (*models.Token, error)
	RevokeToken(ctx context.Context, tokenID string) error
	UpdateTokenLastUsed(ctx context.Context, tokenID string) error
	ListTokensByUser(ctx context.Context, userID string) ([]*models.Token, error)

	CreatePermissionTarget(ctx context.Context, target *models.PermissionTarget) (*models.PermissionTarget, error)
	GetPermissionTargetByID(ctx context.Context, id string) (*models.PermissionTarget, error)
	GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error)
	UpdatePermissionTarget(ctx context.Context, target *models.PermissionTarget) error
	DeletePermissionTarget(ctx context.Context, id string) error
	ListPermissionTargets(ctx context.Context) ([]*models.PermissionTarget, error)
	// ListPermissionAssignments returns every permission assignment (backup export).
	ListPermissionAssignments(ctx context.Context) ([]*models.PermissionAssignment, error)
	// ListUserGroupMemberships returns every user-to-group membership (backup export).
	ListUserGroupMemberships(ctx context.Context) ([]*models.UserGroup, error)
	CreatePermissionAssignment(ctx context.Context, assignment *models.PermissionAssignment) error
	DeletePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error
	GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error)
	GetOrCreatePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error
	SyncSSOPermissionsForGroup(ctx context.Context, groupID string, targetIDs []string) error

	CreateSSOProvider(ctx context.Context, provider *models.SSOProvider) error
	GetSSOProviderBySlug(ctx context.Context, slug string) (*models.SSOProvider, error)
	GetSSOProviderByID(ctx context.Context, id string) (*models.SSOProvider, error)
	UpdateSSOProvider(ctx context.Context, provider *models.SSOProvider) error
	DeleteSSOProvider(ctx context.Context, id string) error
	ListEnabledSSOProviders(ctx context.Context) ([]*models.SSOProvider, error)
	ListAllSSOProviders(ctx context.Context) ([]*models.SSOProvider, error)
	UpdateUserSSO(ctx context.Context, userID string, externalID, providerSlug string) error
	SyncSSOGroups(ctx context.Context, userID string, groupIDs []string) error
	GetUserByExternalID(ctx context.Context, externalID, providerSlug string) (*models.User, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	GetGroupByName(ctx context.Context, name string) (*models.Group, error)
	GetOrCreateGroup(ctx context.Context, name string) (*models.Group, error)

	ArtifactExists(ctx context.Context, sha256 string, repositoryID string) (bool, error)

	// ArtifactExistsBySHA256 checks whether a blob with the given SHA-256 hash exists
	// in ANY repository. Returns the repository ID of the first match and true if found.
	// Use this for cross-repository deduplication: if the blob is already stored,
	// the upload pipeline can skip re-uploading it to the blob store.
	ArtifactExistsBySHA256(ctx context.Context, sha256 string) (repositoryID string, exists bool, err error)

	// BlobReferenceCount returns the number of package-table rows referencing
	// the blob identified by sha256, across every format table that stores a
	// blob_id FK into the global blobs table. 0 means the blob is a GC
	// candidate. Use SafeDeleteBlob (internal/storage/refcount.go) instead of
	// calling blobStore.Delete directly.
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)

	// BeginBackupSnapshot freezes the database at one instant for a backup and
	// registers a lease that keeps the blob bytes that instant references from
	// being deleted until Release. The lease is renewed in the background and
	// expires leaseTTL after the last renewal, so a crashed backup does not
	// hold deletions back forever. See SnapshotReader.
	BeginBackupSnapshot(ctx context.Context, leaseTTL time.Duration) (SnapshotReader, error)

	// Go Module Proxy methods
	CreateGoModule(ctx context.Context, mod *models.GoModule) error
	GetGoModule(ctx context.Context, repoID, modulePath, version string) (*models.GoModule, error)
	DeleteGoModule(ctx context.Context, repoID, modulePath, version string) error
	ListGoModuleVersions(ctx context.Context, repoID, modulePath string) ([]*models.GoModule, error)

	// ListGoModulesByRepo is a bulk-list-by-repo method used only by backup
	// export (no module path filter required).
	ListGoModulesByRepo(ctx context.Context, repoID string) ([]*models.GoModule, error)

	InsertAuditLog(ctx context.Context, event *models.AuditLog) error
	ListAuditLogs(ctx context.Context, params models.AuditListParams) (*models.AuditPage, error)
	ListAllTokens(ctx context.Context, page, perPage int) ([]*models.Token, int, error)

	// Cleanup policy methods
	CreateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error
	GetCleanupPolicy(ctx context.Context, id string) (*models.CleanupPolicy, error)
	UpdateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error
	DeleteCleanupPolicy(ctx context.Context, id string) error
	ListCleanupPolicies(ctx context.Context, repoID string) ([]*models.CleanupPolicy, error)
	ListEnabledCleanupPolicies(ctx context.Context) ([]*models.CleanupPolicy, error)
	// ListProjectCleanupPolicies lists a project's default policies (RepositoryID "").
	ListProjectCleanupPolicies(ctx context.Context, projectID string) ([]*models.CleanupPolicy, error)
	// ListProjectCleanupRepositories lists the live hosted repositories a project
	// default policy applies to: those without an enabled repository-level policy.
	ListProjectCleanupRepositories(ctx context.Context, projectID string) ([]string, error)
	UpdateCleanupPolicyRunResult(ctx context.Context, id string, deletedCount int, runErr error) error

	// Webhook methods
	CreateWebhook(ctx context.Context, wh *models.Webhook) error
	GetWebhook(ctx context.Context, id string) (*models.Webhook, error)
	UpdateWebhook(ctx context.Context, wh *models.Webhook) error
	DeleteWebhook(ctx context.Context, id string) error
	ListWebhooks(ctx context.Context, repoID string) ([]*models.Webhook, error) // repoID="" = global
	ListWebhooksForEvent(ctx context.Context, repoID, eventType string) ([]*models.Webhook, error)
	CreateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error
	UpdateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error
	ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]*models.WebhookDelivery, error)
	// ClaimPendingWebhookDeliveries atomically leases up to limit due deliveries
	// (FOR UPDATE SKIP LOCKED) so several server instances never share a row.
	ClaimPendingWebhookDeliveries(ctx context.Context, limit int, lease time.Duration) ([]*models.WebhookDelivery, error)

	// Backup methods
	CreateBackupJob(ctx context.Context, job *models.BackupJob) error
	GetBackupJob(ctx context.Context, id string) (*models.BackupJob, error)
	UpdateBackupJob(ctx context.Context, job *models.BackupJob) error
	ListBackupJobs(ctx context.Context, limit int) ([]*models.BackupJob, error)

	// Cargo methods
	CreateCargoCrate(ctx context.Context, crate *models.CargoCrate) error
	GetCargoCrate(ctx context.Context, repoID, name, version string) (*models.CargoCrate, error)
	DeleteCargoCrate(ctx context.Context, id string) error
	ListCargoCrateVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error)
	// CargoIndexStamp is a cheap validator of one crate's sparse index entry:
	// it changes when a live version is added, removed or (un)yanked.
	CargoIndexStamp(ctx context.Context, repoID, name string) (string, error)
	// ListCargoIndexVersions returns the live versions of a crate with only the
	// columns the sparse index needs (Name, Version, MetadataJSON, Checksum,
	// Yanked), oldest first.
	ListCargoIndexVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error)
	SearchCargoCrates(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrate, error)
	// SearchCargoCrateSummaries matches crates by name or description and
	// returns one summary per crate (at most limit, by name) together with the
	// total number of matching crates.
	SearchCargoCrateSummaries(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrateSummary, int, error)
	YankCargoCrate(ctx context.Context, repoID, name, version string, yanked bool) error
	IncrementCargoDownloadCount(ctx context.Context, repoID, name, version string) error

	// RubyGems methods
	CreateRubyGem(ctx context.Context, gem *models.RubyGem) error
	GetRubyGem(ctx context.Context, repoID, name, version, platform string) (*models.RubyGem, error)
	DeleteRubyGem(ctx context.Context, id string) error
	ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error)
	// RubyGemsSpecsStamp is a cheap validator of every specs index of a
	// repository: it changes when a listed gem is added, removed or yanked.
	RubyGemsSpecsStamp(ctx context.Context, repoID string) (string, error)
	// ListRubyGemSpecs returns the listed (not yanked) gems with only Name,
	// Version and Platform set, by name then version descending.
	ListRubyGemSpecs(ctx context.Context, repoID string) ([]*models.RubyGem, error)
	ListRubyGemVersions(ctx context.Context, repoID, name string) ([]*models.RubyGem, error)
	SearchRubyGems(ctx context.Context, repoID, query string) ([]*models.RubyGem, error)
	YankRubyGem(ctx context.Context, repoID, name, version, platform string, yanked bool) error

	// Raw artifact methods
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
	GetRawArtifact(ctx context.Context, repoID, path string) (*models.RawArtifact, error)
	DeleteRawArtifact(ctx context.Context, repoID, path string) error
	ListRawArtifacts(ctx context.Context, repoID, prefix string) ([]*models.RawArtifact, error)

	// Presigned uploads (staging rows awaiting their complete call).
	// CreatePresignedUpload fills ID and CreatedAt. GetPresignedUpload returns
	// nil, nil when the id is unknown or belongs to another repository.
	CreatePresignedUpload(ctx context.Context, u *models.PresignedUpload) error
	GetPresignedUpload(ctx context.Context, repoID, id string) (*models.PresignedUpload, error)
	DeletePresignedUpload(ctx context.Context, id string) error
	// ClaimPresignedUpload takes the complete lease of an upload for up to
	// lease. It reports false when the upload is already completed or another
	// complete holds an unexpired lease.
	ClaimPresignedUpload(ctx context.Context, repoID, id string, lease time.Duration) (bool, error)
	// ReleasePresignedUpload gives up the lease of an upload that was not completed.
	ReleasePresignedUpload(ctx context.Context, id string) error
	// MarkPresignedUploadCompleted records that the upload produced artifactID.
	MarkPresignedUploadCompleted(ctx context.Context, id, artifactID string) error
	// CreatePresignedUploadCapped is CreatePresignedUpload that fails with
	// ErrPresignedUploadCap when u.CreatedBy already has maxPending uploads
	// that are neither completed nor expired (maxPending <= 0: no cap). The
	// count and the insert are serialized per user, so parallel creates cannot
	// overshoot the cap.
	CreatePresignedUploadCapped(ctx context.Context, u *models.PresignedUpload, maxPending int) error
	// ExistingPresignedUploadIDs returns the subset of ids that have a row.
	ExistingPresignedUploadIDs(ctx context.Context, ids []string) ([]string, error)
	// SetPresignedUploadMultipartID records the backend handle of the upload's
	// open multipart upload.
	SetPresignedUploadMultipartID(ctx context.Context, id, multipartID string) error
	// ExtendPresignedUploadExpiry moves the complete deadline of a pending
	// upload forward to expiresAt (it never shortens it), so an upload that is
	// still receiving parts does not expire under the client.
	ExtendPresignedUploadExpiry(ctx context.Context, id string, expiresAt time.Time) error
	ListExpiredPresignedUploads(ctx context.Context, limit int) ([]string, error)

	// SBOM methods
	CreateSBOMReport(ctx context.Context, report *models.SBOMReport) error
	GetSBOMReport(ctx context.Context, repoID, artifactID, format, specVersion string) (*models.SBOMReport, error)
	ListSBOMReports(ctx context.Context, repoID string) ([]*models.SBOMReport, error)
	DeleteSBOMReport(ctx context.Context, id string) error

	// License compliance methods
	CreateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error
	GetLicenseScanResult(ctx context.Context, repoID, artifactID string) (*models.LicenseScanResult, error)
	UpdateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error
	ListLicenseScanResults(ctx context.Context, repoID string) ([]*models.LicenseScanResult, error)
	CreateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error
	GetLicensePolicy(ctx context.Context, id string) (*models.LicensePolicy, error)
	UpdateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error
	DeleteLicensePolicy(ctx context.Context, id string) error
	ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error)

	// Secret detection methods
	CreateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error
	GetSecretScanResult(ctx context.Context, repoID, artifactID string) (*models.SecretScanResult, error)
	UpdateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error
	ListSecretScanResults(ctx context.Context, repoID string, flaggedOnly bool) ([]*models.SecretScanResult, error)
	QuarantineArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error

	// Vulnerability scanning methods
	CreateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error
	GetVulnScanResult(ctx context.Context, repoID, artifactID string) (*models.VulnScanResult, error)
	UpdateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error
	ListVulnScanResults(ctx context.Context, repoID string, vulnerableOnly bool) ([]*models.VulnScanResult, error)
	QuarantineVulnArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error

	// Storage quota methods
	// GetProjectQuota returns the project's storage ceiling (unlimited when never set).
	GetProjectQuota(ctx context.Context, projectID string) (*models.ProjectQuota, error)
	// SetProjectQuota stores the ceiling (0 = unlimited) and initialises the
	// counters from the repository counters when a limit is first enabled.
	SetProjectQuota(ctx context.Context, projectID string, maxSizeBytes, maxArtifactCount int64, warnThresholdPercent int) (*models.ProjectQuota, error)
	// GetProjectUsage sums the repository counters of a project and lists them per repository.
	GetProjectUsage(ctx context.Context, projectID string) (*models.ProjectUsage, error)
	GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error)
	UpdateStorageQuota(ctx context.Context, quota *models.StorageQuota) error
	// ListStorageQuotas returns the limits of every repository quota row (backup export).
	ListStorageQuotas(ctx context.Context) ([]*models.StorageQuota, error)
	IncrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	// ReserveStorageUsage atomically adds usage if it fits the quota; false,nil when exceeded.
	// A non-zero reservation leaves a pending entry (quota_pending) until the
	// package row insert or the matching DecrementStorageUsage consumes it, so
	// a reconcile during the upload stays exact.
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	// ChargeStorageUsage adds usage for a package row that already exists,
	// past any limit, leaving no pending entry (see ReserveStorageUsage).
	ChargeStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	// ReconcileStorageUsage recomputes one repository's usage counters from its
	// package rows (sum of blob sizes, one artifact per row) and returns the
	// corrected quota.
	ReconcileStorageUsage(ctx context.Context, repoID string) (*models.StorageQuota, error)
	// ReconcileAllStorageUsage does the same for every repository and returns how
	// many counters it had to correct. Concurrent callers (other replicas) are
	// serialized: a caller that finds a run in progress returns 0, nil.
	ReconcileAllStorageUsage(ctx context.Context) (int64, error)

	// Storage backends
	CreateStorageBackend(ctx context.Context, backend *models.StorageBackend) error
	ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error)
	GetStorageBackend(ctx context.Context, name string) (*models.StorageBackend, error)
	UpdateStorageBackend(ctx context.Context, backend *models.StorageBackend) error
	DeleteStorageBackend(ctx context.Context, id string) error
	CountBlobsOnStorageBackend(ctx context.Context, backendID string) (int64, error)
	// StorageBackendBusyReason says why a backend must not change kind/config ("" = idle).
	StorageBackendBusyReason(ctx context.Context, backendID string) (string, error)
	UpdateStorageBackendStatus(ctx context.Context, id, status string, message *string) error

	// Artifact properties methods
	SetArtifactProperty(ctx context.Context, repoID, artifactID, key, value string) error
	GetArtifactProperty(ctx context.Context, repoID, artifactID, key string) (string, error)
	DeleteArtifactProperty(ctx context.Context, repoID, artifactID, key string) error
	ListArtifactProperties(ctx context.Context, repoID, artifactID string) (map[string]string, error)
	SearchByProperty(ctx context.Context, repoID, key, value string) ([]string, error)

	// Package delta methods
	CreatePackageDelta(ctx context.Context, delta *models.PackageDelta) error
	GetPackageDelta(ctx context.Context, repoID, fromSHA256, toSHA256 string) (*models.PackageDelta, error)
	ListPackageDeltas(ctx context.Context, repoID, toSHA256 string) ([]*models.PackageDelta, error)
	DeletePackageDelta(ctx context.Context, id string) error

	// Project methods
	CreateProject(ctx context.Context, project *models.Project) error
	GetProject(ctx context.Context, id string) (*models.Project, error)
	GetProjectByName(ctx context.Context, name string) (*models.Project, error)
	UpdateProject(ctx context.Context, project *models.Project) error
	DeleteProject(ctx context.Context, id string) error
	// ListProjects returns the projects the user belongs to (recorded owner,
	// member, or through a group).
	ListProjects(ctx context.Context, userID string) ([]*models.Project, error)
	ListAllProjects(ctx context.Context) ([]*models.Project, error)
	AddProjectMember(ctx context.Context, projectID, userID, role string) error
	ListProjectMembers(ctx context.Context, projectID string) ([]*models.ProjectMember, error)
	GetProjectMember(ctx context.Context, projectID, userID string) (*models.ProjectMember, error)
	// ChangeProjectMember sets, adds or removes (newRole "") a member's role
	// under the project row lock; guard vetoes the change (see ProjectRoleGuard).
	ChangeProjectMember(ctx context.Context, projectID, userID, newRole string, guard ProjectRoleGuard) error
	// ChangeProjectGroup attaches, changes or detaches (newRole "") a group the same way.
	ChangeProjectGroup(ctx context.Context, projectID, groupID, newRole string, createdBy *string, guard ProjectRoleGuard) error
	// ListProjectRepositories returns one page of the project's live repositories and their total.
	ListProjectRepositories(ctx context.Context, projectID string, page, limit int) ([]*models.Repository, int, error)
	// CountRepositoriesByProject returns the live repository count per project ID.
	CountRepositoriesByProject(ctx context.Context, projectIDs []string) (map[string]int, error)
	// CreateProjectToken mints a project token: service user, membership and
	// deploy token in one transaction (ErrProjectTokenLimit, ErrProjectTokenName).
	CreateProjectToken(ctx context.Context, spec ProjectTokenSpec) (*models.Token, error)
	// ListProjectTokens returns the project's active tokens; createdBy limits them to one creator.
	ListProjectTokens(ctx context.Context, projectID string, createdBy *string) ([]*models.Token, error)
	// RevokeProjectToken revokes a token of the project and disables its service user; false when there is none.
	RevokeProjectToken(ctx context.Context, projectID, tokenID string) (bool, error)
	ListProjectGroups(ctx context.Context, projectID string) ([]*models.ProjectGroup, error)
	GetProjectGroup(ctx context.Context, projectID, groupID string) (*models.ProjectGroup, error)
	// SetProjectGroup attaches a group with role, or changes an existing role,
	// without any guard (backup restore); API changes use ChangeProjectGroup.
	SetProjectGroup(ctx context.Context, projectID, groupID, role string, createdBy *string) error
	// ListProjectGrantsByUser returns every project role the user holds:
	// recorded owner, membership rows and group attachments, one grant per
	// source (cached like the other per-request auth lookups).
	ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error)

	// System settings methods
	GetSystemSetting(ctx context.Context, key string) (string, error)
	SetSystemSetting(ctx context.Context, key, value string) error

	// Conda methods
	CreateCondaPackage(ctx context.Context, pkg *models.CondaPackage) error
	// GetCondaPackageFile returns the live package file with exactly this
	// name, version, build, platform and extension (".tar.bz2" or ".conda").
	GetCondaPackageFile(ctx context.Context, repoID, name, version, buildString, platform, extension string) (*models.CondaPackage, error)
	DeleteCondaPackage(ctx context.Context, id string) error
	ListCondaPackages(ctx context.Context, repoID, platform string) ([]*models.CondaPackage, error)
	// CondaRepodataStamp is a cheap validator of one platform's repodata.json:
	// it changes exactly when a live package is added or removed.
	CondaRepodataStamp(ctx context.Context, repoID, platform string) (string, error)

	// ListAllCondaPackages is a bulk-list-by-repo method used only by backup
	// export (no platform filter required).
	ListAllCondaPackages(ctx context.Context, repoID string) ([]*models.CondaPackage, error)

	// Conan methods. A conan_packages row is one stored file of a recipe or
	// package revision.
	//
	// CreateConanPackage stores the file, replacing the blob of the live row with
	// the same key. GetConanPackage returns (nil, nil) when there is none.
	CreateConanPackage(ctx context.Context, pkg *models.ConanPackage) error
	GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error)
	// ListConanRecipeFiles returns every live file of one recipe: all its recipe
	// and package revisions.
	ListConanRecipeFiles(ctx context.Context, repoID, name, version, userChannel string) ([]*models.ConanPackage, error)
	// ListConanRecipes returns the distinct recipes of a repository.
	ListConanRecipes(ctx context.Context, repoID string) ([]models.ConanRecipe, error)
	// FindConanPackageBySHA1 returns a live file of the repository with this
	// SHA-1 (Conan checksum deploy), or (nil, nil).
	FindConanPackageBySHA1(ctx context.Context, repoID, sha1 string) (*models.ConanPackage, error)
	// SetConanPackageDigests records the digests of a file stored without them;
	// it leaves updated_at (the revision time) alone.
	SetConanPackageDigests(ctx context.Context, id, md5, sha1 string) error
	DeleteConanPackage(ctx context.Context, id string) error
	ListConanPackages(ctx context.Context, repoID string) ([]*models.ConanPackage, error)

	// Ivy methods
	CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error
	GetIvyArtifact(ctx context.Context, repoID, organisation, module, revision, artifactName, artifactExt string) (*models.IvyArtifact, error)
	DeleteIvyArtifact(ctx context.Context, id string) error
	ListIvyArtifacts(ctx context.Context, repoID string) ([]*models.IvyArtifact, error)

	// SBT methods
	CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error
	GetSBTArtifact(ctx context.Context, repoID, groupID, artifactID, version, filename string) (*models.SBTArtifact, error)
	DeleteSBTArtifact(ctx context.Context, id string) error
	ListSBTArtifacts(ctx context.Context, repoID string) ([]*models.SBTArtifact, error)

	// Release bundle and promotion pipeline methods
	CreatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error
	GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error)
	GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error)
	UpdatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error
	DeletePromotionPipeline(ctx context.Context, id string) error
	ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error)

	CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error
	GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error)
	GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error)
	UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error
	DeleteReleaseBundle(ctx context.Context, id string) error
	ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error)

	AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error
	// RemoveBundleItem deletes an item of a draft bundle (ErrBundleNotDraft otherwise).
	RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error
	ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error)
	GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error)

	CreateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error
	UpdateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error
	ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error)
	// AddBundleItems atomically adds items to a draft bundle (ErrBundleNotDraft otherwise).
	AddBundleItems(ctx context.Context, bundleID string, items []*models.ReleaseBundleItem) error
	// SealReleaseBundle conditionally moves a non-empty draft bundle to sealed; false when it was not a draft.
	// A non-nil sig is stored with the bundle and requires the item set to still match sig.ItemIDs.
	SealReleaseBundle(ctx context.Context, bundleID, sealedBy string, sealedAt time.Time, sig *models.BundleSignature) (bool, error)

	// Scan lookups/deletes by ID scoped to a repository (nil / not-found errors when absent).
	GetVulnScanResultByID(ctx context.Context, repoID, id string) (*models.VulnScanResult, error)
	DeleteVulnScanResult(ctx context.Context, repoID, id string) error
	// ListVulnScanOverview returns one keyset page of vulnerability scans across all
	// repositories, worst first, without their findings (ErrInvalidVulnScanFilter
	// for an unknown severity, status or scanner or a page size below 1).
	ListVulnScanOverview(ctx context.Context, params models.VulnScanListParams) (*models.VulnScanOverviewPage, error)
	// VulnScanOverviewSummary counts the overview's scans, optionally of one repository (by name).
	VulnScanOverviewSummary(ctx context.Context, repoName string) (*models.VulnScanSummary, error)
	SetVulnScanQuarantine(ctx context.Context, repoID, id string, quarantined bool) error
	GetLicenseScanResultByID(ctx context.Context, repoID, id string) (*models.LicenseScanResult, error)
	// SetLicenseScanQuarantine quarantines (true) or approves (false) the artifact of one license scan result.
	SetLicenseScanQuarantine(ctx context.Context, repoID, id string, quarantined bool) error
	ListGlobalLicensePolicies(ctx context.Context) ([]*models.LicensePolicy, error)
	GetSBOMReportByID(ctx context.Context, repoID, id string) (*models.SBOMReport, error)
	// UpsertVEXStatement records or replaces the admin VEX decision for (repo, artifact, vuln) and fills the stored row back.
	UpsertVEXStatement(ctx context.Context, s *models.VEXStatement) error
	// ListVEXStatements lists a repository's VEX decisions, only one artifact's when artifactID != "".
	ListVEXStatements(ctx context.Context, repoID, artifactID string) ([]*models.VEXStatement, error)
	// DeleteVEXStatement deletes a decision scoped to repoID; ErrVEXStatementNotFound when absent.
	DeleteVEXStatement(ctx context.Context, repoID, id string) error
	// DeleteSBOMReportInRepo deletes the report and returns the sha256 of its blob ("" if none).
	DeleteSBOMReportInRepo(ctx context.Context, repoID, id string) (string, error)

	// GPG signing keys (docs/security/signing.md). Private keys are sealed by internal/crypto.
	// CreateGPGKey stores k inactive and fills ID/CreatedAt; ErrGPGKeyExists on a taken name or fingerprint.
	CreateGPGKey(ctx context.Context, k *models.GPGKey) error
	// CreateGPGKeyIfNone stores k as the global active key only when no key exists, under a cluster-wide lock.
	CreateGPGKeyIfNone(ctx context.Context, k *models.GPGKey) (bool, error)
	// ListGPGKeys lists every key without its private part.
	ListGPGKeys(ctx context.Context) ([]*models.GPGKey, error)
	// GetGPGKey loads one key with its sealed private part; ErrGPGKeyNotFound when absent.
	GetGPGKey(ctx context.Context, id string) (*models.GPGKey, error)
	// GetGPGKeyByFingerprint loads a key by fingerprint; ErrGPGKeyNotFound when absent.
	GetGPGKeyByFingerprint(ctx context.Context, fingerprint string) (*models.GPGKey, error)
	// DeleteGPGKey removes a key and its repository assignments; ErrGPGKeyNotFound when absent.
	DeleteGPGKey(ctx context.Context, id string) error
	// SetGlobalGPGKey makes id the one globally active key; ErrGPGKeyNotFound when absent.
	SetGlobalGPGKey(ctx context.Context, id string) error
	// ClearGlobalGPGKey leaves no key globally active.
	ClearGlobalGPGKey(ctx context.Context) error
	// SetRepositoryGPGKey makes keyID the signing key of one repository.
	SetRepositoryGPGKey(ctx context.Context, repoID, keyID, assignedBy string) error
	// ClearRepositoryGPGKey removes a repository's own key and reports whether there was one.
	ClearRepositoryGPGKey(ctx context.Context, repoID string) (bool, error)
	// ListGPGKeyAssignments lists every repository-to-key binding.
	ListGPGKeyAssignments(ctx context.Context) ([]models.GPGKeyAssignment, error)
	// ListRepositoriesForGPGKey returns the repositories bound to keyID.
	ListRepositoriesForGPGKey(ctx context.Context, keyID string) ([]models.GPGSigningRepo, error)
	// ListSigningRepositoriesWithoutKey returns the hosted deb/rpm repositories that follow the global key.
	ListSigningRepositoriesWithoutKey(ctx context.Context) ([]models.GPGSigningRepo, error)
	// ResolveGPGKeyForRepository returns the repository's own key, else the global active one, with its sealed private part; nil when unsigned.
	ResolveGPGKeyForRepository(ctx context.Context, repoID string) (*models.GPGKey, error)

	// GetRepositoryDependencyTrack returns the repository's Dependency-Track override, nil when none is set.
	GetRepositoryDependencyTrack(ctx context.Context, repoID string) (*models.RepositoryDependencyTrack, error)
	// UpsertRepositoryDependencyTrack stores the override and fills the stored row back.
	UpsertRepositoryDependencyTrack(ctx context.Context, c *models.RepositoryDependencyTrack) error
	// DeleteRepositoryDependencyTrack removes the override and reports whether one existed.
	DeleteRepositoryDependencyTrack(ctx context.Context, repoID string) (bool, error)
	// UpsertDependencyTrackPush records the push state of an artifact and fills the stored row back.
	UpsertDependencyTrackPush(ctx context.Context, s *models.DependencyTrackPush) error
	// GetDependencyTrackPush returns the push state of an artifact, nil when never pushed.
	GetDependencyTrackPush(ctx context.Context, repoID, artifactID string) (*models.DependencyTrackPush, error)
	// GetRepositoryAttestationAttach reports whether the repository attaches signed attestations to docker images as OCI referrers (false when never set).
	GetRepositoryAttestationAttach(ctx context.Context, repoID string) (bool, error)
	// SetRepositoryAttestationAttach stores that switch.
	SetRepositoryAttestationAttach(ctx context.Context, repoID string, enabled bool) error
	// ListDependencyTrackPushes lists a repository's pushes, most recently updated first.
	ListDependencyTrackPushes(ctx context.Context, repoID string, limit int) ([]*models.DependencyTrackPush, error)
	// CreateSystemSettingIfAbsent stores the setting unless the key exists and reports whether it was stored.
	CreateSystemSettingIfAbsent(ctx context.Context, key, value string) (bool, error)

	// SetArtifactProperties upserts props in one transaction; replace=true clears existing ones first.
	SetArtifactProperties(ctx context.Context, repoID, artifactID string, props map[string]string, replace bool) error

	// AI analysis methods
	UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error
	GetAIAnalysis(ctx context.Context, repoID, artifactID string) (*models.AIAnalysis, error)
	ListAIAnalyses(ctx context.Context, repoID string, status string, licenseReviewOnly bool, limit, offset int) ([]*models.AIAnalysis, int, error)
	DeleteAIAnalysis(ctx context.Context, repoID, artifactID string) error
	ListPendingAIAnalyses(ctx context.Context, limit int) ([]*models.AIAnalysis, error)
}

// Transaction defines the interface for database transactions.
type Transaction interface {
	// Commit commits the transaction
	Commit() error

	// Rollback rolls back the transaction
	Rollback() error

	// Exec executes a query without returning rows
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Query executes a query that returns rows
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)

	// QueryRow executes a query that returns a single row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DatabaseConfig contains database connection configuration.
type DatabaseConfig struct {
	Driver                 string            `yaml:"driver"` // "postgres", "mysql", "sqlite"
	Host                   string            `yaml:"host"`
	Port                   int               `yaml:"port"`
	User                   string            `yaml:"user"`
	Password               string            `yaml:"password"`
	Database               string            `yaml:"database"`
	SSLMode                string            `yaml:"sslMode"` // For PostgreSQL
	Options                map[string]string `yaml:"options"`
	MaxOpenConns           int               `yaml:"maxOpenConns"`           // Maximum number of open connections (default: 25)
	MaxIdleConns           int               `yaml:"maxIdleConns"`           // Maximum number of idle connections (default: 5)
	ConnMaxLifetimeMinutes int               `yaml:"connMaxLifetimeMinutes"` // Maximum lifetime of a connection in minutes (default: 30)
	// AuthCacheTTLMillis is how long per-request authentication lookups (token,
	// user, groups, permission targets, repository by name) are served from
	// memory. Writes through this process invalidate immediately; other
	// replicas observe a change after at most this long. 0 disables the cache.
	AuthCacheTTLMillis int `yaml:"authCacheTtlMs"`
	// ConnectTimeout is how long startup keeps retrying the initial database
	// connection (capped exponential backoff) before giving up. 0 fails at the
	// first error.
	ConnectTimeout time.Duration `yaml:"connectTimeout"`
}

// ComponentListOptions selects and orders a repository's package listing.
type ComponentListOptions struct {
	// Format restricts the listing to one package format ("" = all).
	Format string
	// Name restricts it to rows whose listed name equals Name exactly.
	Name string
	// Query keeps rows whose listed name or version contains Query, ignoring case.
	Query string
	// SortByName orders by (name, version) instead of newest first.
	SortByName bool
	Page       int
	Limit      int
}

// ComponentSearcher is implemented by stores that can filter and order a
// repository's package listing (GET /api/v1/repositories/{name}/packages with
// package_name, q or sort). Kept separate from the main interface so only the
// real store has to provide it. It returns the requested page and the total
// number of matching rows.
type ComponentSearcher interface {
	SearchComponents(ctx context.Context, repoID string, opts ComponentListOptions) ([]*models.Component, int, error)
}
