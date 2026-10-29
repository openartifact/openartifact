// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	formatstorage "github.com/openartifact/openartifact/internal/format/storage"
)

// Querier is the part of *sql.DB, *sql.Tx and *sql.Conn the codecs read with.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Regen asks for the derived metadata of a package to be regenerated after a
// replicated change, through the format's metadata worker (its RegenQueue). A
// change of any format reports one; the notifier ignores the formats whose
// metadata is generated on read.
type Regen struct {
	// Format is the repository format ("maven", "npm").
	Format string
	// RepoID is the repository.
	RepoID string
	// PackageID and Name are the packages row the change touched.
	PackageID string
	Name      string
	// Attrs is the packages.attrs of that row (the maven group and artifact id,
	// the npm scope); empty when it has none.
	Attrs json.RawMessage
}

// Attr returns the string value of a key of Attrs ("" when absent or not a string).
func (r Regen) Attr(key string) string {
	var attrs map[string]any
	if err := json.Unmarshal(r.Attrs, &attrs); err != nil {
		return ""
	}

	s, _ := attrs[key].(string)

	return s
}

// Reservation is quota to reserve before a unit is applied: the bytes of one
// file and 1 when that file counts as an artifact, as the quota_pending
// triggers of package_files consume them (migration 061): one reservation per
// file row the apply adds or re-points at another blob.
type Reservation struct {
	Size  int64
	Count int
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
	// Export reads the live state of a unit. It returns (nil, nil) when the
	// unit has no live row (never existed, or deleted).
	Export(ctx context.Context, q Querier, repoID, key string) (*Content, error)

	// Usage is the storage usage the unit adds to the repository's quota, as
	// the reservations to make before applying it (see Reservation). Files the
	// repository already holds with the same blob are not new usage.
	Usage(ctx context.Context, q Querier, repoID, key string, c *Content) ([]Reservation, error)

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

	// LockTables are the tables Enable locks against writers (SHARE mode) for
	// the instant between registering a repository's log and backfilling it, so
	// no concurrent change slips between the two.
	LockTables() []string
}

// liveRowsOfRepo is the predicate (table alias t) of the live rows of repository $1.
const liveRowsOfRepo = "t.repository_id = $1::uuid AND t.deleted_at IS NULL"

// backfillSource describes one federation_record call over a table: key and ref
// are the SQL expressions producing the unit key and its content_ref (see
// migration 071) and from is the FROM (and WHERE) clause selecting the rows.
type backfillSource struct {
	table string // names the source in error messages
	kind  string // KindArtifact or KindRef
	key   string
	ref   string
	// stamp is the SQL expression of the change stamp ("" for t.updated_at).
	stamp string
	from  string
}

// backfillUnits registers the rows of each source in the change log of the
// repository, with the row's updated_at as the stamp.
func backfillUnits(ctx context.Context, tx *sql.Tx, repoID string, sources ...backfillSource) error {
	for _, s := range sources {
		event := EventArtifactAdded
		if s.kind == KindRef {
			event = EventRefMoved
		}

		stamp := s.stamp
		if stamp == "" {
			stamp = "t.updated_at"
		}

		_, err := tx.ExecContext(ctx,
			//nolint:gosec // expressions come from the static sources
			`SELECT federation_record($1::uuid, `+s.key+`, $2, $3, `+s.ref+`, false, `+stamp+`) FROM `+s.from,
			repoID, s.kind, event)
		if err != nil {
			return fmt.Errorf("backfill %s: %w", s.table, err)
		}
	}

	return nil
}

// CodecFor returns the codec of a repository format: docker's dedicated one,
// or the generic package codec driven by the format's storage descriptor. A
// format without either is ErrUnsupportedFormat.
func CodecFor(format string) (Codec, error) {
	if format == dockerFormat {
		return dockerCodec{}, nil
	}

	st, ok := formatstorage.Lookup(format)
	if !ok || st.Federation == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
	}

	return newPackageCodec(format, st), nil
}

// dockerFormat is the one format with dedicated tables and its own codec.
const dockerFormat = "docker"

// blobInfo is a blobs row.
type blobInfo struct {
	id        string
	sha256    string
	size      int64
	mediaType string
}
