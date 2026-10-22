// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"context"
	"errors"
	"testing"
)

func TestGate(t *testing.T) {
	var unbounded Gate
	if err := unbounded.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}

	unbounded.Release()

	g := NewGate(1)
	if err := g.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := g.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("full gate: err = %v, want context.Canceled", err)
	}

	g.Release()

	if err := g.Acquire(t.Context()); err != nil {
		t.Fatalf("after release: %v", err)
	}
}
