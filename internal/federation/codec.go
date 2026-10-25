// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"fmt"
)

// Querier is the part of *sql.DB, *sql.Tx and *sql.Conn the codecs read with.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Regen asks for the metadata of a package or repository to be regenerated
// after a replicated change, through the format's RegenQueue notifier.
type Regen struct {
	// Kind is the format name ("maven", "npm").
	Kind string
	// RepoID is the repository.
	RepoID string
	// GroupID and ArtifactID key a Maven refresh; PackageID keys an npm one.
	GroupID    string
	ArtifactID string
	PackageID  string
}

// ArtifactInfo describes a unit for webhooks and audit.
type ArtifactInfo struct {
	Name    string
	Version string
	Path    string
	SHA256  string
	Size    int64
}

// Change reports what applying or deleting a unit did, for the engine's
// post-commit work (quota, blob cleanup, metadata regeneration, webhooks).
type Change struct {
	// Replaced is true when a live row of the unit existed and was updated in
	// place; its previous usage is ReplacedSize/ReplacedCount.
	Replaced      bool
	ReplacedSize  int64
	ReplacedCount int

	// RemovedSize/RemovedCount is the usage of the row a Delete removed.
	RemovedSize  int64
	RemovedCount int

	// ReleasedBlobs lists sha256 digests that lost a reference (candidates for
	// storage.SafeDeleteBlob once the transaction committed). A soft delete
	// releases none: the trash keeps its blobs.
	ReleasedBlobs []string

	// Regen lists the metadata refreshes to queue.
	Regen []Regen

	// Artifact describes the unit; nil for a unit without a webhook event
	// (a tag, a package-level reference).
	Artifact *ArtifactInfo

	// Missing is true when Delete found no live row (nothing to do).
	Missing bool
}

// Codec maps the units of one repository format to their rows. A codec holds
// no state; every method takes the repository it works on. Keys are the unit
// keys of docs/design/federation.md, built identically by the capture triggers
// (migration 071).
type Codec interface {
	// Format is the repository format the codec serves.
	Format() string

	// Export reads the live state of a unit. It returns (nil, nil) when the
	// unit has no live row (never existed, or deleted).
	Export(ctx context.Context, q Querier, repoID, key string) (*Content, error)

	// Usage is the storage usage the unit's row will add to the repository's
	// quota: bytes and artifact count, as the quota_pending triggers count
	// them (migration 061). Blobs the repository already holds for the unit
	// are not new usage.
	Usage(ctx context.Context, q Querier, repoID, key string, c *Content) (size int64, count int, err error)

	// Import writes the unit as a live row, updating the live row of the same
	// key in place when there is one. All blobs of c exist in the blob store
	// and in the blobs table; blobIDs maps their sha256 to blobs.id. Rows are
	// written inside tx, which the engine owns; Import must not commit.
	Import(ctx context.Context, tx *sql.Tx, repoID, key string, c *Content, blobIDs map[string]string) (*Change, error)

	// Delete removes the live row of the unit: it stamps deleted_at/deleted_by
	// when soft is true (the trash keeps it), and removes it otherwise.
	Delete(ctx context.Context, tx *sql.Tx, repoID, key string, soft bool, deletedBy string) (*Change, error)

	// Backfill registers a unit in the change log for every live row of the
	// repository (enabling federation on a repository that has content), by
	// calling federation_record with each row's updated_at as its stamp.
	Backfill(ctx context.Context, tx *sql.Tx, repoID string) error
}

//nolint:gochecknoglobals // codec registry, filled by init functions of the codec files
var codecs = map[string]Codec{}

// register adds a codec; a format has exactly one.
func register(c Codec) {
	if _, dup := codecs[c.Format()]; dup {
		panic("federation: duplicate codec for " + c.Format())
	}

	codecs[c.Format()] = c
}

// CodecFor returns the codec of a repository format.
func CodecFor(format string) (Codec, error) {
	c, ok := codecs[format]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
	}

	return c, nil
}
