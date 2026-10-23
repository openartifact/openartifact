// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockPoll is how often a waiter retries a lock held by another process.
const lockPoll = 100 * time.Millisecond

// lockPath is the lock file of t: one per scanner, next to (not inside) its
// database and install directories, so that cleaning those never removes it.
func (t *cliTool) lockPath() string {
	return filepath.Join(filepath.Dir(t.cacheDir), "."+t.name+".lock")
}

// withLock runs fn holding t's exclusive cross-process lock, waiting for
// whoever holds it (another replica sharing the cache volume) until ctx ends.
// Installs and database updates of one scanner never overlap, in this process
// or any other using the same cache directory.
func (t *cliTool) withLock(ctx context.Context, fn func() error) error {
	unlock, err := t.acquire(ctx)
	if err != nil {
		return err
	}

	defer unlock()

	return fn()
}

func (t *cliTool) acquire(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(t.cacheDir), 0o750); err != nil {
		return nil, fmt.Errorf("create scanner cache dir: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("waiting for the %s cache lock: %w", t.name, err)
		}

		unlock, held, err := tryLockFile(t.lockPath())
		if err != nil {
			return nil, err
		}

		if !held {
			return unlock, nil
		}

		if !sleepCtx(ctx, lockPoll) {
			return nil, fmt.Errorf("waiting for the %s cache lock: %w", t.name, ctx.Err())
		}
	}
}
