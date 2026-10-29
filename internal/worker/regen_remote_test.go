// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/openartifact/openartifact/internal/workq"
)

// settled records what a queue reports to its consumer.
type settled struct {
	mu    sync.Mutex
	calls []settleCall
}

type settleCall struct {
	key  string
	done bool
}

func (s *settled) record(key string, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, settleCall{key, done})
}

func (s *settled) all() []settleCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]settleCall(nil), s.calls...)
}

func TestRegenQueueSettlesFinishedKeys(t *testing.T) {
	var got settled

	q := newTestQueue(t, func(context.Context, string) error { return nil })
	q.settle = got.record

	q.Notify("repo")
	pollUntil(t, "the regeneration to settle", func() bool { return len(got.all()) == 1 })

	if c := got.all()[0]; c.key != "repo" || !c.done {
		t.Fatalf("settle = %+v, want repo done", c)
	}
}

func TestRegenQueueSettlesAFailedRegenerationAsNotDone(t *testing.T) {
	var got settled

	q := newTestQueue(t, func(context.Context, string) error { return errTestBoom })
	q.settle = got.record

	q.Notify("repo")
	pollUntil(t, "the regeneration to settle", func() bool { return len(got.all()) == 1 })

	// A transient database or storage error must not drop the durable request:
	// it stays claimed until the lease lapses and is then retried.
	if c := got.all()[0]; c.done {
		t.Fatalf("settle = %+v, want not done", c)
	}
}

func TestRegenQueueSettlesALockThatStaysBusyAsNotDone(t *testing.T) {
	var got settled

	q := newTestQueue(t, func(context.Context, string) error { return errLockBusy })
	q.maxRetries = 2
	q.settle = got.record

	q.Notify("repo")
	pollUntil(t, "the regeneration to give up", func() bool { return len(got.all()) == 1 })

	if c := got.all()[0]; c.done {
		t.Fatalf("settle = %+v, want not done so the request is claimed again", c)
	}
}

func TestRegenQueueSettlesOnceForNotificationsDuringARun(t *testing.T) {
	var (
		got     settled
		started = make(chan struct{})
		release = make(chan struct{})
		runs    int
		runsMu  sync.Mutex
	)

	q := newTestQueue(t, func(context.Context, string) error {
		runsMu.Lock()
		runs++
		first := runs == 1
		runsMu.Unlock()

		if first {
			close(started)
			<-release
		}

		return nil
	})
	q.settle = got.record

	q.Notify("repo")
	<-started
	q.Notify("repo")
	close(release)

	pollUntil(t, "the rerun to settle", func() bool { return len(got.all()) >= 1 })

	runsMu.Lock()
	defer runsMu.Unlock()

	if runs != 2 || len(got.all()) != 1 {
		t.Fatalf("runs = %d, settles = %d; want 2 runs and one settle after the rerun", runs, len(got.all()))
	}
}

func TestMavenRegenCodecRoundTrip(t *testing.T) {
	k := mavenKey{repoID: "repo-1", groupID: "org.example", artifactID: "lib"}

	id, payload := mavenRegenCodec().encode(k)
	if len(id) != 64 {
		t.Fatalf("key = %q, want a sha256 hex digest", id)
	}

	got, err := mavenRegenCodec().decode(workq.Request{Kind: "regen:maven", Key: id, Payload: payload})
	if err != nil || got != k {
		t.Fatalf("decode = %+v, %v; want %+v", got, err, k)
	}

	other, _ := mavenRegenCodec().encode(mavenKey{repoID: "repo-1", groupID: "org.example", artifactID: "lib2"})
	if other == id {
		t.Fatal("different artifacts share a request key")
	}

	if _, err := mavenRegenCodec().decode(workq.Request{Key: id, Payload: []byte("not json")}); err == nil {
		t.Fatal("a malformed payload must be an error")
	}
}

func TestStringRegenCodecUsesTheIDAsKey(t *testing.T) {
	id, payload := stringRegenCodec().encode("repo-1")
	if id != "repo-1" || payload != nil {
		t.Fatalf("encode = %q, %v", id, payload)
	}

	got, err := stringRegenCodec().decode(workq.Request{Key: "repo-1"})
	if err != nil || got != "repo-1" {
		t.Fatalf("decode = %q, %v", got, err)
	}
}

func TestForwardingQueueWritesRequestsInsteadOfScheduling(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	ran := make(chan string, 1)
	q := newTestQueue(t, func(_ context.Context, key string) error {
		ran <- key
		return nil
	})
	q.setCodec(stringRegenCodec())
	q.forwardTo(workq.NewStore(db))

	mock.ExpectExec("INSERT INTO worker_requests .* ON CONFLICT \\(kind, key\\) DO UPDATE").
		WithArgs("regen:test", "repo-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	q.Notify("repo-1")

	select {
	case key := <-ran:
		t.Fatalf("a forwarding queue regenerated %q itself", key)
	default:
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardingQueueWritesAnIdleRequestOncePerWindow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	q := newTestQueue(t, func(context.Context, string) error { return nil })
	q.setCodec(stringRegenCodec())
	q.forwardTo(workq.NewStore(db))

	// Reads of a group fire notifyIfIdle on every request: one write, not one
	// per read.
	mock.ExpectExec("INSERT INTO worker_requests .* ON CONFLICT \\(kind, key\\) DO NOTHING").
		WithArgs("regen:test", "group-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	for range 5 {
		q.notifyIfIdle("group-1")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
