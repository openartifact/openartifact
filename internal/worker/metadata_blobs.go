// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"sort"

	sq "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// metadataBlobs keeps the blob-store objects behind generated
// repository_metadata rows consistent. Identical metadata content (the empty
// Packages/.gz/.xz of two repositories, say) is one blob shared across
// repositories, so
//
//   - a superseded blob may only be deleted after re-checking, under the
//     per-blob lock shared with uploaders, that no metadata row of any
//     repository and no package row still references it, and
//   - the freshly committed set is verified afterwards: a concurrent reclaim
//     from another repository can have removed an object this run had only
//     referenced from still-uncommitted rows.
//
// storage.SafeDeleteBlob is not used because it only counts package-table
// rows, not repository_metadata.
type metadataBlobs struct {
	db    sq.Database
	store storage.BlobStore
	log   *logger.Logger
	who   string // log prefix, e.g. "rpm worker"
}

// ensure re-stores any blob of the committed set that is missing from the blob
// store. A missing blob is written first, then its presence is checked again
// under the per-blob lock, which serializes with reclaim (the lock covers no
// blob store write; the commit is empty because the rows already exist).
// attrs builds the Put metadata for one path.
func (m metadataBlobs) ensure(ctx context.Context, repoID string, blobs map[string][]byte, newSHA map[string]string, attrs func(path, sum string) map[string]string) {
	for path, data := range blobs {
		sum := newSHA[path]

		err := storage.CommitBlob(ctx, m.db, m.store, storage.BytesBlob(sum, data, attrs(path, sum)), func(context.Context, string) error {
			return nil
		})
		if err != nil {
			m.log.Errorw(m.who+": failed to verify metadata blob", "repo_id", repoID, "path", path, "sha256", sum, "error", err)
		}
	}
}

// reclaim deletes superseded metadata blobs that nothing references any more.
func (m metadataBlobs) reclaim(ctx context.Context, shas []string) {
	for _, sum := range shas {
		if err := m.reclaimOne(ctx, sum); err != nil {
			m.log.Warnw(m.who+": failed to delete superseded metadata blob", "sha256", sum, "error", err)
		}
	}
}

func (m metadataBlobs) reclaimOne(ctx context.Context, sum string) error {
	if err := storage.ValidateBlobID(sum); err != nil {
		return fmt.Errorf("invalid blob id: %w", err)
	}

	return storage.WithBlobLock(ctx, m.db, sum, func(ctx context.Context) error {
		var refs int
		if err := m.db.GetDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_metadata WHERE blob_sha256 = $1`, sum).Scan(&refs); err != nil {
			return fmt.Errorf("count metadata references: %w", err)
		}

		if refs > 0 {
			return nil
		}

		pkgRefs, err := m.db.BlobReferenceCount(ctx, sum)
		if err != nil {
			return fmt.Errorf("count package references: %w", err)
		}

		if pkgRefs > 0 {
			return nil
		}

		return storage.DiscardUnreferencedBytes(ctx, m.db, m.store, sum)
	})
}

// supersededMetadata compares the previous and the freshly generated metadata
// set. stalePaths are previous paths that are no longer generated (their rows
// must go); reclaim are blob checksums that were referenced by a previous row
// that was removed or re-pointed and that the new set no longer uses (candidates
// for deletion once nothing else references them).
func supersededMetadata(oldSHA, newSHA map[string]string) ([]string, []string) {
	var stalePaths, reclaim []string

	stillUsed := make(map[string]struct{}, len(newSHA))
	for _, sum := range newSHA {
		stillUsed[sum] = struct{}{}
	}

	seen := make(map[string]struct{})

	for path, old := range oldSHA {
		cur, kept := newSHA[path]
		if !kept {
			stalePaths = append(stalePaths, path)
		}

		if old == "" || (kept && cur == old) {
			continue
		}

		if _, ok := stillUsed[old]; ok {
			continue
		}

		if _, dup := seen[old]; dup {
			continue
		}

		seen[old] = struct{}{}

		reclaim = append(reclaim, old)
	}

	sort.Strings(stalePaths)
	sort.Strings(reclaim)

	return stalePaths, reclaim
}
