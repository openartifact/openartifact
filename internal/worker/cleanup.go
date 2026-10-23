// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/cron"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Sentinel errors for cleanup worker.
var (
	ErrRepositoryNotFound = errors.New("repository not found")
	// ErrPolicyNotFound is returned by TriggerPolicy for an unknown policy ID.
	ErrPolicyNotFound = errors.New("cleanup policy not found")
	// ErrPolicyBusy is returned when the policy is already being executed.
	ErrPolicyBusy = errors.New("cleanup policy is already running")
)

const (
	policyRunTimeout    = 30 * time.Minute
	policyRecordTimeout = 15 * time.Second
	fallbackInterval    = 24 * time.Hour

	// orphanInterval throttles orphan-blob collection: the unreferenced-blob
	// scan counts references across every package table, which is too heavy
	// to repeat on each one-minute tick.
	orphanInterval = 15 * time.Minute

	// orphanMaxWindows bounds one orphan sweep to this many scan windows
	// (storage.OrphanScanWindow blobs each); a larger table is continued by the
	// next sweep from where this one stopped.
	orphanMaxWindows = 50

	// quotaReconcileInterval throttles the recount of every repository's quota
	// counters from its package rows (database.ReconcileAllStorageUsage). The
	// counters are written with asynchronous commits, so a database crash can
	// leave them off; this is what brings them back. The first run happens on
	// the first tick after startup, which also repairs a crash right before it.
	quotaReconcileInterval = 6 * time.Hour
)

type CleanupWorker struct {
	db        database.Database
	blobStore storage.BlobStore
	log       *logger.Logger
	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}

	runCtx context.Context //nolint:containedctx // lifetime of the worker, cancelled by Stop
	cancel context.CancelFunc
	wg     sync.WaitGroup // manually triggered policy runs

	rpmNotifier, debNotifier MetadataNotifier

	lastOrphanGC         time.Time // only touched from the tick goroutine
	lastQuotaReconcileAt time.Time // only touched from the tick goroutine
}

func NewCleanupWorker(db database.Database, blobStore storage.BlobStore, log *logger.Logger) *CleanupWorker {
	runCtx, cancel := context.WithCancel(context.Background())

	return &CleanupWorker{
		db:        db,
		blobStore: blobStore,
		log:       log,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		runCtx:    runCtx,
		cancel:    cancel,
	}
}

// Start begins the schedule. Only the first call counts.
func (w *CleanupWorker) Start() {
	w.startOnce.Do(w.start)
}

func (w *CleanupWorker) start() {
	go func() {
		defer close(w.doneCh)

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				w.safeCycle()
			case <-w.stopCh:
				return
			}
		}
	}()
}

// safeCycle runs one cleanup cycle and contains a panic to that cycle: the
// schedule (policies, orphan GC, quota reconcile) must keep ticking.
func (w *CleanupWorker) safeCycle() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in cleanup cycle; continuing", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.runCtx, policyRunTimeout)
	defer cancel()

	w.runCleanupCycle(ctx)
}

// Stop is idempotent and safe without Start.
func (w *CleanupWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.cancel()
	})

	// Never started: nothing will close doneCh, and a later Start must not.
	w.startOnce.Do(func() { close(w.doneCh) })

	<-w.doneCh
	w.wg.Wait()
}

func (w *CleanupWorker) runCleanupCycle(ctx context.Context) {
	w.runDuePolicies(ctx)
	now := time.Now()

	w.collectOrphansIfDue(ctx, now)
	w.reconcileQuotasIfDue(ctx, now)
}

// reconcileQuotasIfDue recounts every repository's quota usage from its package
// rows, at most once per quotaReconcileInterval. Concurrent replicas are
// serialized by the database call, so running it on each is harmless.
func (w *CleanupWorker) reconcileQuotasIfDue(ctx context.Context, now time.Time) {
	if ctx.Err() != nil || now.Sub(w.lastQuotaReconcileAt) < quotaReconcileInterval {
		return
	}

	w.lastQuotaReconcileAt = now

	corrected, err := w.db.ReconcileAllStorageUsage(ctx)
	if err != nil {
		w.log.Warnw("quota usage reconcile failed", "error", err)

		return
	}

	if corrected > 0 {
		w.log.Infow("corrected drifted quota usage counters", "repositories", corrected)
	}
}

// collectOrphansIfDue reclaims blobs that no package row references any more
// (storage.OrphanMinAge old, so in-flight uploads are never touched), at most
// once per orphanInterval. Repository deletes also trigger a pass of their own;
// this one catches what those missed (crash, storage error) and orphans left
// by package deletes whose inline cleanup failed.
func (w *CleanupWorker) collectOrphansIfDue(ctx context.Context, now time.Time) {
	if w.blobStore == nil || ctx.Err() != nil || now.Sub(w.lastOrphanGC) < orphanInterval {
		return
	}

	w.lastOrphanGC = now

	// Every replica ticks; the scan is expensive, so only one runs it at a time.
	if w.db.GetDB() != nil {
		release, err := w.acquirePolicyLock(ctx, "orphan-blob-gc")
		if err != nil {
			if !errors.Is(err, ErrPolicyBusy) {
				w.log.Warnw("orphan blob collection: could not take lock", "error", err)
			}

			return
		}
		defer release()
	}

	removed, err := storage.CollectOrphanBlobsSweep(ctx, w.db, w.blobStore, storage.OrphanMinAge, storage.OrphanBatch, orphanMaxWindows)
	if err != nil {
		w.log.Warnw("orphan blob collection failed", "removed", removed, "error", err)
	}

	if removed > 0 {
		w.log.Infow("collected orphan blobs", "removed", removed)
	}
}

func (w *CleanupWorker) runDuePolicies(ctx context.Context) {
	policies, err := w.db.ListEnabledCleanupPolicies(ctx)
	if err != nil {
		w.log.Errorw("failed to list enabled cleanup policies", "error", err)
		return
	}

	for _, policy := range policies {
		if ctx.Err() != nil {
			return
		}

		if w.shouldRunPolicy(policy) {
			w.executePolicy(ctx, policy)
		}
	}
}

func (w *CleanupWorker) shouldRunPolicy(policy *models.CleanupPolicy) bool {
	return w.shouldRunPolicyAt(policy, time.Now())
}

// shouldRunPolicyAt reports whether the policy's cron schedule has fired since
// its last run (or since its creation if it never ran). A policy whose stored
// schedule cannot be parsed falls back to a 24h interval.
func (w *CleanupWorker) shouldRunPolicyAt(policy *models.CleanupPolicy, now time.Time) bool {
	expr := policy.ScheduleCron
	if expr == "" {
		expr = models.DefaultCleanupCron
	}

	base := policy.CreatedAt
	if policy.LastRunAt != nil {
		base = *policy.LastRunAt
	}

	sched, err := cron.Parse(expr)
	if err != nil {
		if w.log != nil {
			w.log.Warnw("invalid cleanup schedule, falling back to daily interval", "policy_id", policy.ID, "schedule_cron", expr, "error", err)
		}

		return policy.LastRunAt == nil || now.Sub(*policy.LastRunAt) > fallbackInterval
	}

	next := sched.Next(base)

	return !next.IsZero() && !now.Before(next)
}

// acquirePolicyLock takes a session-level advisory lock for the policy on a
// dedicated connection, so lock and unlock are guaranteed to run on the same
// session. The returned release func unlocks and returns the connection.
func (w *CleanupWorker) acquirePolicyLock(ctx context.Context, policyID string) (func(), error) {
	conn, err := w.db.GetDB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection for cleanup lock: %w", err)
	}

	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", "cleanup:"+policyID).Scan(&locked); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to acquire cleanup lock: %w", err)
	}

	if !locked {
		_ = conn.Close()
		return nil, ErrPolicyBusy
	}

	return func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), policyRecordTimeout)
		defer cancel()

		if _, err := conn.ExecContext(uctx, "SELECT pg_advisory_unlock(hashtext($1))", "cleanup:"+policyID); err != nil {
			// Closing the session releases session-level advisory locks as well; make
			// sure the pooled connection is not reused with the lock still held.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}

		_ = conn.Close()
	}, nil
}

func (w *CleanupWorker) executePolicy(ctx context.Context, policy *models.CleanupPolicy) {
	release, err := w.acquirePolicyLock(ctx, policy.ID)
	if err != nil {
		w.log.Debugw("could not acquire lock for cleanup policy", "policy_id", policy.ID, "error", err)
		return
	}
	defer release()

	w.runAndRecord(ctx, policy)
}

// runAndRecord executes the policy and persists the outcome. The caller must
// hold the policy lock.
func (w *CleanupWorker) runAndRecord(ctx context.Context, policy *models.CleanupPolicy) {
	deletedCount, runErr := w.runPolicy(ctx, policy)

	// Persist the result even when ctx expired or was cancelled mid-run.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), policyRecordTimeout)
	defer cancel()

	if err := w.db.UpdateCleanupPolicyRunResult(rctx, policy.ID, deletedCount, runErr); err != nil {
		w.log.Errorw("failed to update cleanup policy run result", "policy_id", policy.ID, "error", err)
	}

	if runErr != nil {
		w.log.Errorw("cleanup policy execution failed", "policy_id", policy.ID, "policy_name", policy.Name, "error", runErr)
	} else {
		w.log.Infow("cleanup policy executed successfully", "policy_id", policy.ID, "policy_name", policy.Name, "deleted", deletedCount)
	}
}

// TriggerPolicy starts an immediate asynchronous run of the given policy
// (regardless of schedule or enabled flag). It returns ErrPolicyNotFound for an
// unknown policy and ErrPolicyBusy if the policy is already running; the
// outcome is recorded in the policy's last_run_* fields.
func (w *CleanupWorker) TriggerPolicy(ctx context.Context, policyID string) error {
	policy, err := w.db.GetCleanupPolicy(ctx, policyID)
	if err != nil {
		return fmt.Errorf("failed to load cleanup policy: %w", err)
	}

	if policy == nil {
		return ErrPolicyNotFound
	}

	runCtx := w.runCtx
	if runCtx == nil {
		runCtx = context.Background()
	}

	release, err := w.acquirePolicyLock(runCtx, policy.ID)
	if err != nil {
		return err
	}

	w.wg.Go(func() {
		defer release()
		defer func() {
			if r := recover(); r != nil {
				w.log.Errorw("panic in cleanup policy run", "policy_id", policy.ID, "panic", r)
			}
		}()

		rctx, cancel := context.WithTimeout(runCtx, policyRunTimeout)
		defer cancel()

		w.runAndRecord(rctx, policy)
	})

	return nil
}

// formatTable maps a repository format to the single table that stores its
// blob-bearing rows, for the generic cleanup-by-policy code below. Every table
// listed here has an `id` PK, a `repository_id` FK, `created_at`, and one or
// more FKs into the global blobs table.
type formatTable struct {
	table string
	// nameCol is the package name, matched against name_pattern.
	nameCol string
	// partitionCols identify "the same package" for keep_last_n: versions are
	// only ranked against other versions of the same name and architecture /
	// distribution, so a newer arm64 build never evicts the only amd64 one.
	partitionCols []string
	// versionExpr identifies one version; rows sharing it (e.g. a pypi sdist
	// and wheel) count as a single version for keep_last_n.
	versionExpr string
	// blobCols are the (possibly nullable) FK columns to blobs.id owned by the row.
	blobCols []string
	// quotaTracked marks formats whose uploads reserve storage quota (one
	// artifact per row, the row's blob size). Deleting such a row must give that
	// usage back. Every hosted format is metered.
	quotaTracked bool
	// kind is the trash kind of the table's rows, for the soft-delete path.
	kind models.TrashKind
}

// CleanupFormatSupported reports whether the cleanup worker can run policies
// for repositories of the given format; the API uses it to reject policies
// that could never run.
func CleanupFormatSupported(format string) bool {
	_, ok := cleanupFormatTable(format)

	return ok
}

// cleanupFormatTable covers formats whose packages live in one table with
// direct blob FKs. npm is intentionally not listed: its tarballs live in
// npm_versions, joined to npm_packages for name/repository_id — a different
// shape the single-table query below cannot express.
func cleanupFormatTable(format string) (formatTable, bool) {
	switch format {
	case "rpm":
		return formatTable{"rpm_packages", "name", []string{"name", "arch"}, "version || '-' || release || ':' || epoch::text", []string{"blob_id"}, true, models.TrashKindRPM}, true
	case "deb":
		return formatTable{"deb_packages", "name", []string{"name", "arch", "distribution_id::text"}, "version", []string{"blob_id"}, true, models.TrashKindDeb}, true
	case "helm":
		return formatTable{"helm_charts", "name", []string{"name"}, "version", []string{"blob_id"}, true, models.TrashKindHelm}, true
	case "pypi":
		return formatTable{"pypi_packages", "name", []string{"name"}, "version", []string{"blob_id"}, true, models.TrashKindPyPI}, true
	case "nuget":
		return formatTable{"nuget_packages", "package_id", []string{"package_id"}, "version", []string{"blob_id"}, true, models.TrashKindNuGet}, true
	case "go": // the format id is "go"; the table is gomod_modules (chk_repo_format)
		return formatTable{"gomod_modules", "module_path", []string{"module_path"}, "version", []string{"zip_blob_id", "mod_blob_id"}, true, models.TrashKindGoMod}, true
	default:
		return formatTable{}, false
	}
}

func (w *CleanupWorker) runPolicy(ctx context.Context, policy *models.CleanupPolicy) (int, error) {
	// Never run a policy that could select everything: re-validate what the API
	// enforces, since rows may predate validation.
	if err := policy.Validate(); err != nil {
		return 0, err
	}

	if policy.RepositoryID == "" && policy.ProjectID != "" {
		return w.runProjectPolicy(ctx, policy)
	}

	repo, err := w.db.GetRepositoryByID(ctx, policy.RepositoryID)
	if err != nil {
		return 0, fmt.Errorf("failed to get repository: %w", err)
	}

	if repo == nil {
		return 0, ErrRepositoryNotFound
	}

	tableInfo, ok := cleanupFormatTable(repo.Format)
	if !ok {
		return 0, fmt.Errorf("%w: %s", errUnsupportedCleanupFormat, repo.Format)
	}

	deleted, err := w.deleteArtifacts(ctx, policy, repo, tableInfo)
	// Also after a partial run: whatever was removed before the error must
	// leave the generated metadata.
	if deleted > 0 {
		w.refreshMetadata(ctx, repo)
	}

	return deleted, err
}

// errUnsupportedCleanupFormat: the repository's format has no cleanup support.
var errUnsupportedCleanupFormat = errors.New("unsupported format for cleanup")

// runProjectPolicy applies a project default policy: it is expanded into one
// run per live hosted repository of the project that has no enabled policy of
// its own (a repository-level policy overrides the default, it never adds to
// it), each with that repository's format table. Repositories whose format has
// no cleanup support, or that vanished meanwhile, are skipped; other failures
// do not stop the remaining repositories and are reported together.
func (w *CleanupWorker) runProjectPolicy(ctx context.Context, policy *models.CleanupPolicy) (int, error) {
	repoIDs, err := w.db.ListProjectCleanupRepositories(ctx, policy.ProjectID)
	if err != nil {
		return 0, fmt.Errorf("failed to list project repositories: %w", err)
	}

	total := 0

	var errs []error

	for _, repoID := range repoIDs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}

		perRepo := *policy
		perRepo.RepositoryID = repoID

		n, err := w.runPolicy(ctx, &perRepo)
		total += n

		if err != nil && !errors.Is(err, errUnsupportedCleanupFormat) && !errors.Is(err, ErrRepositoryNotFound) {
			errs = append(errs, fmt.Errorf("repository %s: %w", repoID, err))
		}
	}

	return total, errors.Join(errs...)
}

// MetadataNotifier schedules the regeneration of a repository's generated
// metadata (repodata, dists). It is satisfied by the rpm and deb metadata workers.
type MetadataNotifier interface{ NotifyStale(repoID string) }

// SetMetadataNotifiers wires the rpm and deb metadata workers so a cleanup run
// that removed packages refreshes the repository's index (otherwise
// primary.xml / Packages keep listing files that no longer exist). Either may
// be nil when that worker is not running.
func (w *CleanupWorker) SetMetadataNotifiers(rpm, deb MetadataNotifier) {
	w.rpmNotifier, w.debNotifier = rpm, deb
}

// refreshMetadata runs once per policy run for the repository the run touched.
// rpm is marked stale in the database first, so the stale sweep recovers the
// refresh even if the notifier is absent or the process dies.
func (w *CleanupWorker) refreshMetadata(ctx context.Context, repo *models.Repository) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), policyRecordTimeout)
	defer cancel()

	switch repo.Format {
	case "rpm":
		if err := w.db.MarkRepoStale(rctx, repo.ID); err != nil {
			w.log.Warnw("cleanup: failed to mark rpm repository stale", "repository_id", repo.ID, "error", err)
		}

		if w.rpmNotifier != nil {
			w.rpmNotifier.NotifyStale(repo.ID)
		}
	case "deb":
		if w.debNotifier != nil {
			w.debNotifier.NotifyStale(repo.ID)
		}
	}
}

// cleanupCandidate is a row selected for deletion: its primary key (for the
// DELETE), its version (for the immutable-release guard) and the blob IDs it
// references (to resolve sha256 for SafeDeleteBlob).
type cleanupCandidate struct {
	id      string
	name    string
	version string
	blobIDs []string
}

// cleanupActor is the deleted_by recorded on rows a cleanup policy moves to the
// trash; there is no HTTP principal in a worker.
const cleanupActor = "cleanup-policy"

// selectCandidates returns the rows the policy would delete. A row is selected
// when it is outside the newest keep_last_n versions of its package (if set)
// AND older than keep_since_days (if set), i.e. it is kept when either
// criterion protects it; name_pattern then restricts the selection.
func (w *CleanupWorker) selectCandidates(ctx context.Context, policy *models.CleanupPolicy, t formatTable) ([]cleanupCandidate, error) {
	var nameRegex *regexp.Regexp

	if policy.NamePattern != "" {
		var err error

		nameRegex, err = regexp.Compile(policy.NamePattern)
		if err != nil {
			return nil, fmt.Errorf("invalid name pattern regex: %w", err)
		}
	}

	partition := strings.Join(t.partitionCols, ", ")

	blobSelect := make([]string, len(t.blobCols))
	blobOut := make([]string, len(t.blobCols))

	for i, c := range t.blobCols {
		blobSelect[i] = fmt.Sprintf("COALESCE(%s::text, '') AS b%d", c, i)
		blobOut[i] = fmt.Sprintf("b%d", i)
	}

	args := []any{policy.RepositoryID}
	conds := []string{}

	if policy.KeepLastN != nil {
		args = append(args, *policy.KeepLastN)
		conds = append(conds, fmt.Sprintf("rk > $%d", len(args)))
	}

	if policy.KeepSinceDays != nil {
		args = append(args, *policy.KeepSinceDays)
		conds = append(conds, fmt.Sprintf("created_at < now() - make_interval(days => $%d)", len(args)))
	}

	// Inner: the newest created_at per (package, version) so that multi-file
	// versions rank as one. Outer: dense rank of versions within a package.
	query := fmt.Sprintf(`
		SELECT id, cname, cver, %s FROM (
			SELECT id, cname, cver, %s, created_at,
			       DENSE_RANK() OVER (PARTITION BY %s ORDER BY vts DESC, ver DESC) AS rk
			FROM (
				SELECT id, %s AS cname, version AS cver, %s, created_at,
				       %s,
				       (%s) AS ver,
				       MAX(created_at) OVER (PARTITION BY %s, (%s)) AS vts
				FROM %s
				WHERE repository_id = $1 AND deleted_at IS NULL
			) base
		) ranked
		WHERE %s`,
		strings.Join(blobOut, ", "),
		strings.Join(blobOut, ", "),
		partitionAliases(len(t.partitionCols)),
		t.nameCol, strings.Join(blobSelect, ", "),
		partitionSelect(t.partitionCols),
		t.versionExpr,
		partition, t.versionExpr,
		t.table,
		strings.Join(conds, " AND "))

	rows, err := w.db.GetDB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query artifacts for deletion: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var candidates []cleanupCandidate

	for rows.Next() {
		c := cleanupCandidate{blobIDs: make([]string, len(t.blobCols))}
		dest := []any{&c.id, &c.name, &c.version}

		for i := range c.blobIDs {
			dest = append(dest, &c.blobIDs[i])
		}

		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("failed to scan artifact row: %w", err)
		}

		if nameRegex != nil && !nameRegex.MatchString(c.name) {
			continue
		}

		candidates = append(candidates, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating artifact rows: %w", err)
	}

	return candidates, nil
}

func partitionAliases(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("p%d", i)
	}

	return strings.Join(parts, ", ")
}

func partitionSelect(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprintf("%s AS p%d", c, i)
	}

	return strings.Join(parts, ", ")
}

// trashOutcome is what trashCandidates did with a batch of selected rows.
type trashOutcome int

const (
	// trashHard: the repository keeps no trash (or the database cannot), the
	// row must be deleted permanently.
	trashHard trashOutcome = iota
	// trashMoved: the row sits in the trash now; nothing else to release.
	trashMoved
	// trashSkipped: leave the row alone (already gone, or the move failed).
	trashSkipped
)

// trashCandidates moves cs to the trash, in one transaction, when the
// repository's retention allows it. A failed move skips the rows: deleting
// permanently because the soft path broke would defeat the point of the trash.
func (w *CleanupWorker) trashCandidates(ctx context.Context, repoID string, t formatTable, cs []cleanupCandidate) trashOutcome {
	store, ok := w.db.(database.TrashStore)
	if !ok {
		return trashHard
	}

	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.id
	}

	soft, err := store.TrashRows(ctx, repoID, t.kind, ids, cleanupActor)

	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		// Concurrent deletes removed every row since they were selected.
		return trashSkipped
	case err != nil:
		w.log.Warnw("failed to move artifacts to trash", "count", len(ids), "error", err)

		return trashSkipped
	case soft:
		return trashMoved
	default:
		return trashHard
	}
}

func (w *CleanupWorker) deleteArtifacts(ctx context.Context, policy *models.CleanupPolicy, repo *models.Repository, tableInfo formatTable) (int, error) {
	toDelete, err := w.selectCandidates(ctx, policy, tableInfo)
	if err != nil {
		return 0, err
	}

	totalDeleted := 0

	// Usage of the rows removed so far is handed back to the repository's quota
	// in one update when the run ends, however it ends (finished, timed out,
	// cancelled); a crash in between leaves it to the periodic reconcile.
	var released quotaRelease

	defer func() { w.releaseQuota(ctx, policy.RepositoryID, released) }()

	for chunk := range slices.Chunk(toDelete, cleanupChunkSize) {
		if err := ctx.Err(); err != nil {
			return totalDeleted, fmt.Errorf("cleanup interrupted: %w", err)
		}

		totalDeleted += w.deleteChunk(ctx, policy, repo, tableInfo, chunk, &released)
	}

	return totalDeleted, nil
}

// cleanupChunkSize is how many selected rows are looked up and deleted per
// batch: a handful of statements per chunk instead of several per row, while
// the bundle-protection answer stays a few seconds old at most.
const cleanupChunkSize = 200

// blobFacts is what the cleanup needs to know about one blob row.
type blobFacts struct {
	sha256 string
	size   int64
	// bundled: a release bundle references the blob and pins it.
	bundled bool
}

// deleteChunk removes the rows of chunk the policy may remove and returns how
// many it removed (or moved to the trash). The blob facts of the whole chunk
// come from one query; the rows go with one statement; the underlying blobs
// leave storage only when no other row still references them.
func (w *CleanupWorker) deleteChunk(
	ctx context.Context,
	policy *models.CleanupPolicy,
	repo *models.Repository,
	t formatTable,
	chunk []cleanupCandidate,
	released *quotaRelease,
) int {
	facts, err := w.loadBlobFacts(ctx, chunk)
	if err != nil {
		// A failed protection check protects: deleting on doubt cannot be undone.
		w.log.Warnw("failed to look up blobs for cleanup, skipping batch", "count", len(chunk), "error", err)

		return 0
	}

	eligible := w.eligibleCandidates(repo, chunk, facts)
	if len(eligible) == 0 {
		return 0
	}

	// Soft delete keeps the rows, their blobs and their quota usage until the
	// purge; only the permanent path below releases them.
	switch w.trashCandidates(ctx, policy.RepositoryID, t, eligible) {
	case trashMoved:
		return len(eligible)
	case trashSkipped:
		return 0
	case trashHard:
	}

	deleted := w.deleteRows(ctx, t.table, eligible)

	var (
		removed int
		shas    []string
		seen    = map[string]struct{}{}
	)

	for _, c := range eligible {
		if _, ok := deleted[c.id]; !ok {
			continue
		}

		removed++

		if t.quotaTracked {
			released.rows++
		}

		for _, blobID := range c.blobIDs {
			f, ok := facts[blobID]
			if !ok {
				continue
			}

			// The usage an upload of the row reserved, read before the rows went
			// (a concurrent delete could drop the blob and its size after).
			if t.quotaTracked {
				released.bytes += f.size
			}

			if _, dup := seen[f.sha256]; !dup {
				seen[f.sha256] = struct{}{}
				shas = append(shas, f.sha256)
			}
		}
	}

	for _, sha256 := range shas {
		if ctx.Err() != nil {
			break
		}

		if err := storage.SafeDeleteBlob(ctx, w.db, w.blobStore, sha256); err != nil {
			w.log.Warnw("failed to delete blob from storage", "sha256", sha256, "error", err)
		}
	}

	return removed
}

// eligibleCandidates drops the rows a cleanup must leave: those whose blobs a
// release bundle pins and, in an immutable repository, released versions (a
// worker has no admin to force).
func (w *CleanupWorker) eligibleCandidates(repo *models.Repository, chunk []cleanupCandidate, facts map[string]blobFacts) []cleanupCandidate {
	eligible := make([]cleanupCandidate, 0, len(chunk))

nextRow:
	for _, c := range chunk {
		for _, blobID := range c.blobIDs {
			if facts[blobID].bundled {
				w.log.Debugw("skipping cleanup: blob is referenced by a release bundle", "blob_id", blobID)

				continue nextRow
			}
		}

		if repo.ImmutableReleases && trash.IsRelease(repo.Format, c.version) {
			w.log.Debugw("skipping cleanup: released version is immutable",
				"repository_id", repo.ID, "id", c.id, "name", c.name, "version", c.version)

			continue
		}

		eligible = append(eligible, c)
	}

	return eligible
}

// loadBlobFacts reads sha256, size and release-bundle pinning of every blob the
// chunk's rows reference, keyed by blob id, in one query. Blobs that no longer
// exist are simply absent.
func (w *CleanupWorker) loadBlobFacts(ctx context.Context, chunk []cleanupCandidate) (map[string]blobFacts, error) {
	var ids []string

	for _, c := range chunk {
		for _, id := range c.blobIDs {
			if id != "" {
				ids = append(ids, id)
			}
		}
	}

	facts := make(map[string]blobFacts, len(ids))
	if len(ids) == 0 {
		return facts, nil
	}

	rows, err := w.db.GetDB().QueryContext(ctx,
		`SELECT b.id::text, b.sha256, b.size_bytes,
		        EXISTS(SELECT 1 FROM release_bundle_items rbi WHERE rbi.sha256 = b.sha256)
		 FROM blobs b WHERE b.id = ANY($1::uuid[])`, uuidArrayLiteral(ids))
	if err != nil {
		return nil, fmt.Errorf("query blobs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id string
			f  blobFacts
		)

		if err := rows.Scan(&id, &f.sha256, &f.size, &f.bundled); err != nil {
			return nil, fmt.Errorf("scan blob: %w", err)
		}

		facts[id] = f
	}

	return facts, rows.Err()
}

// deleteRows deletes the rows with one statement and returns the ids it
// removed; rows a concurrent API delete got to first are not among them (that
// delete already released their quota and blobs). If the batch statement fails
// the rows are retried one by one, so one bad row cannot keep the rest.
func (w *CleanupWorker) deleteRows(ctx context.Context, table string, cs []cleanupCandidate) map[string]struct{} {
	conn := w.db.GetDB()
	deleted := make(map[string]struct{}, len(cs))

	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.id
	}

	//nolint:gosec // G201: table is a constant of the format registry; ids are a bound argument
	batch := fmt.Sprintf("DELETE FROM %s WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL RETURNING id::text", table)

	rows, err := conn.QueryContext(ctx, batch, uuidArrayLiteral(ids))
	if err == nil {
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				w.log.Warnw("failed to read deleted artifact id", "error", scanErr)
				break
			}

			deleted[id] = struct{}{}
		}

		if rowsErr := rows.Err(); rowsErr != nil {
			w.log.Warnw("failed to read deleted artifact ids", "error", rowsErr)
		}

		return deleted
	}

	w.log.Warnw("failed to delete artifacts in one batch, retrying one by one", "count", len(ids), "error", err)

	//nolint:gosec // G201: see above
	single := fmt.Sprintf("DELETE FROM %s WHERE id = $1 AND deleted_at IS NULL", table)

	for _, id := range ids {
		res, err := conn.ExecContext(ctx, single, id)
		if err != nil {
			w.log.Warnw("failed to delete artifact from database", "id", id, "error", err)
			continue
		}

		if n, rowsErr := res.RowsAffected(); rowsErr == nil && n > 0 {
			deleted[id] = struct{}{}
		}
	}

	return deleted
}

// uuidArrayLiteral renders ids as a Postgres array literal for ANY($1::uuid[]).
func uuidArrayLiteral(ids []string) string {
	return "{" + strings.Join(ids, ",") + "}"
}

// quotaRelease is the usage a cleanup run took out of a repository: the summed
// blob sizes and the number of package rows deleted.
type quotaRelease struct {
	bytes int64
	rows  int
}

// releaseQuota gives r back to the repository's quota counters. It is detached
// from ctx cancellation: a run that timed out still deleted its rows.
func (w *CleanupWorker) releaseQuota(ctx context.Context, repoID string, r quotaRelease) {
	if r.rows == 0 && r.bytes == 0 {
		return
	}

	if err := quota.ReleaseN(ctx, w.db, repoID, r.bytes, r.rows); err != nil {
		w.log.Warnw("failed to release quota for cleaned-up artifacts",
			"repository_id", repoID, "artifacts", r.rows, "bytes", r.bytes, "error", err)
	}
}
