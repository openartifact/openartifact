// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package bgtask

import (
	"context"
	"testing"
	"time"
)

func TestShutdownCancelsAndAwaitsTasks(t *testing.T) {
	r := New(t.Context())
	started := make(chan struct{})
	finished := make(chan struct{})

	r.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(finished)
	})
	<-started

	if !r.Shutdown(5 * time.Second) {
		t.Fatal("Shutdown timed out although the task honours cancellation")
	}

	select {
	case <-finished:
	default:
		t.Fatal("Shutdown returned before the task finished")
	}
}

func TestShutdownIsBounded(t *testing.T) {
	r := New(t.Context())
	release := make(chan struct{})
	started := make(chan struct{})

	r.Go(func(context.Context) {
		close(started)
		<-release
	})
	<-started

	if r.Shutdown(20 * time.Millisecond) {
		t.Fatal("Shutdown reported a drain although a task ignores cancellation")
	}

	close(release)

	if !r.Shutdown(5 * time.Second) {
		t.Fatal("second Shutdown must drain once the task returned")
	}
}

func TestGoAfterShutdownRunsWithCancelledContext(t *testing.T) {
	r := New(t.Context())
	r.Shutdown(time.Second)

	var got error

	r.Go(func(ctx context.Context) { got = ctx.Err() })

	if got == nil {
		t.Fatal("a task handed over after Shutdown must see a cancelled context")
	}
}
