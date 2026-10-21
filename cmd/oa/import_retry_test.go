// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func retryTestNow() time.Time { return time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC) }

// ─── fault injection ──────────────────────────────────────────────────────────

type failFunc func(w http.ResponseWriter, r *http.Request, next http.Handler)

// flakyServer fronts a handler and fails the first `failures` matching
// requests per method+path.
type flakyServer struct {
	*httptest.Server

	mu      sync.Mutex
	seen    map[string]int
	matched int
}

func newFlaky(t *testing.T, next http.Handler, match func(*http.Request) bool, failures int, fail failFunc) *flakyServer {
	t.Helper()

	f := &flakyServer{seen: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if match == nil || !match(r) {
			next.ServeHTTP(w, r)
			return
		}

		f.mu.Lock()
		key := r.Method + " " + r.URL.Path
		f.seen[key]++
		n := f.seen[key]
		f.matched++
		f.mu.Unlock()

		if n <= failures {
			fail(w, r, next)
			return
		}

		next.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)

	return f
}

// hits is how many matching requests arrived, failed or not.
func (f *flakyServer) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.matched
}

// failStatus answers with code (plus optional header key/value pairs) without
// handling the request.
func failStatus(code int, hdr ...string) failFunc {
	return func(w http.ResponseWriter, r *http.Request, _ http.Handler) {
		_, _ = io.Copy(io.Discard, r.Body)

		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}

		w.WriteHeader(code)
	}
}

// failDrop closes the connection without answering.
func failDrop(w http.ResponseWriter, _ *http.Request, _ http.Handler) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

// failTruncated sends the headers of a long response and only a few bytes of
// it, then closes the connection: the client sees the body end early.
func failTruncated(w http.ResponseWriter, _ *http.Request, _ http.Handler) {
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}

	_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\nabc")
	_ = buf.Flush()
	_ = conn.Close()
}

// failLostResponse lets the real handler store the request, then reports a
// server error: the work happened but the client never learns it.
func failLostResponse(code int) failFunc {
	return func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		next.ServeHTTP(httptest.NewRecorder(), r)
		w.WriteHeader(code)
	}
}

type fakeSleeper struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *fakeSleeper) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	s.mu.Unlock()

	return ctx.Err()
}

func (s *fakeSleeper) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.waits)
}

func (s *fakeSleeper) apply(r *retrier) {
	r.sleep = s.sleep
	r.jitter = func(d time.Duration) time.Duration { return d }
	r.now = retryTestNow
}

const always = math.MaxInt

func isDownload(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repository/")
}

func isListing(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/service/rest/v1/assets")
}

func isMavenPut(r *http.Request) bool { return r.Method == http.MethodPut }

func isDebPost(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/debs")
}

type flakySide struct {
	match    func(*http.Request) bool
	failures int
	fail     failFunc
}

type scenario struct {
	format string // maven, raw or deb
	src    flakySide
	dst    flakySide
	args   []string
}

type outcome struct {
	rep     *importReport
	oa      *fakeOA
	src     *flakyServer
	dst     *flakyServer
	sleeps  *fakeSleeper
	dstName string
}

func (o outcome) items() int { return len(o.oa.repos[o.dstName].items) }

// runScenario imports one asset through fault-injecting proxies on both sides.
func runScenario(t *testing.T, sc scenario) outcome {
	t.Helper()

	var (
		srcRepo = fakeSourceRepo{kind: "hosted"}
		dstRepo = &fakeRepo{repoType: "hosted"}
		dstName string
		extra   []string
	)

	switch sc.format {
	case "maven":
		srcRepo.name, srcRepo.format, dstName, dstRepo.format = "rel", "maven2", "mvn", "maven"
		srcRepo.files = []fakeFile{{path: "com/acme/lib/1.0/lib-1.0.jar", data: []byte("jar-bytes"), created: nexusStamp}}
	case "raw":
		srcRepo.name, srcRepo.format, dstName, dstRepo.format = "files", "raw", "raw", "raw"
		srcRepo.files = []fakeFile{{path: "a.bin", data: []byte("raw-bytes"), created: nexusStamp}}
	case "deb":
		srcRepo.name, srcRepo.format, dstName, dstRepo.format = "ubuntu", "apt", "apt", "deb"
		srcRepo.files = []fakeFile{{path: "pool/p/pkg/pkg_1.0_amd64.deb", data: []byte("!<arch>deb"), created: nexusStamp}}
		extra = []string{"--deb-distribution", "stable", "--deb-component", "main"}
	default:
		t.Fatalf("unknown format %q", sc.format)
	}

	nexus := newFakeNexus(t, srcRepo)
	oa := newFakeOA(t, map[string]*fakeRepo{dstName: dstRepo})
	src := newFlaky(t, nexus.Config.Handler, sc.src.match, sc.src.failures, sc.src.fail)
	dst := newFlaky(t, oa.srv.Config.Handler, sc.dst.match, sc.dst.failures, sc.dst.fail)

	args := []string{"nexus", "--source-url", src.URL, "--repo", srcRepo.name + ":" + dstName, "--concurrency", "1"}
	cfg := parseCfg(t, append(append(args, extra...), sc.args...)...)

	sleeps := &fakeSleeper{}
	sleeps.apply(cfg.retrier)

	tgt := &importTarget{c: &Client{baseURL: dst.URL, token: oa.token, httpClient: dst.Client()}}

	imp, err := newImporter(cfg, newImportSource(cfg, src.Client()), tgt, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(imp.state.close)

	return outcome{rep: imp.run(context.Background()), oa: oa, src: src, dst: dst, sleeps: sleeps, dstName: dstName}
}

func ms(n ...int) []time.Duration {
	out := make([]time.Duration, len(n))
	for i, v := range n {
		out[i] = time.Duration(v) * time.Millisecond
	}

	return out
}

// ─── end to end ───────────────────────────────────────────────────────────────

func TestImportRetriesTransientFailures(t *testing.T) {
	cases := []struct {
		name        string
		sc          scenario
		wantWaits   []time.Duration
		wantSrcHits int // matching source requests, 0 = unchecked
		wantDstHits int // matching destination requests, 0 = unchecked
	}{
		{"download 503", scenario{format: "maven", src: flakySide{isDownload, 2, failStatus(503)}}, ms(500, 1000), 3, 0},
		{"download 502", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(502)}}, ms(500), 2, 0},
		{"download 500", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(500)}}, ms(500), 2, 0},
		{"download 429", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(429)}}, ms(500), 2, 0},
		{"download 408", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(408)}}, ms(500), 2, 0},
		// The body dies mid-stream while it is being uploaded: the whole
		// transfer starts over with a freshly opened source stream.
		{"download cut off mid-stream", scenario{format: "maven", src: flakySide{isDownload, 1, failTruncated}}, ms(500), 2, 0},
		{"deb download cut off mid-stream", scenario{format: "deb", src: flakySide{isDownload, 1, failTruncated}}, ms(500), 2, 0},
		{"listing 503", scenario{format: "maven", src: flakySide{isListing, 1, failStatus(503)}}, ms(500), 2, 0},
		{"maven PUT 503", scenario{format: "maven", dst: flakySide{isMavenPut, 2, failStatus(503)}}, ms(500, 1000), 0, 3},
		{"raw PUT 500", scenario{format: "raw", dst: flakySide{isMavenPut, 1, failStatus(500)}}, ms(500), 0, 2},
		{"raw PUT connection dropped", scenario{format: "raw", dst: flakySide{isMavenPut, 1, failDrop}}, ms(500), 0, 2},
		{"maven PUT lost response", scenario{format: "maven", dst: flakySide{isMavenPut, 1, failLostResponse(503)}}, ms(500), 0, 2},
		{"deb POST 503", scenario{format: "deb", dst: flakySide{isDebPost, 2, failStatus(503)}}, ms(500, 1000), 0, 3},
		// The first POST stored the package and only its answer was lost: the
		// SHA-256 pre-check finds it, so there is no second POST (which the
		// server would reject as a duplicate or store twice).
		{"deb POST lost response", scenario{format: "deb", dst: flakySide{isDebPost, 1, failLostResponse(503)}}, ms(500), 0, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := runScenario(t, tc.sc)

			if o.rep.Failed != 0 || o.rep.Uploaded != 1 {
				t.Fatalf("report = %+v", o.rep)
			}

			if o.rep.Retries != len(tc.wantWaits) {
				t.Fatalf("retries = %d, want %d", o.rep.Retries, len(tc.wantWaits))
			}

			if got := o.sleeps.recorded(); !slices.Equal(got, tc.wantWaits) {
				t.Fatalf("waits = %v, want %v", got, tc.wantWaits)
			}

			if tc.wantSrcHits != 0 && o.src.hits() != tc.wantSrcHits {
				t.Fatalf("source hits = %d, want %d", o.src.hits(), tc.wantSrcHits)
			}

			if tc.wantDstHits != 0 && o.dst.hits() != tc.wantDstHits {
				t.Fatalf("destination hits = %d, want %d", o.dst.hits(), tc.wantDstHits)
			}

			if o.items() != 1 {
				t.Fatalf("destination holds %d items, want 1", o.items())
			}

			var out bytes.Buffer

			o.rep.print(&out)

			if want := fmt.Sprintf("retries:               %d", len(tc.wantWaits)); !strings.Contains(out.String(), want) {
				t.Fatalf("summary lacks %q:\n%s", want, out.String())
			}
		})
	}
}

func TestImportRetriedUploadKeepsContentIntact(t *testing.T) {
	// A download that dies after the headers must be re-opened from the start;
	// the destination must end up with the full, verified content.
	o := runScenario(t, scenario{format: "maven", dst: flakySide{isMavenPut, 2, failStatus(503)}})

	jar := o.oa.find("mvn", "com/acme/lib/1.0/lib-1.0.jar")
	if o.rep.failed() || jar == nil || jar.sha != sum256([]byte("jar-bytes")) {
		t.Fatalf("report = %+v item = %+v", o.rep, jar)
	}
}

func TestImportDoesNotRetryClientErrors(t *testing.T) {
	sides := []struct {
		name   string
		format string
		src    func(code int) flakySide
		dst    func(code int) flakySide
		hits   func(o outcome) int
	}{
		{"download", "maven", func(c int) flakySide { return flakySide{isDownload, always, failStatus(c)} }, nil, func(o outcome) int { return o.src.hits() }},
		{"listing", "maven", func(c int) flakySide { return flakySide{isListing, always, failStatus(c)} }, nil, func(o outcome) int { return o.src.hits() }},
		{"maven PUT", "maven", nil, func(c int) flakySide { return flakySide{isMavenPut, always, failStatus(c)} }, func(o outcome) int { return o.dst.hits() }},
		{"raw PUT", "raw", nil, func(c int) flakySide { return flakySide{isMavenPut, always, failStatus(c)} }, func(o outcome) int { return o.dst.hits() }},
		{"deb POST", "deb", nil, func(c int) flakySide { return flakySide{isDebPost, always, failStatus(c)} }, func(o outcome) int { return o.dst.hits() }},
	}

	for _, side := range sides {
		for _, code := range []int{400, 401, 403, 404} {
			t.Run(fmt.Sprintf("%s %d", side.name, code), func(t *testing.T) {
				sc := scenario{format: side.format}
				if side.src != nil {
					sc.src = side.src(code)
				}

				if side.dst != nil {
					sc.dst = side.dst(code)
				}

				o := runScenario(t, sc)

				if o.rep.Failed != 1 || o.rep.Uploaded != 0 || o.rep.Retries != 0 {
					t.Fatalf("report = %+v", o.rep)
				}

				if got := side.hits(o); got != 1 {
					t.Fatalf("matching requests = %d, want exactly 1 (no retry)", got)
				}

				if w := o.sleeps.recorded(); len(w) != 0 {
					t.Fatalf("slept %v on a %d", w, code)
				}
			})
		}
	}
}

func TestImportGivesUpAfterConfiguredRetries(t *testing.T) {
	o := runScenario(t, scenario{
		format: "maven",
		src:    flakySide{isDownload, always, failStatus(500)},
		args:   []string{"--retries", "2"},
	})

	if o.rep.Failed != 1 || o.rep.Retries != 2 || o.src.hits() != 3 {
		t.Fatalf("report = %+v source hits = %d, want 2 retries over 3 attempts", o.rep, o.src.hits())
	}

	if got := o.sleeps.recorded(); !slices.Equal(got, ms(500, 1000)) {
		t.Fatalf("waits = %v", got)
	}

	if msg := o.rep.Failures[0].Error; !strings.Contains(msg, "HTTP 500") || !strings.Contains(msg, "after 2 retries") {
		t.Fatalf("failure = %q", msg)
	}
}

func TestImportRetriesZeroDisablesRetrying(t *testing.T) {
	o := runScenario(t, scenario{
		format: "maven",
		src:    flakySide{isDownload, 1, failStatus(503)},
		args:   []string{"--retries", "0"},
	})

	if o.rep.Failed != 1 || o.rep.Retries != 0 || o.src.hits() != 1 || len(o.sleeps.recorded()) != 0 {
		t.Fatalf("report = %+v source hits = %d", o.rep, o.src.hits())
	}
}

func TestImportHonorsRetryAfter(t *testing.T) {
	date := retryTestNow().Add(10 * time.Second).Format(http.TimeFormat)

	cases := []struct {
		name      string
		sc        scenario
		wantWaits []time.Duration
		wantFail  bool
	}{
		{"429 seconds", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(429, "Retry-After", "7")}}, []time.Duration{7 * time.Second}, false},
		{"503 HTTP date", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(503, "Retry-After", date)}}, []time.Duration{10 * time.Second}, false},
		{"429 on upload", scenario{format: "raw", dst: flakySide{isMavenPut, 1, failStatus(429, "Retry-After", "3")}}, []time.Duration{3 * time.Second}, false},
		{"zero means now", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(503, "Retry-After", "0")}}, []time.Duration{0}, false},
		{"ignored on 500", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(500, "Retry-After", "7")}}, ms(500), false},
		{"garbage falls back to backoff", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(429, "Retry-After", "soon")}}, ms(500), false},
		{"beyond max wait fails fast", scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(503, "Retry-After", "120")}}, nil, true},
		{
			"beyond custom max wait fails fast",
			scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(429, "Retry-After", "60")}, args: []string{"--retry-max-wait", "5s"}},
			nil, true,
		},
		{
			"within custom max wait",
			scenario{format: "maven", src: flakySide{isDownload, 1, failStatus(429, "Retry-After", "5")}, args: []string{"--retry-max-wait", "5s"}},
			[]time.Duration{5 * time.Second}, false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := runScenario(t, tc.sc)

			if got := o.sleeps.recorded(); !slices.Equal(got, tc.wantWaits) {
				t.Fatalf("waits = %v, want %v", got, tc.wantWaits)
			}

			if tc.wantFail != o.rep.failed() {
				t.Fatalf("failed = %v, want %v: %+v", o.rep.failed(), tc.wantFail, o.rep)
			}
		})
	}
}

// stubSource serves one body per Open call.
type stubSource struct {
	body  string
	opens int
}

func (s *stubSource) Repository(context.Context, string) (sourceRepo, error) {
	return sourceRepo{}, errNotFound
}

func (s *stubSource) Assets(context.Context, sourceRepo) ([]sourceAsset, error) { return nil, nil }

func (s *stubSource) Details(context.Context, *sourceAsset) error { return nil }

func (s *stubSource) Open(context.Context, sourceAsset) (io.ReadCloser, error) {
	s.opens++

	return io.NopCloser(strings.NewReader(s.body)), nil
}

func TestUploadWithoutSourceSHA256IsNotRepeatedForDebRPM(t *testing.T) {
	for _, tc := range []struct {
		format    importFormat
		match     func(*http.Request) bool
		wantTries int
	}{
		{formatDeb, isDebPost, 1},
		{formatRPM, func(r *http.Request) bool {
			return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/packages")
		}, 1},
		// Overwriting formats do not need a digest to be repeated safely.
		{formatRaw, isMavenPut, 3},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			oa := newFakeOA(t, map[string]*fakeRepo{"dst": {format: string(tc.format), repoType: "hosted"}})
			dst := newFlaky(t, oa.srv.Config.Handler, tc.match, 2, failStatus(503))

			rt := newRetrier(3, time.Minute)
			(&fakeSleeper{}).apply(rt)

			src := &stubSource{body: "payload"}
			im := &importer{src: src, retry: rt, tgt: &importTarget{c: &Client{baseURL: dst.URL, token: oa.token, httpClient: dst.Client()}}}
			job := importJob{repo: sourceRepo{Name: "src", Format: tc.format}, dst: "dst"}
			asset := sourceAsset{Path: "pkg/file.bin", Size: int64(len(src.body))} // no SHA256

			_, _, err := im.uploadWithRetry(context.Background(), job, asset, uploadMeta{})

			if tc.wantTries == 1 {
				if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
					t.Fatalf("err = %v, want the 503 to be final", err)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}

			if dst.hits() != tc.wantTries || src.opens != tc.wantTries {
				t.Fatalf("upload attempts = %d, source opens = %d, want %d each", dst.hits(), src.opens, tc.wantTries)
			}
		})
	}
}

// ─── retrier ──────────────────────────────────────────────────────────────────

func statusErr(code int, retryAfter string) error {
	return &httpStatusError{status: code, retryAfter: retryAfter, msg: fmt.Sprintf("HTTP %d", code)}
}

func TestRetrierClassifiesErrors(t *testing.T) {
	rt := newRetrier(10, time.Minute)
	(&fakeSleeper{}).apply(rt)

	cases := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{"400", statusErr(400, ""), false},
		{"401", statusErr(401, ""), false},
		{"403", statusErr(403, ""), false},
		{"404", statusErr(404, ""), false},
		{"409", statusErr(409, ""), false},
		{"408", statusErr(408, ""), true},
		{"429", statusErr(429, ""), true},
		{"500", statusErr(500, ""), true},
		{"503", statusErr(503, ""), true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"EOF in url error", &url.Error{Op: "Get", URL: "http://x", Err: io.EOF}, true},
		{"connection reset", &url.Error{Op: "Get", URL: "http://x", Err: syscall.ECONNRESET}, true},
		{"cancelled", &url.Error{Op: "Get", URL: "http://x", Err: context.Canceled}, false},
		{"redirect refused", &url.Error{Op: "Get", URL: "http://x", Err: errors.New("refusing to follow redirect")}, false},
		{"verification", fmt.Errorf("%w: checksum mismatch", errImportVerify), false},
		{"usage", errImportUsage, false},
		{"conflict", errUploadConflict, false},
		{"permanent 503", permanent(statusErr(503, "")), false},
		{"wrapped 503", fmt.Errorf("listing: %w", statusErr(503, "")), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := rt.delay(0, tc.err); got != tc.wantRetry {
				t.Fatalf("retry = %v, want %v", got, tc.wantRetry)
			}
		})
	}
}

func TestRetrierBackoffDoublesAndCaps(t *testing.T) {
	rt := newRetrier(6, 4*time.Second)
	(&fakeSleeper{}).apply(rt)

	var got []time.Duration

	for attempt := range 6 {
		d, ok := rt.delay(attempt, statusErr(503, ""))
		if !ok {
			t.Fatalf("attempt %d not retried", attempt)
		}

		got = append(got, d)
	}

	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("backoff = %v, want %v", got, want)
	}

	if _, ok := rt.delay(6, statusErr(503, "")); ok {
		t.Fatal("retried past the limit")
	}
}

func TestHalfJitterStaysWithinBounds(t *testing.T) {
	for _, d := range []time.Duration{0, 1, 500 * time.Millisecond, 30 * time.Second} {
		for range 200 {
			if got := halfJitter(d); got < d/2 || got > d {
				t.Fatalf("halfJitter(%v) = %v, want within [%v, %v]", d, got, d/2, d)
			}
		}
	}
}

func TestRetrierStopsWhenContextEnds(t *testing.T) {
	rt := newRetrier(5, time.Minute)
	rt.jitter = func(d time.Duration) time.Duration { return d }

	ctx, cancel := context.WithCancel(context.Background())
	rt.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	calls := 0
	err := rt.do(ctx, "op", func(int) error {
		calls++
		return statusErr(503, "")
	})

	if calls != 1 || err == nil {
		t.Fatalf("calls = %d err = %v, want one attempt and the error", calls, err)
	}
}

func TestNilRetrierRunsOnce(t *testing.T) {
	var rt *retrier

	calls := 0
	_ = rt.do(context.Background(), "op", func(int) error {
		calls++
		return statusErr(503, "")
	})

	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestParseRetryFlags(t *testing.T) {
	base := []string{"nexus", "--source-url", "https://n.example", "--repo", "a"}

	cfg := parseCfg(t, base...)
	if cfg.Retries != 3 || cfg.RetryMaxWait != 30*time.Second || cfg.retrier == nil || cfg.retrier.max != 3 {
		t.Fatalf("defaults: retries=%d maxWait=%v", cfg.Retries, cfg.RetryMaxWait)
	}

	cfg = parseCfg(t, append(slices.Clone(base), "--retries", "7", "--retry-max-wait", "2m")...)
	if cfg.Retries != 7 || cfg.RetryMaxWait != 2*time.Minute || cfg.retrier.max != 7 || cfg.retrier.maxWait != 2*time.Minute {
		t.Fatalf("explicit: %+v", cfg)
	}

	for _, bad := range [][]string{{"--retries", "-1"}, {"--retry-max-wait", "0s"}, {"--retry-max-wait", "-5s"}} {
		_, err := parseImportFlags(append(slices.Clone(base), bad...), func(string) string { return "" }, strings.NewReader(""))
		if !errors.Is(err, errImportUsage) {
			t.Fatalf("%v: err = %v, want usage error", bad, err)
		}
	}
}
