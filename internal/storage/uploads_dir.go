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
	"time"

	"github.com/openartifact/openartifact/internal/iocopy"
)

// uploadWalkBatch is how many upload ids a walk hands to its callback at once.
const uploadWalkBatch = 200

// dirUploadStore keeps each upload as one file <dir>/<id> that grows with every
// append. FilesystemStore uses it under <blob root>/uploads (the same
// filesystem as the blobs, so promotion is a link); it is also the upload store
// of a blob store that cannot hold uploads (oci), where dir is a host
// directory only this replica can see.
type dirUploadStore struct {
	dir string
}

var _ UploadStore = dirUploadStore{}

// NewDirUploadStore returns an upload store over the directory dir (created on
// first use). PromoteUpload is not supported: the upload is not on the blob
// store's filesystem, so the caller streams it into BlobStore.Put.
func NewDirUploadStore(dir string) UploadStore {
	return dirUploadStore{dir: dir}
}

// UploadsSupported implements UploadStore.
func (dirUploadStore) UploadsSupported() bool { return true }

func (d dirUploadStore) path(id string) (string, error) {
	if err := validUploadID(id); err != nil {
		return "", err
	}

	return filepath.Join(d.dir, id), nil
}

// AppendUpload implements UploadStore. The file is cut back to the recorded
// offset first (a failed append may have left bytes behind), the body is
// written after it and synced, and only then is the position committed.
func (d dirUploadStore) AppendUpload(
	ctx context.Context,
	id string,
	at UploadPosition,
	r io.Reader,
	commit func(ctx context.Context, state string, n int64) error,
) error {
	path, err := d.path(id)
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("uploads: append %s: %w", id, err)
	}

	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return fmt.Errorf("uploads: create %s: %w", d.dir, err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("uploads: open %s: %w", id, err)
	}

	defer func() { _ = f.Close() }()

	if err := fitFile(f, at.Offset); err != nil {
		return err
	}

	if _, err := f.Seek(at.Offset, io.SeekStart); err != nil {
		return fmt.Errorf("uploads: seek %s: %w", id, err)
	}

	n, err := iocopy.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}

	if err != nil {
		return fmt.Errorf("uploads: write %s: %w", id, err)
	}

	if n == 0 {
		return nil
	}

	return commit(ctx, "", n)
}

// fitFile makes f exactly size bytes long: longer is cut back (bytes of an
// append that was never committed), shorter means data was lost.
func fitFile(f *os.File, size int64) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("uploads: stat %s: %w", f.Name(), err)
	}

	switch {
	case info.Size() < size:
		return fmt.Errorf("%w: %s holds %d of %d bytes", ErrUploadDataLost, filepath.Base(f.Name()), info.Size(), size)
	case info.Size() > size:
		if err := f.Truncate(size); err != nil {
			return fmt.Errorf("uploads: truncate %s: %w", f.Name(), err)
		}
	}

	return nil
}

// CompleteUpload implements UploadStore: the file is the object already, so
// completing it checks (and trims) its length and reports its version.
func (d dirUploadStore) CompleteUpload(_ context.Context, id string, at UploadPosition) (StagedObject, error) {
	path, err := d.path(id)
	if err != nil {
		return StagedObject{}, err
	}

	flags := os.O_RDWR

	if at.Offset == 0 {
		// An upload that never received a byte is the empty object.
		if err := os.MkdirAll(d.dir, 0o755); err != nil {
			return StagedObject{}, fmt.Errorf("uploads: create %s: %w", d.dir, err)
		}

		flags |= os.O_CREATE
	}

	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StagedObject{}, fmt.Errorf("%w: %s", ErrUploadDataLost, id)
		}

		return StagedObject{}, fmt.Errorf("uploads: open %s: %w", id, err)
	}

	defer func() { _ = f.Close() }()

	if err := fitFile(f, at.Offset); err != nil {
		return StagedObject{}, err
	}

	if err := f.Sync(); err != nil {
		return StagedObject{}, fmt.Errorf("uploads: sync %s: %w", id, err)
	}

	info, err := f.Stat()
	if err != nil {
		return StagedObject{}, fmt.Errorf("uploads: stat %s: %w", id, err)
	}

	return StagedObject{Size: info.Size(), Version: stagedVersion(info)}, nil
}

// PromoteUpload implements UploadStore.
func (dirUploadStore) PromoteUpload(context.Context, string, string, string, map[string]string) error {
	return ErrPromoteNotSupported
}

// OpenUpload implements UploadStore.
func (d dirUploadStore) OpenUpload(_ context.Context, id, version string) (io.ReadCloser, error) {
	path, err := d.path(id)
	if err != nil {
		return nil, err
	}

	return openVerifiedFile(path, version)
}

// openVerifiedFile opens path if it still is version (see stagedVersion).
func openVerifiedFile(path, version string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrStagingNotFound
		}

		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("stat %s: %w", filepath.Base(path), err)
	}

	if stagedVersion(info) != version {
		_ = f.Close()

		return nil, ErrStagingChanged
	}

	return f, nil
}

// DeleteUpload implements UploadStore.
func (d dirUploadStore) DeleteUpload(_ context.Context, id string) error {
	path, err := d.path(id)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("uploads: delete %s: %w", id, err)
	}

	return nil
}

// WalkUploads implements UploadStore.
func (d dirUploadStore) WalkUploads(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	dir, err := os.Open(d.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("uploads: open %s: %w", d.dir, err)
	}

	defer func() { _ = dir.Close() }()

	batch := make([]string, 0, uploadWalkBatch)

	for {
		entries, rerr := dir.ReadDir(uploadWalkBatch)
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("uploads: walk %s: %w", d.dir, err)
			}

			if id, ok := expiredUpload(e, olderThan); ok {
				batch = append(batch, id)
			}
		}

		if len(batch) > 0 {
			if err := fn(batch); err != nil {
				return err
			}

			batch = batch[:0]
		}

		if errors.Is(rerr, io.EOF) {
			return nil
		}

		if rerr != nil {
			return fmt.Errorf("uploads: read %s: %w", d.dir, rerr)
		}
	}
}

// expiredUpload returns the upload id of a directory entry that is a regular
// file named like an upload and last modified before olderThan.
func expiredUpload(e os.DirEntry, olderThan time.Time) (string, bool) {
	if !e.Type().IsRegular() || validUploadID(e.Name()) != nil {
		return "", false
	}

	info, err := e.Info()
	if err != nil || !info.ModTime().Before(olderThan) {
		return "", false
	}

	return e.Name(), true
}
