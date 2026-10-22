// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	connectBackoffInitial = 500 * time.Millisecond
	connectBackoffMax     = 5 * time.Second
)

// waitForDatabase calls ping until it succeeds or timeout elapses, backing off
// exponentially (capped at connectBackoffMax) between attempts and logging
// every wait at info level. A timeout of 0 (or less) makes a single attempt.
// sleep waits for d or until ctx is done and reports whether the full wait
// elapsed; tests inject it to avoid real delays.
func waitForDatabase(
	ctx context.Context,
	timeout time.Duration,
	ping func(context.Context) error,
	sleep func(context.Context, time.Duration) bool,
	log *logger.Logger,
) error {
	err := ping(ctx)
	if err == nil || timeout <= 0 {
		return err
	}

	deadline := time.Now().Add(timeout)
	backoff := connectBackoffInitial

	for attempt := 1; ; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("database not reachable after %s: %w", timeout, err)
		}

		wait := min(backoff, remaining)

		log.Infow("Waiting for database",
			"attempt", attempt,
			"retry_in", wait.String(),
			"timeout_remaining", remaining.Round(time.Second).String(),
			"error", err,
		)

		if !sleep(ctx, wait) {
			return fmt.Errorf("waiting for database interrupted: %w", ctx.Err())
		}

		if err = ping(ctx); err == nil {
			return nil
		}

		backoff = min(backoff*2, connectBackoffMax)
	}
}

// sleepContext waits for d or until ctx is done; it reports whether d elapsed.
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
