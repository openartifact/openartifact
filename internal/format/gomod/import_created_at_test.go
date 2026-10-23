// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestUploadHonorsImportCreatedAt: an upload carrying an origin creation time
// (as importmeta resolves it for a manage principal) stores it as created_at;
// an ordinary upload leaves it unset so the database stamps now.
func TestUploadHonorsImportCreatedAt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		at, want time.Time
	}{
		{"imported", formattest.Origin(), formattest.Origin()},
		{"normal upload", time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db, _, _ := newTestRouter(t)
			h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost")

			router := chi.NewRouter()
			router.Route("/repos/{name}", formattest.AtOrigin(func(sub chi.Router) {
				h.RegisterRepoRoutes(sub)
				sub.Put("/*", h.HandlePut)
			}, tc.at))

			w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/v1.0.0", moduleZip(t, testModule, "v1.0.0"))
			if w.Code != http.StatusCreated {
				t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
			}

			if len(db.mods) != 1 || !db.mods[0].CreatedAt.Equal(tc.want) {
				t.Fatalf("mods = %+v, want created_at %v", db.mods, tc.want)
			}
		})
	}
}
