// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/promote"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newCopyHandler(t *testing.T, db *authzMockDB) *CopyMoveHandler {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	return NewCopyMoveHandler(db, log, auth.NewEvaluator(db, log, false))
}

func (c authzCaller) transfer(t *testing.T, h *CopyMoveHandler, op, source, body string) *httptest.ResponseRecorder {
	t.Helper()

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), c.p)))
		})
	})
	h.RegisterAuthed(router)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/repositories/"+source+"/"+op, strings.NewReader(body))
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

const copyBody = `{"target":"target","items":[{"package":"p","version":"1"}]}`

// TestCopyMoveAuthorizationGate: every refusal that happens before the engine
// runs, for copy and for move.
func TestCopyMoveAuthorizationGate(t *testing.T) {
	db := newAuthzDB()
	db.grant("reader-id", "src", "read")
	db.grant("reader-id", "target", "read")
	db.grant("writer-id", "target", "read", "write")
	db.grant("both-id", "src", "read")
	db.grant("both-id", "target", "read", "write")
	db.grant("blind-id", "target", "write")
	h := newCopyHandler(t, db)

	reader, writer, both, blind, stranger := authzUser(
		"reader",
	), authzUser(
		"writer",
	), authzUser(
		"both",
	), authzUser(
		"blind",
	), authzUser(
		"stranger",
	)

	for _, op := range []string{"copy", "move"} {
		// No read on the source: it does not exist for the caller.
		expectStatus(t, op+" by a stranger", stranger.transfer(t, h, op, "src", copyBody), http.StatusNotFound)
		expectStatus(t, op+" by a caller with write on the target only", writer.transfer(t, h, op, "src", copyBody), http.StatusNotFound)
		expectStatus(
			t,
			op+" by a caller that cannot read the source but writes the target",
			blind.transfer(t, h, op, "src", copyBody),
			http.StatusNotFound,
		)
		// Read on the source, no write on the target.
		expectStatus(t, op+" without write on the target", reader.transfer(t, h, op, "src", copyBody), http.StatusForbidden)
		// A target the caller cannot see is unknown.
		expectStatus(
			t,
			op+" into an invisible target",
			both.transfer(t, h, op, "src", `{"target":"other","items":[{"package":"p","version":"1"}]}`),
			http.StatusNotFound,
		)
		expectStatus(
			t,
			op+" into a missing target",
			both.transfer(t, h, op, "src", `{"target":"nope","items":[{"package":"p","version":"1"}]}`),
			http.StatusNotFound,
		)
		// Body checks and the admin-only force.
		expectStatus(t, op+" without items", both.transfer(t, h, op, "src", `{"target":"target","items":[]}`), http.StatusBadRequest)
		expectStatus(
			t,
			op+" without a target",
			both.transfer(t, h, op, "src", `{"items":[{"package":"p","version":"1"}]}`),
			http.StatusBadRequest,
		)
		expectStatus(
			t,
			op+" with force by a non-admin",
			both.transfer(t, h, op, "src", `{"target":"target","force":true,"items":[{"package":"p","version":"1"}]}`),
			http.StatusForbidden,
		)
		expectStatus(t, op+" from an unknown source", both.transfer(t, h, op, "nope", copyBody), http.StatusNotFound)
	}
}

// TestCopyMoveScopedTokenIsLimitedToItsTargets: a token scoped to the source
// grant cannot see the target, even though its owner can write it.
func TestCopyMoveScopedTokenIsLimitedToItsTargets(t *testing.T) {
	db := newAuthzDB()
	db.grant("ci-id", "src", "read", "delete")
	db.grant("ci-id", "target", "read", "write")
	h := newCopyHandler(t, db)

	scoped := authzUser("ci")
	scoped.p.TokenScopes = []string{"ci-idsrc"}

	expectStatus(t, "scoped token without the target grant", scoped.transfer(t, h, "copy", "src", copyBody), http.StatusNotFound)
}

// TestCopyMoveAuthorizerChecksReadAndDeletePerArtifact: the per-artifact check
// of the engine needs read for a copy and read plus delete for a move, at the
// artifact's path; administrators pass.
func TestCopyMoveAuthorizerChecksReadAndDeletePerArtifact(t *testing.T) {
	db := newAuthzDB()
	db.grant("reader-id", "src", "read")
	db.grant("deleter-id", "src", "read", "delete")
	h := newCopyHandler(t, db)
	src := db.repos[0]
	art := promote.Artifact{Path: "a/b.bin"}
	ctx := context.Background()

	reader, deleter, stranger := authzUser("reader"), authzUser("deleter"), authzUser("stranger")

	cases := []struct {
		who     authzCaller
		admin   bool
		move    bool
		wantErr bool
	}{
		{reader, false, false, false},
		{reader, false, true, true},
		{deleter, false, true, false},
		{stranger, false, false, true},
		{stranger, true, true, false},
	}

	for _, tc := range cases {
		err := h.authorizer(tc.who.p, tc.admin, src, tc.move)(ctx, art)
		if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, promote.ErrNotPermitted)) {
			t.Errorf("%s admin=%v move=%v: err = %v, wantErr %v", tc.who.name, tc.admin, tc.move, err, tc.wantErr)
		}
	}
}

// TestCopyErrorMapping: the copy-specific failures and the shared ones.
func TestCopyErrorMapping(t *testing.T) {
	h := newCopyHandler(t, newAuthzDB())

	tests := []struct {
		err  error
		want int
		code string
	}{
		{promote.ErrNotPermitted, http.StatusForbidden, "forbidden"},
		{promote.ErrItemNotFound, http.StatusNotFound, "not_found"},
		{promote.ErrSourceImmutable, http.StatusConflict, "immutable_release"},
		{promote.ErrSourceHeld, http.StatusConflict, "held_by_bundle"},
		{promote.ErrBadItem, http.StatusBadRequest, "validation_failed"},
		{promote.ErrFormatMismatch, http.StatusBadRequest, "validation_failed"},
		{promote.ErrNotHosted, http.StatusBadRequest, "validation_failed"},
		{promote.ErrQuarantined, http.StatusForbidden, ""},
		{errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}

	for _, tc := range tests {
		rw := httptest.NewRecorder()
		h.writeError(rw, true, tc.err)

		if rw.Code != tc.want || !strings.Contains(rw.Body.String(), tc.code) {
			t.Errorf("%v: %d %s, want %d %q", tc.err, rw.Code, rw.Body.String(), tc.want, tc.code)
		}
	}
}
