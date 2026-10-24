// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package iocopy is io.Copy with a pooled buffer.
package iocopy

import (
	"io"
	"os"
	"sync"
)

// bufSize is the size of the pooled buffers.
const bufSize = 64 << 10

//nolint:gochecknoglobals // sync.Pool of scratch buffers is the point of the package
var pool = sync.Pool{New: func() any {
	b := make([]byte, bufSize)
	return &b
}}

// Copy copies src to dst through a pooled buffer. io.Copy allocates a fresh
// 32 KiB buffer per call whenever neither side is a plain file (an
// http.ResponseWriter's ReadFrom and os.File's WriteTo/ReadFrom both fall back
// to a generic copy that does), which dominated the allocation profile of blob
// downloads and uploads. A *os.File source into a destination that implements
// io.ReaderFrom (a file, a connection) keeps io.Copy so sendfile and
// copy_file_range still apply.
func Copy(dst io.Writer, src io.Reader) (int64, error) {
	if f, ok := src.(*os.File); ok {
		if rf, ok := dst.(io.ReaderFrom); ok {
			return rf.ReadFrom(f)
		}
	}

	bp, _ := pool.Get().(*[]byte)
	defer pool.Put(bp)

	// The wrappers hide ReaderFrom/WriterTo, which would bypass the pooled buffer.
	return io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, *bp)
}
