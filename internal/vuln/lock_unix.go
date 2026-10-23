// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package vuln

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive advisory lock on path (created 0600) without
// blocking. It returns the unlock function, or held true when another process
// or open file description owns the lock.
func tryLockFile(path string) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}

		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}

	return func() {
		// Closing the descriptor releases the lock.
		_ = f.Close()
	}, false, nil
}
