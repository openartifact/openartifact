// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import "io"

// VerifiedFile is implemented by a Put reader that is the whole, unread content
// of a file whose SHA-256 was computed while the file was written (the upload
// spool). A backend that sees one skips the second spool-and-hash pass: the
// filesystem store links the file into place, the object stores upload it
// directly. VerifiedFile returns the file's path, its SHA-256 and size; ok is
// false when the reader is partly read or has no file behind it, and a store
// then reads it like any other reader. The file must stay unchanged for the
// duration of the Put.
type VerifiedFile interface {
	io.Reader
	VerifiedFile() (path, sha256 string, size int64, ok bool)
}

// verifiedFileMatching is verifiedFileOf for a reader whose caller claims a
// digest (empty: none): a file hashing to something else is not returned, so
// the store reads the reader and reports the mismatch itself.
func verifiedFileMatching(r io.Reader, claimed string) (string, string, int64, bool) {
	path, sha, size, ok := verifiedFileOf(r)
	if !ok || (claimed != "" && claimed != sha) {
		return "", "", 0, false
	}

	return path, sha, size, true
}

// verifiedFileOf returns the file behind r when r is a VerifiedFile that is
// still unread.
func verifiedFileOf(r io.Reader) (string, string, int64, bool) {
	vf, isVF := r.(VerifiedFile)
	if !isVF {
		return "", "", 0, false
	}

	return vf.VerifiedFile()
}
