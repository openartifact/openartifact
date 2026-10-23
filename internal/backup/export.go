// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
)

// DefaultLeaseTTL is how long a backup's blob-retention lease survives without a
// heartbeat (see database.SnapshotReader).
const DefaultLeaseTTL = 5 * time.Minute

// blobPageSize is the page size of the blobs table walk of a full backup.
const blobPageSize = 1000

var (
	// ErrBlobMissing reports a blob that the database snapshot lists but whose
	// bytes the store cannot produce. A backup without it could not be restored,
	// so the export fails instead of skipping it.
	ErrBlobMissing = errors.New("blob listed in the database snapshot is missing from storage")
	// ErrBlobCorrupt reports stored bytes that do not hash to their blob id or
	// do not have the recorded size.
	ErrBlobCorrupt = errors.New("stored blob does not match its recorded sha256 or size")
	// ErrBlobCountMismatch reports an archive that holds fewer blobs than the
	// snapshot lists.
	ErrBlobCountMismatch = errors.New("archive holds fewer blobs than the database snapshot lists")
)

// SetLeaseTTL sets how long a running backup's blob-retention lease lives
// without a heartbeat. Zero or negative selects DefaultLeaseTTL.
func (e *Engine) SetLeaseTTL(d time.Duration) { e.leaseTTL = d }

func (e *Engine) leaseTTLOrDefault() time.Duration {
	if e.leaseTTL <= 0 {
		return DefaultLeaseTTL
	}

	return e.leaseTTL
}

// exporter writes one backup archive from one database snapshot. It embeds the
// Engine for its helpers but shadows db with the snapshot reader: every export
// method reads through it, so all tables and the blob list come from the same
// instant, and none of them can reach a write method.
type exporter struct {
	*Engine

	db database.SnapshotReader
}

// exportArchive registers a backup lease, freezes the database, and writes the
// metadata tables and (for a full backup) every blob that snapshot references.
//
// Blobs deleted while the backup runs stay readable: deleting a blob only
// queues its bytes (database.SnapshotReader), and the lease keeps the sweeper
// from removing anything this snapshot can still see until the backup ends.
func (e *Engine) exportArchive(ctx context.Context, tw *tar.Writer, manifest *BackupManifest, backupType string) error {
	snap, err := e.db.BeginBackupSnapshot(ctx, e.leaseTTLOrDefault())
	if err != nil {
		return fmt.Errorf("failed to take database snapshot: %w", err)
	}
	defer snap.Release()

	manifest.SnapshotAt = time.Now().UTC()

	x := &exporter{Engine: e, db: snap}

	if err := x.exportMetadata(ctx, tw, manifest); err != nil {
		return fmt.Errorf("failed to export metadata: %w", err)
	}

	if backupType != "full" {
		return nil
	}

	if err := x.exportBlobs(ctx, tw, manifest); err != nil {
		return fmt.Errorf("failed to export blobs: %w", err)
	}

	return nil
}

// exportBlobs writes every stored object of the snapshot, in sha256 order, to
// the archive: the blobs rows plus the generated metadata and deltas that have
// none. It reads the tables (not the storage listing), so exactly the objects
// the exported records reference are archived, whichever backend or tier holds
// them. The expected count comes from the snapshot; an archive with fewer is an
// error.
func (x *exporter) exportBlobs(ctx context.Context, tw *tar.Writer, manifest *BackupManifest) error {
	expected, err := x.db.CountBlobs(ctx)
	if err != nil {
		return err
	}

	var exported int64

	after := ""

	for {
		page, err := x.db.ListBlobsAfter(ctx, after, blobPageSize)
		if err != nil {
			return err
		}

		for _, b := range page {
			if err := x.exportOneBlob(ctx, tw, b); err != nil {
				return err
			}

			exported++
		}

		if len(page) < blobPageSize {
			break
		}

		after = page[len(page)-1].SHA256
	}

	if exported < expected {
		return fmt.Errorf("%w: exported %d of %d", ErrBlobCountMismatch, exported, expected)
	}

	manifest.TableCounts["blobs"] = int(exported)
	manifest.BlobCount = int(expected)

	return nil
}

// exportOneBlob copies one blob into the archive, verifying its sha256 and
// size while streaming. The store reads across tiers, so a copy that is being
// moved (or whose tier copy was just retired) is still found.
func (x *exporter) exportOneBlob(ctx context.Context, tw *tar.Writer, b database.BlobRow) error {
	reader, err := x.blobStore.Get(ctx, b.SHA256)
	if errors.Is(err, storage.ErrBlobNotFound) {
		return fmt.Errorf("%w: %s", ErrBlobMissing, b.SHA256)
	}

	if err != nil {
		return fmt.Errorf("failed to get blob %s: %w", b.SHA256, err)
	}

	defer func() { _ = reader.Close() }()

	header := &tar.Header{
		Name: "blobs/" + b.SHA256,
		Size: b.SizeBytes,
		Mode: 0o600,
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write blob header: %w", err)
	}

	h := sha256.New()

	n, err := io.Copy(io.MultiWriter(tw, h), reader)
	if errors.Is(err, tar.ErrWriteTooLong) {
		return fmt.Errorf("%w: %s (more than the recorded %d bytes)", ErrBlobCorrupt, b.SHA256, b.SizeBytes)
	}

	if err != nil {
		return fmt.Errorf("failed to write blob content %s: %w", b.SHA256, err)
	}

	if n != b.SizeBytes || hex.EncodeToString(h.Sum(nil)) != b.SHA256 {
		return fmt.Errorf("%w: %s (%d bytes, recorded %d)", ErrBlobCorrupt, b.SHA256, n, b.SizeBytes)
	}

	return nil
}
