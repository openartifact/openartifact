// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/branding"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/dtrack"
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// backgroundDrainTimeout bounds how long shutdown waits for the cancelled
	// background tasks (async backups and scans, startup repairs).
	backgroundDrainTimeout = 15 * time.Second

	// auditFlushTimeout bounds the final audit flush at shutdown.
	auditFlushTimeout = 5 * time.Second
)

// options are the server flags that select what the process does besides
// serving.
type options struct {
	migrate     bool
	repairGomod bool
	configFile  string
}

// app is one server process: the configuration, the long-lived services built
// at startup and the router they are mounted on. Its methods are the startup
// steps, called in order by serve, and the ordered shutdown.
type app struct {
	opts options
	cfg  *config.Config
	log  *logger.Logger

	// tasks runs the fire-and-forget goroutines (async backups and scans,
	// startup repairs); shutdown cancels them and waits, bounded.
	tasks *bgtask.Runner

	// closers run in reverse registration order when serve returns, like the
	// defers they replace.
	closers []func()

	db          *database.PostgresDB
	reg         *metrics.Registry
	auditLogger *audit.Logger

	store   storageStack
	workers workerSet
	feat    features
	web     webStack

	// stopOnce makes stopWorkers idempotent; extraStops are the stops of the
	// services that start after the workers (federation, AI, Dependency-Track,
	// scanner registry) and run after them.
	stopOnce   sync.Once
	drainOnce  sync.Once
	extraStops []func()
}

// onClose registers fn to run when serve returns, after every function
// registered later.
func (a *app) onClose(fn func()) { a.closers = append(a.closers, fn) }

// close runs the registered closers, last registered first.
func (a *app) close() {
	for _, fn := range slices.Backward(a.closers) {
		fn()
	}
}

// fail logs a fatal startup/runtime error through the structured logger and
// returns it for main to turn into a non-zero exit status.
func (a *app) fail(msg string, err error) error {
	a.log.Errorw(msg, "error", err)

	return reportedError{fmt.Errorf("%s: %w", msg, err)}
}

// serve is the server lifetime after the flags were handled: startup in
// dependency order, the HTTP server, then the ordered shutdown.
func serve(opts options) error {
	a, err := newApp(opts)
	if err != nil {
		return err
	}

	defer a.close()

	a.logStartup()

	if err := a.setupTelemetry(); err != nil {
		return err
	}

	if err := a.openDatabase(); err != nil {
		return err
	}

	if done, err := a.migrate(); done || err != nil {
		return err
	}

	if err := a.initCore(); err != nil {
		return err
	}

	if err := a.setupStorage(); err != nil {
		return err
	}

	if opts.repairGomod {
		return runGomodRepair(context.Background(), a.db, a.store.blobs, a.log)
	}

	a.startGomodRepair()
	a.setupServedStore()
	a.setupWorkers()

	if err := a.setupFeatures(); err != nil {
		return err
	}

	srv, err := a.buildHTTP()
	if err != nil {
		return err
	}

	if err := srv.Start(context.Background()); err != nil {
		return a.fail("failed to start server", err)
	}

	a.log.Infow("Server started", "address", a.web.addr)

	serveErr := a.awaitShutdownSignal(srv)

	return a.shutdown(srv, serveErr)
}

// awaitShutdownSignal waits for an interrupt, or for the accept loop to die
// underneath us. The latter still runs the ordered shutdown, then exits
// non-zero: it returns that error.
func (a *app) awaitShutdownSignal(srv *api.HTTPServer) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigChan:
		a.log.Infow("Shutdown signal received, gracefully stopping")

		return nil
	case serveErr := <-srv.ServeErrors():
		a.log.Errorw("HTTP server stopped unexpectedly, shutting down", "error", serveErr)

		return serveErr
	}
}

// shutdown is the ordered shutdown: stop accepting HTTP, cancel and drain the
// background tasks, stop the workers (they may still emit audit events), then
// flush audit; the DB closes last via the registered closers. Audit is flushed
// even if a prior step failed.
func (a *app) shutdown(srv *api.HTTPServer, serveErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.cfg.Server.ShutdownTimeout)*time.Second)
	defer cancel()

	if err := srv.Stop(ctx); err != nil {
		a.log.Errorw("Server shutdown error", "error", err)
	}

	a.drainTasks()
	a.stopWorkers()

	auditCtx, auditCancel := context.WithTimeout(context.Background(), auditFlushTimeout)
	defer auditCancel()

	if err := a.auditLogger.Shutdown(auditCtx); err != nil {
		a.log.Warnw("Audit logger shutdown incomplete", "error", err)
	}

	if serveErr != nil {
		return reportedError{serveErr}
	}

	a.log.Infow("Server stopped successfully")

	return nil
}

// drainTasks cancels the background tasks and waits for them, bounded by
// backgroundDrainTimeout. It is safe to call more than once.
func (a *app) drainTasks() {
	a.drainOnce.Do(func() {
		if a.tasks.Shutdown(backgroundDrainTimeout) {
			a.log.Infow("Background tasks stopped")
			return
		}

		a.log.Warnw("Background tasks still running after the drain timeout, continuing shutdown", "timeout", backgroundDrainTimeout.String())
	})
}

// stopWorkers stops every background worker exactly once. It runs in the
// shutdown sequence (after HTTP and the background tasks stop, before the
// audit logger flushes) and also via the closers so early-return paths never
// leak goroutines.
func (a *app) stopWorkers() {
	a.stopOnce.Do(func() {
		w := &a.workers

		for _, stop := range []func(){
			w.webhook.Stop, w.cleanup.Stop, w.presignedUpload.Stop, w.dockerGC.Stop, w.dockerUploadSweeper.Stop, w.trashPurge.Stop, w.auditRetention.Stop, w.authPurge.Stop, w.proxyCache.Stop,
			w.tierWorker.Stop, w.blobSweeper.Stop, a.store.replStop, a.store.tiers.Stop, a.store.tierAccess.Stop, w.journalWorker.Stop,
		} {
			stop()
		}

		// The metadata workers drain pending regenerations (bounded wait each),
		// so stop them in parallel to keep shutdown within one drain window.
		var drain sync.WaitGroup

		for _, stop := range []func(){w.npm.Stop, w.maven.Stop, w.deb.Stop, w.rpm.Stop, w.groupMeta.Stop} {
			drain.Go(stop)
		}

		drain.Wait()

		for _, stop := range a.extraStops {
			stop()
		}
	})
}

// features are the optional services built after the workers: federation, AI
// analysis, MCP, Dependency-Track and the attestation attacher.
type features struct {
	fed *federationRuntime

	envAI           ai.Settings
	aiWorker        *worker.AIAnalysisWorker
	uploadPublisher *worker.AIAutoAnalyzePublisher
	mcpServer       *aimcp.Server

	dtPusher    *dtrack.Pusher
	dtPublisher *dtrack.UploadPublisher
	attacher    *api.AttestationAttacher
	sbomGen     *sbom.Generator
}

// webStack is the HTTP side: the router, the authenticated sub-router every
// API and repository route is registered on, and what authentication shares.
type webStack struct {
	addr string

	router chi.Router
	authed chi.Router

	trustedProxies  *middleware.TrustedProxies
	brandManager    *branding.Manager
	spaHandler      http.Handler
	evaluator       *auth.Evaluator
	projectSettings *auth.ProjectSettings
	rbacMiddleware  func(http.Handler) http.Handler

	public  publicHandlers
	formats formatHandlers
	uploads uploadHandlers
}
