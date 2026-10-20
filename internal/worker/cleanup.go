// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/cron"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
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
)

type CleanupWorker struct {
	db        database.Database
	blobStore storage.BlobStore
	log       *logger.Logger
	stopCh    chan struct{}
	doneCh    chan struct{}

	runCtx context.Context //nolint:containedctx // lifetime of the worker, cancelled by Stop
	cancel context.CancelFunc
	wg     sync.WaitGroup // manually triggered policy runs

	lastOrphanGC time.Time // only touched from the tick goroutine
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

func (w *CleanupWorker) Start() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				w.log.Errorw("panic in goroutine", "panic", r)
			}
		}()
		defer close(w.doneCh)

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(w.runCtx, policyRunTimeout)
				w.runCleanupCycle(ctx)
				cancel()
			case <-w.stopCh:
				return
			}
		}
	}()
}

func (w *CleanupWorker) Stop() {
	close(w.stopCh)
	w.cancel()
	<-w.doneCh
	w.wg.Wait()
}

func (w *CleanupWorker) runCleanupCycle(ctx context.Context) {
	w.runDuePolicies(ctx)
	w.collectOrphansIfDue(ctx, time.Now())
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

	removed, err := storage.CollectOrphanBlobs(ctx, w.db, w.blobStore, storage.OrphanMinAge, storage.OrphanBatch)
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
}

// cleanupFormatTable covers formats whose packages live in one table with
// direct blob FKs. npm is intentionally not listed: its tarballs live in
// npm_versions, joined to npm_packages for name/repository_id — a different
// shape the single-table query below cannot express.
func cleanupFormatTable(format string) (formatTable, bool) {
	switch format {
	case "rpm":
		return formatTable{"rpm_packages", "name", []string{"name", "arch"}, "version || '-' || release || ':' || epoch::text", []string{"blob_id"}}, true
	case "deb":
		return formatTable{"deb_packages", "name", []string{"name", "arch", "distribution_id::text"}, "version", []string{"blob_id"}}, true
	case "helm":
		return formatTable{"helm_charts", "name", []string{"name"}, "version", []string{"blob_id"}}, true
	case "pypi":
		return formatTable{"pypi_packages", "name", []string{"name"}, "version", []string{"blob_id"}}, true
	case "nuget":
		return formatTable{"nuget_packages", "package_id", []string{"package_id"}, "version", []string{"blob_id"}}, true
	case "go": // repositories.format value is "go", not "gomod" (chk_repo_format)
		return formatTable{"gomod_modules", "module_path", []string{"module_path"}, "version", []string{"zip_blob_id", "mod_blob_id"}}, true
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

	repo, err := w.db.GetRepositoryByID(ctx, policy.RepositoryID)
	if err != nil {
		return 0, fmt.Errorf("failed to get repository: %w", err)
	}

	if repo == nil {
		return 0, ErrRepositoryNotFound
	}

	tableInfo, ok := cleanupFormatTable(repo.Format)
	if !ok {
		return 0, fmt.Errorf("unsupported format for cleanup: %s", repo.Format) //nolint:err113 // dynamic error with runtime context
	}

	return w.deleteArtifacts(ctx, policy, tableInfo)
}

// cleanupCandidate is a row selected for deletion: its primary key (for the
// DELETE) and the blob IDs it references (to resolve sha256 for SafeDeleteBlob).
type cleanupCandidate struct {
	id      string
	name    string
	blobIDs []string
}

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
		SELECT id, cname, %s FROM (
			SELECT id, cname, %s, created_at,
			       DENSE_RANK() OVER (PARTITION BY %s ORDER BY vts DESC, ver DESC) AS rk
			FROM (
				SELECT id, %s AS cname, %s, created_at,
				       %s,
				       (%s) AS ver,
				       MAX(created_at) OVER (PARTITION BY %s, (%s)) AS vts
				FROM %s
				WHERE repository_id = $1
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
		dest := []any{&c.id, &c.name}

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

func (w *CleanupWorker) deleteArtifacts(ctx context.Context, policy *models.CleanupPolicy, tableInfo formatTable) (int, error) {
	toDelete, err := w.selectCandidates(ctx, policy, tableInfo)
	if err != nil {
		return 0, err
	}

	conn := w.db.GetDB()
	totalDeleted := 0

	// Delete artifacts: the DB row first, then the underlying blobs from
	// storage only if no other row still references them.
	for _, c := range toDelete {
		if err := ctx.Err(); err != nil {
			return totalDeleted, fmt.Errorf("cleanup interrupted: %w", err)
		}

		protected := false

		for _, blobID := range c.blobIDs {
			if blobID == "" {
				continue
			}

			var bundleProtected bool

			checkErr := conn.QueryRowContext(ctx,
				`SELECT EXISTS(
					SELECT 1 FROM release_bundle_items rbi
					JOIN blobs b ON b.sha256 = rbi.sha256
					WHERE b.id = $1
				)`, blobID,
			).Scan(&bundleProtected)
			if checkErr != nil {
				w.log.Warnw("failed to check bundle protection for blob", "blob_id", blobID, "error", checkErr)

				protected = true

				break
			}

			if bundleProtected {
				w.log.Debugw("skipping cleanup: blob is referenced by a release bundle", "blob_id", blobID)

				protected = true

				break
			}
		}

		if protected {
			continue
		}

		//nolint:gosec // G201: query uses validated table/column names from internal constants
		deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE id = $1", tableInfo.table)
		if _, err := conn.ExecContext(ctx, deleteQuery, c.id); err != nil {
			w.log.Warnw("failed to delete artifact from database", "id", c.id, "error", err)
			continue
		}

		totalDeleted++

		for _, blobID := range c.blobIDs {
			if blobID == "" {
				continue
			}

			var sha256 string

			if err := conn.QueryRowContext(ctx, "SELECT sha256 FROM blobs WHERE id = $1", blobID).Scan(&sha256); err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					w.log.Warnw("failed to resolve blob sha256 for cleanup", "blob_id", blobID, "error", err)
				}

				continue
			}

			if err := storage.SafeDeleteBlob(ctx, w.db, w.blobStore, sha256); err != nil {
				w.log.Warnw("failed to delete blob from storage", "sha256", sha256, "error", err)
			}
		}
	}

	return totalDeleted, nil
}
