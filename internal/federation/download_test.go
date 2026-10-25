// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakePeer serves /federation/v1/blob for one blob through the real range
// handling of Server.serveBlob, with a hook to misbehave per request.
type fakePeer struct {
	t    *testing.T
	ref  BlobRef
	srv  *Server
	http *httptest.Server

	mu       sync.Mutex
	requests []string // the Range header of each request ("" for none)
	sent     int64    // body bytes written to clients
	// behave decides what request number n (1-based) does; nil serves properly.
	behave func(n int, w http.ResponseWriter, r *http.Request) bool
}

func newFakePeer(t *testing.T, content []byte) *fakePeer {
	t.Helper()

	fs, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}

	sha, err := fs.Put(context.Background(), bytes.NewReader(content), map[string]string{"sha256": sumHex(content)})
	if err != nil {
		t.Fatalf("store blob: %v", err)
	}

	p := &fakePeer{t: t, ref: BlobRef{SHA256: sha, Size: int64(len(content))}, srv: &Server{blobs: fs}}

	p.http = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.http.Close)

	return p
}

func (p *fakePeer) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.requests = append(p.requests, r.Header.Get("Range"))
	n := len(p.requests)
	p.mu.Unlock()

	cw := &countingWriter{ResponseWriter: w, peer: p}

	if p.behave != nil && p.behave(n, cw, r) {
		return
	}

	p.srv.serveBlob(cw, r, &p.ref)
}

func (p *fakePeer) ranges() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.requests...)
}

func (p *fakePeer) bytesSent() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.sent
}

type countingWriter struct {
	http.ResponseWriter

	peer *fakePeer
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)

	c.peer.mu.Lock()
	c.peer.sent += int64(n)
	c.peer.mu.Unlock()

	return n, err //nolint:wrapcheck // test double
}

// abortAfter forwards the response but kills the connection after limit body
// bytes, as a dropped TCP connection would.
type abortAfter struct {
	http.ResponseWriter

	left int
}

func (a *abortAfter) Write(b []byte) (int, error) {
	if len(b) > a.left {
		_, _ = a.ResponseWriter.Write(b[:a.left])

		if f, ok := a.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}

		panic(http.ErrAbortHandler)
	}

	a.left -= len(b)

	return a.ResponseWriter.Write(b) //nolint:wrapcheck // test double
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)

	return hex.EncodeToString(s[:])
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(1, 2))

	for i := range b {
		b[i] = byte(r.UintN(256))
	}

	return b
}

type downloadEnv struct {
	e    *Engine
	pc   *pullCtx
	reg  *prometheus.Registry
	peer *fakePeer
}

func newDownloadEnv(t *testing.T, peer *fakePeer) *downloadEnv {
	t.Helper()

	blobs, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("local blob store: %v", err)
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	reg := prometheus.NewRegistry()

	m, err := NewMetrics(reg, nil)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}

	u, _ := url.Parse(peer.http.URL)

	e := &Engine{
		Blobs: blobs, Log: log, Metrics: m,
		spool:         newSpool(t.TempDir(), 0, 0),
		resumeBackoff: time.Millisecond,
	}

	m.partialBytes.Store(new(e.spool.partialBytes))

	return &downloadEnv{
		e: e, reg: reg, peer: peer,
		pc: &pullCtx{
			repo: &RepoInfo{Name: "repo"}, peer: &Peer{Name: "peer"},
			client: &Client{base: u, token: "t", http: peer.http.Client()},
		},
	}
}

func (d *downloadEnv) run(ctx context.Context) (string, error) {
	return d.e.download(ctx, d.pc, "unit", d.peer.ref)
}

func (d *downloadEnv) resumed() float64 {
	return testutil.ToFloat64(d.e.Metrics.Resumed.WithLabelValues("repo", "peer"))
}

func (d *downloadEnv) partial() string {
	return filepath.Join(d.e.spool.dir, d.peer.ref.SHA256+partialSuffix)
}

func (d *downloadEnv) seedPartial(t *testing.T, b []byte) {
	t.Helper()

	if err := os.MkdirAll(d.e.spool.dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(d.partial(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustEqualFile(t *testing.T, path string, want []byte) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged blob: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("staged blob differs from the original (%d vs %d bytes)", len(got), len(want))
	}
}

func TestDownloadResumesAfterTruncatedTransfer(t *testing.T) {
	t.Parallel()

	content := randomBytes(1 << 20)
	peer := newFakePeer(t, content)
	peer.behave = func(n int, w http.ResponseWriter, r *http.Request) bool {
		if n > 1 {
			return false
		}

		peer.srv.serveBlob(&abortAfter{ResponseWriter: w, left: 300 << 10}, r, &peer.ref)

		return true
	}

	env := newDownloadEnv(t, peer)

	path, err := env.run(context.Background())
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	mustEqualFile(t, path, content)

	got := peer.ranges()
	if len(got) != 2 || got[0] != "" || !strings.HasPrefix(got[1], "bytes=") || got[1] == "bytes=0-" {
		t.Fatalf("requests = %q, want a plain one then a resumed one", got)
	}

	if env.resumed() != 1 {
		t.Errorf("resumed_total = %v, want 1", env.resumed())
	}

	if sent := peer.bytesSent(); sent >= int64(len(content))*3/2 {
		t.Errorf("peer sent %d bytes for a %d byte blob; resuming must not resend the prefix", sent, len(content))
	}

	if _, err := os.Stat(env.partial()); !os.IsNotExist(err) {
		t.Errorf("partial file still exists after the download: %v", err)
	}
}

func TestDownloadPartialSurvivesAFailedPull(t *testing.T) {
	t.Parallel()

	content := randomBytes(512 << 10)
	peer := newFakePeer(t, content)
	peer.behave = func(n int, w http.ResponseWriter, r *http.Request) bool {
		switch n {
		case 1:
			peer.srv.serveBlob(&abortAfter{ResponseWriter: w, left: 200 << 10}, r, &peer.ref)
		case 2:
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			return false
		}

		return true
	}

	env := newDownloadEnv(t, peer)

	_, err := env.run(context.Background())

	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusInternalServerError {
		t.Fatalf("first pull error = %v, want the peer's HTTP 500", err)
	}

	st, statErr := os.Stat(env.partial())
	if statErr != nil || st.Size() == 0 {
		t.Fatalf("partial after failed pull: %v, %v; want the received bytes kept", st, statErr)
	}

	kept := st.Size()

	if pb := env.e.spool.partialBytes(); pb != kept {
		t.Errorf("partial bytes = %d, want %d", pb, kept)
	}

	path, err := env.run(context.Background()) // the next pull
	if err != nil {
		t.Fatalf("second pull: %v", err)
	}

	mustEqualFile(t, path, content)

	got := peer.ranges()
	if want := fmt.Sprintf("bytes=%d-", kept); got[len(got)-1] != want {
		t.Errorf("last request Range = %q, want %q", got[len(got)-1], want)
	}

	if env.resumed() != 1 {
		t.Errorf("resumed_total = %v, want 1", env.resumed())
	}
}

func TestDownloadRestartsWhenPeerIgnoresRange(t *testing.T) {
	t.Parallel()

	content := randomBytes(256 << 10)
	peer := newFakePeer(t, content)
	peer.behave = func(_ int, w http.ResponseWriter, r *http.Request) bool {
		r.Header.Del("Range") // answers 200 with the whole blob

		peer.srv.serveBlob(w, r, &peer.ref)

		return true
	}

	env := newDownloadEnv(t, peer)
	env.seedPartial(t, content[:100<<10])

	path, err := env.run(context.Background())
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	mustEqualFile(t, path, content)

	if env.resumed() != 0 {
		t.Errorf("resumed_total = %v, want 0 when the range was ignored", env.resumed())
	}

	if sent := peer.bytesSent(); sent != int64(len(content)) {
		t.Errorf("peer sent %d bytes, want the whole blob once (%d)", sent, len(content))
	}
}

func TestDownloadMismatchDiscardsThePartialAndDefersTheUnit(t *testing.T) {
	t.Parallel()

	content := randomBytes(256 << 10)

	tests := []struct {
		name   string
		behave func(t *testing.T, peer *fakePeer, env *downloadEnv)
	}{
		{"corrupt prefix kept from an earlier pull", func(t *testing.T, _ *fakePeer, env *downloadEnv) {
			t.Helper()

			env.seedPartial(t, bytes.Repeat([]byte{0xAA}, 100<<10))
		}},
		{"peer serves other bytes", func(_ *testing.T, peer *fakePeer, _ *downloadEnv) {
			bad := bytes.Clone(content)
			bad[len(bad)-1] ^= 0xFF

			peer.srv.blobs = &replacedBlobStore{BlobStore: peer.srv.blobs, id: peer.ref.SHA256, data: bad}
		}},
		{"peer ends the body cleanly before the declared size", func(_ *testing.T, peer *fakePeer, _ *downloadEnv) {
			peer.behave = func(_ int, w http.ResponseWriter, _ *http.Request) bool {
				_, _ = w.Write(content[:len(content)/2]) // chunked: a clean end, not a drop

				return true
			}
		}},
		{"peer sends more than the unit declares", func(_ *testing.T, peer *fakePeer, _ *downloadEnv) {
			peer.behave = func(_ int, w http.ResponseWriter, _ *http.Request) bool {
				_, _ = w.Write(append(bytes.Clone(content), 1, 2, 3)) // chunked: no Content-Length to stop it

				return true
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			peer := newFakePeer(t, content)
			env := newDownloadEnv(t, peer)
			tt.behave(t, peer, env)

			_, err := env.run(context.Background())

			var de *deferError
			if !errors.As(err, &de) || de.reason != "digest_mismatch" {
				t.Fatalf("download error = %v, want a digest_mismatch deferral", err)
			}

			if _, err := os.Stat(env.partial()); !os.IsNotExist(err) {
				t.Errorf("partial file survived a mismatch: %v", err)
			}
		})
	}
}

// replacedBlobStore serves other bytes than the stored ones for one blob.
type replacedBlobStore struct {
	storage.BlobStore

	id   string
	data []byte
}

func (r *replacedBlobStore) Get(_ context.Context, id string) (io.ReadCloser, error) {
	if id != r.id {
		return nil, storage.ErrBlobNotFound
	}

	return io.NopCloser(bytes.NewReader(r.data)), nil
}

func TestDownloadRestartsWhenPartialIsLongerThanTheBlob(t *testing.T) {
	t.Parallel()

	content := randomBytes(64 << 10)
	peer := newFakePeer(t, content)
	env := newDownloadEnv(t, peer)
	env.seedPartial(t, randomBytes(128<<10))

	path, err := env.run(context.Background())
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	mustEqualFile(t, path, content)

	if got := peer.ranges(); len(got) != 1 || got[0] != "" {
		t.Errorf("requests = %q, want one plain request", got)
	}
}

func TestDownloadVerifiesACompletePartial(t *testing.T) {
	t.Parallel()

	content := randomBytes(64 << 10)
	peer := newFakePeer(t, content)
	env := newDownloadEnv(t, peer)
	env.seedPartial(t, content)

	path, err := env.run(context.Background())
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	mustEqualFile(t, path, content)

	if got := peer.ranges(); len(got) != 0 {
		t.Errorf("requests = %q, want none for an already complete partial", got)
	}
}

func TestDownloadRestartsOnUnsatisfiableRange(t *testing.T) {
	t.Parallel()

	content := randomBytes(64 << 10)
	peer := newFakePeer(t, content)
	peer.behave = func(_ int, w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Range") == "" {
			return false
		}

		w.Header().Set("Content-Range", "bytes */1")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)

		return true
	}

	env := newDownloadEnv(t, peer)
	env.seedPartial(t, content[:1000])

	path, err := env.run(context.Background())
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	mustEqualFile(t, path, content)
}

func TestDownloadGivesUpAfterRepeatedFailuresWithoutProgress(t *testing.T) {
	t.Parallel()

	content := randomBytes(64 << 10)
	peer := newFakePeer(t, content)
	peer.behave = func(_ int, w http.ResponseWriter, r *http.Request) bool {
		peer.srv.serveBlob(&abortAfter{ResponseWriter: w, left: 0}, r, &peer.ref)

		return true
	}

	env := newDownloadEnv(t, peer)

	if _, err := env.run(context.Background()); err == nil {
		t.Fatal("download succeeded against a peer that never sends a byte")
	}

	if got := len(peer.ranges()); got != maxResumeRetries+1 {
		t.Errorf("attempts = %d, want %d", got, maxResumeRetries+1)
	}
}

func TestDownloadStopsOnCancellation(t *testing.T) {
	t.Parallel()

	content := randomBytes(64 << 10)
	peer := newFakePeer(t, content)
	env := newDownloadEnv(t, peer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := env.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("download error = %v, want context.Canceled", err)
	}
}

func TestSpoolSweepAppliesAgeAndSizeCaps(t *testing.T) {
	t.Parallel()

	sp := newSpool(t.TempDir(), 250, time.Hour)
	now := time.Now()

	write := func(name string, size int, age time.Duration) {
		path := filepath.Join(sp.dir, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	hex := func(c byte) string { return strings.Repeat(string(c), 64) }

	write(hex('a')+partialSuffix, 100, 2*time.Hour)    // expired
	write(hex('b')+partialSuffix, 100, 40*time.Minute) // oldest of the rest
	write(hex('c')+partialSuffix, 100, 20*time.Minute) //
	write(hex('d')+partialSuffix, 100, time.Minute)    // newest
	write("blob-1"+stagedSuffix, 100, 3*time.Hour)     // expired staged file
	write("blob-2"+stagedSuffix, 100, time.Minute)     // in use, kept
	write("unrelated.txt", 100, 10*time.Hour)          // never touched

	if got := sp.partialBytes(); got != 400 {
		t.Fatalf("partialBytes = %d, want 400", got)
	}

	if removed := sp.sweep(now); removed != 3 {
		t.Errorf("removed = %d, want 3 (expired partial, expired staged, one over the cap)", removed)
	}

	left := map[string]bool{}

	entries, _ := os.ReadDir(sp.dir)
	for _, de := range entries {
		left[de.Name()] = true
	}

	for name, want := range map[string]bool{
		hex('a') + partialSuffix: false, hex('b') + partialSuffix: false,
		hex('c') + partialSuffix: true, hex('d') + partialSuffix: true,
		"blob-1" + stagedSuffix: false, "blob-2" + stagedSuffix: true, "unrelated.txt": true,
	} {
		if left[name] != want {
			t.Errorf("%s present = %v, want %v", name, left[name], want)
		}
	}
}

func TestSpoolSweepKeepsPartialsInUse(t *testing.T) {
	t.Parallel()

	sp := newSpool(t.TempDir(), 1, time.Nanosecond)
	sha := strings.Repeat("e", 64)

	release, err := sp.acquire(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}

	f, err := sp.openPartial(sha)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = f.WriteString("bytes in flight")
	_ = f.Close()

	if removed := sp.sweep(time.Now().Add(time.Hour)); removed != 0 {
		t.Fatalf("sweep removed %d files while the partial was in use", removed)
	}

	release()

	if removed := sp.sweep(time.Now().Add(time.Hour)); removed != 1 {
		t.Fatalf("sweep removed %d files after release, want 1", removed)
	}
}

func TestSpoolAcquireWaitsForTheHolder(t *testing.T) {
	t.Parallel()

	sp := newSpool(t.TempDir(), 0, 0)
	sha := strings.Repeat("f", 64)

	release, err := sp.acquire(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := sp.acquire(ctx, sha); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire error = %v, want the context deadline", err)
	}

	release()

	again, err := sp.acquire(context.Background(), sha)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	again()
}
