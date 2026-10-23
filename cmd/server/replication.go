// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/replication"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// newReplication builds the blob replication service when storage.replica is
// configured (docs/operations/replication.md): it wraps the tiered primary
// store. It returns nil, nil when no replica is configured.
func newReplication(cfg *config.Config, db *database.PostgresDB, primary storage.BlobStore, reg *metrics.Registry, log *logger.Logger) (*replication.Service, error) {
	rc := cfg.StorageReplica
	if !rc.Enabled() {
		return nil, nil //nolint:nilnil // replication is optional
	}

	replica, err := storage.NewBlobStore(rc.StorageConfig)
	if err != nil {
		return nil, fmt.Errorf("open storage.replica: %w", err)
	}

	svc, err := replication.New(replication.Config{
		Primary: primary,
		Replica: replica,
		Queue:   db,
		SQLDB:   db.GetDB(),
		Lock: func(ctx context.Context, sha string, fn func(ctx context.Context) error) error {
			return storage.WithBlobLock(ctx, db, sha, fn)
		},
		Info:    replication.ReplicaInfo{Type: rc.Type, Location: rc.Summary()},
		Options: replication.OptionsFromConfig(rc),
		Metrics: reg.Replication,
		Log:     log,
	})
	if err != nil {
		return nil, fmt.Errorf("start blob replication: %w", err)
	}

	return svc, nil
}
