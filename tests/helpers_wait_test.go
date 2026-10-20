// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"os"
	"testing"
	"time"
)

// waitUntil polls cond until it returns true or the deadline passes.
func waitUntil(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	if cond() {
		return
	}

	t.Fatalf("timed out waiting for: %s", msg)
}

// skipOrFailMissingEnv skips locally but fails in CI, so a misconfigured
// pipeline cannot silently skip the integration tests.
func skipOrFailMissingEnv(t *testing.T, name string) {
	t.Helper()

	if os.Getenv("CI") != "" {
		t.Fatalf("%s not set in CI; integration services are required", name)
	}

	t.Skipf("%s not set", name)
}
