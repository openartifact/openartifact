// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
)

// sendfileProbe sits directly on the connection's response writer, under every
// layer of the stack, and records what reaches its ReadFrom: an *os.File is
// what net/http turns into sendfile(2), anything else is a user-space copy.
type sendfileProbe struct {
	http.ResponseWriter

	files, others *atomic.Int32
}

func (p sendfileProbe) ReadFrom(r io.Reader) (int64, error) {
	if _, ok := r.(*os.File); ok {
		p.files.Add(1)
	} else {
		p.others.Add(1)
	}

	return httpx.ReadFrom(p.ResponseWriter, r)
}

func (p sendfileProbe) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// A file served from the filesystem store through the real middleware stack
// must arrive at the connection as the *os.File itself, so the kernel moves it
// (sendfile) instead of the process copying it in 64 KiB read/write pairs. Each
// layer that wraps the writer, or the blob reader, is a place this can break.
func TestArtifactDownloadReachesSendfile(t *testing.T) {
	log, err := logger.New("info", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	fs, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	store := storage.NewMetricsBlobStore(fs, reg)

	want := make([]byte, 3<<20)
	_, _ = rand.Read(want)

	id, err := store.Put(t.Context(), bytes.NewReader(want), nil)
	if err != nil {
		t.Fatal(err)
	}

	var files, others atomic.Int32

	router := chi.NewRouter()

	// The probe is outermost: the writer net/http made, seen from the stack.
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(sendfileProbe{ResponseWriter: w, files: &files, others: &others}, r)
		})
	})

	for _, mw := range []func(http.Handler) http.Handler{
		MaxBodyMiddleware(1 << 30),
		SecurityHeadersMiddleware(false),
		RecoveryMiddleware(log),
		RequestIDMiddleware,
		MetricsMiddleware(reg),
		telemetry.HTTPMiddleware(),
		TracingMiddleware,
		LoggingMiddleware(log),
		CORSMiddleware(nil),
		RejectNULBytes,
		CompressAPI,
	} {
		router.Use(mw)
	}

	router.Get("/repos/raw/r/blob.txt", func(w http.ResponseWriter, r *http.Request) {
		rc, err := store.Get(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rc.Close()

		// A text type: an artifact must stay byte-exact, never encoded.
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(want)))

		_, _ = iocopy.Copy(w, rc)
	})

	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/repos/raw/r/blob.txt", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip, zstd")

	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("artifact bytes altered (encoding %q, %d of %d bytes)", resp.Header.Get("Content-Encoding"), len(got), len(want))
	}

	if files.Load() != 1 || others.Load() != 0 {
		t.Errorf(
			"the connection's ReadFrom got %d file(s) and %d other reader(s); want the file itself, once: sendfile is unreachable",
			files.Load(),
			others.Load(),
		)
	}
}

// Every response-writer wrapper in the stack keeps the optional interfaces the
// server's writer has.
func TestWrappersKeepWriterInterfaces(t *testing.T) {
	wrappers := map[string]http.ResponseWriter{
		"StatusWriter":   httpx.WrapStatus(httptest.NewRecorder()),
		"artifactWriter": &artifactWriter{ResponseWriter: httptest.NewRecorder()},
	}

	for name, w := range wrappers {
		if _, ok := w.(io.ReaderFrom); !ok {
			t.Errorf("%s hides io.ReaderFrom (sendfile)", name)
		}

		if _, ok := w.(http.Flusher); !ok {
			t.Errorf("%s hides http.Flusher", name)
		}

		if _, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok {
			t.Errorf("%s hides Unwrap (http.ResponseController)", name)
		}
	}
}
