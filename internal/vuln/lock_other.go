// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package vuln

// tryLockFile has no advisory file locking to use on this platform, where the
// scanners are not supported as separate processes sharing a cache directory:
// the caller's in-process serialisation is all there is.
func tryLockFile(string) (func(), bool, error) {
	return func() {}, false, nil
}
