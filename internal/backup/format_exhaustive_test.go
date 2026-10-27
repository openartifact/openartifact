// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestExportFormatTablesCoversEveryFormat: a format missing from the switch
// would be silently absent from backups, so every format must have a branch.
// Classified only: the branches call database list methods that the package's
// fake does not implement for every format.
func TestExportFormatTablesCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "backup.exportFormatTables", repotype.Names(), nil)
	if len(handled) != len(repotype.Names()) {
		t.Fatal("every format must be exported")
	}
}

// TestRegenerateRestoredMetadataFormats: formats with derived repository
// metadata are re-queued after a restore; the others serve straight from rows.
func TestRegenerateRestoredMetadataFormats(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "backup.regenerateRestoredMetadata",
		[]string{"rpm", "deb", "pacman", "apk", "maven"},
		[]string{"docker", "npm", "pypi", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan", "ivy", "sbt", "terraform"})

	// rpm and maven read the database, which an Engine built here has not.
	probed := map[string]string{"deb": "deb:", "pacman": "pacman:", "apk": "apk:"}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		if _, ok := probed[format]; !ok && slices.Contains(handled, format) {
			return
		}

		regen := &recordingRegen{}
		e := &Engine{regen: regen}
		e.regenerateRestoredMetadata(t.Context(), &restoreState{
			hostedRepos: []*models.Repository{{ID: "r1", Name: "r", Format: format}},
		})

		if want, ok := probed[format]; ok {
			if !slices.Equal(regen.calls, []string{want + "r1"}) {
				t.Errorf("%s: calls = %v, want %q", format, regen.calls, want+"r1")
			}

			return
		}

		if !slices.Contains(handled, format) && len(regen.calls) != 0 {
			t.Errorf("%s is ignored but triggered %v", format, regen.calls)
		}
	})
}
