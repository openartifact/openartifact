// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package database provides a base mock for the Database interface.
// Embed BaseMockDB in test mocks and override only the methods you need.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// BaseMockDB provides stub implementations of every Database method.
type BaseMockDB struct{}

func (m *BaseMockDB) GetDB() *sql.DB                 { return nil }
func (m *BaseMockDB) Close() error                   { return nil }
func (m *BaseMockDB) Ping(ctx context.Context) error { return nil }
func (m *BaseMockDB) BeginTx(ctx context.Context) (Transaction, error) {
	return nil, errNotImpl("BeginTx")
}
func (m *BaseMockDB) Migrate(ctx context.Context) error { return nil }

// ErrMockNotImplemented is returned by BaseMockDB methods a test has not overridden.
var ErrMockNotImplemented = errors.New("BaseMockDB method not implemented")

func errNotImpl(name string) error { return fmt.Errorf("%w: %s", ErrMockNotImplemented, name) }

func (m *BaseMockDB) GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error) {
	return nil, errNotImpl("GetRepositoryByName")
}
func (m *BaseMockDB) ListPublicRepositoryNames(ctx context.Context) ([]string, error) {
	return nil, errNotImpl("ListPublicRepositoryNames")
}
func (m *BaseMockDB) GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error) {
	return nil, errNotImpl("GetRepositoryByID")
}
func (m *BaseMockDB) CreateRepository(ctx context.Context, repo *models.Repository) error {
	return errNotImpl("CreateRepository")
}
func (m *BaseMockDB) ListRepositories(ctx context.Context, page, limit int, formatFilter string) ([]*models.Repository, error) {
	return nil, errNotImpl("ListRepositories")
}
func (m *BaseMockDB) CountRepositories(ctx context.Context, formatFilter string) (int, error) {
	return 0, errNotImpl("CountRepositories")
}
func (m *BaseMockDB) UpdateRepositoryAndMembers(ctx context.Context, repo *models.Repository, memberIDs []string) error {
	return errNotImpl("UpdateRepositoryAndMembers")
}
func (m *BaseMockDB) UpdateRepository(ctx context.Context, repo *models.Repository) error {
	return errNotImpl("UpdateRepository")
}
func (m *BaseMockDB) DeleteRepository(ctx context.Context, id string) error {
	return errNotImpl("DeleteRepository")
}
func (m *BaseMockDB) ListComponentsByRepo(ctx context.Context, repoID, formatFilter string, page, limit int) ([]*models.Component, int, error) {
	return nil, 0, errNotImpl("ListComponentsByRepo")
}
func (m *BaseMockDB) GetRPMRepo(ctx context.Context, repositoryID string) (*models.RPMRepo, error) {
	return nil, errNotImpl("GetRPMRepo")
}
func (m *BaseMockDB) CreateRPMRepo(ctx context.Context, repo *models.RPMRepo) error {
	return errNotImpl("CreateRPMRepo")
}
func (m *BaseMockDB) MarkRepoStale(ctx context.Context, repositoryID string) error {
	return errNotImpl("MarkRepoStale")
}
func (m *BaseMockDB) CreateComponent(ctx context.Context, component *models.Component) error {
	return errNotImpl("CreateComponent")
}
func (m *BaseMockDB) CreateDebPackage(ctx context.Context, pkg *models.Component, distID, compID string) error {
	return errNotImpl("CreateDebPackage")
}
func (m *BaseMockDB) CreateRpmPackage(ctx context.Context, pkg *models.Component) error {
	return errNotImpl("CreateRpmPackage")
}
func (m *BaseMockDB) UpsertBlob(ctx context.Context, sha256, blobKey string, sizeBytes int64) (string, error) {
	return "", errNotImpl("UpsertBlob")
}
func (m *BaseMockDB) GetComponentByID(ctx context.Context, id string) (*models.Component, error) {
	return nil, errNotImpl("GetComponentByID")
}
func (m *BaseMockDB) GetRepoComponentBlob(ctx context.Context, repoID, id string) (*models.ComponentBlob, error) {
	return nil, errNotImpl("GetRepoComponentBlob")
}
func (m *BaseMockDB) GetComponentByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	return nil, errNotImpl("GetComponentByFilename")
}
func (m *BaseMockDB) GetComponentBySHA256(ctx context.Context, repoID, sha256 string) (*models.Component, error) {
	return nil, errNotImpl("GetComponentBySHA256")
}

func (m *BaseMockDB) GetComponentsByRepo(ctx context.Context, repoID string) ([]models.Component, error) {
	return nil, errNotImpl("GetComponentsByRepo")
}
func (m *BaseMockDB) DeleteComponent(ctx context.Context, componentID string) error {
	return errNotImpl("DeleteComponent")
}
func (m *BaseMockDB) GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error) {
	return nil, errNotImpl("GetRepoMetadataByPath")
}
func (m *BaseMockDB) GetRepoMetadataByRepo(ctx context.Context, repositoryID string) ([]models.RepoMetadata, error) {
	return nil, errNotImpl("GetRepoMetadataByRepo")
}
func (m *BaseMockDB) UpsertRepoMetadata(ctx context.Context, meta *models.RepoMetadata) error {
	return errNotImpl("UpsertRepoMetadata")
}
func (m *BaseMockDB) CreateDebianDistribution(ctx context.Context, dist *models.DebianDistribution) error {
	return errNotImpl("CreateDebianDistribution")
}
func (m *BaseMockDB) GetDebianDistribution(ctx context.Context, repoID string, name string) (*models.DebianDistribution, error) {
	return nil, errNotImpl("GetDebianDistribution")
}
func (m *BaseMockDB) GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error) {
	return nil, errNotImpl("GetDebianDistributionsByRepo")
}
func (m *BaseMockDB) CreateDebianComponent(ctx context.Context, comp *models.DebianComponent) error {
	return errNotImpl("CreateDebianComponent")
}
func (m *BaseMockDB) GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error) {
	return nil, errNotImpl("GetDebianComponentsByDist")
}
func (m *BaseMockDB) CreateDebianArchitecture(ctx context.Context, arch *models.DebianArchitecture) error {
	return errNotImpl("CreateDebianArchitecture")
}
func (m *BaseMockDB) GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error) {
	return nil, errNotImpl("GetDebianArchitecturesByDist")
}
func (m *BaseMockDB) GetDebianPackageByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	return nil, errNotImpl("GetDebianPackageByFilename")
}
func (m *BaseMockDB) GetDebianPackagesByDistComp(ctx context.Context, repoID string, dist string, comp string, arch string) ([]models.Component, error) {
	return nil, errNotImpl("GetDebianPackagesByDistComp")
}
func (m *BaseMockDB) GetDebPackageByID(ctx context.Context, id string) (*models.DebPackage, error) {
	return nil, errNotImpl("GetDebPackageByID")
}
func (m *BaseMockDB) GetOrCreateDebianDistribution(ctx context.Context, repoID, name string) (*models.DebianDistribution, error) {
	return nil, errNotImpl("GetOrCreateDebianDistribution")
}
func (m *BaseMockDB) GetOrCreateDebianComponent(ctx context.Context, distID, name string) (*models.DebianComponent, error) {
	return nil, errNotImpl("GetOrCreateDebianComponent")
}
func (m *BaseMockDB) GetOrCreateDebianArchitecture(ctx context.Context, distID, name string) (*models.DebianArchitecture, error) {
	return nil, errNotImpl("GetOrCreateDebianArchitecture")
}
func (m *BaseMockDB) SetDebControlFields(ctx context.Context, packageID, maintainer, homepage string, fields map[string]string) error {
	return errNotImpl("SetDebControlFields")
}
func (m *BaseMockDB) GetDebBlobInfo(ctx context.Context, blobID string) (string, int64, error) {
	return "", 0, errNotImpl("GetDebBlobInfo")
}
func (m *BaseMockDB) ListDebPackageEntries(ctx context.Context, repoID, dist, comp, arch string) ([]models.DebPackageEntry, error) {
	return nil, errNotImpl("ListDebPackageEntries")
}
func (m *BaseMockDB) ListDebPackagesByRepo(ctx context.Context, repoID string) ([]*models.DebPackage, error) {
	return nil, errNotImpl("ListDebPackagesByRepo")
}
func (m *BaseMockDB) RpmPackageExists(ctx context.Context, repoID, name, version, release string, epoch int, arch string) (bool, error) {
	return false, errNotImpl("RpmPackageExists")
}
func (m *BaseMockDB) ListRpmPackagesForMetadata(ctx context.Context, repoID string) ([]pkgrpm.Package, error) {
	return nil, errNotImpl("ListRpmPackagesForMetadata")
}
func (m *BaseMockDB) UpdateRpmPackageExtra(ctx context.Context, repoID, sha256 string, extra []byte) error {
	return errNotImpl("UpdateRpmPackageExtra")
}
func (m *BaseMockDB) GetRpmPackageByID(ctx context.Context, id string) (*models.RpmPackage, error) {
	return nil, errNotImpl("GetRpmPackageByID")
}
func (m *BaseMockDB) ListRpmPackagesByRepo(ctx context.Context, repoID string) ([]*models.RpmPackage, error) {
	return nil, errNotImpl("ListRpmPackagesByRepo")
}
func (m *BaseMockDB) CreateMavenArtifact(ctx context.Context, a *models.MavenArtifact) error {
	return errNotImpl("CreateMavenArtifact")
}
func (m *BaseMockDB) GetMavenArtifactByPath(ctx context.Context, repoID, path string) (*models.MavenArtifact, error) {
	return nil, errNotImpl("GetMavenArtifactByPath")
}
func (m *BaseMockDB) GetMavenArtifactsByGAV(ctx context.Context, repoID, groupID, artifactID, version string) ([]*models.MavenArtifact, error) {
	return nil, errNotImpl("GetMavenArtifactsByGAV")
}
func (m *BaseMockDB) GetMavenArtifactsByGA(ctx context.Context, repoID, groupID, artifactID string) ([]*models.MavenArtifact, error) {
	return nil, errNotImpl("GetMavenArtifactsByGA")
}
func (m *BaseMockDB) GetLatestSnapshotFile(ctx context.Context, repoID, groupID, artifactID, version, classifier, extension string) (*models.MavenArtifact, error) {
	return nil, errNotImpl("GetLatestSnapshotFile")
}
func (m *BaseMockDB) GetLatestSnapshot(ctx context.Context, repoID, groupID, artifactID, version string) (*models.MavenArtifact, error) {
	return nil, errNotImpl("GetLatestSnapshot")
}
func (m *BaseMockDB) DeleteMavenArtifact(ctx context.Context, id string) error {
	return errNotImpl("DeleteMavenArtifact")
}
func (m *BaseMockDB) ListMavenArtifactsByRepo(ctx context.Context, repoID string) ([]*models.MavenArtifact, error) {
	return nil, errNotImpl("ListMavenArtifactsByRepo")
}
func (m *BaseMockDB) CreateNPMPackage(ctx context.Context, p *models.NPMPackage) (*models.NPMPackage, error) {
	return nil, errNotImpl("CreateNPMPackage")
}
func (m *BaseMockDB) GetNPMPackageByName(ctx context.Context, repoID, fullName string) (*models.NPMPackage, error) {
	return nil, errNotImpl("GetNPMPackageByName")
}
func (m *BaseMockDB) DeleteNPMPackage(ctx context.Context, packageID string) error {
	return errNotImpl("DeleteNPMPackage")
}
func (m *BaseMockDB) MarkPackumentStale(ctx context.Context, packageID string) error {
	return errNotImpl("MarkPackumentStale")
}
func (m *BaseMockDB) UpdateNPMPackageLatestVersion(ctx context.Context, packageID, latestVersion string) error {
	return errNotImpl("UpdateNPMPackageLatestVersion")
}
func (m *BaseMockDB) CreateNPMVersion(ctx context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	return nil, errNotImpl("CreateNPMVersion")
}
func (m *BaseMockDB) GetNPMVersion(ctx context.Context, packageID, version string) (*models.NPMVersion, error) {
	return nil, errNotImpl("GetNPMVersion")
}
func (m *BaseMockDB) GetNPMVersionsByPackage(ctx context.Context, packageID string) ([]*models.NPMVersion, error) {
	return nil, errNotImpl("GetNPMVersionsByPackage")
}
func (m *BaseMockDB) UpdateNPMVersionManifest(ctx context.Context, packageID, version string, manifest []byte) error {
	return errNotImpl("UpdateNPMVersionManifest")
}
func (m *BaseMockDB) DeleteNPMVersion(ctx context.Context, packageID, version string) error {
	return errNotImpl("DeleteNPMVersion")
}
func (m *BaseMockDB) UpsertNPMDistTag(ctx context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	return nil, errNotImpl("UpsertNPMDistTag")
}
func (m *BaseMockDB) GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error) {
	return nil, errNotImpl("GetNPMDistTags")
}
func (m *BaseMockDB) DeleteNPMDistTag(ctx context.Context, packageID, tag string) error {
	return errNotImpl("DeleteNPMDistTag")
}
func (m *BaseMockDB) ListAllNPMPackages(ctx context.Context, repoID string) ([]*models.NPMPackage, error) {
	return nil, errNotImpl("ListAllNPMPackages")
}
func (m *BaseMockDB) ListNPMVersionsByRepo(ctx context.Context, repoID string) ([]*models.NPMVersion, error) {
	return nil, errNotImpl("ListNPMVersionsByRepo")
}
func (m *BaseMockDB) CreateDockerBlob(ctx context.Context, blob *models.DockerBlob) error {
	return errNotImpl("CreateDockerBlob")
}
func (m *BaseMockDB) GetDockerBlob(ctx context.Context, repoID, digest string) (*models.DockerBlob, error) {
	return nil, errNotImpl("GetDockerBlob")
}
func (m *BaseMockDB) DeleteDockerBlob(ctx context.Context, repoID, digest string) error {
	return errNotImpl("DeleteDockerBlob")
}
func (m *BaseMockDB) ListDockerBlobs(ctx context.Context, repoID string) ([]*models.DockerBlob, error) {
	return nil, errNotImpl("ListDockerBlobs")
}
func (m *BaseMockDB) ListDockerManifestsByRepo(ctx context.Context, repoID string) ([]*models.DockerManifest, error) {
	return nil, errNotImpl("ListDockerManifestsByRepo")
}
func (m *BaseMockDB) CreateDockerUpload(ctx context.Context, upload *models.DockerUpload) error {
	return errNotImpl("CreateDockerUpload")
}
func (m *BaseMockDB) GetDockerUpload(ctx context.Context, repositoryID, uploadUUID string) (*models.DockerUpload, error) {
	return nil, errNotImpl("GetDockerUpload")
}
func (m *BaseMockDB) UpdateDockerUploadOffset(ctx context.Context, repositoryID, uploadUUID string, oldOffset, newOffset int64) (bool, error) {
	return false, errNotImpl("UpdateDockerUploadOffset")
}
func (m *BaseMockDB) DeleteDockerUpload(ctx context.Context, repositoryID, uploadUUID string) error {
	return errNotImpl("DeleteDockerUpload")
}
func (m *BaseMockDB) CreateDockerManifest(ctx context.Context, mf *models.DockerManifest) error {
	return errNotImpl("CreateDockerManifest")
}
func (m *BaseMockDB) GetDockerManifest(ctx context.Context, repoID, image, digest string) (*models.DockerManifest, error) {
	return nil, errNotImpl("GetDockerManifest")
}
func (m *BaseMockDB) DeleteDockerManifest(ctx context.Context, repoID, image, digest string) error {
	return errNotImpl("DeleteDockerManifest")
}
func (m *BaseMockDB) UpsertDockerTag(ctx context.Context, tag *models.DockerTag) error {
	return errNotImpl("UpsertDockerTag")
}
func (m *BaseMockDB) GetDockerTag(ctx context.Context, repoID, image, name string) (*models.DockerTag, error) {
	return nil, errNotImpl("GetDockerTag")
}
func (m *BaseMockDB) DeleteDockerTag(ctx context.Context, repoID, image, name string) error {
	return errNotImpl("DeleteDockerTag")
}
func (m *BaseMockDB) DeleteDockerTagsByDigest(ctx context.Context, repoID, image, digest string) error {
	return errNotImpl("DeleteDockerTagsByDigest")
}
func (m *BaseMockDB) ListDockerTagsByRepo(ctx context.Context, repoID string, limit int) ([]*models.DockerTag, error) {
	return nil, errNotImpl("ListDockerTagsByRepo")
}
func (m *BaseMockDB) ListDockerTags(ctx context.Context, repoID, image, last string, n int) ([]*models.DockerTag, error) {
	return nil, errNotImpl("ListDockerTags")
}
func (m *BaseMockDB) EnsureBlob(ctx context.Context, sha256, mediaType string, size int64) (string, error) {
	return "", errNotImpl("EnsureBlob")
}
func (m *BaseMockDB) ListDockerManifestsBySubject(ctx context.Context, repoID, image, subjectDigest string) ([]*models.DockerManifest, error) {
	return nil, errNotImpl("ListDockerManifestsBySubject")
}
func (m *BaseMockDB) AddGroupMember(ctx context.Context, groupRepoID, memberRepoID string, priority int) error {
	return errNotImpl("AddGroupMember")
}
func (m *BaseMockDB) RemoveGroupMember(ctx context.Context, groupRepoID, memberRepoID string) error {
	return errNotImpl("RemoveGroupMember")
}
func (m *BaseMockDB) ListGroupMembers(ctx context.Context, groupRepoID string) ([]*models.RepositoryGroupMember, error) {
	return nil, errNotImpl("ListGroupMembers")
}
func (m *BaseMockDB) GetProxyCacheEntry(ctx context.Context, repoID, cacheKey string) (*models.ProxyCacheEntry, error) {
	return nil, errNotImpl("GetProxyCacheEntry")
}
func (m *BaseMockDB) UpsertProxyCacheEntry(ctx context.Context, entry *models.ProxyCacheEntry) error {
	return errNotImpl("UpsertProxyCacheEntry")
}
func (m *BaseMockDB) DeleteProxyCacheEntry(ctx context.Context, repoID, cacheKey string) error {
	return errNotImpl("DeleteProxyCacheEntry")
}
func (m *BaseMockDB) DeleteExpiredProxyCacheEntries(ctx context.Context) (int64, error) {
	return 0, errNotImpl("DeleteExpiredProxyCacheEntries")
}
func (m *BaseMockDB) CreateHelmChart(ctx context.Context, chart *models.HelmChart) error {
	return errNotImpl("CreateHelmChart")
}
func (m *BaseMockDB) GetHelmChart(ctx context.Context, repoID, name, version string) (*models.HelmChart, error) {
	return nil, errNotImpl("GetHelmChart")
}
func (m *BaseMockDB) DeleteHelmChart(ctx context.Context, repoID, name, version string) error {
	return errNotImpl("DeleteHelmChart")
}
func (m *BaseMockDB) ListHelmCharts(ctx context.Context, repoID string) ([]*models.HelmChart, error) {
	return nil, errNotImpl("ListHelmCharts")
}
func (m *BaseMockDB) ListHelmChartVersions(ctx context.Context, repoID, name string) ([]*models.HelmChart, error) {
	return nil, errNotImpl("ListHelmChartVersions")
}
func (m *BaseMockDB) CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error {
	return errNotImpl("CreatePyPIPackage")
}
func (m *BaseMockDB) GetPyPIPackage(ctx context.Context, repoID, name, version, filename string) (*models.PyPIPackage, error) {
	return nil, errNotImpl("GetPyPIPackage")
}
func (m *BaseMockDB) GetPyPIPackageByFilename(ctx context.Context, repoID, filename string) (*models.PyPIPackage, error) {
	return nil, errNotImpl("GetPyPIPackageByFilename")
}
func (m *BaseMockDB) DeletePyPIPackage(ctx context.Context, repoID, name, version, filename string) error {
	return errNotImpl("DeletePyPIPackage")
}
func (m *BaseMockDB) ListPyPIPackages(ctx context.Context, repoID string) ([]*models.PyPIPackage, error) {
	return nil, errNotImpl("ListPyPIPackages")
}
func (m *BaseMockDB) ListPyPIPackageVersions(ctx context.Context, repoID, name string) ([]*models.PyPIPackage, error) {
	return nil, errNotImpl("ListPyPIPackageVersions")
}
func (m *BaseMockDB) CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error {
	return errNotImpl("CreateNuGetPackage")
}
func (m *BaseMockDB) GetNuGetPackage(ctx context.Context, repoID, packageID, version string) (*models.NuGetPackage, error) {
	return nil, errNotImpl("GetNuGetPackage")
}
func (m *BaseMockDB) DeleteNuGetPackage(ctx context.Context, repoID, packageID, version string) error {
	return errNotImpl("DeleteNuGetPackage")
}
func (m *BaseMockDB) ListNuGetPackages(ctx context.Context, repoID string) ([]*models.NuGetPackage, error) {
	return nil, errNotImpl("ListNuGetPackages")
}
func (m *BaseMockDB) ListNuGetPackageVersions(ctx context.Context, repoID, packageID string) ([]*models.NuGetPackage, error) {
	return nil, errNotImpl("ListNuGetPackageVersions")
}
func (m *BaseMockDB) SearchNuGetPackages(ctx context.Context, repoID, query string, skip, take int) ([]*models.NuGetPackage, int, error) {
	return nil, 0, errNotImpl("SearchNuGetPackages")
}
func (m *BaseMockDB) IncrementNuGetDownloadCount(ctx context.Context, repoID, packageID, version string) error {
	return errNotImpl("IncrementNuGetDownloadCount")
}
func (m *BaseMockDB) CreateUser(ctx context.Context, user *models.User) (*models.User, error) {
	return nil, errNotImpl("CreateUser")
}
func (m *BaseMockDB) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	return nil, errNotImpl("GetUserByID")
}
func (m *BaseMockDB) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	return nil, errNotImpl("GetUserByUsername")
}
func (m *BaseMockDB) UpdateUser(ctx context.Context, user *models.User) error {
	return errNotImpl("UpdateUser")
}
func (m *BaseMockDB) UpdateUserLastLogin(ctx context.Context, id string, at time.Time) error {
	return errNotImpl("UpdateUserLastLogin")
}
func (m *BaseMockDB) UpdateUserPassword(ctx context.Context, id, passwordHash string, mustChange bool) error {
	return errNotImpl("UpdateUserPassword")
}
func (m *BaseMockDB) ListUsers(ctx context.Context, page, perPage int) ([]*models.User, int, error) {
	return nil, 0, errNotImpl("ListUsers")
}
func (m *BaseMockDB) CountUsers(ctx context.Context) (int, error) { return 0, errNotImpl("CountUsers") }
func (m *BaseMockDB) CreateGroup(ctx context.Context, group *models.Group) (*models.Group, error) {
	return nil, errNotImpl("CreateGroup")
}
func (m *BaseMockDB) GetGroupByID(ctx context.Context, id string) (*models.Group, error) {
	return nil, errNotImpl("GetGroupByID")
}
func (m *BaseMockDB) UpdateGroup(ctx context.Context, group *models.Group) error {
	return errNotImpl("UpdateGroup")
}
func (m *BaseMockDB) DeleteGroup(ctx context.Context, id string) error {
	return errNotImpl("DeleteGroup")
}
func (m *BaseMockDB) ListGroups(ctx context.Context) ([]*models.Group, error) {
	return nil, errNotImpl("ListGroups")
}
func (m *BaseMockDB) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	return errNotImpl("AddUserToGroup")
}
func (m *BaseMockDB) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	return errNotImpl("RemoveUserFromGroup")
}
func (m *BaseMockDB) ListGroupsByUser(ctx context.Context, userID string) ([]*models.Group, error) {
	return nil, errNotImpl("ListGroupsByUser")
}
func (m *BaseMockDB) ListUsersByGroup(ctx context.Context, groupID string) ([]*models.User, error) {
	return nil, errNotImpl("ListUsersByGroup")
}
func (m *BaseMockDB) CreateSession(ctx context.Context, session *models.Session) (*models.Session, error) {
	return nil, errNotImpl("CreateSession")
}
func (m *BaseMockDB) GetSessionByTokenHash(ctx context.Context, hash string) (*models.Session, error) {
	return nil, errNotImpl("GetSessionByTokenHash")
}
func (m *BaseMockDB) DeleteSession(ctx context.Context, tokenHash string) error {
	return errNotImpl("DeleteSession")
}
func (m *BaseMockDB) UpdateSessionLastSeen(ctx context.Context, sessionID string, expiresAt time.Time) error {
	return errNotImpl("UpdateSessionLastSeen")
}
func (m *BaseMockDB) ConsumeSAMLAssertion(ctx context.Context, providerSlug, assertionID string, expiresAt time.Time) (bool, error) {
	return false, errNotImpl("ConsumeSAMLAssertion")
}
func (m *BaseMockDB) DeleteSessionsByUser(ctx context.Context, userID string) error {
	return errNotImpl("DeleteSessionsByUser")
}
func (m *BaseMockDB) DeleteOtherSessionsByUser(ctx context.Context, userID, keepTokenHash string) error {
	return errNotImpl("DeleteOtherSessionsByUser")
}
func (m *BaseMockDB) CreateBootstrapAdmin(ctx context.Context, username, passwordHash string) (bool, error) {
	return false, errNotImpl("CreateBootstrapAdmin")
}
func (m *BaseMockDB) RepairBootstrapAdmin(ctx context.Context, username string) (bool, error) {
	return false, errNotImpl("RepairBootstrapAdmin")
}
func (m *BaseMockDB) CreateToken(ctx context.Context, token *models.Token) (*models.Token, error) {
	return nil, errNotImpl("CreateToken")
}
func (m *BaseMockDB) GetTokenByHash(ctx context.Context, hash string) (*models.Token, error) {
	return nil, errNotImpl("GetTokenByHash")
}
func (m *BaseMockDB) GetTokenByID(ctx context.Context, tokenID string) (*models.Token, error) {
	return nil, errNotImpl("GetTokenByID")
}
func (m *BaseMockDB) RevokeToken(ctx context.Context, tokenID string) error {
	return errNotImpl("RevokeToken")
}
func (m *BaseMockDB) UpdateTokenLastUsed(ctx context.Context, tokenID string) error {
	return errNotImpl("UpdateTokenLastUsed")
}
func (m *BaseMockDB) ListTokensByUser(ctx context.Context, userID string) ([]*models.Token, error) {
	return nil, errNotImpl("ListTokensByUser")
}
func (m *BaseMockDB) CreatePermissionTarget(ctx context.Context, target *models.PermissionTarget) (*models.PermissionTarget, error) {
	return nil, errNotImpl("CreatePermissionTarget")
}
func (m *BaseMockDB) GetPermissionTargetByID(ctx context.Context, id string) (*models.PermissionTarget, error) {
	return nil, errNotImpl("GetPermissionTargetByID")
}
func (m *BaseMockDB) GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error) {
	return nil, errNotImpl("GetPermissionTargetByName")
}
func (m *BaseMockDB) UpdatePermissionTarget(ctx context.Context, target *models.PermissionTarget) error {
	return errNotImpl("UpdatePermissionTarget")
}
func (m *BaseMockDB) DeletePermissionTarget(ctx context.Context, id string) error {
	return errNotImpl("DeletePermissionTarget")
}
func (m *BaseMockDB) ListPermissionTargets(ctx context.Context) ([]*models.PermissionTarget, error) {
	return nil, errNotImpl("ListPermissionTargets")
}
func (m *BaseMockDB) CreatePermissionAssignment(ctx context.Context, assignment *models.PermissionAssignment) error {
	return errNotImpl("CreatePermissionAssignment")
}
func (m *BaseMockDB) DeletePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error {
	return errNotImpl("DeletePermissionAssignment")
}
func (m *BaseMockDB) GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error) {
	return nil, errNotImpl("GetPermissionTargetsForPrincipal")
}
func (m *BaseMockDB) GetOrCreatePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error {
	return errNotImpl("GetOrCreatePermissionAssignment")
}
func (m *BaseMockDB) SyncSSOPermissionsForGroup(ctx context.Context, groupID string, targetIDs []string) error {
	return errNotImpl("SyncSSOPermissionsForGroup")
}
func (m *BaseMockDB) CreateSSOProvider(ctx context.Context, provider *models.SSOProvider) error {
	return errNotImpl("CreateSSOProvider")
}
func (m *BaseMockDB) GetSSOProviderBySlug(ctx context.Context, slug string) (*models.SSOProvider, error) {
	return nil, errNotImpl("GetSSOProviderBySlug")
}
func (m *BaseMockDB) GetSSOProviderByID(ctx context.Context, id string) (*models.SSOProvider, error) {
	return nil, errNotImpl("GetSSOProviderByID")
}
func (m *BaseMockDB) UpdateSSOProvider(ctx context.Context, provider *models.SSOProvider) error {
	return errNotImpl("UpdateSSOProvider")
}
func (m *BaseMockDB) DeleteSSOProvider(ctx context.Context, id string) error {
	return errNotImpl("DeleteSSOProvider")
}
func (m *BaseMockDB) ListEnabledSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	return nil, errNotImpl("ListEnabledSSOProviders")
}
func (m *BaseMockDB) ListAllSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	return nil, errNotImpl("ListAllSSOProviders")
}
func (m *BaseMockDB) UpdateUserSSO(ctx context.Context, userID string, externalID, providerSlug string) error {
	return errNotImpl("UpdateUserSSO")
}
func (m *BaseMockDB) SyncSSOGroups(ctx context.Context, userID string, groupIDs []string) error {
	return errNotImpl("SyncSSOGroups")
}
func (m *BaseMockDB) GetUserByExternalID(ctx context.Context, externalID, providerSlug string) (*models.User, error) {
	return nil, errNotImpl("GetUserByExternalID")
}
func (m *BaseMockDB) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return nil, errNotImpl("GetUserByEmail")
}
func (m *BaseMockDB) GetGroupByName(ctx context.Context, name string) (*models.Group, error) {
	return nil, errNotImpl("GetGroupByName")
}
func (m *BaseMockDB) GetOrCreateGroup(ctx context.Context, name string) (*models.Group, error) {
	return nil, errNotImpl("GetOrCreateGroup")
}
func (m *BaseMockDB) ArtifactExists(ctx context.Context, sha256 string, repositoryID string) (bool, error) {
	return false, errNotImpl("ArtifactExists")
}
func (m *BaseMockDB) ArtifactExistsBySHA256(ctx context.Context, sha256 string) (string, bool, error) {
	return "", false, errNotImpl("ArtifactExistsBySHA256")
}
func (m *BaseMockDB) BlobReferenceCount(ctx context.Context, sha256 string) (int, error) {
	return 0, nil
}
func (m *BaseMockDB) CreateGoModule(ctx context.Context, mod *models.GoModule) error {
	return errNotImpl("CreateGoModule")
}
func (m *BaseMockDB) GetGoModule(ctx context.Context, repoID, modulePath, version string) (*models.GoModule, error) {
	return nil, errNotImpl("GetGoModule")
}
func (m *BaseMockDB) DeleteGoModule(ctx context.Context, repoID, modulePath, version string) error {
	return errNotImpl("DeleteGoModule")
}
func (m *BaseMockDB) ListGoModuleVersions(ctx context.Context, repoID, modulePath string) ([]*models.GoModule, error) {
	return nil, errNotImpl("ListGoModuleVersions")
}
func (m *BaseMockDB) ListGoModulesByRepo(ctx context.Context, repoID string) ([]*models.GoModule, error) {
	return nil, errNotImpl("ListGoModulesByRepo")
}
func (m *BaseMockDB) InsertAuditLog(ctx context.Context, event *models.AuditLog) error {
	return errNotImpl("InsertAuditLog")
}
func (m *BaseMockDB) ListAuditLogs(ctx context.Context, params models.AuditListParams) (*models.AuditPage, error) {
	return nil, errNotImpl("ListAuditLogs")
}
func (m *BaseMockDB) ListAllTokens(ctx context.Context, page, perPage int) ([]*models.Token, int, error) {
	return nil, 0, errNotImpl("ListAllTokens")
}
func (m *BaseMockDB) CreateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	return errNotImpl("CreateCleanupPolicy")
}
func (m *BaseMockDB) GetCleanupPolicy(ctx context.Context, id string) (*models.CleanupPolicy, error) {
	return nil, errNotImpl("GetCleanupPolicy")
}
func (m *BaseMockDB) UpdateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	return errNotImpl("UpdateCleanupPolicy")
}
func (m *BaseMockDB) DeleteCleanupPolicy(ctx context.Context, id string) error {
	return errNotImpl("DeleteCleanupPolicy")
}
func (m *BaseMockDB) ListCleanupPolicies(ctx context.Context, repoID string) ([]*models.CleanupPolicy, error) {
	return nil, errNotImpl("ListCleanupPolicies")
}
func (m *BaseMockDB) ListProjectCleanupPolicies(ctx context.Context, projectID string) ([]*models.CleanupPolicy, error) {
	return nil, errNotImpl("ListProjectCleanupPolicies")
}
func (m *BaseMockDB) ListProjectCleanupRepositories(ctx context.Context, projectID string) ([]string, error) {
	return nil, errNotImpl("ListProjectCleanupRepositories")
}
func (m *BaseMockDB) ListEnabledCleanupPolicies(ctx context.Context) ([]*models.CleanupPolicy, error) {
	return nil, errNotImpl("ListEnabledCleanupPolicies")
}
func (m *BaseMockDB) UpdateCleanupPolicyRunResult(ctx context.Context, id string, deletedCount int, runErr error) error {
	return errNotImpl("UpdateCleanupPolicyRunResult")
}
func (m *BaseMockDB) CreateWebhook(ctx context.Context, wh *models.Webhook) error {
	return errNotImpl("CreateWebhook")
}
func (m *BaseMockDB) GetWebhook(ctx context.Context, id string) (*models.Webhook, error) {
	return nil, errNotImpl("GetWebhook")
}
func (m *BaseMockDB) UpdateWebhook(ctx context.Context, wh *models.Webhook) error {
	return errNotImpl("UpdateWebhook")
}
func (m *BaseMockDB) DeleteWebhook(ctx context.Context, id string) error {
	return errNotImpl("DeleteWebhook")
}
func (m *BaseMockDB) ListWebhooks(ctx context.Context, repoID string) ([]*models.Webhook, error) {
	return nil, errNotImpl("ListWebhooks")
}
func (m *BaseMockDB) ListWebhooksForEvent(ctx context.Context, repoID, eventType string) ([]*models.Webhook, error) {
	return nil, errNotImpl("ListWebhooksForEvent")
}
func (m *BaseMockDB) CreateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	return errNotImpl("CreateWebhookDelivery")
}
func (m *BaseMockDB) UpdateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	return errNotImpl("UpdateWebhookDelivery")
}
func (m *BaseMockDB) ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]*models.WebhookDelivery, error) {
	return nil, errNotImpl("ListWebhookDeliveries")
}
func (m *BaseMockDB) ClaimPendingWebhookDeliveries(ctx context.Context, limit int, lease time.Duration) ([]*models.WebhookDelivery, error) {
	return nil, errNotImpl("ClaimPendingWebhookDeliveries")
}
func (m *BaseMockDB) CreateBackupJob(ctx context.Context, job *models.BackupJob) error {
	return errNotImpl("CreateBackupJob")
}
func (m *BaseMockDB) GetBackupJob(ctx context.Context, id string) (*models.BackupJob, error) {
	return nil, errNotImpl("GetBackupJob")
}
func (m *BaseMockDB) UpdateBackupJob(ctx context.Context, job *models.BackupJob) error {
	return errNotImpl("UpdateBackupJob")
}
func (m *BaseMockDB) ListBackupJobs(ctx context.Context, limit int) ([]*models.BackupJob, error) {
	return nil, errNotImpl("ListBackupJobs")
}
func (m *BaseMockDB) CreateCargoCrate(ctx context.Context, crate *models.CargoCrate) error {
	return errNotImpl("CreateCargoCrate")
}
func (m *BaseMockDB) GetCargoCrate(ctx context.Context, repoID, name, version string) (*models.CargoCrate, error) {
	return nil, errNotImpl("GetCargoCrate")
}
func (m *BaseMockDB) DeleteCargoCrate(ctx context.Context, id string) error {
	return errNotImpl("DeleteCargoCrate")
}
func (m *BaseMockDB) ListCargoCrateVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	return nil, errNotImpl("ListCargoCrateVersions")
}
func (m *BaseMockDB) SearchCargoCrates(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrate, error) {
	return nil, errNotImpl("SearchCargoCrates")
}
func (m *BaseMockDB) SearchCargoCrateSummaries(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrateSummary, int, error) {
	return nil, 0, errNotImpl("SearchCargoCrateSummaries")
}
func (m *BaseMockDB) YankCargoCrate(ctx context.Context, repoID, name, version string, yanked bool) error {
	return errNotImpl("YankCargoCrate")
}
func (m *BaseMockDB) IncrementCargoDownloadCount(ctx context.Context, repoID, name, version string) error {
	return errNotImpl("IncrementCargoDownloadCount")
}
func (m *BaseMockDB) CreateRubyGem(ctx context.Context, gem *models.RubyGem) error {
	return errNotImpl("CreateRubyGem")
}
func (m *BaseMockDB) GetRubyGem(ctx context.Context, repoID, name, version, platform string) (*models.RubyGem, error) {
	return nil, errNotImpl("GetRubyGem")
}
func (m *BaseMockDB) DeleteRubyGem(ctx context.Context, id string) error {
	return errNotImpl("DeleteRubyGem")
}
func (m *BaseMockDB) ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
	return nil, errNotImpl("ListRubyGems")
}
func (m *BaseMockDB) ListRubyGemVersions(ctx context.Context, repoID, name string) ([]*models.RubyGem, error) {
	return nil, errNotImpl("ListRubyGemVersions")
}
func (m *BaseMockDB) SearchRubyGems(ctx context.Context, repoID, query string) ([]*models.RubyGem, error) {
	return nil, errNotImpl("SearchRubyGems")
}
func (m *BaseMockDB) YankRubyGem(ctx context.Context, repoID, name, version, platform string, yanked bool) error {
	return errNotImpl("YankRubyGem")
}
func (m *BaseMockDB) UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error {
	return errNotImpl("UpsertRawArtifact")
}
func (m *BaseMockDB) CreatePresignedUpload(ctx context.Context, u *models.PresignedUpload) error {
	return errNotImpl("CreatePresignedUpload")
}
func (m *BaseMockDB) GetPresignedUpload(ctx context.Context, repoID, id string) (*models.PresignedUpload, error) {
	return nil, errNotImpl("GetPresignedUpload")
}
func (m *BaseMockDB) DeletePresignedUpload(ctx context.Context, id string) error {
	return errNotImpl("DeletePresignedUpload")
}
func (m *BaseMockDB) ClaimPresignedUpload(ctx context.Context, repoID, id string, lease time.Duration) (bool, error) {
	return false, errNotImpl("ClaimPresignedUpload")
}
func (m *BaseMockDB) ReleasePresignedUpload(ctx context.Context, id string) error {
	return errNotImpl("ReleasePresignedUpload")
}
func (m *BaseMockDB) MarkPresignedUploadCompleted(ctx context.Context, id, artifactID string) error {
	return errNotImpl("MarkPresignedUploadCompleted")
}
func (m *BaseMockDB) CreatePresignedUploadCapped(ctx context.Context, u *models.PresignedUpload, maxPending int) error {
	return errNotImpl("CreatePresignedUploadCapped")
}
func (m *BaseMockDB) ExistingPresignedUploadIDs(ctx context.Context, ids []string) ([]string, error) {
	return nil, errNotImpl("ExistingPresignedUploadIDs")
}
func (m *BaseMockDB) ListExpiredPresignedUploads(ctx context.Context, limit int) ([]string, error) {
	return nil, errNotImpl("ListExpiredPresignedUploads")
}
func (m *BaseMockDB) GetRawArtifact(ctx context.Context, repoID, path string) (*models.RawArtifact, error) {
	return nil, errNotImpl("GetRawArtifact")
}
func (m *BaseMockDB) DeleteRawArtifact(ctx context.Context, repoID, path string) error {
	return errNotImpl("DeleteRawArtifact")
}
func (m *BaseMockDB) ListRawArtifacts(ctx context.Context, repoID, prefix string) ([]*models.RawArtifact, error) {
	return nil, errNotImpl("ListRawArtifacts")
}
func (m *BaseMockDB) CreateSBOMReport(ctx context.Context, report *models.SBOMReport) error {
	return errNotImpl("CreateSBOMReport")
}
func (m *BaseMockDB) GetSBOMReport(ctx context.Context, repoID, artifactID, format, specVersion string) (*models.SBOMReport, error) {
	return nil, errNotImpl("GetSBOMReport")
}
func (m *BaseMockDB) ListSBOMReports(ctx context.Context, repoID string) ([]*models.SBOMReport, error) {
	return nil, errNotImpl("ListSBOMReports")
}
func (m *BaseMockDB) DeleteSBOMReport(ctx context.Context, id string) error {
	return errNotImpl("DeleteSBOMReport")
}
func (m *BaseMockDB) CreateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error {
	return errNotImpl("CreateLicenseScanResult")
}
func (m *BaseMockDB) GetLicenseScanResult(ctx context.Context, repoID, artifactID string) (*models.LicenseScanResult, error) {
	return nil, errNotImpl("GetLicenseScanResult")
}
func (m *BaseMockDB) UpdateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error {
	return errNotImpl("UpdateLicenseScanResult")
}
func (m *BaseMockDB) ListLicenseScanResults(ctx context.Context, repoID string) ([]*models.LicenseScanResult, error) {
	return nil, errNotImpl("ListLicenseScanResults")
}
func (m *BaseMockDB) CreateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	return errNotImpl("CreateLicensePolicy")
}
func (m *BaseMockDB) GetLicensePolicy(ctx context.Context, id string) (*models.LicensePolicy, error) {
	return nil, errNotImpl("GetLicensePolicy")
}
func (m *BaseMockDB) UpdateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	return errNotImpl("UpdateLicensePolicy")
}
func (m *BaseMockDB) DeleteLicensePolicy(ctx context.Context, id string) error {
	return errNotImpl("DeleteLicensePolicy")
}

// ListLicensePolicies reports no policies: every upload that declares a license
// reads them (licensepolicy.Enforce), so an unconfigured mock must not fail
// those uploads. Tests of policy behavior override it.
func (m *BaseMockDB) ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error) {
	return nil, nil
}
func (m *BaseMockDB) CreateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error {
	return errNotImpl("CreateSecretScanResult")
}
func (m *BaseMockDB) GetSecretScanResult(ctx context.Context, repoID, artifactID string) (*models.SecretScanResult, error) {
	return nil, errNotImpl("GetSecretScanResult")
}
func (m *BaseMockDB) UpdateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error {
	return errNotImpl("UpdateSecretScanResult")
}
func (m *BaseMockDB) ListSecretScanResults(ctx context.Context, repoID string, flaggedOnly bool) ([]*models.SecretScanResult, error) {
	return nil, errNotImpl("ListSecretScanResults")
}
func (m *BaseMockDB) QuarantineArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	return errNotImpl("QuarantineArtifact")
}
func (m *BaseMockDB) CreateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	return errNotImpl("CreateVulnScanResult")
}
func (m *BaseMockDB) GetVulnScanResult(ctx context.Context, repoID, artifactID string) (*models.VulnScanResult, error) {
	return nil, errNotImpl("GetVulnScanResult")
}
func (m *BaseMockDB) UpdateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	return errNotImpl("UpdateVulnScanResult")
}
func (m *BaseMockDB) ListVulnScanResults(ctx context.Context, repoID string, vulnerableOnly bool) ([]*models.VulnScanResult, error) {
	return nil, errNotImpl("ListVulnScanResults")
}
func (m *BaseMockDB) QuarantineVulnArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	return errNotImpl("QuarantineVulnArtifact")
}
func (m *BaseMockDB) GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error) {
	return nil, errNotImpl("GetOrCreateStorageQuota")
}
func (m *BaseMockDB) UpdateStorageQuota(ctx context.Context, quota *models.StorageQuota) error {
	return errNotImpl("UpdateStorageQuota")
}
func (m *BaseMockDB) IncrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	return errNotImpl("IncrementStorageUsage")
}
func (m *BaseMockDB) DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	return errNotImpl("DecrementStorageUsage")
}
func (m *BaseMockDB) CountBlobsOnStorageBackend(ctx context.Context, backendID string) (int64, error) {
	return 0, errNotImpl("CountBlobsOnStorageBackend")
}
func (m *BaseMockDB) ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error) {
	return false, errNotImpl("ReserveStorageUsage")
}
func (m *BaseMockDB) ReconcileStorageUsage(ctx context.Context, repoID string) (*models.StorageQuota, error) {
	return nil, errNotImpl("ReconcileStorageUsage")
}
func (m *BaseMockDB) ReconcileAllStorageUsage(ctx context.Context) (int64, error) {
	return 0, errNotImpl("ReconcileAllStorageUsage")
}
func (m *BaseMockDB) ReplaceGroupMembers(ctx context.Context, groupRepoID string, memberIDs []string) error {
	return errNotImpl("ReplaceGroupMembers")
}
func (m *BaseMockDB) CreateRepositoryWithMembers(ctx context.Context, repo *models.Repository, memberIDs []string) error {
	return errNotImpl("CreateRepositoryWithMembers")
}
func (m *BaseMockDB) CreateProjectRepository(ctx context.Context, repo *models.Repository, memberIDs []string, maxRepositories int) error {
	return errNotImpl("CreateProjectRepository")
}
func (m *BaseMockDB) MoveRepositoryToProject(ctx context.Context, repoID, toProjectID string, maxRepositories int) error {
	return errNotImpl("MoveRepositoryToProject")
}
func (m *BaseMockDB) CreateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	return errNotImpl("CreateStorageBackend")
}
func (m *BaseMockDB) ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error) {
	return nil, errNotImpl("ListStorageBackends")
}
func (m *BaseMockDB) GetStorageBackend(ctx context.Context, name string) (*models.StorageBackend, error) {
	return nil, errNotImpl("GetStorageBackend")
}
func (m *BaseMockDB) UpdateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	return errNotImpl("UpdateStorageBackend")
}
func (m *BaseMockDB) DeleteStorageBackend(ctx context.Context, id string) error {
	return errNotImpl("DeleteStorageBackend")
}
func (m *BaseMockDB) UpdateStorageBackendStatus(ctx context.Context, id, status string, message *string) error {
	return errNotImpl("UpdateStorageBackendStatus")
}
func (m *BaseMockDB) SetArtifactProperty(ctx context.Context, repoID, artifactID, key, value string) error {
	return errNotImpl("SetArtifactProperty")
}
func (m *BaseMockDB) GetArtifactProperty(ctx context.Context, repoID, artifactID, key string) (string, error) {
	return "", errNotImpl("GetArtifactProperty")
}
func (m *BaseMockDB) DeleteArtifactProperty(ctx context.Context, repoID, artifactID, key string) error {
	return errNotImpl("DeleteArtifactProperty")
}
func (m *BaseMockDB) ListArtifactProperties(ctx context.Context, repoID, artifactID string) (map[string]string, error) {
	return nil, errNotImpl("ListArtifactProperties")
}
func (m *BaseMockDB) SearchByProperty(ctx context.Context, repoID, key, value string) ([]string, error) {
	return nil, errNotImpl("SearchByProperty")
}
func (m *BaseMockDB) CreatePackageDelta(ctx context.Context, delta *models.PackageDelta) error {
	return errNotImpl("CreatePackageDelta")
}
func (m *BaseMockDB) GetPackageDelta(ctx context.Context, repoID, fromSHA256, toSHA256 string) (*models.PackageDelta, error) {
	return nil, errNotImpl("GetPackageDelta")
}
func (m *BaseMockDB) ListPackageDeltas(ctx context.Context, repoID, toSHA256 string) ([]*models.PackageDelta, error) {
	return nil, errNotImpl("ListPackageDeltas")
}
func (m *BaseMockDB) DeletePackageDelta(ctx context.Context, id string) error {
	return errNotImpl("DeletePackageDelta")
}
func (m *BaseMockDB) CreateProject(ctx context.Context, project *models.Project) error {
	return errNotImpl("CreateProject")
}
func (m *BaseMockDB) GetProject(ctx context.Context, id string) (*models.Project, error) {
	return nil, errNotImpl("GetProject")
}
func (m *BaseMockDB) GetProjectByName(ctx context.Context, name string) (*models.Project, error) {
	return nil, errNotImpl("GetProjectByName")
}
func (m *BaseMockDB) UpdateProject(ctx context.Context, project *models.Project) error {
	return errNotImpl("UpdateProject")
}
func (m *BaseMockDB) DeleteProject(ctx context.Context, id string) error {
	return errNotImpl("DeleteProject")
}
func (m *BaseMockDB) ListAllProjects(ctx context.Context) ([]*models.Project, error) {
	return nil, errNotImpl("ListAllProjects")
}
func (m *BaseMockDB) ListProjectGroups(ctx context.Context, projectID string) ([]*models.ProjectGroup, error) {
	return nil, errNotImpl("ListProjectGroups")
}
func (m *BaseMockDB) GetProjectGroup(ctx context.Context, projectID, groupID string) (*models.ProjectGroup, error) {
	return nil, errNotImpl("GetProjectGroup")
}
func (m *BaseMockDB) SetProjectGroup(ctx context.Context, projectID, groupID, role string, createdBy *string) error {
	return errNotImpl("SetProjectGroup")
}
func (m *BaseMockDB) ChangeProjectGroup(ctx context.Context, projectID, groupID, newRole string, createdBy *string, guard ProjectRoleGuard) error {
	return errNotImpl("ChangeProjectGroup")
}
func (m *BaseMockDB) CreateProjectToken(ctx context.Context, spec ProjectTokenSpec) (*models.Token, error) {
	return nil, errNotImpl("CreateProjectToken")
}

func (m *BaseMockDB) ListProjectTokens(ctx context.Context, projectID string, createdBy *string) ([]*models.Token, error) {
	return nil, errNotImpl("ListProjectTokens")
}

func (m *BaseMockDB) RevokeProjectToken(ctx context.Context, projectID, tokenID string) (bool, error) {
	return false, errNotImpl("RevokeProjectToken")
}

func (m *BaseMockDB) ChangeProjectMember(ctx context.Context, projectID, userID, newRole string, guard ProjectRoleGuard) error {
	return errNotImpl("ChangeProjectMember")
}
func (m *BaseMockDB) ListProjectRepositories(ctx context.Context, projectID string, page, limit int) ([]*models.Repository, int, error) {
	return nil, 0, errNotImpl("ListProjectRepositories")
}
func (m *BaseMockDB) CountRepositoriesByProject(ctx context.Context, projectIDs []string) (map[string]int, error) {
	return nil, errNotImpl("CountRepositoriesByProject")
}
func (m *BaseMockDB) ListProjects(ctx context.Context, userID string) ([]*models.Project, error) {
	return nil, errNotImpl("ListProjects")
}
func (m *BaseMockDB) AddProjectMember(ctx context.Context, projectID, userID, role string) error {
	return errNotImpl("AddProjectMember")
}
func (m *BaseMockDB) ListProjectMembers(ctx context.Context, projectID string) ([]*models.ProjectMember, error) {
	return nil, errNotImpl("ListProjectMembers")
}
func (m *BaseMockDB) GetProjectMember(ctx context.Context, projectID, userID string) (*models.ProjectMember, error) {
	return nil, errNotImpl("GetProjectMember")
}

// ListProjectGrantsByUser reports no grants: the evaluator consults it for
// every request a permission target did not already allow.
func (m *BaseMockDB) ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error) {
	return nil, nil
}
func (m *BaseMockDB) GetSystemSetting(ctx context.Context, key string) (string, error) {
	return "", errNotImpl("GetSystemSetting")
}
func (m *BaseMockDB) SetSystemSetting(ctx context.Context, key, value string) error {
	return errNotImpl("SetSystemSetting")
}
func (m *BaseMockDB) CreateCondaPackage(ctx context.Context, pkg *models.CondaPackage) error {
	return errNotImpl("CreateCondaPackage")
}
func (m *BaseMockDB) GetCondaPackageFile(ctx context.Context, repoID, name, version, buildString, platform, extension string) (*models.CondaPackage, error) {
	return nil, errNotImpl("GetCondaPackageFile")
}
func (m *BaseMockDB) DeleteCondaPackage(ctx context.Context, id string) error {
	return errNotImpl("DeleteCondaPackage")
}
func (m *BaseMockDB) ListCondaPackages(ctx context.Context, repoID, platform string) ([]*models.CondaPackage, error) {
	return nil, errNotImpl("ListCondaPackages")
}
func (m *BaseMockDB) ListAllCondaPackages(ctx context.Context, repoID string) ([]*models.CondaPackage, error) {
	return nil, errNotImpl("ListAllCondaPackages")
}
func (m *BaseMockDB) CreateConanPackage(ctx context.Context, pkg *models.ConanPackage) error {
	return errNotImpl("CreateConanPackage")
}
func (m *BaseMockDB) GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error) {
	return nil, errNotImpl("GetConanPackage")
}
func (m *BaseMockDB) ListConanRecipeFiles(ctx context.Context, repoID, name, version, userChannel string) ([]*models.ConanPackage, error) {
	return nil, errNotImpl("ListConanRecipeFiles")
}
func (m *BaseMockDB) ListConanRecipes(ctx context.Context, repoID string) ([]models.ConanRecipe, error) {
	return nil, errNotImpl("ListConanRecipes")
}
func (m *BaseMockDB) DeleteConanPackage(ctx context.Context, id string) error {
	return errNotImpl("DeleteConanPackage")
}
func (m *BaseMockDB) ListConanPackages(ctx context.Context, repoID string) ([]*models.ConanPackage, error) {
	return nil, errNotImpl("ListConanPackages")
}
func (m *BaseMockDB) CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error {
	return errNotImpl("CreateIvyArtifact")
}
func (m *BaseMockDB) GetIvyArtifact(ctx context.Context, repoID, organisation, module, revision, artifactName, artifactExt string) (*models.IvyArtifact, error) {
	return nil, errNotImpl("GetIvyArtifact")
}
func (m *BaseMockDB) DeleteIvyArtifact(ctx context.Context, id string) error {
	return errNotImpl("DeleteIvyArtifact")
}
func (m *BaseMockDB) ListIvyArtifacts(ctx context.Context, repoID string) ([]*models.IvyArtifact, error) {
	return nil, errNotImpl("ListIvyArtifacts")
}
func (m *BaseMockDB) CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error {
	return errNotImpl("CreateSBTArtifact")
}
func (m *BaseMockDB) GetSBTArtifact(ctx context.Context, repoID, groupID, artifactID, version, filename string) (*models.SBTArtifact, error) {
	return nil, errNotImpl("GetSBTArtifact")
}
func (m *BaseMockDB) DeleteSBTArtifact(ctx context.Context, id string) error {
	return errNotImpl("DeleteSBTArtifact")
}
func (m *BaseMockDB) ListSBTArtifacts(ctx context.Context, repoID string) ([]*models.SBTArtifact, error) {
	return nil, errNotImpl("ListSBTArtifacts")
}
func (m *BaseMockDB) CreatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return errNotImpl("CreatePromotionPipeline")
}
func (m *BaseMockDB) GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error) {
	return nil, errNotImpl("GetPromotionPipelineByName")
}
func (m *BaseMockDB) GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error) {
	return nil, errNotImpl("GetPromotionPipelineByID")
}
func (m *BaseMockDB) UpdatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error {
	return errNotImpl("UpdatePromotionPipeline")
}
func (m *BaseMockDB) DeletePromotionPipeline(ctx context.Context, id string) error {
	return errNotImpl("DeletePromotionPipeline")
}
func (m *BaseMockDB) ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error) {
	return nil, errNotImpl("ListPromotionPipelines")
}
func (m *BaseMockDB) CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return errNotImpl("CreateReleaseBundle")
}
func (m *BaseMockDB) GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error) {
	return nil, errNotImpl("GetReleaseBundle")
}
func (m *BaseMockDB) GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error) {
	return nil, errNotImpl("GetReleaseBundleByID")
}
func (m *BaseMockDB) UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	return errNotImpl("UpdateReleaseBundle")
}
func (m *BaseMockDB) DeleteReleaseBundle(ctx context.Context, id string) error {
	return errNotImpl("DeleteReleaseBundle")
}
func (m *BaseMockDB) ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error) {
	return nil, 0, errNotImpl("ListReleaseBundles")
}
func (m *BaseMockDB) AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error {
	return errNotImpl("AddBundleItem")
}
func (m *BaseMockDB) RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error {
	return errNotImpl("RemoveBundleItem")
}
func (m *BaseMockDB) ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error) {
	return nil, errNotImpl("ListBundleItems")
}
func (m *BaseMockDB) GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error) {
	return nil, errNotImpl("GetBundleItem")
}
func (m *BaseMockDB) CreateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error {
	return errNotImpl("CreateBundlePromotion")
}
func (m *BaseMockDB) UpdateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error {
	return errNotImpl("UpdateBundlePromotion")
}
func (m *BaseMockDB) ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error) {
	return nil, errNotImpl("ListBundlePromotions")
}
func (m *BaseMockDB) AddBundleItems(ctx context.Context, bundleID string, items []*models.ReleaseBundleItem) error {
	return errNotImpl("AddBundleItems")
}
func (m *BaseMockDB) SealReleaseBundle(ctx context.Context, bundleID, sealedBy string, sealedAt time.Time) (bool, error) {
	return false, errNotImpl("SealReleaseBundle")
}
func (m *BaseMockDB) GetVulnScanResultByID(ctx context.Context, repoID, id string) (*models.VulnScanResult, error) {
	return nil, errNotImpl("GetVulnScanResultByID")
}
func (m *BaseMockDB) DeleteVulnScanResult(ctx context.Context, repoID, id string) error {
	return errNotImpl("DeleteVulnScanResult")
}
func (m *BaseMockDB) SetVulnScanQuarantine(ctx context.Context, repoID, id string, quarantined bool) error {
	return errNotImpl("SetVulnScanQuarantine")
}
func (m *BaseMockDB) GetLicenseScanResultByID(ctx context.Context, repoID, id string) (*models.LicenseScanResult, error) {
	return nil, errNotImpl("GetLicenseScanResultByID")
}
func (m *BaseMockDB) SetLicenseScanQuarantine(ctx context.Context, repoID, id string, quarantined bool) error {
	return errNotImpl("SetLicenseScanQuarantine")
}
func (m *BaseMockDB) ListGlobalLicensePolicies(ctx context.Context) ([]*models.LicensePolicy, error) {
	return nil, errNotImpl("ListGlobalLicensePolicies")
}
func (m *BaseMockDB) GetSBOMReportByID(ctx context.Context, repoID, id string) (*models.SBOMReport, error) {
	return nil, errNotImpl("GetSBOMReportByID")
}
func (m *BaseMockDB) DeleteSBOMReportInRepo(ctx context.Context, repoID, id string) (string, error) {
	return "", errNotImpl("DeleteSBOMReportInRepo")
}
func (m *BaseMockDB) SetArtifactProperties(ctx context.Context, repoID, artifactID string, props map[string]string, replace bool) error {
	return errNotImpl("SetArtifactProperties")
}
func (m *BaseMockDB) UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error {
	return errNotImpl("UpsertAIAnalysis")
}
func (m *BaseMockDB) GetAIAnalysis(ctx context.Context, repoID, artifactID string) (*models.AIAnalysis, error) {
	return nil, errNotImpl("GetAIAnalysis")
}
func (m *BaseMockDB) ListAIAnalyses(ctx context.Context, repoID string, status string, licenseReviewOnly bool, limit, offset int) ([]*models.AIAnalysis, int, error) {
	return nil, 0, errNotImpl("ListAIAnalyses")
}
func (m *BaseMockDB) DeleteAIAnalysis(ctx context.Context, repoID, artifactID string) error {
	return errNotImpl("DeleteAIAnalysis")
}
func (m *BaseMockDB) ListPendingAIAnalyses(ctx context.Context, limit int) ([]*models.AIAnalysis, error) {
	return nil, errNotImpl("ListPendingAIAnalyses")
}

func (m *BaseMockDB) GetProjectQuota(ctx context.Context, projectID string) (*models.ProjectQuota, error) {
	return nil, errNotImpl("GetProjectQuota")
}
func (m *BaseMockDB) SetProjectQuota(ctx context.Context, projectID string, maxSizeBytes, maxArtifactCount int64, warnThresholdPercent int) (*models.ProjectQuota, error) {
	return nil, errNotImpl("SetProjectQuota")
}
func (m *BaseMockDB) GetProjectUsage(ctx context.Context, projectID string) (*models.ProjectUsage, error) {
	return nil, errNotImpl("GetProjectUsage")
}
