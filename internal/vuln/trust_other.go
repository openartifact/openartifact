// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package vuln

import "os"

// ownedByServer cannot tell on this platform, where file ownership is not a
// uid, so the mode check in checkInstallTree is all there is.
func ownedByServer(os.FileInfo) bool { return true }
