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
)

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

	size, err := io.Copy(spool, io.TeeReader(data, hasher))
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
