// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"bytes"
	"fmt"
	"io"
)

// ReaderAtLimited returns r as an io.ReaderAt and its size without copying
// when r already is one (*io.SectionReader, *bytes.Reader: it must be unread,
// as their size ignores the read position); any other reader is read to its end
// into memory, failing with ErrContentTooLarge past limit bytes. Zip parsers need
// random access, so they use this instead of always buffering.
func ReaderAtLimited(r io.Reader, limit int64) (io.ReaderAt, int64, error) {
	if sized, ok := r.(interface {
		io.ReaderAt
		Size() int64
	}); ok {
		if sized.Size() > limit {
			return nil, 0, fmt.Errorf("%w: limit is %d bytes", ErrContentTooLarge, limit)
		}

		return sized, sized.Size(), nil
	}

	data, err := ReadAllLimited(r, limit)
	if err != nil {
		return nil, 0, err
	}

	return bytes.NewReader(data), int64(len(data)), nil
}
