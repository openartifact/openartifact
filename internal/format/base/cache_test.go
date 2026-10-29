// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
)

func TestRevalidate(t *testing.T) {
	userID := "u1"
	anonymous := &auth.Principal{IsAnonymous: true, Credential: auth.CredentialNone}
	user := &auth.Principal{UserID: &userID, Username: "alice"}
	legacy := &auth.Principal{IsLegacy: true, Username: "system/legacy-api-key"}

	const etag = `"abc"`

	tests := []struct {
		name        string
		principal   *auth.Principal // nil: no principal in the context
		public      bool
		ifNoneMatch string
		authHeader  string
		wantStatus  int
		wantControl string
	}{
		{"anonymous on public repo", anonymous, true, "", "", http.StatusOK, "public, max-age=60"},
		{"anonymous on private repo", anonymous, false, "", "", http.StatusOK, "private, no-cache"},
		{"user on public repo", user, true, "", "", http.StatusOK, "private, no-cache"},
		{"user on private repo", user, false, "", "", http.StatusOK, "private, no-cache"},
		{"legacy key on public repo", legacy, true, "", "", http.StatusOK, "private, no-cache"},
		{"missing principal fails closed", nil, true, "", "", http.StatusOK, "private, no-cache"},
		{
			"unauthenticated bearer header is judged by the principal, not the header",
			anonymous,
			true,
			"",
			"Bearer x",
			http.StatusOK,
			"public, max-age=60",
		},
		{"match anonymous public", anonymous, true, etag, "", http.StatusNotModified, "public, max-age=60"},
		{"match user private", user, false, etag, "", http.StatusNotModified, "private, no-cache"},
		{"mismatch", user, false, `"zzz"`, "", http.StatusOK, "private, no-cache"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", http.NoBody)
			if tc.principal != nil {
				req = req.WithContext(middleware.WithPrincipal(req.Context(), tc.principal))
			}

			if tc.ifNoneMatch != "" {
				req.Header.Set("If-None-Match", tc.ifNoneMatch)
			}

			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}

			rw := httptest.NewRecorder()
			done := Revalidate(rw, req, tc.public, etag, 60)

			if wantDone := tc.wantStatus == http.StatusNotModified; done != wantDone {
				t.Fatalf("Revalidate = %v, want %v", done, wantDone)
			}

			if done && rw.Code != http.StatusNotModified {
				t.Errorf("status = %d, want 304", rw.Code)
			}

			h := rw.Header()
			if h.Get("ETag") != etag {
				t.Errorf("ETag = %q, want %q", h.Get("ETag"), etag)
			}

			if h.Get("Cache-Control") != tc.wantControl {
				t.Errorf("Cache-Control = %q, want %q", h.Get("Cache-Control"), tc.wantControl)
			}

			if h.Get("Vary") != "Authorization" {
				t.Errorf("Vary = %q, want Authorization", h.Get("Vary"))
			}
		})
	}
}

func TestContentETag(t *testing.T) {
	t.Parallel()

	// SHA-256 of "abc", quoted: a strong validator over the bytes alone.
	const want = `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`

	if got := ContentETag([]byte("abc")); got != want {
		t.Fatalf("ContentETag(abc) = %s, want %s", got, want)
	}

	if ContentETag([]byte("abc")) == ContentETag([]byte("abd")) {
		t.Fatal("different bodies share an ETag")
	}
}
