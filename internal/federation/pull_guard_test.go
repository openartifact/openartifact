// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/openartifact/openartifact/pkg/logger"
)

// pullGuardEnv is an engine pulling from a peer that answers every request
// with body. The database is a mock that accepts the progress writes.
type pullGuardEnv struct {
	e  *Engine
	pc *pullCtx
}

func newPullGuardEnv(t *testing.T, body any) *pullGuardEnv {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	mock.MatchExpectationsInOrder(false)
	mock.ExpectExec("UPDATE federation_peers").WillReturnResult(sqlmock.NewResult(0, 1))

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	u, _ := url.Parse(srv.URL)

	return &pullGuardEnv{
		e: &Engine{Store: NewStore(db), SQL: db, Log: log, BatchSize: 10},
		pc: &pullCtx{
			repo: &RepoInfo{Name: "repo"}, peer: &Peer{ID: uuid.NewString(), Name: "peer"},
			client: &Client{base: u, token: "t", http: srv.Client()},
		},
	}
}

func TestDrainStopsOnAPageThatPromisesMoreButMovesNothing(t *testing.T) {
	t.Parallel()

	logID := uuid.New()
	env := newPullGuardEnv(t, LogResponse{LogID: logID, HeadSeq: 5, More: true})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := env.e.drain(ctx, env.pc, &Progress{RemoteLogID: logID})
	if !errors.Is(err, errPeerResponse) {
		t.Fatalf("drain = %v, want errPeerResponse instead of asking for the same page again", err)
	}
}

func TestApplyPageRefusesAnEntryNotAfterTheCursor(t *testing.T) {
	t.Parallel()

	env := newPullGuardEnv(t, struct{}{})
	prog := &Progress{Cursor: 7}

	err := env.e.applyPage(context.Background(), env.pc, []Entry{{Seq: 7, UnitKey: "u"}}, prog)
	if !errors.Is(err, errPeerResponse) {
		t.Fatalf("applyPage = %v, want errPeerResponse", err)
	}

	if prog.Cursor != 7 {
		t.Errorf("cursor = %d, want it left at 7", prog.Cursor)
	}
}

func TestApplyFetchedRefusesAnotherUnitThanAskedFor(t *testing.T) {
	t.Parallel()

	env := newPullGuardEnv(t, UnitResponse{Entry: Entry{Seq: 1, UnitKey: "other", Kind: "k"}})

	res, err := env.e.applyFetched(context.Background(), env.pc, "wanted", 0)

	var de *deferError
	if !errors.As(err, &de) || de.reason != "invalid_unit" || !errors.Is(err, errUnitKeyMismatch) {
		t.Fatalf("applyFetched = %v, %v; want an invalid_unit deferral", res, err)
	}

	if res != outcomeDeferred {
		t.Errorf("outcome = %v, want deferred", res)
	}
}
