// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/storage"
)

// Upload chunks are scratch data: the tiered store hands them to the primary's
// chunk store, never to a cold tier, and tracks nothing for them.
func TestStoreChunksLiveOnThePrimary(t *testing.T) {
	r := newRig(t)
	id := uuid.NewString()

	c := storage.ChunksFor(r.store, t.TempDir())
	if !c.ChunksSupported() {
		t.Fatal("ChunksSupported = false")
	}

	if _, err := c.PutChunk(t.Context(), id, bytes.NewReader([]byte("chunk"))); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(r.hotDir, "uploads", id)); err != nil {
		t.Fatalf("chunk is not on the primary: %v", err)
	}

	if _, err := os.Stat(filepath.Join(r.coldDir, "uploads", id)); !os.IsNotExist(err) {
		t.Fatalf("chunk reached the cold tier: %v", err)
	}
}
