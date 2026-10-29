// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type TestHarness struct {
	DB        *sql.DB
	Database  *database.PostgresDB
	BlobStore storage.BlobStore
	Container *postgres.PostgresContainer
	TempDir   string
	Log       *logger.Logger
}

func NewHarness(ctx context.Context) (*TestHarness, error) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		return nil, fmt.Errorf("creating logger: %w", err)
	}

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("starting postgres container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		container.Terminate(ctx)
		return nil, fmt.Errorf("getting container host: %w", err)
	}

	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		container.Terminate(ctx)
		return nil, fmt.Errorf("getting container port: %w", err)
	}

	postgresDB, err := database.NewPostgresDB(database.DatabaseConfig{
		Driver:                 "postgres",
		Host:                   host,
		Port:                   int(port.Num()),
		User:                   "testuser",
		Password:               "testpass",
		Database:               "testdb",
		SSLMode:                "disable",
		MaxOpenConns:           10,
		MaxIdleConns:           2,
		ConnMaxLifetimeMinutes: 5,
	}, log)
	if err != nil {
		container.Terminate(ctx)
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	db := postgresDB.GetDB()

	migrator := database.NewMigrator(db)
	if err := migrator.Apply(ctx); err != nil {
		postgresDB.Close()
		container.Terminate(ctx)
		return nil, fmt.Errorf("applying migrations: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "openartifact-integration-*")
	if err != nil {
		postgresDB.Close()
		container.Terminate(ctx)
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}

	blobStore, err := storage.NewFilesystemStore(tempDir)
	if err != nil {
		os.RemoveAll(tempDir)
		postgresDB.Close()
		container.Terminate(ctx)
		return nil, fmt.Errorf("creating filesystem store: %w", err)
	}

	return &TestHarness{
		DB:        db,
		Database:  postgresDB,
		BlobStore: blobStore,
		Container: container,
		TempDir:   tempDir,
		Log:       log,
	}, nil
}

func (h *TestHarness) Reset(t *testing.T) {
	t.Helper()
	tables := []string{
		"repository_metadata",
		"rpm_packages",
		"rpm_repos",
		"blobs",
		"repositories",
	}
	for _, table := range tables {
		if _, err := h.DB.ExecContext(t.Context(), "TRUNCATE TABLE "+table+" CASCADE"); err != nil {
			t.Fatalf("truncating %s: %v", table, err)
		}
	}
}

func (h *TestHarness) Close(ctx context.Context) {
	if h.Database != nil {
		h.Database.Close()
	}
	if h.TempDir != "" {
		os.RemoveAll(h.TempDir)
	}
	if h.Container != nil {
		h.Container.Terminate(ctx)
	}
}
