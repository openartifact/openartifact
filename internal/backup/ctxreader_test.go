// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCtxReader_StopsCopyWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	src := ctxReader{ctx: ctx, r: strings.NewReader(strings.Repeat("x", 1<<20))}

	buf := make([]byte, 16)
	if _, err := src.Read(buf); err != nil {
		t.Fatalf("read before cancel: %v", err)
	}

	cancel()

	n, err := io.Copy(io.Discard, src)
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("copy after cancel = %d, %v; want 0, context canceled", n, err)
	}
}
