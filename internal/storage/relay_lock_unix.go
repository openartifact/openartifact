// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package storage

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockShared takes a shared advisory lock on path (created 0600), waiting for
// an exclusive holder. Every open file description conflicts with the others,
// so it excludes other goroutines of this process as well as other replicas
// on the same volume. The returned function releases the lock.
func lockShared(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}

	if err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	return func() { _ = f.Close() }, nil
}

// tryLockExclusive takes an exclusive advisory lock on path without waiting.
// held is true when a shared or exclusive holder owns the lock.
func tryLockExclusive(path string) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
			return nil, true, nil
		}

		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}

	return func() { _ = f.Close() }, false, nil
}
