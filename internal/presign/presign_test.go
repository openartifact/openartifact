// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package presign

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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
	WrapStore

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
		{
			"repo false beats global on",
			on,
			http.MethodGet,
			Blob{SHA256: testSHA, Size: 500, Repo: "r"},
			&models.Repository{PresignDownloads: new(false)},
			nil,
			nil,
			nil,
			false,
		},
		{
			"repo true beats global off",
			Settings{MinSize: 100},
			http.MethodGet,
			Blob{SHA256: testSHA, Size: 500, Repo: "r"},
			&models.Repository{PresignDownloads: new(true)},
			nil,
			nil,
			nil,
			true,
		},
		{
			"repo nil override inherits",
			on,
			http.MethodGet,
			Blob{SHA256: testSHA, Size: 500, Repo: "r"},
			&models.Repository{},
			nil,
			nil,
			nil,
			true,
		},
		{
			"repo lookup failure streams",
			on,
			http.MethodGet,
			Blob{SHA256: testSHA, Size: 500, Repo: "r"},
			nil,
			errors.New("db down"),
			nil,
			nil,
			false,
		},
		{
			"held repository skips the lookup",
			on,
			http.MethodGet,
			Blob{SHA256: testSHA, Size: 500, Repo: "r", Repository: &models.Repository{PresignDownloads: new(true)}},
			nil,
			errors.New("must not be called"),
			nil,
			nil,
			true,
		},
		{"missing object when checked", on, http.MethodGet, Blob{SHA256: testSHA, Size: 500, CheckExists: true}, nil, nil, nil, nil, false},
		{
			"present object when checked",
			on,
			http.MethodGet,
			Blob{SHA256: testSHA, Size: 500, CheckExists: true},
			nil,
			nil,
			map[string]int64{testSHA: 500},
			nil,
			true,
		},
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

	if try(
		t,
		s,
		http.MethodGet,
		Blob{SHA256: testSHA, Size: 10, Filename: `we"ird name.jar`, ContentType: "application/java-archive"},
	) == nil {
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

// TestTryRedirect_ContentTypeIsAllowlisted: the signed Content-Type is one a
// browser cannot render as a page, whatever the uploader chose, and the
// response is always an attachment.
func TestTryRedirect_ContentTypeIsAllowlisted(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantType string
		wantDisp string
	}{
		{"archive kept", "application/zip", "application/zip", "attachment; filename=f.bin"},
		{"parameters dropped", "application/gzip; charset=binary", "application/gzip", "attachment; filename=f.bin"},
		{"case folded", "Application/Java-Archive", "application/java-archive", "attachment; filename=f.bin"},
		{
			"vendor package type kept",
			"application/vnd.debian.binary-package",
			"application/vnd.debian.binary-package",
			"attachment; filename=f.bin",
		},
		{"json kept", "application/json", "application/json", "attachment; filename=f.bin"},
		{"html", "text/html", "application/octet-stream", "attachment; filename=f.bin"},
		{"html with charset", "text/html; charset=utf-8", "application/octet-stream", "attachment; filename=f.bin"},
		{"svg", "image/svg+xml", "application/octet-stream", "attachment; filename=f.bin"},
		{"xhtml", "application/xhtml+xml", "application/octet-stream", "attachment; filename=f.bin"},
		{"xml", "application/xml", "application/octet-stream", "attachment; filename=f.bin"},
		{"vendor xml", "application/vnd.foo+xml", "application/octet-stream", "attachment; filename=f.bin"},
		{"javascript", "text/javascript", "application/octet-stream", "attachment; filename=f.bin"},
		{"unparsable", "text/html;;;=", "application/octet-stream", "attachment; filename=f.bin"},
		{"no override stays unset", "", "", "attachment; filename=f.bin"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &fakeStore{}
			s := Wrap(inner, nil, Settings{Downloads: true, TTL: time.Minute}, nil, testLogger(t))

			if try(t, s, http.MethodGet, Blob{SHA256: testSHA, Size: 10, Filename: "f.bin", ContentType: tc.in}) == nil {
				t.Fatal("no redirect")
			}

			got := inner.signed[len(inner.signed)-1]
			if got.ContentType != tc.wantType || got.ContentDisposition != tc.wantDisp {
				t.Errorf("signed type %q disposition %q, want %q / %q", got.ContentType, got.ContentDisposition, tc.wantType, tc.wantDisp)
			}
		})
	}
}

// TestTryRedirect_NoFilenameStillAttachment: a blob without a filename is not
// rendered inline either.
func TestTryRedirect_NoFilenameStillAttachment(t *testing.T) {
	inner := &fakeStore{}
	s := Wrap(inner, nil, Settings{Downloads: true, TTL: time.Minute}, nil, testLogger(t))

	if try(t, s, http.MethodGet, Blob{SHA256: testSHA, Size: 10}) == nil {
		t.Fatal("no redirect")
	}

	if got := inner.signed[len(inner.signed)-1].ContentDisposition; got != "attachment" {
		t.Errorf("ContentDisposition = %q, want attachment", got)
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

func TestStore_HealthCheckReachesTheWrappedStore(t *testing.T) {
	dir := t.TempDir()

	local, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	s := Wrap(local, nil, Settings{}, nil, testLogger(t))
	if err := s.HealthCheck(context.Background()); err != nil {
		t.Fatalf("healthy store: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := s.HealthCheck(context.Background()); err == nil {
		t.Fatal("the wrapped store's failure is not reported")
	}
}
