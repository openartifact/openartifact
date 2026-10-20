// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AIAnalysisRequest carries the context needed to analyze one artifact.
type AIAnalysisRequest struct {
	RepositoryID    string
	ArtifactID      string
	ArtifactFormat  string
	ArtifactName    string
	ArtifactVersion string
	// ManifestText is the extracted plain-text content (package.json, pom.xml, etc.)
	ManifestText string
}

// AIAnalysisWorker processes artifact uploads asynchronously and enriches them
// with AI-generated metadata (tags, summary, license status, security notes).
type AIAnalysisWorker struct {
	db       database.Database
	aiClient *ai.Client
	log      *logger.Logger

	mu       sync.Mutex
	timers   map[string]*time.Timer
	debounce time.Duration
	queue    chan AIAnalysisRequest
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	started  bool
	ctx      context.Context //nolint:containedctx // worker lifetime context, cancelled by Stop
	cancel   context.CancelFunc
}

// NewAIAnalysisWorker creates a new worker. Pass nil aiClient to disable AI calls
// (records will be created with status "pending" for later processing).
func NewAIAnalysisWorker(db database.Database, aiClient *ai.Client, log *logger.Logger) *AIAnalysisWorker {
	ctx, cancel := context.WithCancel(context.Background())

	return &AIAnalysisWorker{
		ctx:      ctx,
		cancel:   cancel,
		db:       db,
		aiClient: aiClient,
		log:      log,
		timers:   make(map[string]*time.Timer),
		debounce: 2 * time.Second,
		queue:    make(chan AIAnalysisRequest, 500),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// WithDebounce sets the debounce duration (default 2s).
func (w *AIAnalysisWorker) WithDebounce(d time.Duration) *AIAnalysisWorker {
	w.debounce = d
	return w
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
	ctx, cancel := context.WithTimeout(w.ctx, 60*time.Second)
	defer cancel()

	// Mark as processing
	pending := &models.AIAnalysis{
		RepositoryID:    req.RepositoryID,
		ArtifactID:      req.ArtifactID,
		ArtifactFormat:  req.ArtifactFormat,
		ArtifactName:    req.ArtifactName,
		ArtifactVersion: req.ArtifactVersion,
		Status:          "processing",
		LicenseStatus:   "UNKNOWN",
	}
	if err := w.db.UpsertAIAnalysis(ctx, pending); err != nil {
		w.log.Errorw("ai worker: failed to mark processing", "artifact_id", req.ArtifactID, "error", err)
		return
	}

	if w.aiClient == nil {
		// AI disabled — leave as pending for external processing
		pending.Status = "pending"
		_ = w.db.UpsertAIAnalysis(ctx, pending)

		return
	}

	if req.ManifestText == "" {
		w.log.Warnw("ai worker: empty manifest text, skipping", "artifact_id", req.ArtifactID)

		pending.Status = "failed"
		pending.ErrorMessage = "no manifest text available for analysis"
		_ = w.db.UpsertAIAnalysis(ctx, pending)

		return
	}

	result, usage, err := w.aiClient.Analyze(ctx, req.ManifestText)
	if err != nil {
		w.log.Errorw("ai worker: analysis failed", "artifact_id", req.ArtifactID, "error", err)

		pending.Status = "failed"
		pending.ErrorMessage = "ai analysis failed; see server logs"
		_ = w.db.UpsertAIAnalysis(ctx, pending)

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
		PromptTokens:          usage.PromptTokens,
		CompletionTokens:      usage.CompletionTokens,
	}

	if err := w.db.UpsertAIAnalysis(ctx, analysis); err != nil {
		w.log.Errorw("ai worker: failed to save analysis", "artifact_id", req.ArtifactID, "error", err)
		return
	}

	w.log.Infow("ai analysis completed",
		"artifact_id", req.ArtifactID,
		"format", req.ArtifactFormat,
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
