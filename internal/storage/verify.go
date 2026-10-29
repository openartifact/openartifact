// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// spoolBufSize is the copy buffer of spoolVerified. Large enough to keep the
// write syscalls few on multi-megabyte blobs.
const spoolBufSize = 128 * 1024

// spoolBufs pools the copy buffers: io.Copy into an *os.File allocates a fresh
// 32 KiB buffer per call, which showed up as io.copyBuffer in allocation
// profiles of upload-heavy servers.
var spoolBufs = sync.Pool{ //nolint:gochecknoglobals // process-wide buffer pool
	New: func() any {
		b := make([]byte, spoolBufSize)

		return &b
	},
}

// ErrDigestMismatch is returned by BlobStore.Put when the streamed bytes do
// not hash to the sha256 the caller claimed in the metadata. Nothing is
// stored when it is returned.
var ErrDigestMismatch = errors.New("blob digest mismatch")

// spoolVerified copies data into a private temp file in dir (os.TempDir when
// empty) while hashing it, and rejects the stream unless it hashes to want. On
// success the returned file is rewound and the caller owns it (Close and
// Remove). Every Put implementation that cannot commit a verified temp object
// atomically spools through here, so the bytes uploaded are exactly the bytes
// that were hashed.
func spoolVerified(data io.Reader, want, dir, pattern string) (*os.File, int64, error) {
	spool, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, 0, fmt.Errorf("create spool file: %w", err)
	}

	discard := func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}

	hasher := sha256.New()

	buf := spoolBufs.Get().(*[]byte) //nolint:forcetypeassert // the pool only holds *[]byte

	// The writer is wrapped so *os.File.ReadFrom does not take over and
	// allocate its own buffer; the TeeReader source has no WriteTo either.
	size, err := io.CopyBuffer(struct{ io.Writer }{spool}, io.TeeReader(data, hasher), *buf)

	spoolBufs.Put(buf)

	if err != nil {
		discard()

		return nil, 0, fmt.Errorf("spool blob data: %w", err)
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != want {
		discard()

		return nil, 0, fmt.Errorf("%w: claimed %s, computed %s", ErrDigestMismatch, want, got)
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		discard()

		return nil, 0, fmt.Errorf("rewind spool: %w", err)
	}

	return spool, size, nil
}

// staleSpoolAge is how old a leftover spool file must be before the startup
// sweep removes it: anything younger may belong to a live Put.
const staleSpoolAge = time.Hour

// spoolPatterns match the spool files of the S3, Azure and OCI backends.
var spoolPatterns = []string{"s3-put-*.tmp", "azure-put-*.tmp", "oci-put-*.tmp"}

// prepareSpoolDir creates the configured spool directory and removes the
// spool files a crashed Put left in it. An empty dir means os.TempDir and is
// neither created nor swept (it is shared with other programs).
func prepareSpoolDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create spool dir: %w", err)
	}

	sweepSpool(dir, staleSpoolAge)

	return dir, nil
}

// sweepSpool removes spool files older than minAge from dir. Best effort.
func sweepSpool(dir string, minAge time.Duration) {
	cutoff := time.Now().Add(-minAge)

	for _, pat := range spoolPatterns {
		matches, err := filepath.Glob(filepath.Join(dir, pat))
		if err != nil {
			continue
		}

		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
				_ = os.Remove(m)
			}
		}
	}
}
