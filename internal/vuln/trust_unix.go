// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package vuln

import (
	"os"
	"syscall"
)

// ownedByServer reports whether fi belongs to the user the server runs as.
func ownedByServer(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)

	return ok && int(st.Uid) == os.Geteuid()
}
