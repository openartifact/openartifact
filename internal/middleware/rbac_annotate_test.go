// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
)

// TestRBACAnnotatePermission drives the artifact-property routes and the
// package-data routes through the real RBACMiddleware for callers that hold one
// set of actions: `annotate` or `write` is enough to change properties, `read`
// to read them, and `annotate` never authorizes anything else.
func TestRBACAnnotatePermission(t *testing.T) {
	const (
		props    = "/api/v1/repositories/app/artifacts/a1/properties"
		propKey  = props + "/build.name"
		search   = "/api/v1/repositories/app/artifacts/search"
		upload   = "/repos/app/lib-1.0.jar"
		debs     = "/api/v1/repositories/app/debs"
		presign  = "/api/v1/repositories/app/uploads"
		trash    = "/api/v1/repositories/app/trash"
		users    = "/api/v1/users"
		ok       = http.StatusOK
		forbid   = http.StatusForbidden
		unauthed = http.StatusUnauthorized
	)

	type req struct {
		method, path string
	}

	writes := []req{{http.MethodPut, props}, {http.MethodPatch, props}, {http.MethodDelete, propKey}}
	reads := []req{{http.MethodGet, props}, {http.MethodGet, search}}
	others := []req{
		{http.MethodPut, upload}, {http.MethodPost, upload}, {http.MethodDelete, upload},
		{http.MethodPost, debs}, {http.MethodPost, presign}, {http.MethodGet, trash},
		{http.MethodGet, users}, {http.MethodDelete, props},
	}

	tests := []struct {
		name                 string
		actions              []string
		kind                 string
		wantWrites, wantRead int
		wantOthers           int
	}{
		{"annotate and read", []string{"read", "annotate"}, "", ok, ok, forbid},
		{"annotate only", []string{"annotate"}, "", ok, forbid, forbid},
		{"write and read", []string{"read", "write"}, "", ok, ok, -1},
		{"read only", []string{"read"}, "", forbid, ok, forbid},
		{"delete only", []string{"delete"}, "", forbid, forbid, -1},
		{"manage only", []string{"manage"}, "", forbid, forbid, -1},
		{"annotate token", []string{"read", "annotate"}, auth.TokenKindAccess, ok, ok, forbid},
		{"annotate deploy token", []string{"read", "annotate"}, auth.TokenKindDeploy, ok, ok, forbid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := projectCaller{kind: tc.kind, actions: tc.actions}

			for _, r := range writes {
				if got := projectStatus(t, caller, "", r.method, r.path); got != tc.wantWrites {
					t.Errorf("%s %s = %d, want %d", r.method, r.path, got, tc.wantWrites)
				}
			}

			for _, r := range reads {
				if got := projectStatus(t, caller, "", r.method, r.path); got != tc.wantRead {
					t.Errorf("%s %s = %d, want %d", r.method, r.path, got, tc.wantRead)
				}
			}

			if tc.wantOthers == -1 {
				return
			}

			for _, r := range others {
				if got := projectStatus(t, caller, "", r.method, r.path); got != tc.wantOthers {
					t.Errorf("%s %s = %d, want %d", r.method, r.path, got, tc.wantOthers)
				}
			}
		})
	}

	t.Run("anonymous is never widened", func(t *testing.T) {
		for _, r := range append(writes, reads...) {
			if got := projectStatus(t, projectCaller{anonymous: true}, "", r.method, r.path); got != unauthed {
				t.Errorf("anonymous %s %s = %d, want %d", r.method, r.path, got, unauthed)
			}
		}
	})
}

// TestRBACAnnotateScopedToken: a token scoped to an annotate target keeps
// working with it only; a token scoped to a read-only target cannot write
// properties even though the owner also holds `annotate` elsewhere.
func TestRBACAnnotateScopedToken(t *testing.T) {
	const props = "/api/v1/repositories/app/artifacts/a1/properties"

	// The scope names a target the owner does not hold: the token loses every
	// target and so every action.
	caller := projectCaller{kind: auth.TokenKindAccess, actions: []string{"read", "annotate"}, scopes: []string{"other-target"}}
	if got := projectStatus(t, caller, "", http.MethodPatch, props); got != http.StatusForbidden {
		t.Fatalf("PATCH with an unrelated scope = %d, want 403", got)
	}

	caller.scopes = []string{"t-actions"}
	if got := projectStatus(t, caller, "", http.MethodPatch, props); got != http.StatusOK {
		t.Fatalf("PATCH with the annotate target as scope = %d, want 200", got)
	}
}
