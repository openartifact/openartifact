// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// swapDirsByRename moves live aside, puts fresh in its place and removes the
// old copy. Open files of the old copy stay readable until closed.
func swapDirsByRename(fresh, live string) error {
	old := live + ".old"
	if err := os.RemoveAll(old); err != nil {
		return fmt.Errorf("clear %s: %w", old, err)
	}

	if err := os.Rename(live, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move %s aside: %w", live, err)
	}

	if err := os.Rename(fresh, live); err != nil {
		// Put the previous copy back rather than leave no database.
		_ = os.Rename(old, live)

		return fmt.Errorf("install %s: %w", live, err)
	}

	return os.RemoveAll(old)
}
