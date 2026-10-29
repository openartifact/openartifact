// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/federation"
)

const fedAPIOtherURL = "https://93.184.216.35"

// The token is sent to the peer's URL, so a peer cannot be moved to another URL
// while keeping the token entered for the old one.
func TestFederationPatchBaseURLNeedsTheTokenAgain(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	id := e.addPeer(t, "files", "east") // token "peer-secret-token" for fedAPIPeerURL

	storedToken := func() string {
		t.Helper()

		p, err := e.store.GetPeer(context.Background(), fedAPIRepoID(t, e.db, "files"), id)
		if err != nil {
			t.Fatalf("get peer: %v", err)
		}

		s, err := p.Secrets()
		if err != nil {
			t.Fatalf("open secrets: %v", err)
		}

		return s.Token
	}

	patch := func(body string) (int, string) {
		t.Helper()

		code, _, raw := e.do(t, fedAPIAdmin, http.MethodPatch, fedAPIPath("files", "/peers/"+id), body)

		return code, raw
	}

	tests := []struct {
		name      string
		body      string
		code      int
		wantURL   string
		wantToken string
	}{
		{
			"new url without a token",
			fmt.Sprintf(`{"base_url":%q}`, fedAPIOtherURL),
			http.StatusBadRequest,
			fedAPIPeerURL,
			"peer-secret-token",
		},
		{
			"new url with an empty token",
			fmt.Sprintf(`{"base_url":%q,"token":""}`, fedAPIOtherURL),
			http.StatusBadRequest,
			fedAPIPeerURL,
			"peer-secret-token",
		},
		{
			"new url, renamed, without a token",
			fmt.Sprintf(`{"name":"x","base_url":%q}`, fedAPIOtherURL),
			http.StatusBadRequest,
			fedAPIPeerURL,
			"peer-secret-token",
		},
		{"same url without a token", fmt.Sprintf(`{"base_url":%q}`, fedAPIPeerURL), http.StatusOK, fedAPIPeerURL, "peer-secret-token"},
		{
			"same url with a trailing slash",
			fmt.Sprintf(`{"base_url":%q}`, fedAPIPeerURL+"/"),
			http.StatusOK,
			fedAPIPeerURL + "/",
			"peer-secret-token",
		},
		{"no url, a new token", `{"token":"rotated"}`, http.StatusOK, fedAPIPeerURL + "/", "rotated"},
		{
			"new url with a token",
			fmt.Sprintf(`{"base_url":%q,"token":"for-the-new-host"}`, fedAPIOtherURL),
			http.StatusOK,
			fedAPIOtherURL,
			"for-the-new-host",
		},
		{"disable without touching the url", `{"enabled":false}`, http.StatusOK, fedAPIOtherURL, "for-the-new-host"},
	}

	for _, tt := range tests { // sequential: each case starts from the state the previous one left
		code, raw := patch(tt.body)
		if code != tt.code {
			t.Fatalf("%s: PATCH = %d %s, want %d", tt.name, code, raw, tt.code)
		}

		if tt.code == http.StatusBadRequest && !strings.Contains(raw, "validation_failed") {
			t.Errorf("%s: body = %s, want validation_failed", tt.name, raw)
		}

		got, err := e.store.GetPeer(context.Background(), fedAPIRepoID(t, e.db, "files"), id)
		if err != nil {
			t.Fatalf("%s: get peer: %v", tt.name, err)
		}

		if got.BaseURL != tt.wantURL || storedToken() != tt.wantToken {
			t.Errorf("%s: stored url %q, token %q; want %q, %q", tt.name, got.BaseURL, storedToken(), tt.wantURL, tt.wantToken)
		}
	}
}

// What the API returns as last_error is the classified reason: text of the
// peer, the network stack or the database never reaches a client, whether it was
// stored by this version or before the vocabulary existed.
func TestFederationLastErrorCarriesNoUpstreamText(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	id := e.addPeer(t, "files", "east")

	const secret = "hunter2 internal.corp.example 10.0.0.7:5432"

	ctx := context.Background()

	tests := []struct {
		name  string
		store func() error
		want  string
	}{
		{"peer answered 500 with a body", func() error {
			return e.store.MarkFailure(ctx, id, fmt.Errorf("pull: %w", &federation.HTTPError{Status: 500, Body: secret}), time.Minute)
		}, "upstream_error (HTTP 500)"},
		{"peer refused the token", func() error {
			return e.store.MarkFailure(ctx, id, &federation.HTTPError{Status: 401, Body: secret}, time.Minute)
		}, "unauthorized (HTTP 401)"},
		{"unclassified error", func() error {
			return e.store.MarkFailure(ctx, id, errors.New("dial tcp "+secret), time.Minute)
		}, "internal_error"},
		{"text stored before the vocabulary existed", func() error {
			_, err := e.db.ExecContext(
				ctx,
				`UPDATE federation_peers SET last_error = $2 WHERE id = $1::uuid`,
				id,
				"peer answered 500: "+secret,
			)

			return err
		}, "upstream_error"},
	}

	for _, tt := range tests {
		if err := tt.store(); err != nil {
			t.Fatalf("%s: store: %v", tt.name, err)
		}

		_, st, raw := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")

		peers, _ := st["peers"].([]any)
		if len(peers) != 1 {
			t.Fatalf("%s: peers = %v", tt.name, st["peers"])
		}

		m, _ := peers[0].(map[string]any)

		if m["last_error"] != tt.want {
			t.Errorf("%s: last_error = %v, want %q (body %s)", tt.name, m["last_error"], tt.want, raw)
		}

		if strings.Contains(raw, "hunter2") || strings.Contains(raw, "corp") || strings.Contains(raw, "10.0.0.7") {
			t.Errorf("%s: response leaks upstream text: %s", tt.name, raw)
		}
	}

	// The deferred units of the peer.
	if err := e.store.Defer(ctx, id, "unit-a", 1, "blob_missing", &federation.HTTPError{Status: 404, Body: secret}); err != nil {
		t.Fatalf("defer: %v", err)
	}

	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO federation_deferred (peer_id, unit_key, seq, attempts, reason, last_error, next_attempt_at)
		 VALUES ($1::uuid, 'unit-b', 2, 1, 'apply_failed', $2, now())`, id, "update package_files: "+secret); err != nil {
		t.Fatalf("seed legacy deferred row: %v", err)
	}

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files", "/peers/"+id+"/deferred"), "")
	if code != http.StatusOK {
		t.Fatalf("deferred: %d %s", code, raw)
	}

	if strings.Contains(raw, "hunter2") || strings.Contains(raw, "corp") || strings.Contains(raw, "update package_files") {
		t.Errorf("deferred list leaks upstream text: %s", raw)
	}

	want := map[string]string{"unit-a": "not_found (HTTP 404)", "unit-b": "upstream_error"}
	items, _ := m["items"].([]any)

	if len(items) != len(want) {
		t.Fatalf("deferred items = %v, want %d", items, len(want))
	}

	for _, it := range items {
		d, _ := it.(map[string]any)

		if key, _ := d["unit_key"].(string); d["last_error"] != want[key] {
			t.Errorf("deferred %s last_error = %v, want %q", key, d["last_error"], want[key])
		}
	}
}
