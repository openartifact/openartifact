// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dtrack

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/openartifact/openartifact/internal/workq"
)

func TestForwardingPusherWritesAWorkerRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	p := NewPusher(newFakeStore(), &fakeSources{}, testLogger(t))
	p.ForwardTo(workq.NewStore(db))

	mock.ExpectExec("INSERT INTO worker_requests .* ON CONFLICT \\(kind, key\\) DO UPDATE").
		WithArgs(RequestKind, "repo-1/pkg:a@1", []byte(`{"repo_id":"repo-1","artifact_id":"pkg:a@1","trigger":"upload"}`)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if !p.Enqueue(Job{RepoID: "repo-1", ArtifactID: "pkg:a@1", Trigger: TriggerUpload}) {
		t.Fatal("Enqueue = false, want the request written")
	}

	if len(p.jobs) != 0 {
		t.Fatal("a forwarding pusher queued the job itself")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHandleRequestQueuesTheJobOfTheWorkerRequest(t *testing.T) {
	p := NewPusher(newFakeStore(), &fakeSources{}, testLogger(t))

	res := p.handleRequest(context.Background(), workq.Request{
		Kind: RequestKind, Key: "repo-1/a",
		Payload: []byte(`{"repo_id":"repo-1","artifact_id":"a","trigger":"scan"}`),
	})
	if res != workq.Done {
		t.Fatalf("result = %v, want Done", res)
	}

	select {
	case j := <-p.jobs:
		if j != (Job{RepoID: "repo-1", ArtifactID: "a", Trigger: TriggerScan}) {
			t.Fatalf("job = %+v", j)
		}
	default:
		t.Fatal("the request queued no job")
	}
}

func TestHandleRequestDropsAMalformedPayload(t *testing.T) {
	p := NewPusher(newFakeStore(), &fakeSources{}, testLogger(t))

	if res := p.handleRequest(context.Background(), workq.Request{Key: "k", Payload: []byte("nope")}); res != workq.Done {
		t.Fatalf("result = %v, want Done so a poison request is not repeated", res)
	}
}

func TestHandleRequestRetriesWhileTheQueueIsFull(t *testing.T) {
	p := NewPusher(newFakeStore(), &fakeSources{}, testLogger(t))
	p.jobs = make(chan Job) // no buffer, no worker: always full

	res := p.handleRequest(context.Background(), workq.Request{Payload: []byte(`{"repo_id":"r","artifact_id":"a","trigger":"scan"}`)})
	if res != workq.Retry {
		t.Fatalf("result = %v, want Retry", res)
	}
}
