// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// The simple pages are compressed on request and decode to the identity body.
func TestSimplePagesGzip(t *testing.T) {
	db := &benchPyPIDB{DB: formattest.NewDB(), all: benchPyPIProjects(400), versions: benchPyPIVersions(200)}

	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost:8080")
	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	get := func(path, acceptEncoding string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		return w
	}

	for _, path := range []string{"/repos/myrepo/simple/", "/repos/myrepo/simple/demo/"} {
		plain, zipped := get(path, ""), get(path, "gzip")

		if plain.Code != http.StatusOK || plain.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s identity: status %d encoding %q", path, plain.Code, plain.Header().Get("Content-Encoding"))
		}

		if zipped.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("%s not gzip-encoded", path)
		}

		zr, err := gzip.NewReader(zipped.Body)
		if err != nil {
			t.Fatal(err)
		}

		if got, _ := io.ReadAll(zr); !bytes.Equal(got, plain.Body.Bytes()) {
			t.Errorf("%s: decoded body differs from identity body", path)
		}
	}
}
