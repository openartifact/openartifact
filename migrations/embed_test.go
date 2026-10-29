// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package migrations_test

import (
	"regexp"
	"testing"

	"github.com/openartifact/openartifact/migrations"
)

func TestFSContainsMigrations(t *testing.T) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	if len(entries) < 1 {
		t.Fatal("expected at least one embedded migration file")
	}

	if entries[0].Name() != "001_baseline.sql" {
		t.Errorf("first migration = %q, want 001_baseline.sql", entries[0].Name())
	}

	pattern := regexp.MustCompile(`^\d{3}_.*\.sql$`)
	for _, e := range entries {
		if !pattern.MatchString(e.Name()) {
			t.Errorf("entry %q does not match expected pattern", e.Name())
		}
	}
}
