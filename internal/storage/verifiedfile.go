// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import "io"

// VerifiedFile is implemented by a Put reader that is the whole, unread content
// of a file whose SHA-256 was computed while the file was written (the upload
// spool). A backend that sees one skips the second spool-and-hash pass: the
// filesystem store links the file into place, the object stores upload it
// directly. ok is false when the reader is partly read or has no file behind it;
// a store then reads it like any other reader. The file must stay unchanged for
// the duration of the Put.
type VerifiedFile interface {
	io.Reader
	VerifiedFile() (path, sha256 string, size int64, ok bool)
}

// verifiedFileOf returns the file behind r when r is a VerifiedFile that is
// still unread.
func verifiedFileOf(r io.Reader) (path, sha256 string, size int64, ok bool) {
	vf, isVF := r.(VerifiedFile)
	if !isVF {
		return "", "", 0, false
	}

	return vf.VerifiedFile()
}
