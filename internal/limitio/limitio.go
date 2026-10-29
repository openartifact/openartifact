// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package limitio bounds how much of a reader is consumed. Unlike
// io.LimitReader, which reports a plain EOF at the limit, the readers here fail
// with the caller's error, so input over the limit is reported instead of
// being silently cut short (a decompression bomb is rejected, not half-read).
package limitio

import (
	"fmt"
	"io"
)

// capped is the reader Capped returns.
type capped struct {
	r        io.Reader
	left     int64
	tooLarge error
}

// Capped returns a reader that yields at most n bytes of r and then fails
// every read with tooLarge, even where r would have reported EOF.
func Capped(r io.Reader, n int64, tooLarge error) io.Reader {
	return &capped{r: r, left: n, tooLarge: tooLarge}
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, c.tooLarge
	}

	if int64(len(p)) > c.left {
		p = p[:c.left]
	}

	n, err := c.r.Read(p)
	c.left -= int64(n)

	return n, err
}

// ReadAll reads r to its end and fails with tooLarge (wrapped with the limit)
// when it holds more than limit bytes. It never holds more than limit+1 bytes.
func ReadAll(r io.Reader, limit int64, tooLarge error) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: max %d bytes", tooLarge, limit)
	}

	return data, nil
}
