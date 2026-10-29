// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !noai

package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AIAnalysisRequest carries the context needed to analyze one artifact.
type AIAnalysisRequest struct {
	RepositoryID    string
	ArtifactID      string
	ArtifactFormat  string
	ArtifactName    string
	ArtifactVersion string
	// ManifestText is caller-supplied plain-text content (package.json,
	// pom.xml, ...). When empty the worker extracts the manifest from the
	// stored artifact itself.
	ManifestText string
}

// aiRuntime is the hot-swappable part of the worker's configuration.
type aiRuntime struct {
	client      *ai.Client
	autoAnalyze bool
}

// AIAnalysisStore is the part of the database AIAnalysisWorker uses needs.
type AIAnalysisStore interface {
	GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error)
	GetRepoComponentBlob(ctx context.Context, repoID string, id string) (*models.ComponentBlob, error)
	UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error
}

// AIAnalysisWorker processes artifact uploads asynchronously and enriches them
// with AI-generated metadata (tags, summary, license status, security notes).
type AIAnalysisWorker struct {
	db    AIAnalysisStore
	blobs storage.BlobStore
	log   *logger.Logger
	rt    atomic.Pointer[aiRuntime]

	mu       sync.Mutex
	timers   map[string]*time.Timer
	debounce time.Duration
	queue    chan AIAnalysisRequest
	uploads  chan UploadRef
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	started  bool
	ctx      context.Context //nolint:containedctx // worker lifetime context, cancelled by Stop
	cancel   context.CancelFunc
}

// NewAIAnalysisWorker creates a new worker. Pass nil aiClient to disable AI calls
// (records will be created with status "pending" for later processing). The
// client and the auto-analysis switch can be replaced at runtime with Configure.
func NewAIAnalysisWorker(db AIAnalysisStore, aiClient *ai.Client, log *logger.Logger) *AIAnalysisWorker {
	ctx, cancel := context.WithCancel(context.Background())

	w := &AIAnalysisWorker{
		ctx:      ctx,
		cancel:   cancel,
		db:       db,
		log:      log,
		timers:   make(map[string]*time.Timer),
		debounce: 2 * time.Second,
		queue:    make(chan AIAnalysisRequest, 500),
		uploads:  make(chan UploadRef, 500),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	w.rt.Store(&aiRuntime{client: aiClient})

	return w
}

// WithBlobStore gives the worker access to stored artifacts so it can extract
// manifests itself; without it only caller-supplied manifest text works.
func (w *AIAnalysisWorker) WithBlobStore(bs storage.BlobStore) *AIAnalysisWorker {
	w.blobs = bs
	return w
}

// WithDebounce sets the debounce duration (default 2s).
func (w *AIAnalysisWorker) WithDebounce(d time.Duration) *AIAnalysisWorker {
	w.debounce = d
	return w
}

// Configure atomically replaces the model client and the auto-analysis switch.
// Analyses already running finish on the client they started with; every
// analysis that starts afterwards uses the new one.
func (w *AIAnalysisWorker) Configure(s ai.Settings) {
	w.rt.Store(&aiRuntime{client: s.NewClient(), autoAnalyze: s.Enabled && s.AutoAnalyze})
}

// Enabled reports whether a model client is configured.
func (w *AIAnalysisWorker) Enabled() bool { return w.rt.Load().client != nil }

// AutoAnalyze reports whether uploads are analysed without being asked to.
func (w *AIAnalysisWorker) AutoAnalyze() bool {
	rt := w.rt.Load()
	return rt.client != nil && rt.autoAnalyze
}

// Start launches the background processing goroutine.
func (w *AIAnalysisWorker) Start() {
	w.mu.Lock()
	w.started = true
	w.mu.Unlock()

	go func() {
		defer close(w.doneCh)

		for {
			select {
			case req := <-w.queue:
				w.safeProcess(req)
			case ref := <-w.uploads:
				w.safeResolveUpload(ref)
			case <-w.stopCh:
				return
			}
		}
	}()
}

// safeProcess isolates panics to a single request so the worker keeps running.
func (w *AIAnalysisWorker) safeProcess(req AIAnalysisRequest) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("ai worker: panic processing request", "artifact_id", req.ArtifactID, "panic", r)
		}
	}()

	w.process(req)
}

// Stop gracefully shuts down the worker.
func (w *AIAnalysisWorker) Stop() {
	w.mu.Lock()
	for key, t := range w.timers {
		t.Stop()
		delete(w.timers, key)
	}

	started := w.started
	w.mu.Unlock()

	w.stopOnce.Do(func() { close(w.stopCh) })

	if !started {
		w.cancel()
		return
	}

	select {
	case <-w.doneCh:
	case <-time.After(10 * time.Second):
		w.cancel() // abort in-flight LLM call
		<-w.doneCh
	}

	w.cancel()
}

// Notify schedules an AI analysis for the given artifact (debounced).
func (w *AIAnalysisWorker) Notify(req AIAnalysisRequest) {
	key := req.RepositoryID + ":" + req.ArtifactID

	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.timers[key]; ok {
		t.Stop()
	}

	var t *time.Timer

	t = time.AfterFunc(w.debounce, func() {
		select {
		case w.queue <- req:
		default:
			w.log.Warnw("ai analysis queue full, dropping request",
				"repo_id", req.RepositoryID, "artifact_id", req.ArtifactID)
		}

		w.mu.Lock()
		if w.timers[key] == t {
			delete(w.timers, key)
		}
		w.mu.Unlock()
	})
	w.timers[key] = t
}

// process performs the actual AI analysis and persists results.
func (w *AIAnalysisWorker) process(req AIAnalysisRequest) {
	rt := w.rt.Load()

	req, blob, failure := w.resolve(req)

	pending := &models.AIAnalysis{
		RepositoryID:    req.RepositoryID,
		ArtifactID:      req.ArtifactID,
		ArtifactFormat:  req.ArtifactFormat,
		ArtifactName:    req.ArtifactName,
		ArtifactVersion: req.ArtifactVersion,
		Status:          "processing",
		LicenseStatus:   "UNKNOWN",
	}

	if rt.client == nil {
		// AI disabled: leave as pending for external processing.
		pending.Status = "pending"
		w.save(pending)

		return
	}

	if failure != "" {
		w.fail(pending, req, failure)
		return
	}

	w.save(pending)

	if req.ManifestText == "" {
		text, msg := w.extract(req, blob)
		if msg != "" {
			w.fail(pending, req, msg)
			return
		}

		req.ManifestText = text
	}

	w.analyze(rt.client, pending, req)
}

// save upserts an analysis record, logging a failure.
func (w *AIAnalysisWorker) save(a *models.AIAnalysis) bool {
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()

	if err := w.db.UpsertAIAnalysis(ctx, a); err != nil {
		w.log.Errorw("ai worker: failed to save analysis", "artifact_id", a.ArtifactID, "status", a.Status, "error", err)
		return false
	}

	return true
}

// fail records a failed analysis with a user-visible reason.
func (w *AIAnalysisWorker) fail(a *models.AIAnalysis, req AIAnalysisRequest, msg string) {
	w.log.Warnw("ai worker: analysis not possible", "artifact_id", req.ArtifactID, "reason", msg)

	a.Status = "failed"
	a.ErrorMessage = msg
	w.save(a)
}

// analyze sends the manifest to the model and stores the outcome.
func (w *AIAnalysisWorker) analyze(client *ai.Client, pending *models.AIAnalysis, req AIAnalysisRequest) {
	ctx, cancel := context.WithTimeout(w.ctx, 60*time.Second)
	defer cancel()

	result, usage, err := client.Analyze(ctx, req.ManifestText)
	if err != nil {
		w.log.Errorw("ai worker: analysis failed", "artifact_id", req.ArtifactID, "error", err)

		pending.Status = "failed"
		pending.ErrorMessage = "ai analysis failed; see server logs"
		pending.ModelUsed = client.Model()
		w.save(pending)

		return
	}

	analysis := &models.AIAnalysis{
		RepositoryID:          req.RepositoryID,
		ArtifactID:            req.ArtifactID,
		ArtifactFormat:        req.ArtifactFormat,
		ArtifactName:          req.ArtifactName,
		ArtifactVersion:       req.ArtifactVersion,
		Summary:               result.ArtifactSummary,
		Tags:                  result.Tags,
		LicenseStatus:         result.LicenseStatus,
		LicenseName:           result.LicenseName,
		SecurityNotes:         result.SecurityNotes,
		QuarantineRecommended: result.QuarantineRecommended,
		Status:                "completed",
		ModelUsed:             usage.Model,
		PromptTokens:          usage.PromptTokens,
		CompletionTokens:      usage.CompletionTokens,
	}

	if !w.save(analysis) {
		return
	}

	w.log.Infow("ai analysis completed",
		"artifact_id", req.ArtifactID,
		"format", req.ArtifactFormat,
		"model", usage.Model,
		"license_status", result.LicenseStatus,
		"quarantine", result.QuarantineRecommended,
		"tags", result.Tags,
	)

	// LLM output is untrusted (the manifest is attacker-controlled prompt
	// input), so a recommendation is recorded for human review but never
	// triggers automatic quarantine.
	if result.QuarantineRecommended {
		w.log.Warnw("ai worker: quarantine recommended; manual review required",
			"artifact_id", req.ArtifactID, "repo_id", req.RepositoryID)
	}
}
