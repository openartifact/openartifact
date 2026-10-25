// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package presign

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errNoBlob = errors.New("no such blob")

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeStore is a BlobStore whose blobs are (size, present) entries and that
// signs deterministic fake URLs.
type fakeStore struct {
	storage.BlobStore

	sizes   map[string]int64
	signErr error
	signed  []storage.PresignGetOptions
}

func (f *fakeStore) GetSize(_ context.Context, id string) (int64, error) {
	if sz, ok := f.sizes[id]; ok {
		return sz, nil
	}

	return 0, errNoBlob
}

func (f *fakeStore) Exists(_ context.Context, id string) (bool, error) {
	_, ok := f.sizes[id]
	return ok, nil
}

func (f *fakeStore) PresignGet(_ context.Context, id string, _ time.Duration, opts storage.PresignGetOptions) (string, error) {
	if f.signErr != nil {
		return "", f.signErr
	}

	f.signed = append(f.signed, opts)

	return "https://cdn.example/" + id + "?X-Amz-Signature=sig", nil
}

type repoDB struct {
	databasetest.BaseMockDB

	repo *models.Repository
	err  error
}

func (d *repoDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return d.repo, d.err
}

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	return log
}

func try(t *testing.T, s *Store, method string, blob Blob) *httptest.ResponseRecorder {
	t.Helper()

	rw := httptest.NewRecorder()
	if s.TryRedirect(rw, httptest.NewRequestWithContext(context.Background(), method, "/x", nil), blob) {
		return rw
	}

	if rw.Code != http.StatusOK || rw.Body.Len() != 0 || len(rw.Header()) != 0 {
		t.Fatalf("TryRedirect returned false after writing a response: %d %v", rw.Code, rw.Header())
	}

	return nil
}

func TestTryRedirect(t *testing.T) {
	on := Settings{Downloads: true, MinSize: 100, TTL: time.Minute}

	tests := []struct {
		name     string
		settings Settings
		method   string
		blob     Blob
		repo     *models.Repository
		dbErr    error
		sizes    map[string]int64
		signErr  error
		want     bool
	}{
		{"redirects a large blob", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500}, nil, nil, nil, nil, true},
		{"size from the store", on, http.MethodGet, Blob{SHA256: testSHA}, nil, nil, map[string]int64{testSHA: 500}, nil, true},
		{"below threshold", on, http.MethodGet, Blob{SHA256: testSHA, Size: 99}, nil, nil, nil, nil, false},
		{"threshold is inclusive", on, http.MethodGet, Blob{SHA256: testSHA, Size: 100}, nil, nil, nil, nil, true},
		{"unknown blob streams", on, http.MethodGet, Blob{SHA256: testSHA}, nil, nil, nil, nil, false},
		{"global off", Settings{MinSize: 100}, http.MethodGet, Blob{SHA256: testSHA, Size: 500}, nil, nil, nil, nil, false},
		{"HEAD never redirects", on, http.MethodHead, Blob{SHA256: testSHA, Size: 500}, nil, nil, nil, nil, false},
		{"repo false beats global on", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500, Repo: "r"}, &models.Repository{PresignDownloads: new(false)}, nil, nil, nil, false},
		{"repo true beats global off", Settings{MinSize: 100}, http.MethodGet, Blob{SHA256: testSHA, Size: 500, Repo: "r"}, &models.Repository{PresignDownloads: new(true)}, nil, nil, nil, true},
		{"repo nil override inherits", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500, Repo: "r"}, &models.Repository{}, nil, nil, nil, true},
		{"repo lookup failure streams", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500, Repo: "r"}, nil, errors.New("db down"), nil, nil, false},
		{"held repository skips the lookup", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500, Repo: "r", Repository: &models.Repository{PresignDownloads: new(true)}}, nil, errors.New("must not be called"), nil, nil, true},
		{"missing object when checked", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500, CheckExists: true}, nil, nil, nil, nil, false},
		{"present object when checked", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500, CheckExists: true}, nil, nil, map[string]int64{testSHA: 500}, nil, true},
		{"sign failure streams", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500}, nil, nil, nil, errors.New("boom"), false},
		{"unsupported streams", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500}, nil, nil, nil, storage.ErrPresignNotSupported, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeStore{sizes: tt.sizes, signErr: nil}
			s := Wrap(inner, &repoDB{repo: tt.repo, err: tt.dbErr}, tt.settings, nil, testLogger(t))
			inner.signErr = tt.signErr // after Wrap's support probe

			s.supported = true // exercise the sign-failure path even for an erroring signer

			rw := try(t, s, tt.method, tt.blob)
			if (rw != nil) != tt.want {
				t.Fatalf("redirected = %v, want %v", rw != nil, tt.want)
			}

			if rw == nil {
				return
			}

			if rw.Code != http.StatusTemporaryRedirect {
				t.Errorf("status = %d, want 307", rw.Code)
			}

			if loc := rw.Header().Get("Location"); !strings.HasPrefix(loc, "https://cdn.example/"+testSHA+"?") {
				t.Errorf("Location = %q", loc)
			}

			if rw.Header().Get("Cache-Control") != "private, no-store" {
				t.Errorf("Cache-Control = %q", rw.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestTryRedirect_SignsFilenameAndContentType(t *testing.T) {
	inner := &fakeStore{}
	s := Wrap(inner, nil, Settings{Downloads: true, TTL: time.Minute}, nil, testLogger(t))

	if try(t, s, http.MethodGet, Blob{SHA256: testSHA, Size: 10, Filename: `we"ird name.jar`, ContentType: "application/java-archive"}) == nil {
		t.Fatal("no redirect")
	}

	got := inner.signed[len(inner.signed)-1] // [0] is the support probe
	if !strings.HasPrefix(got.ContentDisposition, "attachment;") || !strings.Contains(got.ContentDisposition, "name.jar") {
		t.Errorf("ContentDisposition = %q", got.ContentDisposition)
	}

	if strings.Contains(got.ContentDisposition, "\r") || strings.Contains(got.ContentDisposition, "\n") {
		t.Errorf("ContentDisposition contains a line break: %q", got.ContentDisposition)
	}

	if got.ContentType != "application/java-archive" {
		t.Errorf("ContentType = %q", got.ContentType)
	}
}

func TestWrap_UnsupportedBackend(t *testing.T) {
	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	s := Wrap(local, nil, Settings{Downloads: true}, nil, testLogger(t))
	if s.Supported() {
		t.Fatal("filesystem store must not report presign support")
	}

	if try(t, s, http.MethodGet, Blob{SHA256: testSHA, Size: 10}) != nil {
		t.Fatal("redirected on an unsupported backend")
	}
}

func TestTry_PlainStoreNeverRedirects(t *testing.T) {
	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	rw := httptest.NewRecorder()
	if Try(local, rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil), Blob{SHA256: testSHA}) {
		t.Fatal("a store without the Redirector capability redirected")
	}
}
