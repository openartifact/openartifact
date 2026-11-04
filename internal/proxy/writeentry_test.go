// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

func TestWriteEntryCachePolicy(t *testing.T) {
	entry := &models.ProxyCacheEntry{BlobKey: "abc", ContentType: "text/plain", SizeBytes: 2}

	cases := []struct {
		name      string
		public    bool
		anonymous bool
		want      string
	}{
		{"private repo", false, false, "private, no-cache"},
		{"public repo, authenticated", true, false, "private, no-cache"},
		{"public repo, anonymous", true, true, "public, no-cache"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: tc.anonymous}))
			rec := httptest.NewRecorder()

			writeEntry(rec, req, tc.public, entry, strings.NewReader("hi"))

			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.want)
			}

			if rec.Header().Get("Vary") != "Authorization" {
				t.Fatalf("Vary = %q", rec.Header().Get("Vary"))
			}

			req.Header.Set("If-None-Match", `"abc"`)
			rec = httptest.NewRecorder()
			writeEntry(rec, req, tc.public, entry, strings.NewReader("hi"))

			if rec.Code != http.StatusNotModified {
				t.Fatalf("revalidation status = %d", rec.Code)
			}
		})
	}
}
