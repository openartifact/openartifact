// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// An unchanged artifact must not be rewritten on every regeneration: the
// document embeds lastUpdated, which used to change the hash each time.
func TestMavenRegenerateSkipsUnchangedMetadata(t *testing.T) {
	db := &benchWorkerDB{artifacts: benchMavenArtifacts(20)}
	w := &MavenMetadataWorker{db: db, blobStore: formattest.NewBlobStore()}

	regen := func() int {
		before := db.upserts
		if err := w.regenerate(context.Background(), "repo-1", "com.example", "app"); err != nil {
			t.Fatal(err)
		}

		return db.upserts - before
	}

	first := regen()
	if first == 0 {
		t.Fatal("first regeneration wrote nothing")
	}

	if again := regen(); again != 0 {
		t.Errorf("unchanged regeneration wrote %d metadata rows, want 0", again)
	}

	// A new release changes the group document only; snapshot documents stay.
	db.artifacts = append(db.artifacts, &models.MavenArtifact{Version: "9.9.9", Extension: "jar"})

	if changed := regen(); changed != 1 {
		t.Errorf("after adding a release wrote %d rows, want 1 (group metadata)", changed)
	}
}
