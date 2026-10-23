// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package vuln

// swapDirs makes the directory at fresh take the place of live and removes
// what was live; without an atomic exchange there is a moment between the
// two renames in which live is absent.
func swapDirs(fresh, live string) error {
	return swapDirsByRename(fresh, live)
}
