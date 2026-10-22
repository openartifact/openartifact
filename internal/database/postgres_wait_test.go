// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

var errDown = errors.New("connection refused")

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()

	l, err := logger.New("info", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return l
}

func TestWaitForDatabaseRetriesUntilUp(t *testing.T) {
	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls < 4 {
			return errDown
		}

		return nil
	}

	var waits []time.Duration

	sleep := func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)

		return true
	}

	if err := waitForDatabase(t.Context(), time.Minute, ping, sleep, testLogger(t)); err != nil {
		t.Fatalf("want success, got %v", err)
	}

	if calls != 4 || len(waits) != 3 {
		t.Fatalf("calls=%d waits=%v", calls, waits)
	}

	if waits[0] != connectBackoffInitial || waits[1] != 2*connectBackoffInitial {
		t.Fatalf("backoff not exponential: %v", waits)
	}
}

func TestWaitForDatabaseBackoffIsCapped(t *testing.T) {
	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls < 12 {
			return errDown
		}

		return nil
	}

	var maxWait time.Duration

	sleep := func(_ context.Context, d time.Duration) bool {
		maxWait = max(maxWait, d)

		return true
	}

	if err := waitForDatabase(t.Context(), time.Hour, ping, sleep, testLogger(t)); err != nil {
		t.Fatal(err)
	}

	if maxWait != connectBackoffMax {
		t.Fatalf("max wait %v, want %v", maxWait, connectBackoffMax)
	}
}

func TestWaitForDatabaseZeroTimeoutFailsImmediately(t *testing.T) {
	calls := 0
	ping := func(context.Context) error { calls++; return errDown }
	sleep := func(context.Context, time.Duration) bool {
		t.Fatal("must not sleep with timeout 0")

		return false
	}

	err := waitForDatabase(t.Context(), 0, ping, sleep, testLogger(t))
	if !errors.Is(err, errDown) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWaitForDatabaseTimesOut(t *testing.T) {
	// The injected sleep stands in for the real wait: it consumes real
	// wall-clock time only through a short spin on the deadline, so the
	// retry loop observes the timeout without a fixed sleep.
	timeout := 50 * time.Millisecond
	start := time.Now()
	ping := func(context.Context) error { return errDown }
	sleep := func(_ context.Context, d time.Duration) bool {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
		}

		return true
	}

	err := waitForDatabase(t.Context(), timeout, ping, sleep, testLogger(t))
	if !errors.Is(err, errDown) {
		t.Fatalf("want wrapped errDown, got %v", err)
	}

	if time.Since(start) < timeout {
		t.Fatalf("returned before the timeout elapsed")
	}
}

func TestWaitForDatabaseInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ping := func(context.Context) error { return errDown }
	sleep := func(c context.Context, d time.Duration) bool {
		cancel()

		return sleepContext(c, d)
	}

	err := waitForDatabase(ctx, time.Hour, ping, sleep, testLogger(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
