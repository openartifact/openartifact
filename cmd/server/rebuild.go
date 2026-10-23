// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// rebuildRegenDrain bounds the wait for the metadata workers to finish after a
// rebuild: a whole catalogue is regenerated, not one upload's worth.
const rebuildRegenDrain = 6 * time.Hour

var (
	errRebuildUsage   = errors.New("openartifact rebuild takes no positional arguments")
	errRebuildAborted = errors.New("rebuild did not complete")
)

// exitError makes main exit with a specific status without printing a message
// (the rebuild report already said everything).
type exitError struct {
	error

	code int
}

func (e exitError) Unwrap() error { return e.error }

type rebuildFlags struct {
	config          string
	report          string
	concurrency     int
	dryRun          bool
	deep            bool
	pruneSuperseded bool
	json            bool
}

func parseRebuildFlags(args []string) (*rebuildFlags, error) {
	var f rebuildFlags

	fs := flag.NewFlagSet("openartifact rebuild", flag.ContinueOnError)
	fs.StringVar(&f.config, "config", os.Getenv("OPENARTIFACT_CONFIG"), "path to YAML config file")
	fs.StringVar(&f.report, "report", "", "write the JSON report to this file")
	fs.IntVar(&f.concurrency, "concurrency", 8, "parallel storage reads")
	fs.BoolVar(&f.dryRun, "dry-run", false, "list what would be restored; write nothing")
	fs.BoolVar(&f.deep, "deep", false, "also scan the first bytes of blobs whose backend metadata lost the sidecar marker")
	fs.BoolVar(&f.pruneSuperseded, "prune-superseded", false, "delete older sidecars of rows that have a newer one")
	fs.BoolVar(&f.json, "json", false, "print the report as JSON on stdout (logs go to stderr)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}

		return nil, reportedError{err}
	}

	if fs.NArg() > 0 {
		return nil, errRebuildUsage
	}

	if f.concurrency < 1 {
		f.concurrency = 1
	}

	return &f, nil
}

// rebuildBlobStore opens the blob store sidecars are read from: the configured
// primary store. It is the one place the store is chosen.
func rebuildBlobStore(cfg *config.Config) (storage.BlobStore, error) {
	store, err := storage.NewBlobStore(cfg.Storage)
	if err != nil {
		return nil, fmt.Errorf("open blob store: %w", err)
	}

	return store, nil
}

// runRebuild implements `openartifact rebuild`: it loads the configuration like
// the server, brings the schema up to date (an empty database is fine), and
// restores the journaled rows found as sidecars in the blob store.
func runRebuild(args []string) error {
	f, err := parseRebuildFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}

	if err != nil {
		return err
	}

	cfg, err := config.Load(f.config)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	if err := crypto.InitFromEnv(); err != nil {
		return fmt.Errorf("invalid %s: %w", crypto.EnvKey, err)
	}

	// Logs go to stderr so --json owns stdout.
	log, err := logger.New(cfg.Logger.Level, cfg.Logger.Format, "stderr")
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	defer func() { _ = log.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep, err := executeRebuild(ctx, cfg, f, log)
	if rep != nil {
		if werr := emitReport(f, rep, os.Stdout, os.Stderr); werr != nil && err == nil {
			err = werr
		}
	}

	if err != nil {
		log.Errorw("rebuild failed", "error", err)

		return exitError{code: recovery.ExitFailed, error: fmt.Errorf("%w: %w", errRebuildAborted, err)}
	}

	if code := rep.ExitCode(); code != recovery.ExitOK {
		return exitError{code: code, error: errRebuildAborted}
	}

	return nil
}

func executeRebuild(ctx context.Context, cfg *config.Config, f *rebuildFlags, log *logger.Logger) (*recovery.Report, error) {
	store, err := rebuildBlobStore(cfg)
	if err != nil {
		return nil, err
	}

	db, err := database.NewPostgresDB(cfg.Database, log)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	defer func() { _ = db.Close() }()

	db.SetTrashRetentionDays(cfg.Trash.RetentionDays)

	// Same semantics as server start; the tool works on a database that has
	// never been migrated. A dry run migrates too (the schema is needed to tell
	// which rows exist) but writes no rows.
	if os.Getenv("MIGRATE_ON_START") != "false" {
		if err := db.Migrate(ctx); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}

	hooks := recovery.Hooks{}
	workers := newRebuildWorkers(db, cfg, log)

	defer workers.stop()

	if !f.dryRun {
		hooks.ReconcileUsage = func(ctx context.Context) error {
			_, err := db.ReconcileAllStorageUsage(ctx)
			if err != nil {
				return fmt.Errorf("reconcile storage usage: %w", err)
			}

			return nil
		}
		hooks.Regenerate = workers.regenerate(db)
	}

	rb := recovery.NewRebuilder(db.GetDB(), store, log, recovery.RebuildOptions{
		DryRun: f.dryRun, Deep: f.deep, PruneSuperseded: f.pruneSuperseded, Concurrency: f.concurrency,
	}, hooks)

	return rb.Run(ctx) //nolint:wrapcheck // reported as is
}

// rebuildWorkers are the metadata workers a rebuild uses to regenerate what a
// package row does not carry (repodata, dists, maven-metadata.xml, packuments).
type rebuildWorkers struct {
	rpm   *worker.RPMetadataWorker
	deb   *worker.DebMetadataWorker
	maven *worker.MavenMetadataWorker
	npm   *worker.NPMMetadataWorker
	once  sync.Once
}

func newRebuildWorkers(db *database.PostgresDB, cfg *config.Config, log *logger.Logger) *rebuildWorkers {
	// Metadata blobs are written to the primary store, whichever store the
	// sidecars are read from.
	blobs, err := storage.NewBlobStore(cfg.Storage)
	if err != nil {
		log.Warnw("rebuild: primary blob store unavailable; metadata will not be regenerated", "error", err)

		return &rebuildWorkers{}
	}

	reg := metrics.NewRegistry()
	limiter := worker.NewRegenLimiter(cfg.Workers.MetadataRegenConcurrency)
	keys := gpgkeys.New(db, log, fileSigner(cfg, log))

	return &rebuildWorkers{
		rpm:   worker.NewRPMetadataWorker(db, blobs, keys, log, reg).WithRegenLimiter(limiter).WithDrainTimeout(rebuildRegenDrain),
		deb:   worker.NewDebMetadataWorker(db, blobs, keys, log).WithMetrics(reg).WithRegenLimiter(limiter).WithDrainTimeout(rebuildRegenDrain),
		maven: worker.NewMavenMetadataWorker(db, blobs, db.GetDB(), log, reg).WithRegenLimiter(limiter).WithDrainTimeout(rebuildRegenDrain),
		npm:   worker.NewNPMMetadataWorker(db, log, db.GetDB(), reg).WithRegenLimiter(limiter).WithDrainTimeout(rebuildRegenDrain),
	}
}

// stop drains the pending regenerations and stops the workers; idempotent.
func (w *rebuildWorkers) stop() {
	w.once.Do(func() {
		if w.rpm == nil {
			return
		}

		var wg sync.WaitGroup

		for _, stop := range []func(){w.rpm.Stop, w.deb.Stop, w.maven.Stop, w.npm.Stop} {
			wg.Go(stop)
		}

		wg.Wait()
	})
}

// regenerate schedules the regeneration of the hosted repositories that
// received rows, then waits for the workers to drain.
func (w *rebuildWorkers) regenerate(db *database.PostgresDB) func(ctx context.Context, repoIDs, npmIDs []string) error {
	return func(ctx context.Context, repoIDs, npmIDs []string) error {
		if w.rpm == nil {
			return errors.New("no primary blob store to write metadata to") //nolint:err113 // one-off message
		}

		rows, err := db.GetDB().QueryContext(ctx, `
			SELECT id::text, format FROM repositories
			WHERE repo_type = 'hosted' AND id IN (SELECT unnest($1::text[])::uuid)`, repoIDs)
		if err != nil {
			return fmt.Errorf("list repositories to regenerate: %w", err)
		}

		type repo struct{ id, format string }

		var repos []repo

		for rows.Next() {
			var r repo
			if err := rows.Scan(&r.id, &r.format); err != nil {
				_ = rows.Close()

				return fmt.Errorf("scan repository: %w", err)
			}

			repos = append(repos, r)
		}

		_ = rows.Close()

		if err := rows.Err(); err != nil {
			return fmt.Errorf("list repositories to regenerate: %w", err)
		}

		for _, r := range repos {
			if err := w.notifyRepo(ctx, db, r.id, r.format); err != nil {
				return err
			}
		}

		for _, id := range npmIDs {
			if err := db.MarkPackumentStale(ctx, id); err != nil {
				return fmt.Errorf("mark packument stale: %w", err)
			}

			w.npm.NotifyStale(id)
		}

		w.stop()

		return nil
	}
}

func (w *rebuildWorkers) notifyRepo(ctx context.Context, db *database.PostgresDB, id, format string) error {
	switch format {
	case "rpm":
		if err := db.MarkRepoStale(ctx, id); err != nil {
			return fmt.Errorf("mark rpm repository stale: %w", err)
		}

		w.rpm.NotifyStale(id)
	case "deb":
		w.deb.NotifyStale(id)
	case "maven":
		rows, err := db.GetDB().QueryContext(ctx,
			`SELECT DISTINCT group_id, artifact_id_coord FROM maven_artifacts WHERE repository_id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return fmt.Errorf("list maven artifacts: %w", err)
		}

		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var g, a string
			if err := rows.Scan(&g, &a); err != nil {
				return fmt.Errorf("scan maven artifact: %w", err)
			}

			w.maven.NotifyStale(id, g, a)
		}

		return rows.Err() //nolint:wrapcheck // iteration error
	}

	return nil
}

// emitReport writes the report: JSON to stdout with --json (a summary goes to
// stderr), a text summary to stdout otherwise, and the JSON file for --report.
func emitReport(f *rebuildFlags, rep *recovery.Report, stdout, stderr io.Writer) error {
	if f.report != "" {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return fmt.Errorf("encode report: %w", err)
		}

		if err := os.WriteFile(f.report, //nolint:gosec // G703: the operator names the report file on the command line
			append(data, '\n'), 0o600); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}

	if f.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")

		if err := enc.Encode(rep); err != nil {
			return fmt.Errorf("print report: %w", err)
		}

		printSummary(stderr, rep)

		return nil
	}

	printSummary(stdout, rep)

	return nil
}

func printSummary(w io.Writer, rep *recovery.Report) {
	mode := "rebuild"
	if rep.DryRun {
		mode = "rebuild (dry run: nothing was written)"
	}

	_, _ = fmt.Fprintf(w, "%s: %d objects listed, %d sidecars valid, %d invalid, %d superseded (%d pruned), %d unsupported\n",
		mode, rep.ObjectsListed, rep.SidecarsValid, rep.SidecarsInvalid, rep.Superseded, rep.Pruned, rep.SidecarsUnsupported)

	for _, t := range rep.Tables {
		_, _ = fmt.Fprintf(w, "  %-28s found %-6d inserted %-6d present %-6d unrecoverable %-4d failed %d\n",
			t.Table, t.Found, t.Inserted, t.AlreadyPresent, t.Unrecoverable, t.Failed)
	}

	_, _ = fmt.Fprintf(w, "repositories recovered: %d; unreferenced blobs: %d; missing blobs: %d\n",
		len(rep.ReposRecovered), rep.UnreferencedBlobs, rep.MissingBlobCount)

	for _, msg := range rep.Warnings {
		_, _ = fmt.Fprintf(w, "WARNING: %s\n", msg)
	}

	for _, it := range rep.Failures {
		_, _ = fmt.Fprintf(w, "FAILED %s %s %s: %s\n", it.Table, it.Key, it.Blob, it.Reason)
	}

	for _, it := range rep.Unrecoverable {
		_, _ = fmt.Fprintf(w, "UNRECOVERABLE %s %s: %s\n", it.Table, it.Key, it.Reason)
	}

	for _, it := range rep.Invalid {
		_, _ = fmt.Fprintf(w, "INVALID SIDECAR %s: %s\n", it.Blob, it.Reason)
	}

	_, _ = fmt.Fprintf(w, "post steps: %v; exit status %d\n", rep.PostSteps, rep.ExitCode())
}
