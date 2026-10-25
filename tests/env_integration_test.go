// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"os"
	"testing"
)

// skipOrFailMissingEnv skips locally but fails in CI, so a misconfigured
// pipeline cannot silently skip the integration tests.
func skipOrFailMissingEnv(t *testing.T, name string) {
	t.Helper()

	if os.Getenv("CI") != "" {
		t.Fatalf("%s not set in CI; integration services are required", name)
	}

	t.Skipf("%s not set", name)
}
