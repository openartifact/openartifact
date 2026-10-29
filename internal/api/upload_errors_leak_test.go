// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// leakSecret stands in for parser, signer and filesystem detail that must stay
// in the log.
const leakSecret = "/var/lib/openartifact/tmp/secret-detail"

func newUploadLeakEnv(t *testing.T) (*logger.Logger, *staleUploadDB, storage.BlobStore) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return log, &staleUploadDB{}, store
}

// TestUploadErrorsDoNotLeakTheCause covers the rpm signing failure and the
// unreadable multipart form of the rpm and deb upload routes: the client gets a
// fixed message, the cause goes to the log.
func TestUploadErrorsDoNotLeakTheCause(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T) *httptest.ResponseRecorder
		want int
	}{
		{
			name: "rpm signing failure",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()

				log, db, store := newUploadLeakEnv(t)
				cause := fmt.Errorf("%w: %w", errSigningFailed, errors.New(leakSecret))
				rw := httptest.NewRecorder()
				NewRPMUploadHandler(log, db, store, nil).writeUploadError(rw, cause, "rpms", "a.rpm")

				return rw
			},
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "rpm unreadable multipart form",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()

				return postBrokenForm(t, "/api/v1/repositories/rpms/packages", true)
			},
			want: http.StatusBadRequest,
		},
		{
			name: "deb unreadable multipart form",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				t.Helper()

				return postBrokenForm(t, "/api/v1/repositories/debs/debs", false)
			},
			want: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rw := tc.run(t)
			if rw.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", rw.Code, tc.want, rw.Body.String())
			}

			body := rw.Body.String()
			for _, leak := range []string{leakSecret, "multipart:", "NextPart", "EOF", "failed to parse form", "cannot sign the package: "} {
				if strings.Contains(body, leak) {
					t.Errorf("response leaks %q: %s", leak, body)
				}
			}
		})
	}
}

// postBrokenForm posts a body that is not the multipart form its Content-Type
// announces to the rpm (viaRPM) or the deb upload route.
func postBrokenForm(t *testing.T, path string, viaRPM bool) *httptest.ResponseRecorder {
	t.Helper()

	log, db, store := newUploadLeakEnv(t)
	router := chi.NewRouter()

	if viaRPM {
		NewRPMUploadHandler(log, db, store, nil).Register(router)
	} else {
		NewDebUploadHandler(log, db, store, nil).Register(router)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader("not a multipart body"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}
