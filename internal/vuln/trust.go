// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openartifact/openartifact/internal/pathseg"
)

var errInsecureInstall = errors.New("install location is not private to the server's user")

// checkInstallTree verifies that the installed binary and every directory from
// root (the cache's bin directory) down to it are real (not symlinked),
// owned by the user the server runs as and not writable by group or others,
// so no one else can swap the file between its verification and an exec. It
// returns the binary's stat.
func checkInstallTree(root, bin string) (os.FileInfo, error) {
	rel, err := filepath.Rel(root, bin)
	if err != nil || rel == "." || pathseg.Escapes(rel) {
		return nil, fmt.Errorf("%w: outside the install directory", errInsecureInstall)
	}

	for d := filepath.Dir(bin); ; d = filepath.Dir(d) {
		fi, err := os.Lstat(d)
		if err != nil {
			return nil, err
		}

		if !fi.IsDir() || !privateToServer(fi) {
			return nil, fmt.Errorf("%w: %s", errInsecureInstall, filepath.Base(d))
		}

		if d == root {
			break
		}
	}

	fi, err := os.Lstat(bin)
	if err != nil {
		return nil, err
	}

	if !fi.Mode().IsRegular() || !privateToServer(fi) {
		return nil, fmt.Errorf("%w: %s", errInsecureInstall, filepath.Base(bin))
	}

	return fi, nil
}

// privateToServer is true for an entry the server's user owns that neither
// group nor others can write.
func privateToServer(fi os.FileInfo) bool {
	return fi.Mode().Perm()&0o022 == 0 && ownedByServer(fi)
}

// sameFileState reports whether b describes the same file as a, unchanged:
// the same inode, size and modification time.
func sameFileState(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
