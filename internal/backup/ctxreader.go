// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"io"
)

// ctxReader aborts a read once ctx is done, so copying a large blob into or
// out of an archive stops when the backup is cancelled (server shutdown).
type ctxReader struct {
	ctx context.Context //nolint:containedctx // a reader wrapper lives for one copy
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}

	return c.r.Read(p)
}
