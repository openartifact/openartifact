// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/osv"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// osvMirrorLockKey names the cluster-wide advisory lock (hashed by
	// tryAdvisoryLock) held by the replica running an import pass.
	osvMirrorLockKey = "openartifact:osv-mirror"

	// osvRunTimeout bounds one scheduled pass (downloads, imports, re-match).
	osvRunTimeout = 6 * time.Hour
	// osvPageSize is how many stored packages one re-match step evaluates.
	osvPageSize = 500
	// osvMaxStateError bounds the error text kept in the mirror state.
	osvMaxStateError = 300
	// osvMaxAuditDetails bounds the advisory list of an audit entry.
	osvMaxAuditDetails = 500
	// osvMaxEventFindings bounds the advisories a vulnerability.new event lists.
	osvMaxEventFindings = 50
)

// OSVRematchStats counts what the re-match of one ecosystem did.
type OSVRematchStats struct {
	// Evaluated is the number of package versions matched against the mirror.
	Evaluated int
	// Detected counts new advisory findings, Resolved findings that ended
	// (withdrawn or fixed in the data) and Updated findings whose data changed.
	Detected int
	Resolved int
	Updated  int
	// Silent is set for the first import of an ecosystem: it creates the
	// findings without raising events, so enabling the mirror on an instance
	// full of packages does not flood the webhooks.
	Silent bool
	// Started is when the re-match began and Took how long it ran.
	Started time.Time
	Took    time.Duration
}

// OSVMirrorStats is the outcome of one pass.
type OSVMirrorStats struct {
	// Imported, Unchanged and Failed count the ecosystems by import outcome.
	Imported  int
	Unchanged int
	Failed    int
	// Rematch is the re-match outcome by OSV ecosystem.
	Rematch map[string]OSVRematchStats
	// Busy is set when another replica holds the lock; nothing was done.
	Busy bool
}

// OSVMirrorWorker keeps the local OSV mirror current and matches the stored
// packages against it: once per interval it imports the export of every
// configured ecosystem (a conditional download, incremental by `modified`),
// then re-matches the package versions of hosted repositories and of the proxy
// cache against the advisories that changed. Findings are created, updated or
// closed (a withdrawn advisory); a new finding raises the vulnerability.new
// webhook and an audit entry. The findings are stored like those of a manual
// osv scan, so the security inbox, VEX, export and the policies read them
// unchanged; the worker never touches their quarantine flag.
//
// Replicas are excluded from each other by a Postgres advisory lock held on a
// pinned connection for the whole pass. A pass is resumable: the high-water
// mark moves only after an import is applied, and a re-match a crash cut short
// is flagged in the database and redone in full by the next pass.
type OSVMirrorWorker struct {
	db         database.Database
	store      database.OSVStore
	importer   *osv.Importer
	mirror     *vuln.Mirror
	log        *logger.Logger
	interval   time.Duration
	ecosystems []string
	publisher  webhook.Publisher
	reg        *metrics.Registry

	// lock takes the cluster-wide lock. It is a field so tests can simulate
	// another replica holding it.
	lock func(ctx context.Context) (release func(), acquired bool, err error)

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	// runCtx is cancelled by Stop so an in-flight pass stops promptly.
	runCtx context.Context //nolint:containedctx // lifecycle context of the worker
	cancel context.CancelFunc
}

// NewOSVMirrorWorker creates the worker for the given OSV ecosystems. A
// non-positive interval disables the schedule (RunOnce still works). Without a
// database that implements database.OSVStore the worker does nothing.
func NewOSVMirrorWorker(
	db database.Database, importer *osv.Importer, ecosystems []string, log *logger.Logger, interval time.Duration,
) *OSVMirrorWorker {
	runCtx, cancel := context.WithCancel(context.Background())

	store, _ := db.(database.OSVStore)

	w := &OSVMirrorWorker{
		db:         db,
		store:      store,
		importer:   importer,
		log:        log,
		interval:   interval,
		ecosystems: ecosystems,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
		runCtx:     runCtx,
		cancel:     cancel,
	}

	if store != nil {
		w.mirror = vuln.NewMirror(store)
	}

	w.lock = w.acquireLock

	return w
}

// SetPublisher wires the webhook publisher for vulnerability.new events. It
// must be called before Start.
func (w *OSVMirrorWorker) SetPublisher(p webhook.Publisher) { w.publisher = p }

// WithMetrics records the import and re-match metrics in reg.
func (w *OSVMirrorWorker) WithMetrics(reg *metrics.Registry) *OSVMirrorWorker {
	w.reg = reg

	return w
}

// Start begins the scheduled passes: the first right away, then one per
// interval. Only the first call counts.
func (w *OSVMirrorWorker) Start() {
	w.startOnce.Do(func() {
		if w.interval <= 0 || w.store == nil {
			close(w.doneCh)

			return
		}

		go w.loop()
	})
}

// Stop cancels an in-flight pass and waits for the schedule goroutine to exit.
// It is idempotent and safe without Start.
func (w *OSVMirrorWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.cancel()
	})

	w.startOnce.Do(func() { close(w.doneCh) })

	<-w.doneCh
}

func (w *OSVMirrorWorker) loop() {
	defer close(w.doneCh)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			w.runScheduled()
			timer.Reset(w.interval)
		case <-w.stopCh:
			return
		}
	}
}

// runScheduled runs one scheduled pass. A panic ends the pass, not the schedule.
func (w *OSVMirrorWorker) runScheduled() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in osv mirror pass", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.runCtx, osvRunTimeout)
	defer cancel()

	stats, err := w.RunOnce(ctx)
	if err != nil && w.runCtx.Err() == nil {
		w.log.Errorw("osv mirror pass failed", "error", truncateText(err.Error(), osvMaxStateError))
	}

	if !stats.Busy {
		w.log.Infow("osv mirror pass finished",
			"imported", stats.Imported, "unchanged", stats.Unchanged, "failed", stats.Failed, "rematched", len(stats.Rematch))
	}
}

// acquireLock takes the advisory lock on a pinned connection. A database
// without an SQL pool (test fakes) has no other replicas to exclude.
func (w *OSVMirrorWorker) acquireLock(ctx context.Context) (func(), bool, error) {
	sqlDB := w.db.GetDB()
	if sqlDB == nil {
		return func() {}, true, nil
	}

	return tryAdvisoryLock(ctx, sqlDB, osvMirrorLockKey)
}

// RunOnce runs one pass: it imports every configured ecosystem, then
// re-matches the stored packages against what changed. A failing ecosystem is
// recorded in its state and counted; the others still run. The error is
// non-nil only when the pass could not run to the end (lock failure, cancelled
// ctx, a re-match that failed).
func (w *OSVMirrorWorker) RunOnce(ctx context.Context) (OSVMirrorStats, error) {
	stats := OSVMirrorStats{Rematch: map[string]OSVRematchStats{}}

	if w.store == nil || w.importer == nil {
		return stats, nil
	}

	release, acquired, err := w.lock(ctx)
	if err != nil {
		return stats, fmt.Errorf("lock osv mirror: %w", err)
	}

	if !acquired {
		stats.Busy = true

		return stats, nil
	}
	defer release()

	plan, err := w.importAll(ctx, &stats)
	if err != nil {
		return stats, err
	}

	return stats, w.rematchAll(ctx, plan, &stats)
}

// rematchPlan is what the imports of one pass left to match.
type rematchPlan struct {
	// touched are the changed packages by ecosystem (normalized names).
	touched map[string]map[string]struct{}
	// full marks the ecosystems whose every stored package is matched: a
	// first import, or a pass a crash cut short.
	full map[string]bool
	// silent marks the first imports.
	silent map[string]bool
	// since is when the last completed re-match of an ecosystem started: the
	// package versions stored after it are matched too.
	since map[string]*time.Time
}

// importAll imports every configured ecosystem and records their state.
func (w *OSVMirrorWorker) importAll(ctx context.Context, stats *OSVMirrorStats) (*rematchPlan, error) {
	plan := &rematchPlan{
		touched: map[string]map[string]struct{}{},
		full:    map[string]bool{},
		silent:  map[string]bool{},
		since:   map[string]*time.Time{},
	}

	for _, eco := range w.ecosystems {
		if err := ctx.Err(); err != nil {
			return plan, fmt.Errorf("osv mirror interrupted: %w", err)
		}

		st, err := w.store.GetOSVMirrorState(ctx, eco)
		if err != nil {
			return plan, fmt.Errorf("read osv mirror state: %w", err)
		}

		if st == nil {
			st = &models.OSVMirrorState{Ecosystem: eco}
		}

		plan.silent[eco] = st.HighWater == nil
		plan.full[eco] = st.RematchPending || st.RematchedAt == nil
		plan.since[eco] = st.RematchedAt

		res, importErr := w.importer.Import(ctx, eco, st)
		for k := range res.Touched {
			if plan.touched[k.Ecosystem] == nil {
				plan.touched[k.Ecosystem] = map[string]struct{}{}
			}

			plan.touched[k.Ecosystem][k.NameNorm] = struct{}{}
		}

		w.recordImport(ctx, eco, st, res, importErr, stats)
	}

	return plan, nil
}

// recordImport saves the state of an import and updates the metrics and stats.
func (w *OSVMirrorWorker) recordImport(
	ctx context.Context, eco string, st *models.OSVMirrorState, res *osv.Result, importErr error, stats *OSVMirrorStats,
) {
	result := "imported"

	switch {
	case importErr != nil:
		result = "error"
		stats.Failed++
		st.LastError = truncateText(importErr.Error(), osvMaxStateError)

		if w.runCtx.Err() == nil {
			w.log.Errorw("osv mirror import failed", "ecosystem", eco, "error", st.LastError)
		}
	case res.NotModified:
		result = "unchanged"
		stats.Unchanged++
		st.LastError = ""
	default:
		stats.Imported++
		w.log.Infow("osv mirror import finished", "ecosystem", eco, "records", res.Records, "skipped", res.Skipped,
			"archive_bytes", res.Bytes, "changed_packages", len(res.Touched), "baseline", res.Baseline)
	}

	// A cancelled pass still records its state.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if importErr == nil && !res.NotModified {
		if n, err := w.store.CountOSVAdvisories(saveCtx, eco); err == nil {
			st.Advisories = n
		}
	}

	if err := w.store.SaveOSVMirrorState(saveCtx, st); err != nil {
		w.log.Errorw("failed to save osv mirror state", "ecosystem", eco, "error", err)
	}

	if w.reg == nil {
		return
	}

	w.reg.OSVMirrorImportsTotal.WithLabelValues(eco, result).Inc()

	if importErr == nil {
		w.reg.OSVMirrorLastImportSeconds.WithLabelValues(eco).Set(float64(time.Now().Unix()))
	}

	if st.HighWater != nil {
		w.reg.OSVMirrorAdvisories.WithLabelValues(eco).Set(float64(st.Advisories))
	}
}

// rematchAll matches the stored packages of every ecosystem: the ones named by
// the advisories that changed, and the versions stored since the last re-match.
// An ecosystem that has no completed import is skipped (its re-match stays
// pending): matching against half an export would report findings that the
// full export may not have.
func (w *OSVMirrorWorker) rematchAll(ctx context.Context, plan *rematchPlan, stats *OSVMirrorStats) error {
	var errs []error

	for _, eco := range w.ecosystems {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("osv mirror interrupted: %w", err)
		}

		ready, err := w.mirror.Ready(ctx, eco)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if !ready {
			continue
		}

		scope := rematchScope{touched: plan.touched[eco], since: plan.since[eco], full: plan.full[eco] || plan.silent[eco]}
		rs, err := w.rematch(ctx, eco, scope, plan.silent[eco])
		stats.Rematch[eco] = rs

		if err != nil {
			errs = append(errs, fmt.Errorf("re-match %s: %w", eco, err))

			continue
		}

		// Persist with a fresh context: the pass context may be cancelled right after.
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err = w.store.FinishOSVRematch(finishCtx, eco, rs.Started, rs.Took)

		cancel()

		if err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// rematchRun is the state of the re-match of one ecosystem.
type rematchRun struct {
	eco    string
	format string
	scope  rematchScope
	silent bool
	stats  OSVRematchStats
	repos  map[string]*models.Repository
	// seen deduplicates the versions of the proxy cache (one cached file per
	// artifact file, several per version): a hash of repository, name, version.
	seen map[uint64]struct{}
}

// rematchScope selects the package versions a re-match evaluates.
type rematchScope struct {
	// full matches every stored version.
	full bool
	// touched are the normalized names the changed advisories name.
	touched map[string]struct{}
	// since, when set, also selects the versions stored after it.
	since *time.Time
}

// rematchMargin widens the since bound: a row can commit after the re-match
// that missed it started.
const rematchMargin = 5 * time.Minute

// candidate reports whether the package version, last stored at addedAt, must
// be matched in this run.
func (r *rematchRun) candidate(name string, addedAt time.Time) bool {
	sc := r.scope
	if sc.full {
		return true
	}

	if sc.since != nil && addedAt.After(sc.since.Add(-rematchMargin)) {
		return true
	}

	_, ok := sc.touched[osv.NormalizeName(r.eco, name)]

	return ok
}

// rematch matches the package versions of the ecosystem's format held by
// hosted repositories and by the proxy cache that scope selects.
func (w *OSVMirrorWorker) rematch(ctx context.Context, eco string, scope rematchScope, silent bool) (OSVRematchStats, error) {
	format, _ := osv.FormatOf(eco)
	run := &rematchRun{
		eco: eco, format: format, scope: scope, silent: silent,
		repos: map[string]*models.Repository{}, seen: map[uint64]struct{}{},
	}
	run.stats.Silent = silent

	start := time.Now()
	run.stats.Started = start
	err := w.rematchHosted(ctx, run)

	if err == nil {
		err = w.rematchProxy(ctx, run)
	}

	run.stats.Took = time.Since(start)

	if w.reg != nil && err == nil {
		w.reg.OSVMirrorRematchSeconds.Observe(run.stats.Took.Seconds())
	}

	w.log.Infow("osv mirror re-match finished", "ecosystem", eco, "full", scope.full, "silent", silent,
		"evaluated", run.stats.Evaluated, "detected", run.stats.Detected, "resolved", run.stats.Resolved,
		"updated", run.stats.Updated, "seconds", run.stats.Took.Seconds())

	return run.stats, err
}

func (w *OSVMirrorWorker) rematchHosted(ctx context.Context, run *rematchRun) error {
	var after *models.StoredPackage

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("osv re-match interrupted: %w", err)
		}

		page, err := w.store.ListStoredPackages(ctx, run.format, after, osvPageSize)
		if err != nil {
			return fmt.Errorf("list stored packages: %w", err)
		}

		if len(page) == 0 {
			return nil
		}

		after = &page[len(page)-1]

		var batch []models.StoredPackage

		for _, p := range page {
			if run.candidate(p.Name, p.AddedAt) {
				batch = append(batch, p)
			}
		}

		if err := w.matchBatch(ctx, run, batch); err != nil {
			return err
		}
	}
}

func (w *OSVMirrorWorker) rematchProxy(ctx context.Context, run *rematchRun) error {
	afterID := ""

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("osv re-match interrupted: %w", err)
		}

		page, err := w.store.ListProxyCacheKeys(ctx, run.format, afterID, osvPageSize)
		if err != nil {
			return fmt.Errorf("list proxy cache keys: %w", err)
		}

		if len(page) == 0 {
			return nil
		}

		afterID = page[len(page)-1].ID

		var batch []models.StoredPackage

		for _, k := range page {
			name, version, ok := proxy.PackageCoordinates(run.format, k.CacheKey)
			if !ok || !run.candidate(name, k.CreatedAt) || !run.firstSeen(k.RepositoryID, name, version) {
				continue
			}

			batch = append(batch, models.StoredPackage{RepositoryID: k.RepositoryID, ArtifactID: k.ID, Name: name, Version: version})
		}

		if err := w.matchBatch(ctx, run, batch); err != nil {
			return err
		}
	}
}

// firstSeen records a proxy cache version and reports whether it is new to the run.
func (r *rematchRun) firstSeen(repoID, name, version string) bool {
	h := fnv.New64a()
	_, _ = h.Write([]byte(repoID + "\x00" + name + "\x00" + version))

	sum := h.Sum64()
	if _, dup := r.seen[sum]; dup {
		return false
	}

	r.seen[sum] = struct{}{}

	return true
}

// matchBatch evaluates the package versions against the mirror and reconciles
// the stored findings.
func (w *OSVMirrorWorker) matchBatch(ctx context.Context, run *rematchRun, pkgs []models.StoredPackage) error {
	if len(pkgs) == 0 {
		return nil
	}

	queries := make([]vuln.NameVersion, len(pkgs))
	for i, p := range pkgs {
		queries[i] = vuln.NameVersion{Name: p.Name, Version: p.Version}
	}

	matches, err := w.mirror.Match(ctx, run.eco, queries)
	if err != nil {
		return fmt.Errorf("match packages: %w", err)
	}

	existing, err := w.store.GetOSVScanResults(ctx, pkgs)
	if err != nil {
		return fmt.Errorf("read stored findings: %w", err)
	}

	run.stats.Evaluated += len(pkgs)

	for i, p := range pkgs {
		if err := w.reconcile(ctx, run, p, existing[database.OSVResultKey(p.RepositoryID, p.ArtifactID)], matches[i]); err != nil {
			return err
		}
	}

	return nil
}

// reconcile brings the stored osv result of one package version in line with
// the advisories that affect it now, and raises the events of what changed.
func (w *OSVMirrorWorker) reconcile(
	ctx context.Context, run *rematchRun, p models.StoredPackage, stored *models.VulnScanResult, now []models.Vulnerability,
) error {
	if stored == nil && len(now) == 0 {
		return nil
	}

	var before []models.Vulnerability
	if stored != nil {
		before = stored.Vulnerabilities
	}

	added, removed := diffFindings(before, now)

	if stored != nil && stored.ScanStatus == "completed" && len(added) == 0 && len(removed) == 0 && sameFindings(before, now) {
		return nil
	}

	res := &models.VulnScanResult{
		RepositoryID: p.RepositoryID, ArtifactID: p.ArtifactID, PackageName: p.Name, PackageVersion: p.Version,
		Ecosystem: run.format, Scanner: "osv", Vulnerabilities: now, VulnCount: len(now),
	}
	res.CriticalCount, res.HighCount, res.MediumCount, res.LowCount = vuln.CountSeverities(now)

	if err := w.store.SaveOSVScanResult(ctx, res); err != nil {
		return fmt.Errorf("save finding: %w", err)
	}

	run.stats.Detected += len(added)
	run.stats.Resolved += len(removed)

	if len(added) == 0 && len(removed) == 0 {
		run.stats.Updated++
	}

	w.countFindings(len(added), len(removed), len(added) == 0 && len(removed) == 0)

	if run.silent {
		return nil
	}

	w.announce(ctx, run, p, added, removed)

	return nil
}

func (w *OSVMirrorWorker) countFindings(added, removed int, updated bool) {
	if w.reg == nil {
		return
	}

	w.reg.OSVMirrorFindingsTotal.WithLabelValues("new").Add(float64(added))
	w.reg.OSVMirrorFindingsTotal.WithLabelValues("resolved").Add(float64(removed))

	if updated {
		w.reg.OSVMirrorFindingsTotal.WithLabelValues("updated").Inc()
	}
}

// announce raises the audit entries and the vulnerability.new webhook of a
// package version whose findings changed.
func (w *OSVMirrorWorker) announce(
	ctx context.Context, run *rematchRun, p models.StoredPackage, added []models.Vulnerability, removed []string,
) {
	repo := w.repository(ctx, run, p.RepositoryID)
	subject := p.Name + " " + p.Version

	if len(added) > 0 {
		auditFinding(ctx, audit.ActionScanVulnDetected, p, subject, "OSV mirror: "+describeFindings(added))

		if repo != nil {
			webhook.Emit(ctx, w.publisher, p.RepositoryID, webhook.EventVulnerabilityNew, webhook.VulnerabilityNewPayload(
				webhook.VulnerabilityDetected{
					Artifact: webhook.Artifact{Repository: repo.Name, Format: repo.Format, Name: p.Name, Version: p.Version},
					RepoType: repo.RepoType,
					Findings: webhookFindings(added),
				}))
		}
	}

	if len(removed) > 0 {
		auditFinding(ctx, audit.ActionScanVulnResolved, p, subject, "OSV mirror, no longer affected or withdrawn: "+
			truncateText(strings.Join(removed, ", "), osvMaxAuditDetails))
	}
}

// repository returns the repository of a package, nil when it is gone.
func (w *OSVMirrorWorker) repository(ctx context.Context, run *rematchRun, id string) *models.Repository {
	if repo, ok := run.repos[id]; ok {
		return repo
	}

	repo, err := w.db.GetRepositoryByID(ctx, id)
	if err != nil {
		w.log.Warnw("osv mirror: repository lookup failed", "repository_id", id, "error", err)

		repo = nil
	}

	run.repos[id] = repo

	return repo
}

func auditFinding(ctx context.Context, action string, p models.StoredPackage, subject, details string) {
	l := audit.Default()
	if l == nil {
		return
	}

	repoID, resourceType, resourceID := p.RepositoryID, "artifact", p.ArtifactID

	l.Log(ctx, audit.AuditEvent{
		Action:       action,
		ActorType:    "system",
		Actor:        "osv-mirror",
		RepositoryID: &repoID,
		ResourceType: &resourceType,
		ResourceID:   &resourceID,
		Details:      details,
		Package:      p.Name,
		Version:      p.Version,
		Subject:      subject,
	})
}

func webhookFindings(vulns []models.Vulnerability) []webhook.VulnerabilityFinding {
	if len(vulns) > osvMaxEventFindings {
		vulns = vulns[:osvMaxEventFindings]
	}

	out := make([]webhook.VulnerabilityFinding, len(vulns))
	for i, v := range vulns {
		out[i] = webhook.VulnerabilityFinding{ID: v.ID, Severity: v.Severity, CVSS: v.CVSS, Summary: v.Summary, FixedIn: v.FixedIn}
	}

	return out
}

// describeFindings renders the advisories as "GHSA-x (HIGH), CVE-y (LOW)".
func describeFindings(vulns []models.Vulnerability) string {
	parts := make([]string, len(vulns))
	for i, v := range vulns {
		parts[i] = v.ID + " (" + v.Severity + ")"
	}

	return truncateText(strings.Join(parts, ", "), osvMaxAuditDetails)
}

// diffFindings returns the findings in now that before lacks, and the ids of
// those before has and now lacks.
func diffFindings(before, now []models.Vulnerability) ([]models.Vulnerability, []string) {
	var (
		added   []models.Vulnerability
		removed []string
	)

	had := make(map[string]bool, len(before))
	for _, v := range before {
		had[v.ID] = true
	}

	has := make(map[string]bool, len(now))

	for _, v := range now {
		has[v.ID] = true

		if !had[v.ID] {
			added = append(added, v)
		}
	}

	for _, v := range before {
		if !has[v.ID] {
			removed = append(removed, v.ID)
		}
	}

	return added, removed
}

// sameFindings reports whether two finding lists store identically.
func sameFindings(a, b []models.Vulnerability) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}

	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)

	return errA == nil && errB == nil && bytes.Equal(x, y)
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
