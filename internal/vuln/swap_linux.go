// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package vuln

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// swapDirs makes the directory at fresh take the place of live in one atomic
// step (renameat2 RENAME_EXCHANGE) and removes what was live. When live does
// not exist yet, or the filesystem cannot exchange, it falls back to renames.
func swapDirs(fresh, live string) error {
	err := unix.Renameat2(unix.AT_FDCWD, fresh, unix.AT_FDCWD, live, unix.RENAME_EXCHANGE)
	if err == nil {
		// fresh now holds the previous live directory.
		return os.RemoveAll(fresh)
	}

	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		return swapDirsByRename(fresh, live)
	}

	return fmt.Errorf("exchange %s: %w", live, err)
}
