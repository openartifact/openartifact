// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"bytes"
	"context"
	"io"
)

// ReadBytes reads the licenses of an artifact held in memory.
func ReadBytes(ctx context.Context, format, filename string, data []byte) (Result, error) {
	return Read(ctx, format, filename, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
}
