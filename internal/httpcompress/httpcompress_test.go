// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package httpcompress_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/httpcache"
	"github.com/openartifact/openartifact/internal/httpcompress"
)

// jsonBody is a repetitive JSON document well above MinSize.
func jsonBody() []byte {
	return []byte(`[` + strings.Repeat(`{"name":"repo","format":"npm","public":false},`, 60) + `{}]`)
}

type handlerOpts struct {
	contentType string
	status      int
	body        []byte
	header      map[string]string
	// writes splits body into this many Write calls (0 = one).
	writes int
}

func (o handlerOpts) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		if o.contentType != "" {
			h.Set("Content-Type", o.contentType)
		}

		for k, v := range o.header {
			h.Set(k, v)
		}

		if o.status != 0 {
			w.WriteHeader(o.status)
		}

		if o.writes <= 1 {
			_, _ = w.Write(o.body)

			return
		}

		step := len(o.body)/o.writes + 1
		for i := 0; i < len(o.body); i += step {
			_, _ = w.Write(o.body[i:min(i+step, len(o.body))])
		}
	})
}

func serve(t *testing.T, h http.Handler, method string, reqHeader map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, "/", http.NoBody)
	for k, v := range reqHeader {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	httpcompress.Middleware(h).ServeHTTP(rec, req)

	return rec
}

func decode(t *testing.T, enc string, body []byte) []byte {
	t.Helper()

	var (
		out []byte
		err error
	)

	switch enc {
	case "gzip":
		var zr *gzip.Reader

		if zr, err = gzip.NewReader(bytes.NewReader(body)); err == nil {
			out, err = io.ReadAll(zr)
		}
	case "zstd":
		var zr *zstd.Decoder

		if zr, err = zstd.NewReader(bytes.NewReader(body)); err == nil {
			defer zr.Close()

			out, err = io.ReadAll(zr)
		}
	default:
		t.Fatalf("unexpected Content-Encoding %q", enc)
	}

	if err != nil {
		t.Fatalf("decode %s: %v", enc, err)
	}

	return out
}

func TestNegotiationMatrix(t *testing.T) {
	body := jsonBody()

	for _, tc := range []struct {
		name   string
		accept string
		want   string
	}{
		{"none sent", "", ""},
		{"identity only", "identity", ""},
		{"gzip", "gzip", "gzip"},
		{"zstd", "zstd", "zstd"},
		{"browser list prefers zstd", "gzip, deflate, br, zstd", "zstd"},
		{"curl list", "deflate, gzip", "gzip"},
		{"gzip preferred by q", "gzip;q=1.0, zstd;q=0.5", "gzip"},
		{"zstd preferred by q", "gzip;q=0.2, zstd;q=0.8", "zstd"},
		{"zstd refused", "gzip, zstd;q=0", "gzip"},
		{"gzip refused", "gzip;q=0, zstd", "zstd"},
		{"both refused", "gzip;q=0, zstd;q=0", ""},
		{"star means gzip", "*", "gzip"},
		{"star with gzip refused", "*, gzip;q=0", "zstd"},
		{"star refused", "*;q=0", ""},
		{"x-gzip alias", "x-gzip", "gzip"},
		{"case and spaces", " GZip ; q = 0.9 ", "gzip"},
		{"malformed q is a refusal", "gzip;q=abc", ""},
		{"br only", "br", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tc.accept != "" {
				hdr["Accept-Encoding"] = tc.accept
			}

			rec := serve(t, handlerOpts{contentType: "application/json", body: body}.handler(), http.MethodGet, hdr)

			if got := rec.Header().Get("Content-Encoding"); got != tc.want {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.want)
			}

			if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
				t.Errorf("Vary = %q, must name Accept-Encoding whatever was negotiated", rec.Header().Get("Vary"))
			}

			if tc.want == "" {
				if !bytes.Equal(rec.Body.Bytes(), body) {
					t.Error("identity body was altered")
				}

				return
			}

			if got := decode(t, tc.want, rec.Body.Bytes()); !bytes.Equal(got, body) {
				t.Error("body does not decode to the original")
			}

			if rec.Body.Len() >= len(body)/4 {
				t.Errorf("%d -> %d bytes: expected at least 4x on repetitive JSON", len(body), rec.Body.Len())
			}

			if rec.Header().Get("Content-Length") != "" {
				t.Errorf("Content-Length %q left on an encoded response", rec.Header().Get("Content-Length"))
			}
		})
	}
}

// What is compressible and what is not.
func TestContentTypes(t *testing.T) {
	for _, tc := range []struct {
		contentType string
		compressed  bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/vnd.oci.image.manifest.v1+json", true},
		{"application/xml", true},
		{"text/xml; charset=utf-8", true},
		{"application/x-yaml", true},
		{"text/plain", true},
		{"text/html", true},
		{"application/x-ndjson", true},
		{"image/svg+xml", true},
		{"text/event-stream", false},
		{"application/octet-stream", false},
		{"application/gzip", false},
		{"application/zip", false},
		{"application/vnd.pypi.simple.v1+json", true},
		{"image/png", false},
		{"", false}, // a binary body without a type: sniffed as octet-stream
	} {
		t.Run(tc.contentType, func(t *testing.T) {
			body := jsonBody()
			if tc.contentType == "" {
				body = append([]byte{0x1f, 0x00, 0x01, 0x02}, make([]byte, 4096)...)
			}

			rec := serve(t, handlerOpts{contentType: tc.contentType, body: body}.handler(), http.MethodGet,
				map[string]string{"Accept-Encoding": "gzip"})

			if got := rec.Header().Get("Content-Encoding") == "gzip"; got != tc.compressed {
				t.Errorf("compressed = %v, want %v", got, tc.compressed)
			}

			if !tc.compressed && !bytes.Equal(rec.Body.Bytes(), body) {
				t.Error("an uncompressed response was altered")
			}
		})
	}
}

// An untyped text body is sniffed like net/http does, then compressed.
func TestUntypedTextIsSniffed(t *testing.T) {
	body := bytes.Repeat([]byte("plain words in a plain text body\n"), 100)

	rec := serve(t, handlerOpts{body: body}.handler(), http.MethodGet, map[string]string{"Accept-Encoding": "gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("headers: %v", rec.Header())
	}

	if !bytes.Equal(decode(t, "gzip", rec.Body.Bytes()), body) {
		t.Error("body does not decode to the original")
	}
}

// Responses that must never be re-encoded or encoded at all.
func TestLeavesAlone(t *testing.T) {
	big := jsonBody()
	accept := map[string]string{"Accept-Encoding": "gzip, zstd"}

	for _, tc := range []struct {
		name   string
		opts   handlerOpts
		method string
		req    map[string]string
		want   string // expected Content-Encoding
	}{
		{"already gzip", handlerOpts{contentType: "application/json", body: big, header: map[string]string{"Content-Encoding": "gzip"}}, http.MethodGet, accept, "gzip"},
		{"already zstd", handlerOpts{contentType: "application/json", body: big, header: map[string]string{"Content-Encoding": "zstd"}}, http.MethodGet, accept, "zstd"},
		{"download", handlerOpts{contentType: "text/plain", body: big, header: map[string]string{"Content-Disposition": `attachment; filename="a.txt"`}}, http.MethodGet, accept, ""},
		{"content range", handlerOpts{contentType: "application/json", body: big, header: map[string]string{"Content-Range": "bytes 0-9/100"}}, http.MethodGet, accept, ""},
		{"partial content", handlerOpts{contentType: "application/json", status: http.StatusPartialContent, body: big}, http.MethodGet, accept, ""},
		{"no-transform", handlerOpts{contentType: "application/json", body: big, header: map[string]string{"Cache-Control": "public, no-transform"}}, http.MethodGet, accept, ""},
		{"error status", handlerOpts{contentType: "application/json", status: http.StatusInternalServerError, body: big}, http.MethodGet, accept, ""},
		{"created", handlerOpts{contentType: "application/json", status: http.StatusCreated, body: big}, http.MethodPost, accept, ""},
		{"range request", handlerOpts{contentType: "application/json", body: big}, http.MethodGet, map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-9"}, ""},
		{"HEAD", handlerOpts{contentType: "application/json", body: big}, http.MethodHead, accept, ""},
		{"upgrade", handlerOpts{contentType: "application/json", body: big}, http.MethodGet, map[string]string{"Accept-Encoding": "gzip", "Upgrade": "websocket"}, ""},
		{"declared small", handlerOpts{contentType: "application/json", body: big[:200], header: map[string]string{"Content-Length": "200"}}, http.MethodGet, accept, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, tc.opts.handler(), tc.method, tc.req)

			if got := rec.Header().Get("Content-Encoding"); got != tc.want {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.want)
			}

			// Byte-for-byte what the handler wrote: no second encoding, no
			// change of a ranged or download body.
			if tc.method != http.MethodHead && !bytes.Equal(rec.Body.Bytes(), tc.opts.body) {
				t.Errorf("body altered: %d bytes, handler wrote %d", rec.Body.Len(), len(tc.opts.body))
			}

			if cl := rec.Header().Get("Content-Length"); cl != "" && cl != strconv.Itoa(rec.Body.Len()) && tc.method != http.MethodHead {
				t.Errorf("Content-Length %s != body %d", cl, rec.Body.Len())
			}
		})
	}
}

// A small body is sent as is whether or not the handler declared its length,
// and a body that crosses the threshold in several writes is encoded whole.
func TestSizeThreshold(t *testing.T) {
	accept := map[string]string{"Accept-Encoding": "gzip"}
	small := []byte(`{"ok":true}`)

	rec := serve(t, handlerOpts{contentType: "application/json", body: small}.handler(), http.MethodGet, accept)
	if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), small) {
		t.Errorf("small body: headers %v", rec.Header())
	}

	// Many tiny writes under the threshold: still plain, and complete.
	smallish := bytes.Repeat([]byte("0123456789"), 90) // 900 B
	rec = serve(t, handlerOpts{contentType: "application/json", body: smallish, writes: 30}.handler(), http.MethodGet, accept)

	if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), smallish) {
		t.Errorf("900 B in 30 writes: encoding %q, %d bytes", rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}

	// Many writes crossing the threshold: encoded, and the held prefix is not lost.
	big := jsonBody()
	rec = serve(t, handlerOpts{contentType: "application/json", body: big, writes: 40}.handler(), http.MethodGet, accept)

	if rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(decode(t, "gzip", rec.Body.Bytes()), big) {
		t.Errorf("%d B in 40 writes: encoding %q", len(big), rec.Header().Get("Content-Encoding"))
	}

	// WriteHeader first, body later: same result.
	rec = serve(t, handlerOpts{contentType: "application/json", status: http.StatusOK, body: big}.handler(), http.MethodGet, accept)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("explicit 200: encoding %q", rec.Header().Get("Content-Encoding"))
	}

	// A bare status without a body.
	rec = serve(t, handlerOpts{status: http.StatusAccepted}.handler(), http.MethodPost, accept)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("bare 202: %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

// RFC 9110: an encoded representation is not byte-identical to the plain one,
// so a strong entity tag becomes weak; the handler's weak-comparison 304 keeps
// working for a client that cached the weak tag.
func TestETagAndConditionalRequests(t *testing.T) {
	body := jsonBody()
	etag := `"abc123"`

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")

		if httpcache.ETagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		_, _ = w.Write(body)
	})

	accept := map[string]string{"Accept-Encoding": "gzip"}

	rec := serve(t, handler, http.MethodGet, accept)
	if rec.Header().Get("ETag") != `W/`+etag {
		t.Fatalf("encoded ETag = %q, want a weak tag", rec.Header().Get("ETag"))
	}

	// Identity keeps the strong tag.
	if rec = serve(t, handler, http.MethodGet, nil); rec.Header().Get("ETag") != etag {
		t.Errorf("identity ETag = %q, want it strong", rec.Header().Get("ETag"))
	}

	// A client revalidating with the weak tag (what it cached), and one with the
	// strong tag (what an identity response gave it), both get a 304.
	for _, inm := range []string{`W/` + etag, etag} {
		rec = serve(t, handler, http.MethodGet, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": inm})
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %s: status %d, %d body bytes", inm, rec.Code, rec.Body.Len())
		}

		if rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("304 carries Content-Encoding %q", rec.Header().Get("Content-Encoding"))
		}

		// The 304 stands in for the 200: it must carry the same Vary and a validator.
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") || rec.Header().Get("ETag") == "" {
			t.Errorf("304 headers: %v", rec.Header())
		}
	}
}

// Vary is not duplicated when the handler (or an outer layer) already names it.
func TestVaryNotDuplicated(t *testing.T) {
	rec := serve(
		t,
		handlerOpts{
			contentType: "application/json",
			body:        jsonBody(),
			header:      map[string]string{"Vary": "Authorization, accept-encoding"},
		}.handler(),
		http.MethodGet,
		map[string]string{"Accept-Encoding": "gzip"},
	)

	if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Authorization, accept-encoding" {
		t.Errorf("Vary = %q", got)
	}

	rec = serve(t, handlerOpts{contentType: "application/json", body: jsonBody()}.handler(), http.MethodGet,
		map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
		t.Errorf("Vary = %q", got)
	}
}

// A handler that flushes (a stream) is encoded as it goes, and the client can
// decode what arrived before the handler finished.
func TestFlushStreams(t *testing.T) {
	step := make(chan struct{})
	done := make(chan struct{})

	srv := httptest.NewServer(httpcompress.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(done)

		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"n":1}` + "\n"))
		http.NewResponseController(w).Flush()

		<-step

		_, _ = w.Write([]byte(`{"n":2}` + "\n"))
	})))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("flushed stream not encoded: %v", resp.Header)
	}

	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	line := make([]byte, 8)
	if _, err := io.ReadFull(zr, line); err != nil || string(line) != `{"n":1}`+"\n" {
		t.Fatalf("first event before the handler finished: %q, %v", line, err)
	}

	close(step)

	rest, err := io.ReadAll(zr)
	if err != nil || string(rest) != `{"n":2}`+"\n" {
		t.Fatalf("rest = %q, %v", rest, err)
	}

	<-done
}

// A small flushed document is not worth encoding only when its declared length
// says so; undeclared flushed bodies are streams and are encoded.
func TestFlushedDeclaredSmallStaysPlain(t *testing.T) {
	srv := httptest.NewServer(httpcompress.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "11")
		_, _ = w.Write([]byte(`{"ok":true}`))
		http.NewResponseController(w).Flush()
	})))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Encoding") != "" || string(body) != `{"ok":true}` || resp.ContentLength != 11 {
		t.Errorf("headers %v, body %q", resp.Header, body)
	}
}

// readFromRecorder is a response writer with ReadFrom, like net/http's, that
// records how the body arrived.
type readFromRecorder struct {
	*httptest.ResponseRecorder

	readFrom int
}

func (r *readFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.readFrom++

	return io.Copy(r.ResponseRecorder, struct{ io.Reader }{src})
}

// A response the middleware leaves alone must keep the writer's ReadFrom
// reachable: that is the door to sendfile(2) for file bodies.
func TestReadFromPassesThroughWhenPlain(t *testing.T) {
	big := bytes.Repeat([]byte{7}, 4096)

	handler := func(contentType string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)

			rf, ok := w.(io.ReaderFrom)
			if !ok {
				t.Error("wrapped writer lost io.ReaderFrom")
				return
			}

			if _, err := rf.ReadFrom(bytes.NewReader(big)); err != nil {
				t.Error(err)
			}
		})
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")

	rec := &readFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	httpcompress.Middleware(handler("application/octet-stream")).ServeHTTP(rec, req)

	if rec.readFrom != 1 || !bytes.Equal(rec.Body.Bytes(), big) || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf(
			"plain: underlying ReadFrom calls = %d, encoding %q, %d bytes",
			rec.readFrom,
			rec.Header().Get("Content-Encoding"),
			rec.Body.Len(),
		)
	}

	// A compressible body through ReadFrom is encoded, not sent raw.
	rec = &readFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	httpcompress.Middleware(handler("application/json")).ServeHTTP(rec, req)

	if rec.readFrom != 0 || rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(decode(t, "gzip", rec.Body.Bytes()), big) {
		t.Errorf("json: underlying ReadFrom calls = %d, encoding %q", rec.readFrom, rec.Header().Get("Content-Encoding"))
	}
}

// The wrapper keeps what http.ResponseController needs to reach the real
// connection: Flush and Hijack through Unwrap.
func TestResponseControllerReachesTheConnection(t *testing.T) {
	hijacked := make(chan error, 1)

	srv := httptest.NewServer(httpcompress.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_, _ = conn.Write([]byte("HTTP/1.1 204 No Content\r\n\r\n"))
			_ = conn.Close()
		}

		hijacked <- err
	})))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if err := <-hijacked; err != nil {
		t.Fatalf("Hijack through the wrapper: %v", err)
	}
}

// A panicking handler leaves the response uncommitted so the recovery layer
// can answer, and does not poison the pools.
func TestPanicDoesNotCommit(t *testing.T) {
	boom := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partial":`))

		panic("boom")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")

	func() {
		defer func() { _ = recover() }()

		httpcompress.Middleware(boom).ServeHTTP(rec, req)
	}()

	if rec.Body.Len() != 0 || rec.Flushed {
		t.Errorf("a panicked handler's partial body was committed: %q", rec.Body.String())
	}

	// The pools still hand out a working encoder afterwards.
	body := jsonBody()

	rec = serve(
		t,
		handlerOpts{contentType: "application/json", body: body}.handler(),
		http.MethodGet,
		map[string]string{"Accept-Encoding": "zstd"},
	)
	if !bytes.Equal(decode(t, "zstd", rec.Body.Bytes()), body) {
		t.Error("encoder pool broken after a panic")
	}
}

// Concurrent requests share pooled encoders without mixing their output.
func TestConcurrentRequests(t *testing.T) {
	srv := httptest.NewServer(httpcompress.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat(`{"id":"`+r.URL.Query().Get("id")+`"},`, 500)))
	})))
	defer srv.Close()

	errs := make(chan error, 64)

	for i := range 64 {
		go func() {
			enc := []string{"gzip", "zstd"}[i%2]

			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"?id="+strconv.Itoa(i), http.NoBody)
			req.Header.Set("Accept-Encoding", enc)

			resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()

			raw, _ := io.ReadAll(resp.Body)
			got := decode(t, resp.Header.Get("Content-Encoding"), raw)

			want := strings.Repeat(`{"id":"`+strconv.Itoa(i)+`"},`, 500)
			if string(got) != want || resp.Header.Get("Content-Encoding") != enc {
				errs <- io.ErrUnexpectedEOF
				return
			}

			errs <- nil
		}()
	}

	for range 64 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// Func compresses GET only.
func TestFuncOnlyEncodesGET(t *testing.T) {
	h := httpcompress.Func(handlerOpts{contentType: "application/json", body: jsonBody()}.handler().ServeHTTP)

	for method, want := range map[string]string{http.MethodGet: "gzip", http.MethodPost: "", http.MethodHead: ""} {
		req := httptest.NewRequestWithContext(t.Context(), method, "/", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip")

		rec := httptest.NewRecorder()
		h(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != want {
			t.Errorf("%s: Content-Encoding = %q, want %q", method, got, want)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	body := jsonBody()
	body = bytes.Repeat(body, 12) // ~35 KB, the size of a repository page

	for _, enc := range []string{"gzip", "zstd"} {
		b.Run(enc, func(b *testing.B) {
			h := httpcompress.Middleware(handlerOpts{contentType: "application/json", body: body}.handler())
			req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/", http.NoBody)
			req.Header.Set("Accept-Encoding", enc)

			w := &discardWriter{header: http.Header{}}

			b.ReportAllocs()
			b.SetBytes(int64(len(body)))

			for b.Loop() {
				clear(w.header)

				w.n = 0
				h.ServeHTTP(w, req)
			}

			b.ReportMetric(float64(w.n), "wire-B")
		})
	}
}

type discardWriter struct {
	header http.Header
	n      int
}

func (w *discardWriter) Header() http.Header         { return w.header }
func (w *discardWriter) WriteHeader(int)             {}
func (w *discardWriter) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }
