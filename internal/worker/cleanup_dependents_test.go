// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/ociref"
)

// The docker candidate queries are assembled with format verbs: a verb left
// without its argument would only surface when a policy runs.
func TestDockerCleanupQueriesAreWellFormed(t *testing.T) {
	t.Parallel()

	table, ok := cleanupTableFor(dockerFormat)
	if !ok {
		t.Fatal("docker has no cleanup table")
	}

	queries := map[string]string{
		"candidates": table.sql().query([]string{"NOT prot", "rk > $2"}),
		"orphans":    orphanDependentsSQL(),
	}

	for name, q := range queries {
		if strings.Contains(q, "%!") || strings.Contains(q, "%[") {
			t.Errorf("%s query has an unresolved format verb:\n%s", name, q)
		}

		if !strings.Contains(q, ociref.TagPattern) {
			t.Errorf("%s query does not use the dependent tag pattern", name)
		}
	}
}
