// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ErrMissingBlobSHA256 reports a backed-up record without its blob sha256.
var ErrMissingBlobSHA256 = errors.New("record has no blob sha256")

// bulkListLimit caps single-call DB list methods that have no true cursor
// pagination (a large fixed page rather than looping). Tables genuinely too
// big for this would need real pagination added to the store.
const bulkListLimit = 100000

// maxManifestSize bounds the manifest entry read into memory from an
// untrusted archive.
const maxManifestSize = 16 << 20

// maxTableSize bounds a single JSON table entry decoded from an archive.
const maxTableSize = 2 << 30

// maxBlobEntrySize bounds one blob entry spooled to disk from an untrusted
// archive (the tar header size is attacker-chosen; the content only has to
// hash to its own name).
const maxBlobEntrySize = 64 << 30

// ArchiveVersion is the version of the archive layout this server writes and
// restores. 2.0 stores package data as rows of the generic package tables; the
// 1.x archives of 0.14 and older carry per-format tables and are not restorable.
const ArchiveVersion = "2.0"

// ErrUnsupportedArchive is returned for an archive of another layout version.
// Data of 0.14 and older moves to this version with `oa import` from the old
// server, not with a restore (docs/getting-started/upgrading.md).
var ErrUnsupportedArchive = errors.New(
	"unsupported backup archive: backups of 0.14 and older cannot be restored; " +
		"move the data with `oa import` (see docs/getting-started/upgrading.md)",
)

// ErrRestoreIncomplete is returned when a restore finished but some records
// or blobs could not be restored.
var ErrRestoreIncomplete = errors.New("restore completed with failures")

// ErrInvalidBlobEntry reports a malformed or untrusted blob entry in an archive.
var ErrInvalidBlobEntry = errors.New("invalid blob entry")

// restoreWarn logs a restore problem and records it for the final result.
func (e *Engine) restoreWarn(msg string, kv ...any) {
	e.restoreFailures.Add(1)
	e.log.Warnw(msg, kv...)
}

// restoreSkip logs a record the restore leaves alone on purpose (it already
// exists on the target) and counts it, without making the restore incomplete.
func (e *Engine) restoreSkip(msg string, kv ...any) {
	e.restoreSkips.Add(1)
	e.log.Infow(msg, kv...)
}

// restoreMissingUser reports a record whose user was not restored. When the
// user was skipped because the target already has an account of that name (the
// bootstrap admin), the record is skipped with it; any other missing user is a
// failure.
func (e *Engine) restoreMissingUser(st *restoreState, oldUserID, msg string, kv ...any) {
	if _, kept := st.keptUsers[oldUserID]; kept {
		e.restoreSkip(msg+" (the user already exists on this instance)", kv...)
		return
	}

	e.restoreWarn(msg, kv...)
}

// redactOIDCSecret blanks client_secret inside an OIDC config JSON blob.
func redactOIDCSecret(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}

	delete(m, "client_secret")

	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}

	return out
}

// redactedStorageConfig drops credential-looking keys from a storage backend
// JSON config so backups never carry cloud secrets.
func redactedStorageConfig(raw []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}

	for k := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "secret") || strings.Contains(lk, "key") || strings.Contains(lk, "password") ||
			strings.Contains(lk, "token") {
			delete(m, k)
		}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}

	return out
}

// EngineStore is the part of the database Engine needs.
type EngineStore interface {
	AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error
	AddGroupMember(ctx context.Context, groupRepoID string, memberRepoID string, priority int) error
	AddProjectMember(ctx context.Context, projectID string, userID string, role string) error
	AddUserToGroup(ctx context.Context, userID string, groupID string) error
	BeginBackupSnapshot(ctx context.Context, leaseTTL time.Duration) (database.SnapshotReader, error)
	CreateBundlePromotion(ctx context.Context, p *models.BundlePromotion) error
	CreateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error
	CreateDockerBlob(ctx context.Context, blob *models.DockerBlob) error
	CreateDockerManifest(ctx context.Context, m *models.DockerManifest) error
	CreateGroup(ctx context.Context, group *models.Group) (*models.Group, error)
	CreateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error
	CreatePermissionTarget(ctx context.Context, target *models.PermissionTarget) (*models.PermissionTarget, error)
	CreateProject(ctx context.Context, project *models.Project) error
	CreatePromotionPipeline(ctx context.Context, p *models.PromotionPipeline) error
	CreateRPMRepo(ctx context.Context, repo *models.RPMRepo) error
	CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error
	CreateRepository(ctx context.Context, repo *models.Repository) error
	CreateSBOMReport(ctx context.Context, report *models.SBOMReport) error
	CreateSSOProvider(ctx context.Context, provider *models.SSOProvider) error
	CreateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error
	CreateStorageBackend(ctx context.Context, backend *models.StorageBackend) error
	CreateToken(ctx context.Context, token *models.Token) (*models.Token, error)
	CreateUser(ctx context.Context, user *models.User) (*models.User, error)
	CreateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error
	CreateWebhook(ctx context.Context, wh *models.Webhook) error
	EnsureBlob(ctx context.Context, sha256 string, mediaType string, size int64) (string, error)
	GetBackupJob(ctx context.Context, id string) (*models.BackupJob, error)
	GetOrCreatePermissionAssignment(ctx context.Context, targetID string, principalType string, principalID string) error
	GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error)
	GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error)
	GetRPMRepo(ctx context.Context, repositoryID string) (*models.RPMRepo, error)
	MarkRepoStale(ctx context.Context, repositoryID string) error
	ReconcileAllStorageUsage(ctx context.Context) (int64, error)
	RestorePackageRows(ctx context.Context, set database.PackageRowSet, repoID string, rows []json.RawMessage) (int, error)
	SetProjectGroup(ctx context.Context, projectID string, groupID string, role string, createdBy *string) error
	SetProjectQuota(
		ctx context.Context,
		projectID string,
		maxSizeBytes int64,
		maxArtifactCount int64,
		warnThresholdPercent int,
	) (*models.ProjectQuota, error)
	UpdateBackupJob(ctx context.Context, job *models.BackupJob) error
	UpdateRepository(ctx context.Context, repo *models.Repository) error
	UpdateStorageQuota(ctx context.Context, quota *models.StorageQuota) error
	UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error
	UpsertBlob(ctx context.Context, sha256 string, blobKey string, sizeBytes int64) (string, error)
	UpsertDockerTag(ctx context.Context, tag *models.DockerTag) error
	UpsertVEXStatement(ctx context.Context, s *models.VEXStatement) error
}

type Engine struct {
	db        EngineStore
	blobStore storage.BlobStore
	log       *logger.Logger
	backupDir string

	// leaseTTL is how long a running backup's blob-retention lease survives
	// without a heartbeat (SetLeaseTTL; DefaultLeaseTTL when unset).
	leaseTTL time.Duration

	// opMu serializes restores against each other and against backups: a
	// restore holds it exclusively, a backup shares it with other backups.
	opMu sync.RWMutex
	// restoring is set while a restore holds opMu, so callers can reject a
	// second one before they spool its archive (RestoreInProgress).
	restoring atomic.Bool

	// restoreFailures counts records skipped or failed during the current
	// restore so RunRestore can report partial failure instead of success.
	// Only the restore holding opMu touches it.
	restoreFailures atomic.Int64
	// restoreSkips counts records left alone because the target already has
	// them; they are logged but do not fail the restore.
	restoreSkips atomic.Int64

	// regen schedules metadata regeneration for restored repositories; optional.
	regen *lifecycle.Workers
}

// SetWorkers wires the metadata workers so a restore regenerates the generated
// metadata (deb, maven, rpm, ...) immediately instead of waiting for the next
// upload; each format's Changed lifecycle hook decides what to refresh.
func (e *Engine) SetWorkers(w *lifecycle.Workers) { e.regen = w }

func NewEngine(db EngineStore, blobStore storage.BlobStore, log *logger.Logger, backupDir string) *Engine {
	return &Engine{
		db:        db,
		blobStore: blobStore,
		log:       log,
		backupDir: backupDir,
	}
}

type BackupManifest struct {
	Version     string         `json:"version"`
	CreatedAt   time.Time      `json:"created_at"`
	Type        string         `json:"type"`
	TableCounts map[string]int `json:"table_counts"`
	// SnapshotAt is when the database snapshot every table in the archive was
	// read from was taken (all tables describe that one instant).
	SnapshotAt time.Time `json:"snapshot_at"`
	// BlobCount is the number of blobs the snapshot lists, i.e. how many
	// blobs/ entries a full backup holds (it fails rather than write fewer).
	BlobCount int `json:"blob_count,omitempty"`
}

// ErrRestoreInProgress is returned when a restore is already running (a second
// restore, or a backup, would interleave with it).
var ErrRestoreInProgress = errors.New("a restore is already in progress")

// RestoreInProgress reports whether a restore is running right now.
func (e *Engine) RestoreInProgress() bool { return e.restoring.Load() }

// jobPersistTimeout bounds a backup job status write.
const jobPersistTimeout = 30 * time.Second

// persistJob stores the job's state under a context detached from ctx's
// cancellation: a backup cancelled at shutdown still records itself as failed
// instead of staying "running" forever.
func (e *Engine) persistJob(ctx context.Context, job *models.BackupJob) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobPersistTimeout)
	defer cancel()

	return e.db.UpdateBackupJob(ctx, job)
}

// RunBackup executes a backup job. It fails the job with ErrRestoreInProgress
// while a restore is running: the archive would capture a half-restored state.
func (e *Engine) RunBackup(ctx context.Context, jobID, backupType string) error {
	// Get the job
	job, err := e.db.GetBackupJob(ctx, jobID)
	if err != nil {
		e.log.Errorw("failed to get backup job", "job_id", jobID, "error", err)
		return err
	}

	if job == nil {
		return fmt.Errorf("backup job not found: %s", jobID) //nolint:err113 // dynamic error with runtime context
	}

	if !e.opMu.TryRLock() {
		job.Status = "failed"
		job.ErrorMessage = ErrRestoreInProgress.Error()
		_ = e.persistJob(ctx, job)

		return ErrRestoreInProgress
	}
	defer e.opMu.RUnlock()

	// Update job status to running
	now := time.Now()
	job.Status = "running"

	job.StartedAt = &now
	if err := e.persistJob(ctx, job); err != nil {
		e.log.Errorw("failed to update backup job status", "job_id", jobID, "error", err)
		return err
	}

	// Create backup directory if it doesn't exist
	if err := os.MkdirAll(e.backupDir, 0755); err != nil {
		e.log.Errorw("failed to create backup directory", "path", e.backupDir, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to create backup directory"
		_ = e.persistJob(ctx, job)

		return err
	}

	// Create temp file
	tempFile := filepath.Join(e.backupDir, fmt.Sprintf("backup_%s.tar.gz.tmp", jobID))

	file, err := os.OpenFile(tempFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		e.log.Errorw("failed to create backup file", "path", tempFile, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to create backup file"
		_ = e.persistJob(ctx, job)

		return err
	}

	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)

	// The success path closes (and checks) the writers itself. Any failure
	// (a cancelled export at shutdown included) returns with a partial
	// archive nobody will read: drop it without flushing or syncing, which
	// for a large archive would otherwise hold the process up.
	archiveClosed := false

	defer func() {
		if !archiveClosed {
			abandonArchive(file)
		}
	}()

	// Create manifest
	manifest := BackupManifest{
		Version:     ArchiveVersion,
		CreatedAt:   now,
		Type:        backupType,
		TableCounts: make(map[string]int),
	}

	if err := e.exportArchive(ctx, tarWriter, &manifest, backupType); err != nil {
		e.log.Errorw("failed to export backup", "job_id", jobID, "error", err)

		job.Status = "failed"
		job.ErrorMessage = exportFailureReason(err)
		_ = e.persistJob(ctx, job)
		_ = os.Remove(tempFile)

		return err
	}

	// Write manifest
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		e.log.Errorw("failed to marshal manifest", "job_id", jobID, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to marshal manifest"
		_ = e.persistJob(ctx, job)
		_ = os.Remove(tempFile)

		return err
	}

	header := &tar.Header{
		Name: "manifest.json",
		Size: int64(len(manifestJSON)),
		Mode: 0644,
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		e.log.Errorw("failed to write manifest header", "job_id", jobID, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to write manifest"
		_ = e.persistJob(ctx, job)
		_ = os.Remove(tempFile)

		return err
	}

	if _, err := tarWriter.Write(manifestJSON); err != nil {
		e.log.Errorw("failed to write manifest content", "job_id", jobID, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to write manifest"
		_ = e.persistJob(ctx, job)
		_ = os.Remove(tempFile)

		return err
	}

	// The tar trailer, gzip footer and the last buffered block are written by
	// these closes: a failure here (ENOSPC, EIO) means a truncated archive.
	archiveClosed = true

	if err := closeArchive(tarWriter, gzipWriter, file); err != nil {
		e.log.Errorw("failed to finalize backup archive", "job_id", jobID, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to finalize backup archive"
		_ = e.persistJob(ctx, job)
		_ = os.Remove(tempFile)

		return err
	}

	// Get file size
	fileInfo, err := os.Stat(tempFile)
	if err != nil {
		e.log.Errorw("failed to stat backup file", "path", tempFile, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to stat backup file"
		_ = e.persistJob(ctx, job)
		_ = os.Remove(tempFile)

		return err
	}

	// Rename temp file to final name
	finalPath := filepath.Join(e.backupDir, fmt.Sprintf("backup_%s.tar.gz", jobID))
	if err := os.Rename(tempFile, finalPath); err != nil {
		e.log.Errorw("failed to rename backup file", "from", tempFile, "to", finalPath, "error", err)

		job.Status = "failed"
		job.ErrorMessage = "failed to rename backup file"
		_ = e.persistJob(ctx, job)
		_ = os.Remove(tempFile)

		return err
	}

	// Update job with success
	completedAt := time.Now()
	job.Status = "completed"
	job.StoragePath = finalPath
	job.SizeBytes = fileInfo.Size()
	job.CompletedAt = &completedAt

	job.ErrorMessage = ""
	if err := e.persistJob(ctx, job); err != nil {
		e.log.Errorw("failed to update backup job on completion", "job_id", jobID, "error", err)
		return err
	}

	e.log.Infow("backup completed successfully", "job_id", jobID, "size_bytes", fileInfo.Size(), "path", finalPath)

	return nil
}

// abandonArchive discards a partial archive: it closes the file without
// finishing the tar and gzip streams or syncing, and removes it.
func abandonArchive(f *os.File) {
	_ = f.Close()
	_ = os.Remove(f.Name())
}

// closeArchive finishes the tar and gzip streams, flushes the file to disk
// and closes it, returning the first error. Every step runs even after a
// failure so no descriptor leaks.
func closeArchive(tw *tar.Writer, gz *gzip.Writer, f *os.File) error {
	errs := []error{tw.Close(), gz.Close(), f.Sync(), f.Close()}

	return errors.Join(errs...)
}

// packageExport accumulates the per-repo tables gathered by
// exportPackageTables that have a typed model (docker's dedicated tables and
// the tables every repository can have), keyed by the same names exportMetadata
// writes to the archive. The generic package rows of the other formats are
// archived by exportPackageRows.
type packageExport struct {
	dockerBlobs     []*models.DockerBlob
	dockerManifests []*models.DockerManifest
	dockerTags      []*models.DockerTag
	webhooks        []*models.Webhook
	cleanupPolicies []*models.CleanupPolicy
	sbomReports     []*models.SBOMReport
	vexStatements   []*models.VEXStatement
	licenseResults  []*models.LicenseScanResult
	secretResults   []*models.SecretScanResult
	vulnResults     []*models.VulnScanResult
	aiAnalyses      []*models.AIAnalysis
	errs            []error
}

// exportMetadata exports database metadata to the archive.
//
// Coverage notes (see the caller's report for the full table-by-table
// list): every per-repo package/metadata table backed by a Database
// list-by-repo (or composable) method round-trips.
//
// Soft-deleted data (the trash: repositories and package rows with deleted_at
// set) is not part of a backup. The export goes through the store list
// methods, which treat trashed rows as nonexistent, so a trashed repository
// is skipped with its contents and a restore brings back live data only.
// Repository settings trash_retention_days and immutable_releases round-trip
// through models.Repository.
func (e *exporter) exportMetadata(ctx context.Context, tw *tar.Writer, manifest *BackupManifest) error {
	repos, err := e.db.ListRepositories(ctx, 1, bulkListLimit, "")
	if err != nil {
		return fmt.Errorf("failed to list repositories: %w", err)
	}

	if err := e.writeTable(tw, manifest, "repositories", redactRepositorySecrets(repos), len(repos)); err != nil {
		return err
	}

	users, _, err := e.db.ListUsers(ctx, 1, bulkListLimit)
	if err != nil {
		return fmt.Errorf("failed to list users: %w", err)
	}

	if err := e.writeTable(tw, manifest, "users", users, len(users)); err != nil {
		return err
	}

	groups, err := e.db.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("failed to list groups: %w", err)
	}

	if err := e.writeTable(tw, manifest, "groups", groups, len(groups)); err != nil {
		return err
	}

	if err := e.exportGroupMembers(ctx, tw, manifest, repos); err != nil {
		return err
	}

	if err := e.exportAccessControl(ctx, tw, manifest); err != nil {
		return err
	}

	if err := e.exportPackageRows(ctx, tw, manifest, repos); err != nil {
		return err
	}

	pkg := e.exportPackageTables(ctx, repos)
	if len(pkg.errs) > 0 {
		return fmt.Errorf("incomplete export: %w", errors.Join(pkg.errs...))
	}

	for i, w := range pkg.webhooks {
		if w != nil {
			c := *w
			c.SecretEnc = ""
			pkg.webhooks[i] = &c
		}
	}

	tables := []struct {
		name  string
		data  any
		count int
	}{
		{"docker_blobs", pkg.dockerBlobs, len(pkg.dockerBlobs)},
		{"docker_manifests", pkg.dockerManifests, len(pkg.dockerManifests)},
		{"docker_tags", pkg.dockerTags, len(pkg.dockerTags)},
		{"webhooks", pkg.webhooks, len(pkg.webhooks)},
		{"cleanup_policies", pkg.cleanupPolicies, len(pkg.cleanupPolicies)},
		{"sbom_reports", pkg.sbomReports, len(pkg.sbomReports)},
		{"vex_statements", pkg.vexStatements, len(pkg.vexStatements)},
		{"license_scan_results", pkg.licenseResults, len(pkg.licenseResults)},
		{"secret_scan_results", pkg.secretResults, len(pkg.secretResults)},
		{"vuln_scan_results", pkg.vulnResults, len(pkg.vulnResults)},
		{"ai_analyses", pkg.aiAnalyses, len(pkg.aiAnalyses)},
	}

	for _, t := range tables {
		if err := e.writeTable(tw, manifest, t.name, t.data, t.count); err != nil {
			return err
		}
	}

	return e.exportGlobalTables(ctx, tw, manifest)
}

// groupMemberRecord is one repository_group_members row in the archive. It
// carries repository ids; restore remaps both through repoIDMap.
type groupMemberRecord struct {
	GroupRepoID  string `json:"group_repo_id"`
	MemberRepoID string `json:"member_repo_id"`
	Priority     int    `json:"priority"`
}

// exportGroupMembers writes the ordered member list of every group
// repository. Without it a restored group repository exists but is empty.
func (e *exporter) exportGroupMembers(ctx context.Context, tw *tar.Writer, manifest *BackupManifest, repos []*models.Repository) error {
	records := []groupMemberRecord{}

	for _, repo := range repos {
		if repo.RepoType != "group" {
			continue
		}

		members, err := e.db.ListGroupMembers(ctx, repo.ID)
		if err != nil {
			return fmt.Errorf("failed to list members of group repository %s: %w", repo.ID, err)
		}

		for _, m := range members {
			records = append(records, groupMemberRecord{GroupRepoID: repo.ID, MemberRepoID: m.MemberRepoID, Priority: m.Priority})
		}
	}

	return e.writeTable(tw, manifest, "repository_group_members", records, len(records))
}

// fail records a failed list call. A backup missing rows it could not list
// must not be reported as completed (retention would then rotate away the
// last good one), so exportMetadata aborts when any list call failed.
func (p *packageExport) fail(what, scope string, err error) {
	p.errs = append(p.errs, fmt.Errorf("list %s (%s): %w", what, scope, err))
}

// collectRepoList appends a successfully listed per-repo table to dst; a
// failed list call is recorded on pkg and fails the backup.
func collectRepoList[T any](pkg *packageExport, dst *[]T, what, repoID string, v []T, err error) {
	if err != nil {
		pkg.fail(what, "repo "+repoID, err)
		return
	}

	*dst = append(*dst, v...)
}

// exportPackageTables walks every repository once, gathering every
// per-repo/format-specific table it can reach via an existing bulk-list (or
// composable) Database method. Errors from an individual list call are
// collected in packageExport.errs; exportMetadata fails the backup on any.
func (e *exporter) exportPackageTables(ctx context.Context, repos []*models.Repository) packageExport {
	var pkg packageExport

	for _, repo := range repos {
		if repo.Format == dockerFormat {
			e.exportDockerTables(ctx, repo, &pkg)
		}

		e.exportRepoScopedTables(ctx, repo, &pkg)
	}

	v, err := e.db.ListWebhooks(ctx, "")
	if err != nil {
		pkg.fail("global webhooks", "-", err)
	} else {
		pkg.webhooks = append(pkg.webhooks, v...)
	}

	return pkg
}

// exportDockerTables gathers the dedicated tables of one docker repo: docker
// is the one format that keeps its own tables instead of the generic package
// model.
func (e *exporter) exportDockerTables(ctx context.Context, repo *models.Repository, pkg *packageExport) {
	id := repo.ID

	blobs, err := e.db.ListDockerBlobs(ctx, id)
	collectRepoList(pkg, &pkg.dockerBlobs, "docker blobs", id, blobs, err)

	manifests, err := e.db.ListDockerManifestsByRepo(ctx, id)
	collectRepoList(pkg, &pkg.dockerManifests, "docker manifests", id, manifests, err)

	tags, err := e.db.ListDockerTagsByRepo(ctx, id, bulkListLimit)
	collectRepoList(pkg, &pkg.dockerTags, "docker tags", id, tags, err)
}

// exportRepoScopedTables gathers the tables every repo can have regardless of
// its format (webhooks, policies, scan results, ...).
func (e *exporter) exportRepoScopedTables(ctx context.Context, repo *models.Repository, pkg *packageExport) {
	id := repo.ID

	webhooks, err := e.db.ListWebhooks(ctx, id)
	collectRepoList(pkg, &pkg.webhooks, "webhooks", id, webhooks, err)

	policies, err := e.db.ListCleanupPolicies(ctx, id)
	collectRepoList(pkg, &pkg.cleanupPolicies, "cleanup policies", id, policies, err)

	sboms, err := e.db.ListSBOMReports(ctx, id)
	collectRepoList(pkg, &pkg.sbomReports, "sbom reports", id, sboms, err)

	vexes, err := e.db.ListVEXStatements(ctx, id, "")
	collectRepoList(pkg, &pkg.vexStatements, "vex statements", id, vexes, err)

	licenses, err := e.db.ListLicenseScanResults(ctx, id)
	collectRepoList(pkg, &pkg.licenseResults, "license scan results", id, licenses, err)

	secrets, err := e.db.ListSecretScanResults(ctx, id, false)
	collectRepoList(pkg, &pkg.secretResults, "secret scan results", id, secrets, err)

	vulns, err := e.db.ListVulnScanResults(ctx, id, false)
	collectRepoList(pkg, &pkg.vulnResults, "vuln scan results", id, vulns, err)

	analyses, _, err := e.db.ListAIAnalyses(ctx, id, "", false, bulkListLimit, 0)
	collectRepoList(pkg, &pkg.aiAnalyses, "ai analyses", id, analyses, err)
}

// writeListedTable writes a listed global table. A failed list call fails the
// backup: writing the table empty would report success for a partial archive.
func writeListedTable[T any](e *exporter, tw *tar.Writer, manifest *BackupManifest, name string, v []T, err error) error {
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", name, err)
	}

	return e.writeTable(tw, manifest, name, v, len(v))
}

// exportGlobalTables backs up tables with no repository scope.
func (e *exporter) exportGlobalTables(ctx context.Context, tw *tar.Writer, manifest *BackupManifest) error {
	sso, err := e.db.ListAllSSOProviders(ctx)
	for i, p := range sso {
		if p != nil {
			c := *p
			c.OIDCConfig = redactOIDCSecret(c.OIDCConfig)
			sso[i] = &c
		}
	}

	if err := writeListedTable(e, tw, manifest, "sso_providers", sso, err); err != nil {
		return err
	}

	tokens, _, err := e.db.ListAllTokens(ctx, 1, bulkListLimit)
	// Project tokens belong to service accounts that are not part of the
	// archive (users are exported without them); they are reissued after a restore.
	tokens = slices.DeleteFunc(tokens, func(t *models.Token) bool { return t.ProjectID != nil })

	if err := writeListedTable(e, tw, manifest, "tokens", tokens, err); err != nil {
		return err
	}

	backends, err := e.db.ListStorageBackends(ctx)
	for i, b := range backends {
		if b != nil {
			c := *b
			c.Config = redactedStorageConfig(c.Config)
			backends[i] = &c
		}
	}

	if err := writeListedTable(e, tw, manifest, "storage_backends", backends, err); err != nil {
		return err
	}

	pipelines, err := e.db.ListPromotionPipelines(ctx)
	if err := writeListedTable(e, tw, manifest, "promotion_pipelines", pipelines, err); err != nil {
		return err
	}

	if err := e.exportBundles(ctx, tw, manifest); err != nil {
		return err
	}

	return e.exportProjects(ctx, tw, manifest)
}

// exportBundles backs up release bundles with their items and promotions.
func (e *exporter) exportBundles(ctx context.Context, tw *tar.Writer, manifest *BackupManifest) error {
	bundles, _, err := e.db.ListReleaseBundles(ctx, "", "", 1, bulkListLimit)
	if err := writeListedTable(e, tw, manifest, "release_bundles", bundles, err); err != nil {
		return err
	}

	var bundleItems []*models.ReleaseBundleItem

	var bundlePromotions []*models.BundlePromotion

	for _, b := range bundles {
		items, err := e.db.ListBundleItems(ctx, b.ID)
		if err != nil {
			return fmt.Errorf("failed to list items of bundle %s: %w", b.ID, err)
		}

		bundleItems = append(bundleItems, items...)

		proms, err := e.db.ListBundlePromotions(ctx, b.ID)
		if err != nil {
			return fmt.Errorf("failed to list promotions of bundle %s: %w", b.ID, err)
		}

		bundlePromotions = append(bundlePromotions, proms...)
	}

	if err := e.writeTable(tw, manifest, "release_bundle_items", bundleItems, len(bundleItems)); err != nil {
		return err
	}

	return e.writeTable(tw, manifest, "bundle_promotions", bundlePromotions, len(bundlePromotions))
}

// exportProjects backs up projects, their members and their group grants.
func (e *exporter) exportProjects(ctx context.Context, tw *tar.Writer, manifest *BackupManifest) error {
	projects, err := e.db.ListAllProjects(ctx)
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	if err := e.writeTable(tw, manifest, "projects", projects, len(projects)); err != nil {
		return err
	}

	var (
		members  []*models.ProjectMember
		groups   []*models.ProjectGroup
		quotas   []*models.ProjectQuota
		policies []*models.CleanupPolicy
	)

	for _, project := range projects {
		m, err := e.db.ListProjectMembers(ctx, project.ID)
		if err != nil {
			return fmt.Errorf("failed to list members of project %s: %w", project.ID, err)
		}

		members = append(members, m...)

		g, err := e.db.ListProjectGroups(ctx, project.ID)
		if err != nil {
			return fmt.Errorf("failed to list groups of project %s: %w", project.ID, err)
		}

		groups = append(groups, g...)

		q, err := e.db.GetProjectQuota(ctx, project.ID)
		if err != nil {
			return fmt.Errorf("failed to get quota of project %s: %w", project.ID, err)
		}

		if q.Limited() {
			quotas = append(quotas, q) // counters are rebuilt after a restore
		}

		cp, err := e.db.ListProjectCleanupPolicies(ctx, project.ID)
		if err != nil {
			return fmt.Errorf("failed to list cleanup policies of project %s: %w", project.ID, err)
		}

		policies = append(policies, cp...)
	}

	if err := e.writeTable(tw, manifest, "project_members", members, len(members)); err != nil {
		return err
	}

	if err := e.writeTable(tw, manifest, "project_groups", groups, len(groups)); err != nil {
		return err
	}

	if err := e.writeTable(tw, manifest, "project_quotas", quotas, len(quotas)); err != nil {
		return err
	}

	return e.writeTable(tw, manifest, "project_cleanup_policies", policies, len(policies))
}

// writeTable marshals data as a JSON array under <name>.jsonl in the archive
// and records its row count in the manifest.
func (e *Engine) writeTable(tw *tar.Writer, manifest *BackupManifest, name string, data any, count int) error {
	if err := e.writeJSONLFile(tw, name+".jsonl", data); err != nil {
		return fmt.Errorf("failed to export %s: %w", name, err)
	}

	manifest.TableCounts[name] = count

	return nil
}

// writeJSONLFile writes a slice of objects as JSON lines to the archive.
func (e *Engine) writeJSONLFile(tw *tar.Writer, filename string, data any) error {
	// Marshal data to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	// Write to archive
	header := &tar.Header{
		Name: filename,
		Size: int64(len(jsonData)),
		Mode: 0644,
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	if _, err := tw.Write(jsonData); err != nil {
		return fmt.Errorf("failed to write content: %w", err)
	}

	return nil
}

// restoreState carries the id remaps built up while walking the archive:
// most Create* methods regenerate a server-side id (repositories, users,
// promotion pipelines, release bundles, projects all do), so every table that
// references one of those ids must be rewritten to point at the NEW id before
// it's restored. The package rows of a repository need no remap: their ids
// are derived from the new repository id (see database.RestorePackageRows).
type restoreState struct {
	repoIDMap     map[string]string             // old repository id -> new
	userIDMap     map[string]string             // old user id -> new
	keptUsers     map[string]struct{}           // old ids of users whose name already exists on the target
	pipelineIDMap map[string]string             // old promotion_pipelines id -> new
	bundleIDMap   map[string]string             // old release_bundles id -> new
	projectIDMap  map[string]string             // old projects id -> new
	groupIDMap    map[string]string             // old groups id -> new
	targetIDMap   map[string]string             // old permission_targets id -> new (or the existing same-named target)
	restoredRepos map[string]*models.Repository // old repository id -> the restored repository
	failures      int                           // records skipped/failed outside Engine methods
	// companionIDs are the archived ids of the companion rows (deb distributions
	// and components) restored per new repository id.
	companionIDs map[string][]string
	// mavenCoords are the group/artifact pairs of the restored packages of
	// repositories whose descriptor asks for maven metadata regeneration, by
	// new repository id.
	mavenCoords map[string]map[[2]string]struct{}
	// projectRepos are restored repositories that belonged to a non-system
	// project. Repositories are restored before projects, so they are inserted
	// into the system project and moved once the project exists
	// (restoreProjects).
	projectRepos []projectRepo
}

type projectRepo struct {
	repo         *models.Repository
	oldProjectID string
}

func newRestoreState() *restoreState {
	return &restoreState{
		repoIDMap:     map[string]string{},
		userIDMap:     map[string]string{},
		keptUsers:     map[string]struct{}{},
		pipelineIDMap: map[string]string{},
		bundleIDMap:   map[string]string{},
		projectIDMap:  map[string]string{},
		groupIDMap:    map[string]string{},
		targetIDMap:   map[string]string{},
		restoredRepos: map[string]*models.Repository{},
		companionIDs:  map[string][]string{},
		mavenCoords:   map[string]map[[2]string]struct{}{},
	}
}

// reconcileQuotaUsage recounts every repository's storage quota usage after a
// restore. Restored package rows are inserted directly, without reserving
// quota, and quota rows are not part of a backup, so enforcement would
// otherwise start from zero. Not fatal: the periodic reconcile would catch up.
func (e *Engine) reconcileQuotaUsage(ctx context.Context) {
	if _, err := e.db.ReconcileAllStorageUsage(ctx); err != nil {
		e.log.Warnw("failed to reconcile quota usage after restore", "error", err)
	}
}

// readManifest scans the archive for manifest.json (it is written last) and
// decodes it.
func readManifest(tarReader *tar.Reader) (BackupManifest, error) {
	var manifest BackupManifest

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return manifest, nil
		}

		if err != nil {
			return manifest, fmt.Errorf("failed to read tar header: %w", err)
		}

		if header.Name != "manifest.json" {
			continue
		}

		if header.Size < 0 || header.Size > maxManifestSize {
			return manifest, fmt.Errorf("manifest too large: %d bytes: %w", header.Size, ErrInvalidBlobEntry)
		}

		manifestData := make([]byte, header.Size)
		if _, err := io.ReadFull(tarReader, manifestData); err != nil {
			return manifest, fmt.Errorf("failed to read manifest: %w", err)
		}

		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			return manifest, fmt.Errorf("failed to unmarshal manifest: %w", err)
		}

		return manifest, nil
	}
}

// RunRestore restores a backup from an archive. Only one restore runs at a
// time (ErrRestoreInProgress otherwise): restores create rows with server-side
// ids and keep per-run state, so two interleaved runs would corrupt each
// other's remaps and failure accounting.
func (e *Engine) RunRestore(ctx context.Context, backupPath string) error {
	if !e.opMu.TryLock() {
		return ErrRestoreInProgress
	}
	defer e.opMu.Unlock()

	e.restoring.Store(true)
	defer e.restoring.Store(false)

	// Open the archive
	file, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("failed to open backup file: %w", err)
	}
	defer func() { base.CloseLog(file, e.log) }()

	// Create gzip reader
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer func() { base.CloseLog(gzipReader, e.log) }()

	// Create tar reader
	tarReader := tar.NewReader(gzipReader)

	manifest, err := readManifest(tarReader)
	if err != nil {
		return err
	}

	if manifest.Version != ArchiveVersion {
		return fmt.Errorf("%w: archive version %q, this server restores %q", ErrUnsupportedArchive, manifest.Version, ArchiveVersion)
	}

	// Reset tar reader
	_, _ = file.Seek(0, 0)

	gzipReader, err = gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}

	defer func() { base.CloseLog(gzipReader, e.log) }()

	tarReader = tar.NewReader(gzipReader)

	st := newRestoreState()
	e.restoreFailures.Store(0)
	e.restoreSkips.Store(0)

	// Process files. Order matters: exportMetadata always writes
	// repositories/users/groups first, then every repository's package rows
	// (companions, packages, versions, files, side tables, tags), then
	// promotion_pipelines before release_bundles before
	// release_bundle_items/bundle_promotions, then projects before
	// project_members and project_groups — the restore steps rely on that order to have the id remaps
	// they need already populated in st.
	steps := e.restoreSteps()

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		if strings.HasPrefix(header.Name, "blobs/") {
			if err := e.restoreBlob(ctx, tarReader, header); err != nil {
				e.restoreWarn("failed to restore blob", "name", header.Name, "error", err)
			}

			continue
		}

		if strings.HasPrefix(header.Name, packageRowsDir) {
			if err := e.restorePackageRows(ctx, tarReader, header.Name, st); err != nil {
				return fmt.Errorf("failed to restore %s: %w", header.Name, err)
			}

			continue
		}

		step := steps[header.Name]
		if step == nil {
			continue
		}

		if err := step(ctx, tarReader, st); err != nil {
			return fmt.Errorf("failed to restore %s: %w", header.Name, err)
		}
	}

	e.regenerateRestoredMetadata(ctx, st)
	e.reconcileQuotaUsage(ctx)

	if failed := e.restoreFailures.Load() + int64(st.failures); failed > 0 {
		e.log.Errorw("restore completed with failures", "backup_path", backupPath, "failed", failed)
		return fmt.Errorf("%w: %d records/blobs failed or were skipped", ErrRestoreIncomplete, failed)
	}

	e.log.Infow("restore completed successfully", "backup_path", backupPath, "skipped_existing", e.restoreSkips.Load())

	return nil
}

// restoreBlob writes a backed-up blob back into the blob store under the
// same content-addressed key it had before backup (the tar entry name IS
// that key — see exportOneBlob).
func (e *Engine) restoreBlob(ctx context.Context, r io.Reader, header *tar.Header) error {
	blobID := strings.TrimPrefix(header.Name, "blobs/")
	if err := storage.ValidateBlobID(blobID); err != nil {
		return fmt.Errorf("%w: %q: %w", ErrInvalidBlobEntry, header.Name, err)
	}

	if header.Size < 0 || header.Size > maxBlobEntrySize {
		return fmt.Errorf("%w: %q: size %d exceeds %d bytes", ErrInvalidBlobEntry, header.Name, header.Size, int64(maxBlobEntrySize))
	}

	// Spool to disk (never into memory) while hashing so the content can be
	// verified against its content-addressed name before it is stored.
	spool, err := os.CreateTemp("", "restore-blob-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create blob spool file: %w", err)
	}

	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(spool, hasher), io.LimitReader(ctxReader{ctx: ctx, r: r}, header.Size)); err != nil {
		return fmt.Errorf("failed to read blob %s: %w", blobID, err)
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != blobID {
		return fmt.Errorf("%w: digest mismatch for %s (content hashes to %s)", ErrInvalidBlobEntry, blobID, got)
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind blob spool: %w", err)
	}

	if _, err := e.blobStore.Put(ctx, spool, map[string]string{"sha256": blobID}); err != nil {
		return fmt.Errorf("failed to store blob %s: %w", blobID, err)
	}

	return nil
}

// restoreFunc restores the rows of one backed-up jsonl file.
type restoreFunc func(ctx context.Context, r io.Reader, st *restoreState) error

// restoreSteps maps every backed-up jsonl file to the step that restores it.
// Files it does not list are ignored: manifest.json, which is read up front,
// and files a newer backup wrote that this version cannot restore.
func (e *Engine) restoreSteps() map[string]restoreFunc {
	return map[string]restoreFunc{
		"repositories.jsonl":             e.restoreRepositories,
		"users.jsonl":                    e.restoreUsers,
		"groups.jsonl":                   e.restoreGroups,
		"repository_group_members.jsonl": e.restoreGroupMembers,
		"permission_targets.jsonl":       e.restorePermissionTargets,
		"permission_assignments.jsonl":   e.restorePermissionAssignments,
		"user_groups.jsonl":              e.restoreUserGroups,
		"storage_quotas.jsonl":           e.restoreStorageQuotas,
		"docker_blobs.jsonl": repoScoped(e.log, "docker blob",
			func(c *models.DockerBlob) *string { return &c.RepositoryID }, false, e.db.CreateDockerBlob),
		"docker_manifests.jsonl": repoScoped(e.log, "docker manifest",
			func(c *models.DockerManifest) *string { return &c.RepositoryID }, false, e.restoreDockerManifest),
		// docker_tags.manifest_digest references docker_manifests, restored
		// just above (exportMetadata writes docker_manifests before
		// docker_tags) — a tag whose manifest genuinely failed to restore
		// still fails its FK check here, logged per-row by repoScoped,
		// non-fatal to the rest of the restore.
		"docker_tags.jsonl": repoScoped(e.log, "docker tag",
			func(c *models.DockerTag) *string { return &c.RepositoryID }, false, e.db.UpsertDockerTag),
		"webhooks.jsonl": repoScoped(e.log, "webhook",
			func(c *models.Webhook) *string { return &c.RepositoryID }, true, e.db.CreateWebhook),
		"cleanup_policies.jsonl": repoScoped(e.log, "cleanup policy",
			func(c *models.CleanupPolicy) *string { return &c.RepositoryID }, false, e.db.CreateCleanupPolicy),
		"sbom_reports.jsonl": repoScoped(e.log, "sbom report",
			func(c *models.SBOMReport) *string { return &c.RepositoryID }, false, e.db.CreateSBOMReport),
		"vex_statements.jsonl": repoScoped(e.log, "vex statement",
			func(c *models.VEXStatement) *string { return &c.RepositoryID }, false, e.db.UpsertVEXStatement),
		"license_scan_results.jsonl": repoScoped(e.log, "license scan result",
			func(c *models.LicenseScanResult) *string { return &c.RepositoryID }, false, e.db.CreateLicenseScanResult),
		"secret_scan_results.jsonl": repoScoped(e.log, "secret scan result",
			func(c *models.SecretScanResult) *string { return &c.RepositoryID }, false, e.db.CreateSecretScanResult),
		"vuln_scan_results.jsonl": repoScoped(e.log, "vuln scan result",
			func(c *models.VulnScanResult) *string { return &c.RepositoryID }, false, e.db.CreateVulnScanResult),
		"ai_analyses.jsonl": repoScoped(e.log, "ai analysis",
			func(c *models.AIAnalysis) *string { return &c.RepositoryID }, false, e.db.UpsertAIAnalysis),
		"sso_providers.jsonl":            e.restoreSSOProviders,
		"tokens.jsonl":                   e.restoreTokens,
		"storage_backends.jsonl":         e.restoreStorageBackends,
		"promotion_pipelines.jsonl":      e.restorePromotionPipelines,
		"release_bundles.jsonl":          e.restoreReleaseBundles,
		"release_bundle_items.jsonl":     e.restoreReleaseBundleItems,
		"bundle_promotions.jsonl":        e.restoreBundlePromotions,
		"projects.jsonl":                 e.restoreProjects,
		"project_members.jsonl":          e.restoreProjectMembers,
		"project_quotas.jsonl":           e.restoreProjectQuotas,
		"project_cleanup_policies.jsonl": e.restoreProjectCleanupPolicies,
		"project_groups.jsonl":           e.restoreProjectGroups,
	}
}

// decodeSlice reads a JSON array of T from r. A JSON null element decodes to a
// nil pointer for pointer element types; those are dropped here so no restore
// step dereferences one (a tampered or truncated archive must not panic the
// restore half-applied).
func decodeSlice[T any](r io.Reader) ([]T, error) {
	var items []T
	if err := json.NewDecoder(io.LimitReader(r, maxTableSize)).Decode(&items); err != nil {
		return nil, fmt.Errorf("failed to decode: %w", err)
	}

	return slices.DeleteFunc(items, func(item T) bool {
		v := reflect.ValueOf(item)

		return v.Kind() == reflect.Pointer && v.IsNil()
	}), nil
}

// repoScoped returns the restore step of a table whose rows each belong to one
// repository: it decodes a slice of T and, for every item, remaps the
// repository id that repoID points at before calling create — skipping (with a
// warning) any item whose source repository wasn't restored. If allowGlobal is
// true, an item with an empty repository id (e.g. a global webhook) is created
// as-is instead of being skipped.
func repoScoped[T any](
	log *logger.Logger,
	label string,
	repoID func(T) *string,
	allowGlobal bool,
	create func(context.Context, T) error,
) restoreFunc {
	return func(ctx context.Context, r io.Reader, st *restoreState) error {
		items, err := decodeSlice[T](r)
		if err != nil {
			return fmt.Errorf("failed to decode %s: %w", label, err)
		}

		for _, item := range items {
			id := repoID(item)

			if *id == "" && allowGlobal {
				if err := create(ctx, item); err != nil {
					st.failures++
					log.Warnw("failed to restore item", "table", label, "error", err)
				}

				continue
			}

			newRepoID, ok := st.repoIDMap[*id]
			if !ok {
				st.failures++
				log.Warnw("skipping item: source repository not restored", "table", label, "repo_id", *id)
				continue
			}

			*id = newRepoID

			if err := create(ctx, item); err != nil {
				st.failures++
				log.Warnw("failed to restore item", "table", label, "error", err)
			}
		}

		return nil
	}
}

// restoreRepositories restores repositories from JSON data. CreateRepository
// always generates a fresh server-side id (see postgres.go), so every
// subsequent table referencing a repository must go through repoIDMap.
func (e *Engine) restoreRepositories(ctx context.Context, r io.Reader, st *restoreState) error {
	repos, err := decodeSlice[*models.Repository](r)
	if err != nil {
		return fmt.Errorf("failed to decode repositories: %w", err)
	}

	for _, repo := range repos {
		oldID := repo.ID

		// These names shadow the SPA pages and the isolated /repos/<marker>/ routes.
		if repotype.ReservedName(repo.Name) {
			e.restoreWarn("skipping repository with a reserved name", "name", repo.Name)
			continue
		}

		oldProjectID := ""
		if repo.ProjectID != nil && *repo.ProjectID != models.SystemProjectID {
			oldProjectID = *repo.ProjectID
			sys := models.SystemProjectID
			repo.ProjectID = &sys
		}

		if err := e.db.CreateRepository(ctx, repo); err != nil {
			e.restoreWarn("failed to restore repository", "name", repo.Name, "error", err)
			continue
		}

		st.repoIDMap[oldID] = repo.ID

		st.restoredRepos[oldID] = repo

		if oldProjectID != "" {
			st.projectRepos = append(st.projectRepos, projectRepo{repo: repo, oldProjectID: oldProjectID})
		}
	}

	return nil
}

// restoreUsers restores users from JSON data. CreateUser always generates a
// fresh server-side id, needed downstream for tokens.user_id and
// projects.owner_id/project_members.user_id.
func (e *Engine) restoreUsers(ctx context.Context, r io.Reader, st *restoreState) error {
	users, err := decodeSlice[*models.User](r)
	if err != nil {
		return fmt.Errorf("failed to decode users: %w", err)
	}

	for _, user := range users {
		oldID := user.ID
		if _, err := e.db.CreateUser(ctx, user); err != nil {
			if database.IsUniqueViolation(err) {
				// A fresh target already has its bootstrap admin: keep that
				// account; what depends on the archive's one is skipped with it.
				e.restoreSkip("user already exists, keeping the existing one", "username", user.Username)

				st.keptUsers[oldID] = struct{}{}

				continue
			}

			e.restoreWarn("failed to restore user", "username", user.Username, "error", err)
			continue
		}

		st.userIDMap[oldID] = user.ID
	}

	return nil
}

// restoreGroups restores groups from JSON data.
func (e *Engine) restoreGroups(ctx context.Context, r io.Reader, st *restoreState) error {
	groups, err := decodeSlice[*models.Group](r)
	if err != nil {
		return fmt.Errorf("failed to decode groups: %w", err)
	}

	for _, group := range groups {
		oldID := group.ID

		created, err := e.db.CreateGroup(ctx, group)
		if err != nil {
			e.restoreWarn("failed to restore group", "name", group.Name, "error", err)
			continue
		}

		if created != nil {
			st.groupIDMap[oldID] = created.ID
		}
	}

	return nil
}

// restoreGroupMembers re-adds the ordered member lists of group repositories.
// Both ends are remapped through repoIDMap; a member whose repository was not
// restored is counted as a failure rather than silently shortening the group.
func (e *Engine) restoreGroupMembers(ctx context.Context, r io.Reader, st *restoreState) error {
	records, err := decodeSlice[groupMemberRecord](r)
	if err != nil {
		return fmt.Errorf("failed to decode repository group members: %w", err)
	}

	for _, rec := range records {
		groupID, gok := st.repoIDMap[rec.GroupRepoID]
		memberID, mok := st.repoIDMap[rec.MemberRepoID]

		if !gok || !mok {
			e.restoreWarn(
				"skipping group member: repository not restored",
				"group_repo_id",
				rec.GroupRepoID,
				"member_repo_id",
				rec.MemberRepoID,
			)
			continue
		}

		if err := e.db.AddGroupMember(ctx, groupID, memberID, rec.Priority); err != nil {
			e.restoreWarn(
				"failed to restore group member",
				"group_repo_id",
				rec.GroupRepoID,
				"member_repo_id",
				rec.MemberRepoID,
				"error",
				err,
			)
		}
	}

	return nil
}

// linkRowBlob prepares a restored row that references a blob: it rejects an
// empty sha256 and sets *blobID to the blobs-table id that ensure (EnsureBlob
// or UpsertBlob, which take the blob's name and size) returns for it. what names
// the row kind in errors.
func linkRowBlob(
	ctx context.Context,
	what, sha256 string,
	blobID *string,
	name string,
	size int64,
	ensure func(ctx context.Context, sha256, name string, size int64) (string, error),
) error {
	if sha256 == "" {
		return fmt.Errorf("%w: %s", ErrMissingBlobSHA256, what)
	}

	id, err := ensure(ctx, sha256, name, size)
	if err != nil {
		return fmt.Errorf("upsert blob for %s: %w", what, err)
	}

	*blobID = id

	return nil
}

// restoreDockerManifest re-derives the manifest's blob via EnsureBlob (the
// blob content itself lands separately via restoreBlob) since
// CreateDockerManifest expects BlobID to already be a blobs-table id, not a
// sha256. Used as repoScoped's create callback for docker_manifests.
func (e *Engine) restoreDockerManifest(ctx context.Context, m *models.DockerManifest) error {
	if err := linkRowBlob(ctx, "docker manifest", m.BlobSHA256, &m.BlobID, m.MediaType, m.Size, e.db.EnsureBlob); err != nil {
		return err
	}

	return e.db.CreateDockerManifest(ctx, m)
}

func (e *Engine) restoreSSOProviders(ctx context.Context, r io.Reader, _ *restoreState) error {
	items, err := decodeSlice[*models.SSOProvider](r)
	if err != nil {
		return fmt.Errorf("failed to decode sso providers: %w", err)
	}

	for _, it := range items {
		if err := e.db.CreateSSOProvider(ctx, it); err != nil {
			e.restoreWarn("failed to restore sso provider", "slug", it.Slug, "error", err)
		}
	}

	return nil
}

// restoreTokens remaps UserID (a raw FK, unlike the username-resolved
// creator/promoter/adder fields elsewhere) via userIDMap.
func (e *Engine) restoreTokens(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.Token](r)
	if err != nil {
		return fmt.Errorf("failed to decode tokens: %w", err)
	}

	for _, it := range items {
		newUserID, ok := st.userIDMap[it.UserID]
		if !ok {
			e.restoreMissingUser(st, it.UserID, "skipping token: source user not restored", "user_id", it.UserID, "name", it.Name)
			continue
		}

		it.UserID = newUserID

		if _, err := e.db.CreateToken(ctx, it); err != nil {
			e.restoreWarn("failed to restore token", "name", it.Name, "error", err)
		}
	}

	return nil
}

func (e *Engine) restoreStorageBackends(ctx context.Context, r io.Reader, _ *restoreState) error {
	items, err := decodeSlice[*models.StorageBackend](r)
	if err != nil {
		return fmt.Errorf("failed to decode storage backends: %w", err)
	}

	for _, it := range items {
		if err := e.db.CreateStorageBackend(ctx, it); err != nil {
			if database.IsUniqueViolation(err) {
				// The migrations seed a backend (local); a fresh target already
				// has it, which is not a restore failure.
				e.log.Infow("storage backend already exists, keeping the existing one", "name", it.Name)
				continue
			}

			e.restoreWarn("failed to restore storage backend", "name", it.Name, "error", err)
		}
	}

	return nil
}

// restorePromotionPipelines remaps each pipeline stage's RepositoryID on a
// best-effort basis: stages are opaque JSONB with no live FK, so a stale id
// (source repo not restored) is left as-is rather than blocking the restore.
func (e *Engine) restorePromotionPipelines(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.PromotionPipeline](r)
	if err != nil {
		return fmt.Errorf("failed to decode promotion pipelines: %w", err)
	}

	for _, it := range items {
		oldID := it.ID

		for i := range it.Stages {
			if newRepoID, ok := st.repoIDMap[it.Stages[i].RepositoryID]; ok {
				it.Stages[i].RepositoryID = newRepoID
			}
		}

		if err := e.db.CreatePromotionPipeline(ctx, it); err != nil {
			e.restoreWarn("failed to restore promotion pipeline", "name", it.Name, "error", err)
			continue
		}

		st.pipelineIDMap[oldID] = it.ID
	}

	return nil
}

func (e *Engine) restoreReleaseBundles(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.ReleaseBundle](r)
	if err != nil {
		return fmt.Errorf("failed to decode release bundles: %w", err)
	}

	for _, it := range items {
		oldID := it.ID

		if it.PipelineID != nil {
			if newPipelineID, ok := st.pipelineIDMap[*it.PipelineID]; ok {
				it.PipelineID = &newPipelineID
			} else {
				it.PipelineID = nil
			}
		}

		if err := e.db.CreateReleaseBundle(ctx, it); err != nil {
			e.restoreWarn("failed to restore release bundle", "name", it.Name, "version", it.Version, "error", err)
			continue
		}

		st.bundleIDMap[oldID] = it.ID
	}

	return nil
}

func (e *Engine) restoreReleaseBundleItems(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.ReleaseBundleItem](r)
	if err != nil {
		return fmt.Errorf("failed to decode release bundle items: %w", err)
	}

	for _, it := range items {
		newBundleID, ok := st.bundleIDMap[it.BundleID]
		if !ok {
			e.restoreWarn("skipping release bundle item: source bundle not restored", "bundle_id", it.BundleID, "sha256", it.SHA256)
			continue
		}

		newRepoID, ok := st.repoIDMap[it.SourceRepoID]
		if !ok {
			e.restoreWarn("skipping release bundle item: source repository not restored", "repo_id", it.SourceRepoID, "sha256", it.SHA256)
			continue
		}

		it.BundleID = newBundleID
		it.SourceRepoID = newRepoID

		if err := e.db.AddBundleItem(ctx, it); err != nil {
			e.restoreWarn("failed to restore release bundle item", "sha256", it.SHA256, "error", err)
		}
	}

	return nil
}

func (e *Engine) restoreBundlePromotions(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.BundlePromotion](r)
	if err != nil {
		return fmt.Errorf("failed to decode bundle promotions: %w", err)
	}

	for _, it := range items {
		newBundleID, ok := st.bundleIDMap[it.BundleID]
		if !ok {
			e.restoreWarn("skipping bundle promotion: source bundle not restored", "bundle_id", it.BundleID)
			continue
		}

		newRepoID, ok := st.repoIDMap[it.TargetRepoID]
		if !ok {
			e.restoreWarn("skipping bundle promotion: target repository not restored", "repo_id", it.TargetRepoID)
			continue
		}

		it.BundleID = newBundleID
		it.TargetRepoID = newRepoID

		if err := e.db.CreateBundlePromotion(ctx, it); err != nil {
			e.restoreWarn("failed to restore bundle promotion", "error", err)
		}
	}

	return nil
}

func (e *Engine) restoreProjects(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.Project](r)
	if err != nil {
		return fmt.Errorf("failed to decode projects: %w", err)
	}

	e.createRestoredProjects(ctx, items, st)

	return nil
}

// systemProjectName is the name of the built-in system project.
const systemProjectName = "system"

func (e *Engine) createRestoredProjects(ctx context.Context, items []*models.Project, st *restoreState) {
	for _, it := range items {
		oldID := it.ID

		if it.OwnerID != nil {
			if newOwnerID, ok := st.userIDMap[*it.OwnerID]; ok {
				it.OwnerID = &newOwnerID
			} else {
				it.OwnerID = nil
			}
		}

		// The built-in system project exists in every database and grants
		// nothing: map it to itself instead of recreating it.
		if oldID == models.SystemProjectID || it.Name == systemProjectName {
			st.projectIDMap[oldID] = models.SystemProjectID
			continue
		}

		if err := e.db.CreateProject(ctx, it); err != nil {
			e.restoreWarn("failed to restore project", "name", it.Name, "error", err)
			continue
		}

		st.projectIDMap[oldID] = it.ID
	}

	e.moveRestoredReposToProjects(ctx, st)
}

// moveRestoredReposToProjects puts restored repositories back into their
// project. One whose project was not restored (for example the name already
// exists) stays in the system project, which grants nothing.
func (e *Engine) moveRestoredReposToProjects(ctx context.Context, st *restoreState) {
	for _, it := range st.projectRepos {
		newProjectID, ok := st.projectIDMap[it.oldProjectID]
		if !ok {
			e.restoreWarn("repository stays in the system project: its project was not restored", "repository", it.repo.Name)
			continue
		}

		it.repo.ProjectID = &newProjectID
		if err := e.db.UpdateRepository(ctx, it.repo); err != nil {
			e.restoreWarn("failed to move restored repository into its project", "repository", it.repo.Name, "error", err)
		}
	}

	st.projectRepos = nil
}

func (e *Engine) restoreProjectMembers(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.ProjectMember](r)
	if err != nil {
		return fmt.Errorf("failed to decode project members: %w", err)
	}

	for _, it := range items {
		e.restoreProjectMember(ctx, st, it.ProjectID, it.UserID, it.Role)
	}

	return nil
}

func (e *Engine) restoreProjectMember(ctx context.Context, st *restoreState, oldProjectID, oldUserID, role string) {
	newProjectID, ok := st.projectIDMap[oldProjectID]
	if !ok {
		e.restoreWarn("skipping project member: source project not restored", "project_id", oldProjectID)
		return
	}

	if newProjectID == models.SystemProjectID {
		return // the system project's membership grants nothing
	}

	newUserID, ok := st.userIDMap[oldUserID]
	if !ok {
		e.restoreMissingUser(st, oldUserID, "skipping project member: source user not restored", "user_id", oldUserID)
		return
	}

	if err := e.db.AddProjectMember(ctx, newProjectID, newUserID, role); err != nil {
		e.restoreWarn("failed to restore project member", "project_id", newProjectID, "user_id", newUserID, "error", err)
	}
}

// restoreProjectQuotas restores the project storage ceilings; the usage
// counters are rebuilt from the repositories by reconcileQuotaUsage.
func (e *Engine) restoreProjectQuotas(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.ProjectQuota](r)
	if err != nil {
		return fmt.Errorf("failed to decode project quotas: %w", err)
	}

	for _, it := range items {
		newProjectID, ok := st.projectIDMap[it.ProjectID]
		if !ok || newProjectID == models.SystemProjectID {
			continue
		}

		if _, err := e.db.SetProjectQuota(ctx, newProjectID, it.MaxSizeBytes, it.MaxArtifactCount, it.WarnThresholdPercent); err != nil {
			e.restoreWarn("failed to restore project quota", "project_id", newProjectID, "error", err)
		}
	}

	return nil
}

// restoreProjectCleanupPolicies restores the project default cleanup policies.
func (e *Engine) restoreProjectCleanupPolicies(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.CleanupPolicy](r)
	if err != nil {
		return fmt.Errorf("failed to decode project cleanup policies: %w", err)
	}

	for _, it := range items {
		newProjectID, ok := st.projectIDMap[it.ProjectID]
		if !ok || newProjectID == models.SystemProjectID {
			continue
		}

		it.ProjectID = newProjectID
		it.RepositoryID = ""

		if err := e.db.CreateCleanupPolicy(ctx, it); err != nil {
			e.restoreWarn("failed to restore project cleanup policy", "project_id", newProjectID, "error", err)
		}
	}

	return nil
}

func (e *Engine) restoreProjectGroups(ctx context.Context, r io.Reader, st *restoreState) error {
	items, err := decodeSlice[*models.ProjectGroup](r)
	if err != nil {
		return fmt.Errorf("failed to decode project groups: %w", err)
	}

	for _, it := range items {
		newProjectID, ok := st.projectIDMap[it.ProjectID]
		if !ok {
			e.restoreWarn("skipping project group: source project not restored", "project_id", it.ProjectID)
			continue
		}

		if newProjectID == models.SystemProjectID {
			continue // the system project's grants are nothing
		}

		newGroupID, ok := st.groupIDMap[it.GroupID]
		if !ok {
			e.restoreWarn("skipping project group: source group not restored", "group_id", it.GroupID)
			continue
		}

		var createdBy *string

		if it.CreatedBy != nil {
			if id, ok := st.userIDMap[*it.CreatedBy]; ok {
				createdBy = &id
			}
		}

		if err := e.db.SetProjectGroup(ctx, newProjectID, newGroupID, it.Role, createdBy); err != nil {
			e.restoreWarn("failed to restore project group", "project_id", newProjectID, "group_id", newGroupID, "error", err)
		}
	}

	return nil
}

// exportFailureReason is the reason stored with (and shown for) a failed
// export: the fixed text of the failure's class, never the error itself, which
// carries table names, SQL, blob ids and filesystem paths. The log has the cause.
func exportFailureReason(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "backup was interrupted"
	}

	for _, class := range []error{
		ErrBlobMissing, ErrBlobCorrupt, ErrBlobCountMismatch, errSnapshotStage, errMetadataStage, errBlobsStage,
	} {
		if errors.Is(err, class) {
			return class.Error()
		}
	}

	return "failed to export the backup data"
}
