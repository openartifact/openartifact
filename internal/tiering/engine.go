// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

// metricsUsageTTL bounds how stale the per-tier blob and byte gauges get when
// no move changed them. The usage query aggregates the whole blobs table, so it
// does not run on every pass.
const metricsUsageTTL = 30 * time.Minute

// PromotionPolicyName labels promote-on-read moves in plans and journal rows.
const PromotionPolicyName = "promote-on-read"

// Move reasons recorded in the journal.
const (
	reasonRecovery  = "recovery"
	reasonPromotion = "promote-on-read"
	reasonPolicy    = "policy:"
)

// EngineConfig tunes a mover pass.
type EngineConfig struct {
	// Batch caps the moves started per pass (recoveries included).
	Batch int
	// Concurrency is how many blobs are copied at once.
	Concurrency int
	// ScanWindow is how many blobs rows one candidate query covers.
	ScanWindow int
	// MaxScanRows caps the rows a pass (or a dry run) examines.
	MaxScanRows int
	// MaxAttempts is how often a journal row is retried back to back before it
	// waits for RetryAfter.
	MaxAttempts int
	RetryAfter  time.Duration
}

// DefaultEngineConfig returns the production defaults.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{Batch: 200, Concurrency: 2, ScanWindow: 2000, MaxScanRows: 200000, MaxAttempts: 5, RetryAfter: time.Hour}
}

func (c EngineConfig) normalized() EngineConfig {
	d := DefaultEngineConfig()

	if c.Batch <= 0 {
		c.Batch = d.Batch
	}

	if c.Concurrency <= 0 {
		c.Concurrency = d.Concurrency
	}

	if c.ScanWindow <= 0 {
		c.ScanWindow = d.ScanWindow
	}

	if c.MaxScanRows <= 0 {
		c.MaxScanRows = d.MaxScanRows
	}

	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}

	if c.RetryAfter <= 0 {
		c.RetryAfter = d.RetryAfter
	}

	return c
}

// PassStats summarizes one mover pass.
type PassStats struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Recovered  int
	Promoted   int
	Moved      int
	Skipped    int
	Failed     int
	Bytes      int64
	Scanned    int
	// Aborted is true when the pass stopped on an error before finishing its
	// work (the error itself is returned by RunPass and logged by the caller).
	Aborted bool
}

// RunnerStatus is what the background mover reports about itself.
type RunnerStatus struct {
	// Enabled is false when the mover is switched off in the configuration.
	Enabled bool
	// Running is true while a pass is in progress on this replica.
	Running bool
	// IntervalSeconds is the time between scheduled passes.
	IntervalSeconds int64
	// LastPass is the most recent pass this replica ran (nil before the first).
	LastPass *PassStats
}

// Engine decides which blobs belong on which tier and moves them.
type Engine struct {
	db      DB
	reg     *Registry
	mover   *Mover
	cfg     EngineConfig
	metrics *metrics.Registry
	log     *logger.Logger
	now     func() time.Time

	mu           sync.Mutex
	policyCursor string
	promoCursor  string

	// Metrics state, guarded by mu: the usage last read from the database, when
	// it was read, and the tier names whose gauges currently exist.
	usage     map[string]models.TierUsage
	usageAt   time.Time
	published map[string]struct{}
}

// NewEngine creates an engine. m and log may be nil.
func NewEngine(db DB, reg *Registry, mover *Mover, cfg EngineConfig, m *metrics.Registry, log *logger.Logger) *Engine {
	return &Engine{db: db, reg: reg, mover: mover, cfg: cfg.normalized(), metrics: m, log: log, now: time.Now}
}

// decision is one planned move.
type decision struct {
	facts  models.BlobFacts
	policy string
	from   string // backend id
	to     string // backend id
	reason string
}

// loadPolicies returns the enabled, compilable policies whose target tier is
// usable, in evaluation order. Broken ones are skipped with a warning rather
// than blocking every other policy.
func (e *Engine) loadPolicies(ctx context.Context, snap *Snapshot) ([]*Policy, error) {
	rows, err := e.db.ListStoragePolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("load storage policies: %w", err)
	}

	out := make([]*Policy, 0, len(rows))

	for _, r := range rows {
		if !r.Enabled {
			continue
		}

		p, err := CompilePolicy(r)
		if err != nil {
			e.warn("skipping storage policy that does not compile", "policy", r.Name, "error", err)

			continue
		}

		out = append(out, p)
	}

	SortPolicies(out)

	return usablePolicies(snap, out), nil
}

// usablePolicies drops policies targeting a tier that is missing or cannot be
// opened, and policies restricted to a source tier this server does not know
// (a tier created on another replica a moment ago).
func usablePolicies(snap *Snapshot, policies []*Policy) []*Policy {
	return slices.DeleteFunc(slices.Clone(policies), func(p *Policy) bool {
		t, ok := snap.ByID(p.TargetTierID)
		if !ok || t.Store == nil {
			return true
		}

		if p.FromTierID != "" {
			if _, known := snap.ByID(p.FromTierID); !known {
				return true
			}
		}

		return false
	})
}

func (e *Engine) warn(msg string, kv ...any) {
	if e.log != nil {
		e.log.Warnw(msg, kv...)
	}
}

func anyNeedsRefs(policies []*Policy) bool {
	return slices.ContainsFunc(policies, (*Policy).NeedsRefs)
}

// facts assembles the policy inputs for a page of blobs; refs are loaded only
// when a policy uses them.
func (e *Engine) facts(ctx context.Context, snap *Snapshot, policies []*Policy, blobs []models.BlobFacts) ([]*Facts, error) {
	var refs map[string][]models.BlobRef

	if anyNeedsRefs(policies) && len(blobs) > 0 {
		shas := make([]string, len(blobs))
		for i, b := range blobs {
			shas[i] = b.SHA256
		}

		var err error
		if refs, err = e.db.ListBlobRefs(ctx, shas); err != nil {
			return nil, err //nolint:wrapcheck // already wrapped by the database layer
		}
	}

	out := make([]*Facts, 0, len(blobs))

	for _, b := range blobs {
		t, ok := snap.ByBackend(b.BackendID)
		if !ok || t.Store == nil {
			continue // placed on a tier this server does not know or cannot open: leave it alone
		}

		out = append(out, &Facts{
			SHA256: b.SHA256, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, LastAccessedAt: b.LastAccessedAt,
			TierID: t.ID, Refs: refs[b.SHA256],
		})
	}

	return out, nil
}

// decide applies the policies to a page of candidates and returns the moves.
func (e *Engine) decide(ctx context.Context, snap *Snapshot, policies []*Policy, blobs []models.BlobFacts, now time.Time) ([]decision, error) {
	facts, err := e.facts(ctx, snap, policies, blobs)
	if err != nil {
		return nil, err
	}

	var out []decision

	for _, f := range facts {
		p := Decide(policies, f, now)
		if p == nil || p.TargetTierID == f.TierID {
			continue
		}

		from, _ := snap.ByID(f.TierID)
		to, _ := snap.ByID(p.TargetTierID)

		out = append(out, decision{
			facts:  models.BlobFacts{SHA256: f.SHA256, SizeBytes: f.SizeBytes, CreatedAt: f.CreatedAt, LastAccessedAt: f.LastAccessedAt, BackendID: from.BackendID},
			policy: p.Name, from: from.BackendID, to: to.BackendID, reason: reasonPolicy + p.Name,
		})
	}

	return out, nil
}

// scanPolicies walks the blobs table from `cursor` window by window, handing
// each window's decisions to visit, until the table ends, maxRows were examined
// or visit consumed fewer decisions than offered. It returns the cursor to
// continue from ("" when the table was exhausted).
func (e *Engine) scanPolicies(ctx context.Context, snap *Snapshot, policies []*Policy, cursor string, maxRows int, visit func(ds []decision) int) (string, int, error) {
	if len(policies) == 0 {
		return "", 0, nil
	}

	scanned := 0

	now := e.now()
	backendOf := func(tierID string) string { return backendOfTier(snap, tierID) }

	clauses := make([]models.TierScanClause, len(policies))
	for i, p := range policies {
		clauses[i] = p.ScanClause(now, backendOf)
	}

	for scanned < maxRows {
		found, nxt, n, err := e.db.ScanTierCandidates(ctx, cursor, e.cfg.ScanWindow, clauses)
		if err != nil {
			return cursor, scanned, fmt.Errorf("scan tier candidates: %w", err)
		}

		scanned += n

		ds, err := e.decide(ctx, snap, policies, found, now)
		if err != nil {
			return cursor, scanned, err
		}

		if consumed := visit(ds); consumed < len(ds) {
			if consumed == 0 {
				return cursor, scanned, nil
			}

			return ds[consumed-1].facts.SHA256, scanned, nil
		}

		cursor = nxt
		if cursor == "" {
			return "", scanned, nil
		}

		if err := ctx.Err(); err != nil {
			return cursor, scanned, fmt.Errorf("scan tier candidates: %w", err)
		}
	}

	return cursor, scanned, nil
}

// promotionDecisions returns the moves promote-on-read wants for a page of
// candidates: blobs read since they were placed on their tier, unless the
// policies would move them straight back.
func (e *Engine) promotionDecisions(ctx context.Context, snap *Snapshot, policies []*Policy, blobs []models.BlobFacts, now time.Time) ([]decision, error) {
	facts, err := e.facts(ctx, snap, policies, blobs)
	if err != nil {
		return nil, err
	}

	primary := snap.Primary()

	var out []decision

	for _, f := range facts {
		// Judge the blob as if it were already hot and had just been read: a
		// policy that would immediately send it back suppresses the promotion.
		hot := *f
		hot.TierID, hot.LastAccessedAt = primary.ID, &now

		if p := Decide(policies, &hot, now); p != nil && p.TargetTierID != primary.ID {
			continue
		}

		out = append(out, decision{
			facts:  models.BlobFacts{SHA256: f.SHA256, SizeBytes: f.SizeBytes, CreatedAt: f.CreatedAt, LastAccessedAt: f.LastAccessedAt, BackendID: backendOfTier(snap, f.TierID)},
			policy: PromotionPolicyName, from: backendOfTier(snap, f.TierID), to: primary.BackendID, reason: reasonPromotion,
		})
	}

	return out, nil
}

func backendOfTier(snap *Snapshot, tierID string) string {
	t, ok := snap.ByID(tierID)
	if !ok {
		return ""
	}

	return t.BackendID
}

// scanPromotions walks promotion candidates from cursor, like scanPolicies.
func (e *Engine) scanPromotions(ctx context.Context, snap *Snapshot, policies []*Policy, cursor string, maxRows int, visit func(ds []decision) int) (string, int, error) {
	backends := snap.PromoteBackends()
	if len(backends) == 0 {
		return "", 0, nil
	}

	scanned := 0

	now := e.now()

	for scanned < maxRows {
		page, err := e.db.ListPromotionCandidates(ctx, backends, cursor, e.cfg.ScanWindow)
		if err != nil {
			return cursor, scanned, err //nolint:wrapcheck // already wrapped by the database layer
		}

		scanned += len(page)

		ds, err := e.promotionDecisions(ctx, snap, policies, page, now)
		if err != nil {
			return cursor, scanned, err
		}

		if consumed := visit(ds); consumed < len(ds) {
			if consumed == 0 {
				return cursor, scanned, nil
			}

			return ds[consumed-1].facts.SHA256, scanned, nil
		}

		if len(page) < e.cfg.ScanWindow {
			return "", scanned, nil
		}

		cursor = page[len(page)-1].SHA256
	}

	return cursor, scanned, nil
}

// RunPass runs one mover pass: journaled moves left by a crash first, then
// promote-on-read, then policy moves, each capped by the batch size. Moves run
// Concurrency at a time through the shared throttle.
func (e *Engine) RunPass(ctx context.Context) (PassStats, error) {
	st, err := e.runPass(ctx)
	st.FinishedAt = e.now()

	return st, err
}

func (e *Engine) runPass(ctx context.Context) (PassStats, error) {
	st := PassStats{StartedAt: e.now()}

	if err := e.reg.Refresh(ctx); err != nil {
		e.warn("storage tier reload failed before mover pass", "error", err)
	}

	snap := e.reg.Snapshot()
	if !snap.HasCold() {
		// Nothing to move between, and no usage worth aggregating over the
		// whole blobs table every interval.
		e.refreshMetrics(ctx, snap, false)

		return st, nil
	}

	budget := e.cfg.Batch

	err := e.recoverMoves(ctx, &st, &budget)

	if err == nil && budget > 0 {
		err = e.promote(ctx, snap, &st, &budget)
	}

	if err == nil && budget > 0 {
		err = e.applyPolicies(ctx, snap, &st, &budget)
	}

	e.refreshMetrics(ctx, snap, st.Recovered+st.Promoted+st.Moved > 0)

	if err != nil {
		st.Aborted = true
	}

	return st, err
}

var errMovePanic = errors.New("tier move panicked")

type job struct {
	d      decision
	resume *models.StorageTierMove
}

// execute runs jobs with the configured concurrency and folds results into st.
func (e *Engine) execute(ctx context.Context, jobs []job, st *PassStats, recovery bool) {
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	ch := make(chan job)

	for range min(e.cfg.Concurrency, max(len(jobs), 1)) {
		wg.Go(func() {
			for j := range ch {
				out, err := e.safeRunJob(ctx, j)

				mu.Lock()
				e.fold(st, j, out, err, recovery)
				mu.Unlock()
			}
		})
	}

feed:
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
			break feed
		}
	}

	close(ch)
	wg.Wait()
}

// safeRunJob runs one job and turns a panic into a failed outcome: a bug that
// trips on one blob must not take the whole server down from a worker goroutine
// (nothing above it recovers) nor abort the rest of the pass.
func (e *Engine) safeRunJob(ctx context.Context, j job) (Outcome, error) {
	var res struct {
		out Outcome
		err error
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				res.out, res.err = OutcomeSkipped, fmt.Errorf("%w: %v", errMovePanic, r)

				e.warn("storage tier move panicked", "blob", j.d.facts.SHA256, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			}
		}()

		res.out, res.err = e.runJob(ctx, j)
	}()

	return res.out, res.err
}

func (e *Engine) runJob(ctx context.Context, j job) (Outcome, error) {
	if j.resume != nil {
		return e.mover.Resume(ctx, j.resume)
	}

	return e.mover.Move(ctx, j.d.facts.SHA256, j.d.to, j.d.reason)
}

func (e *Engine) fold(st *PassStats, j job, out Outcome, err error, recovery bool) {
	switch {
	case err != nil:
		st.Failed++

		if !errors.Is(err, context.Canceled) {
			e.warn("storage tier move failed", "blob", j.d.facts.SHA256, "from", j.d.from, "to", j.d.to, "reason", j.d.reason, "error", err)
		}
	case out != OutcomeMoved:
		st.Skipped++
	case recovery:
		st.Recovered++
		st.Bytes += j.d.facts.SizeBytes
	case j.d.reason == reasonPromotion:
		st.Promoted++
		st.Bytes += j.d.facts.SizeBytes
	default:
		st.Moved++
		st.Bytes += j.d.facts.SizeBytes
	}
}

// recoverMoves resumes journaled moves (a crash, an outage, or a failed attempt).
func (e *Engine) recoverMoves(ctx context.Context, st *PassStats, budget *int) error {
	rows, err := e.db.ListRecoverableStorageTierMoves(ctx, e.cfg.MaxAttempts, e.cfg.RetryAfter, *budget)
	if err != nil {
		return fmt.Errorf("list journaled moves: %w", err)
	}

	jobs := make([]job, len(rows))
	for i, r := range rows {
		jobs[i] = job{
			resume: r,
			d:      decision{facts: models.BlobFacts{SHA256: r.SHA256, SizeBytes: r.SizeBytes}, from: r.FromBackendID, to: r.ToBackendID, reason: reasonRecovery},
		}
	}

	e.execute(ctx, jobs, st, true)

	*budget -= len(rows)

	return ctx.Err()
}

func (e *Engine) promote(ctx context.Context, snap *Snapshot, st *PassStats, budget *int) error {
	policies, err := e.loadPolicies(ctx, snap)
	if err != nil {
		return err
	}

	e.mu.Lock()
	cursor := e.promoCursor
	e.mu.Unlock()

	next, scanned, err := e.scanPromotions(ctx, snap, policies, cursor, e.cfg.MaxScanRows, e.visit(ctx, st, budget))

	e.mu.Lock()
	e.promoCursor = next
	e.mu.Unlock()

	st.Scanned += scanned

	return err
}

func (e *Engine) applyPolicies(ctx context.Context, snap *Snapshot, st *PassStats, budget *int) error {
	policies, err := e.loadPolicies(ctx, snap)
	if err != nil {
		return err
	}

	e.mu.Lock()
	cursor := e.policyCursor
	e.mu.Unlock()

	next, scanned, err := e.scanPolicies(ctx, snap, policies, cursor, e.cfg.MaxScanRows, e.visit(ctx, st, budget))

	e.mu.Lock()
	e.policyCursor = next
	e.mu.Unlock()

	st.Scanned += scanned

	return err
}

// visit returns the scan callback that executes as many decisions as the
// remaining budget allows and reports how many it consumed.
func (e *Engine) visit(ctx context.Context, st *PassStats, budget *int) func(ds []decision) int {
	return func(ds []decision) int {
		n := min(len(ds), max(*budget, 0))

		jobs := make([]job, n)
		for i := range n {
			jobs[i] = job{d: ds[i]}
		}

		e.execute(ctx, jobs, st, false)

		*budget -= n

		if ctx.Err() != nil {
			return 0
		}

		return n
	}
}

// refreshMetrics republishes the per-tier gauges. The blob and byte counts come
// from an aggregate over the whole blobs table, so they are re-read only when
// the pass moved something, a tier appeared or metricsUsageTTL passed; the
// cheap journal count is read every time. Gauges are updated in place (a scrape
// never sees them missing) and only the replica that ran a pass publishes them:
// they are a leader view, see docs/operations/storage-tiering.md. With no cold
// tier there is nothing to report and any gauges left from one are dropped.
func (e *Engine) refreshMetrics(ctx context.Context, snap *Snapshot, changed bool) {
	if e.metrics == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if !snap.HasCold() {
		e.dropGauges(nil)

		return
	}

	if e.needsUsage(snap, changed) {
		usage, err := e.db.StorageTierUsage(ctx)
		if err != nil {
			e.warn("storage tier usage unavailable for metrics", "error", err)

			return
		}

		e.usage = make(map[string]models.TierUsage, len(usage))
		for _, u := range usage {
			e.usage[u.BackendID] = u
		}

		e.usageAt = e.now()
	}

	names := make(map[string]struct{}, len(snap.Tiers))

	for _, t := range snap.Tiers {
		u := e.usage[t.BackendID]
		e.metrics.TierBlobs.WithLabelValues(t.Name).Set(float64(u.Blobs))
		e.metrics.TierBytes.WithLabelValues(t.Name).Set(float64(u.Bytes))

		names[t.Name] = struct{}{}
	}

	e.dropGauges(names)

	if n, err := e.db.CountStorageTierMoves(ctx); err == nil {
		e.metrics.TierMovesPending.Set(float64(n))
	}
}

// needsUsage reports whether the cached usage is stale. e.mu is held.
func (e *Engine) needsUsage(snap *Snapshot, changed bool) bool {
	if changed || e.usage == nil || e.now().Sub(e.usageAt) > metricsUsageTTL {
		return true
	}

	for _, t := range snap.Tiers {
		if _, ok := e.published[t.Name]; !ok {
			return true
		}
	}

	return false
}

// dropGauges removes the gauges of tiers that are no longer in keep and records
// keep as the published set. e.mu is held.
func (e *Engine) dropGauges(keep map[string]struct{}) {
	for name := range e.published {
		if _, ok := keep[name]; !ok {
			e.metrics.TierBlobs.DeleteLabelValues(name)
			e.metrics.TierBytes.DeleteLabelValues(name)
		}
	}

	e.published = keep
}
