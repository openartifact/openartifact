// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/lockfile"
)

// TestLockfileNamesDocumented keeps the hand-written docs that list the
// lockfiles `oa check` reads in step with pkg/lockfile: a new lockfile name
// cannot ship until these pages mention it.
func TestLockfileNamesDocumented(t *testing.T) {
	t.Parallel()

	pages := []string{"integrations/mcp.md", "integrations/ci.md"}
	for _, page := range pages {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", filepath.FromSlash(page)))
		if err != nil {
			t.Fatal(err)
		}

		for _, name := range lockfile.Files() {
			if !strings.Contains(string(b), name) {
				t.Errorf("docs/%s does not mention the lockfile %q", page, name)
			}
		}
	}
}
