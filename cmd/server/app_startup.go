// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
)

// newApp loads and validates the configuration, initializes secret encryption
// and the logger.
func newApp(opts options) (*app, error) {
	cfg, err := config.Load(opts.configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	if err := crypto.InitFromEnv(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", crypto.EnvKey, err)
	}

	log, err := logger.New(cfg.Logger.Level, cfg.Logger.Format, cfg.Logger.OutputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	a := &app{opts: opts, cfg: cfg, log: log, tasks: bgtask.New(context.Background())}
	a.onClose(func() { _ = log.Sync() })

	return a, nil
}

// logStartup reports the encryption state and the build being started.
func (a *app) logStartup() {
	if !crypto.Enabled() {
		a.log.Warnw("OPENARTIFACT_SECRET_KEY not set: webhook secrets, upstream passwords and other stored credentials are NOT encrypted at rest")
	}

	a.log.Infow("Starting artifact repository manager",
		"version", version,
		"commit", commit,
		"build_time", buildTime,
		"server_host", a.cfg.Server.Host,
		"server_port", a.cfg.Server.Port,
	)
}

// setupTelemetry installs OpenTelemetry (off unless telemetry.otlp.enabled)
// before the database and the HTTP clients are used; it is flushed on the way
// out.
func (a *app) setupTelemetry() error {
	shutdownTelemetry, err := telemetry.Setup(context.Background(), a.cfg.Telemetry,
		telemetry.Build{Version: version, InstanceID: a.cfg.Server.InstanceID}, prometheus.DefaultGatherer, a.log)
	if err != nil {
		return a.fail("failed to initialize telemetry", err)
	}

	a.onClose(func() {
		ctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
		defer cancel()

		if err := shutdownTelemetry(ctx); err != nil {
			a.log.Warnw("telemetry shutdown", "error", err)
		}
	})

	if otlp := a.cfg.Telemetry.OTLP; otlp.Enabled {
		a.log.Infow("OpenTelemetry enabled",
			"protocol", otlp.Protocol,
			"service_name", otlp.ServiceName,
			"sample_ratio", otlp.SampleRatio,
			"metrics", otlp.Metrics,
		)
	}

	return nil
}

// openDatabase connects to Postgres. The database closes after everything
// registered later, including the background tasks that still use it.
func (a *app) openDatabase() error {
	db, err := database.NewPostgresDB(a.cfg.Database, a.log)
	if err != nil {
		return a.fail("failed to connect to database", err)
	}

	a.db = db
	a.onClose(func() { _ = db.Close() })
	// Registered after the database close so it runs before it: a cancelled
	// task still writes its failure state.
	a.onClose(a.drainTasks)

	db.SetTrashRetentionDays(a.cfg.Trash.RetentionDays)
	db.SetBlobDeletionGrace(a.cfg.BlobDeletion.Grace)

	a.log.Infow("Database connected",
		"host", a.cfg.Database.Host,
		"database", a.cfg.Database.Database,
	)

	return nil
}

// migrate applies the migrations: on request (-migrate, then the process is
// done, reported by the bool result) or at startup unless
// MIGRATE_ON_START=false.
func (a *app) migrate() (bool, error) {
	if a.opts.migrate {
		if err := a.db.Migrate(context.Background()); err != nil {
			return true, a.fail("migration failed", err)
		}

		a.log.Infow("Migrations applied successfully")

		return true, nil
	}

	if os.Getenv("MIGRATE_ON_START") == "false" {
		a.log.Warnw("MIGRATE_ON_START=false: automatic migrations skipped")

		return false, nil
	}

	if err := a.db.Migrate(context.Background()); err != nil {
		return true, a.fail("auto-migrate failed", err)
	}

	return false, nil
}

// initCore configures sessions, creates the bootstrap admin of an empty
// database, and builds the metrics registry and the audit logger.
func (a *app) initCore() error {
	auth.ConfigureSessionDuration(time.Duration(a.cfg.Auth.SessionDurationHours) * time.Hour)

	if err := auth.BootstrapIfEmpty(context.Background(), a.db, a.log); err != nil {
		return a.fail("bootstrap failed", err)
	}

	a.reg = metrics.NewRegistry()
	metrics.RegisterDBPools(a.db.PoolStat, a.db.GetDB().Stats)

	a.auditLogger = audit.NewLogger(a.db, a.log, a.reg, 1000)
	audit.SetDefault(a.auditLogger)

	return nil
}
