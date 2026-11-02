// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// chunkPrefix is the object key prefix of upload chunks in object stores
	// (next to blobs/ and staging/) and the directory name on a filesystem.
	chunkPrefix = "uploads/"

	// chunkWalkBatch is how many chunk ids a walk hands to its callback at once.
	chunkWalkBatch = 200

	// chunkCleanupTimeout bounds the best-effort removal of a partly written
	// chunk in an object store.
	chunkCleanupTimeout = 30 * time.Second

	// chunkTempPrefix and chunkTempSuffix frame the temp file of a PutChunk in
	// the chunk directory; neither form parses as a UUID.
	chunkTempPrefix = ".chunk-"
	chunkTempSuffix = ".tmp"
)

var errInvalidChunkID = errors.New("invalid upload chunk id")

// validChunkID accepts the canonical 36-character UUID form only, so a chunk id
// is never anything but hex digits and dashes when it becomes a path or key.
func validChunkID(id string) error {
	if _, err := uuid.Parse(id); err != nil || len(id) != len(uuid.Nil.String()) {
		return fmt.Errorf("%w: %q", errInvalidChunkID, id)
	}

	return nil
}

// chunkKey is the object key of the chunk id in an object store.
func chunkKey(id string) (string, error) {
	if err := validChunkID(id); err != nil {
		return "", err
	}

	return chunkPrefix + id, nil
}

// dirChunkStore keeps every chunk as a plain file <dir>/<id> on this host.
type dirChunkStore struct {
	dir string
}

// NewDirChunkStore returns a ChunkStore over a directory of the local host. It
// is the fallback of ChunksFor for blob stores that cannot hold chunks: only
// the replica that wrote a chunk can read it back. The directory is created by
// the first PutChunk; a missing one simply holds no chunks.
func NewDirChunkStore(dir string) ChunkStore {
	return dirChunkStore{dir: dir}
}

// ChunksSupported implements ChunkStore.
func (dirChunkStore) ChunksSupported() bool { return true }

// path returns the file of the chunk id.
func (d dirChunkStore) path(id string) (string, error) {
	if err := validChunkID(id); err != nil {
		return "", err
	}

	return filepath.Join(d.dir, id), nil
}

// PutChunk implements ChunkStore. The body goes to a temp file in the chunk
// directory that is renamed into place once complete, so a failed or crashed
// write never leaves a chunk under id.
func (d dirChunkStore) PutChunk(ctx context.Context, id string, r io.Reader) (int64, error) {
	dst, err := d.path(id)
	if err != nil {
		return 0, err
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}

	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return 0, fmt.Errorf("chunks: create %s: %w", d.dir, err)
	}

	tmp, err := os.CreateTemp(d.dir, chunkTempPrefix+"*"+chunkTempSuffix)
	if err != nil {
		return 0, fmt.Errorf("chunks: create temp file: %w", err)
	}

	n, err := io.Copy(tmp, r)
	if err == nil {
		err = tmp.Sync()
	}

	if cerr := tmp.Close(); err == nil {
		err = cerr
	}

	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}

	if err != nil {
		_ = os.Remove(tmp.Name())

		return 0, fmt.Errorf("chunks: write %s: %w", id, err)
	}

	return n, nil
}

// OpenChunk implements ChunkStore.
func (d dirChunkStore) OpenChunk(_ context.Context, id string) (io.ReadCloser, error) {
	p, err := d.path(id)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrChunkNotFound, id)
		}

		return nil, fmt.Errorf("chunks: open %s: %w", id, err)
	}

	return f, nil
}

// DeleteChunk implements ChunkStore.
func (d dirChunkStore) DeleteChunk(_ context.Context, id string) error {
	p, err := d.path(id)
	if err != nil {
		return err
	}

	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("chunks: delete %s: %w", id, err)
	}

	return nil
}

// WalkChunks implements ChunkStore. Files that are not chunk ids are skipped,
// except the temp files of PutChunk: those older than olderThan belong to a
// write that died and are removed.
func (d dirChunkStore) WalkChunks(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	dir, err := os.Open(d.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("chunks: open %s: %w", d.dir, err)
	}

	defer func() { _ = dir.Close() }()

	batch := make([]string, 0, chunkWalkBatch)

	for {
		entries, rerr := dir.ReadDir(chunkWalkBatch)
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}

			id, ok := d.expiredChunk(e, olderThan)
			if !ok {
				continue
			}

			if batch = append(batch, id); len(batch) == chunkWalkBatch {
				if err := fn(batch); err != nil {
					return err
				}

				batch = make([]string, 0, chunkWalkBatch)
			}
		}

		if errors.Is(rerr, io.EOF) {
			if len(batch) == 0 {
				return nil
			}

			return fn(batch)
		}

		if rerr != nil {
			return fmt.Errorf("chunks: read %s: %w", d.dir, rerr)
		}
	}
}

// expiredChunk returns the id of the chunk file e when it was last modified
// before olderThan. A stale temp file is removed on the way and is no chunk.
func (d dirChunkStore) expiredChunk(e os.DirEntry, olderThan time.Time) (string, bool) {
	if !e.Type().IsRegular() {
		return "", false
	}

	name := e.Name()
	isTemp := strings.HasPrefix(name, chunkTempPrefix) && strings.HasSuffix(name, chunkTempSuffix)

	if !isTemp && validChunkID(name) != nil {
		return "", false
	}

	info, err := e.Info()
	if err != nil || !info.ModTime().Before(olderThan) {
		return "", false
	}

	if isTemp {
		_ = os.Remove(filepath.Join(d.dir, name))

		return "", false
	}

	return name, true
}
