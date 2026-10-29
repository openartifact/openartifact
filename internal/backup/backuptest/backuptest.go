// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package backuptest holds test doubles for the backup engine.
package backuptest

import (
	"context"

	"github.com/openartifact/openartifact/internal/database"
)

// LiveSnapshot adapts an in-memory fake to database.SnapshotReader: table reads
// go straight to the embedded reads (an in-memory fake is trivially
// consistent) and the blobs table is the Blobs slice, which the test keeps
// sorted by sha256. A fake's BeginBackupSnapshot returns one of these.
type LiveSnapshot struct {
	database.SnapshotReads

	Blobs []database.BlobRow
	// OnRelease, when set, runs on Release (tests assert the snapshot is ended).
	OnRelease func()
}

// ListBlobsAfter returns the next page of Blobs.
func (s *LiveSnapshot) ListBlobsAfter(_ context.Context, after string, limit int) ([]database.BlobRow, error) {
	var out []database.BlobRow

	for _, b := range s.Blobs {
		if b.SHA256 > after {
			out = append(out, b)
			if len(out) == limit {
				break
			}
		}
	}

	return out, nil
}

// CountBlobs returns len(Blobs).
func (s *LiveSnapshot) CountBlobs(context.Context) (int64, error) { return int64(len(s.Blobs)), nil }

// Release runs OnRelease.
func (s *LiveSnapshot) Release() {
	if s.OnRelease != nil {
		s.OnRelease()
	}
}
