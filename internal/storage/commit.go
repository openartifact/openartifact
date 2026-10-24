// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"

	"github.com/openartifact/openartifact/internal/database"
)

// ErrBlobGone is returned by CommitBlobs when a blob that has no source to
// re-store it from (Blob.Open == nil) is missing from the store at commit
// time: a concurrent delete removed it after the caller last saw it. The
// operation is safe to retry from the start.
var ErrBlobGone = errors.New("blob missing from the store")

// StoreError is the error CommitBlobs returns when it could not look up or
// store a blob (as opposed to an error returned by the caller's commit
// function, which is passed through unchanged).
type StoreError struct {
	SHA256 string
	// Op is "check" for a failed existence check and "store" for a failed Put.
	Op  string
	Err error
}

func (e *StoreError) Error() string {
	return fmt.Sprintf("%s blob %s: %v", e.Op, e.SHA256, e.Err)
}

func (e *StoreError) Unwrap() error { return e.Err }

// Blob is one blob a CommitBlobs call makes sure is in the store.
type Blob struct {
	// SHA256 is the hex sha256 of the content; the blob's identity and the
	// key of its per-blob lock. Put always receives it as the "sha256"
	// metadata entry.
	SHA256 string
	// Metadata is the extra Put metadata ("sha256" is added).
	Metadata map[string]string
	// Open returns a reader over the whole content, positioned at its start.
	// It is called once per Put attempt (normally one; again only when a
	// concurrent delete removed the object before the row was committed). A nil
	// Open marks a blob that must already exist: it is checked under the lock
	// and a missing one fails with ErrBlobGone.
	Open func() (io.Reader, error)
}

// BytesBlob is a Blob whose content is held in memory.
func BytesBlob(sha string, data []byte, meta map[string]string) Blob {
	return Blob{SHA256: sha, Metadata: meta, Open: func() (io.Reader, error) { return bytes.NewReader(data), nil }}
}

// SeekerBlob is a Blob read from src, rewound to its start before every
// attempt (an *os.File spool, a *base.Spool section reader).
func SeekerBlob(sha string, src io.ReadSeeker, meta map[string]string) Blob {
	return Blob{SHA256: sha, Metadata: meta, Open: func() (io.Reader, error) {
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewind blob source: %w", err)
		}

		return src, nil
	}}
}

// ReaderBlob is a Blob whose reader open returns a fresh reader over the whole
// content each time it is called (base.Spool.Reader).
func ReaderBlob(sha string, meta map[string]string, open func() io.Reader) Blob {
	return Blob{SHA256: sha, Metadata: meta, Open: func() (io.Reader, error) { return open(), nil }}
}

// CommitBlob stores one blob and runs commit under its per-blob lock; see
// CommitBlobs. commit receives the blob's storage key.
func CommitBlob(ctx context.Context, db database.Database, store BlobStore, blob Blob, commit func(ctx context.Context, key string) error) error {
	return CommitBlobs(ctx, db, store, []Blob{blob}, func(ctx context.Context, keys []string) error {
		return commit(ctx, keys[0])
	})
}

// CommitBlobs makes sure every blob is in store and runs commit (the DB work:
// the blobs rows and the rows that reference them) under the blobs' per-blob
// locks, with the invariant that when commit runs, every blob's object exists
// in the store and no concurrent SafeDeleteBlob/sweeper can remove it before
// commit returns. It is the one way an uploader stores a blob.
//
// The bytes are written BEFORE the lock is taken: the lock is a database
// transaction pinning a pooled connection (and one of the bounded lock slots),
// so holding it across an object-store PUT would cap concurrent uploads at the
// slot count and let one large upload stall other uploads and deletes of the
// same blob. This is safe because a blob is content-addressed by sha256 (a Put
// of the same bytes is idempotent) and the only remover is the deferred
// deletion sweeper, which takes the per-blob lock and keeps bytes a row
// references. The window between the Put and the lock is the only one in which
// the object may vanish (a delete queued it, and the sweeper ran in between),
// so under the lock every object is checked again and one that vanished is
// stored again from its source, now under the lock; that is rare and keeps the
// critical section otherwise free of blob store writes. commit then runs with
// the object guaranteed present: a committed row never lacks its object.
//
// Blobs that already exist are not written again. Errors from looking up or
// storing a blob are returned as *StoreError, errors from commit unchanged.
// Nothing is cleaned up on failure: an object this call wrote and no row
// references is left for the caller's SafeDeleteBlob (or the orphan sweep),
// exactly as when the Put used to run under the lock.
//
// commit MUST NOT write blobs itself: a Put inside it would pin the lock again.
func CommitBlobs(ctx context.Context, db database.Database, store BlobStore, blobs []Blob, commit func(ctx context.Context, keys []string) error) error {
	if len(blobs) == 0 {
		return commit(ctx, nil)
	}

	keys := make([]string, len(blobs))
	shas := make([]string, len(blobs))

	for i, b := range blobs {
		shas[i] = b.SHA256

		if b.Open == nil {
			continue
		}

		key, exists, err := lookup(ctx, store, b)
		if err != nil {
			return err
		}

		if exists {
			keys[i] = key

			continue
		}

		if keys[i], err = put(ctx, store, b); err != nil {
			return err
		}
	}

	return WithBlobLocks(ctx, db, shas, func(ctx context.Context) error {
		for i, b := range blobs {
			key, exists, err := lookup(ctx, store, b)
			if err != nil {
				return err
			}

			if !exists {
				if b.Open == nil {
					return fmt.Errorf("%w: %s", ErrBlobGone, b.SHA256)
				}

				// A delete won the race between the Put and the lock.
				if key, err = put(ctx, store, b); err != nil {
					return err
				}
			}

			keys[i] = key
		}

		return commit(ctx, keys)
	})
}

// lookup reports whether the blob is in the store, and its storage key.
func lookup(ctx context.Context, store BlobStore, b Blob) (string, bool, error) {
	key, exists, err := store.ExistsBySHA256(ctx, b.SHA256)
	if err != nil {
		return "", false, &StoreError{SHA256: b.SHA256, Op: "check", Err: err}
	}

	return key, exists, nil
}

// put writes the blob and returns its storage key.
func put(ctx context.Context, store BlobStore, b Blob) (string, error) {
	r, err := b.Open()
	if err != nil {
		return "", &StoreError{SHA256: b.SHA256, Op: "store", Err: err}
	}

	meta := maps.Clone(b.Metadata)
	if meta == nil {
		meta = make(map[string]string, 1)
	}

	meta["sha256"] = b.SHA256

	key, err := store.Put(ctx, r, meta)
	if err != nil {
		return "", &StoreError{SHA256: b.SHA256, Op: "store", Err: err}
	}

	return key, nil
}
