// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openartifact/openartifact/internal/pglock"
)

// tryClusterLock takes the cluster-wide advisory lock for key on a pinned
// connection of sqlDB, so a background pass runs on one replica at a time. A
// database without an SQL pool (test fakes) has no other replicas to exclude:
// the lock is trivially held.
func tryClusterLock(ctx context.Context, sqlDB *sql.DB, key string) (func(), bool, error) {
	if sqlDB == nil {
		return func() {}, true, nil
	}

	return pglock.Try(ctx, sqlDB, key)
}

// lockForRegeneration takes the advisory lock for key on a pinned connection
// held until the returned release func runs. A lock held by another session
// yields errLockBusy so the regeneration queue retries the key later instead of
// dropping it. The connection is only held while the lock is.
func lockForRegeneration(ctx context.Context, db *sql.DB, key string) (func(), error) {
	release, locked, err := pglock.Try(ctx, db, key)
	if err != nil {
		return nil, fmt.Errorf("advisory lock %s: %w", key, err)
	}

	if !locked {
		return nil, fmt.Errorf("advisory lock %s: %w", key, errLockBusy)
	}

	return release, nil
}
