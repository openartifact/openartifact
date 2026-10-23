// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeUploadID = "up-1"

// fakePutAttempt is one PUT the fake object store received.
type fakePutAttempt struct {
	part          int // 0 for the single-PUT target
	status        int
	body          []byte
	contentLength int64
	chunked       bool
	hasAuth       bool
	header        http.Header
}

// uploadFixture is a fake OpenArtifact API plus a fake object store that
// implement the multipart upload contract: the API plans the parts and issues
// part URLs, the store accepts exactly the planned sizes. Knobs make the
// pieces misbehave.
type uploadFixture struct {
	api, store *httptest.Server

	// Configuration, set before the upload starts.
	defaultPartSize int64
	ignoreMultipart bool          // answer a multipart request with a single-PUT target
	badPartCount    bool          // plan one part too many
	wrongPartSize   bool          // issue part URLs that claim a wrong size
	expiredFirst    map[int]bool  // the first URL issued for the part answers 403
	storeStatuses   map[int][]int // statuses the store answers a part with before accepting it
	completeReply   *fakeAPIReply // replaces the complete answer
	gateSize        int           // hold the first N concurrent PUTs until N have arrived

	// Observations, guarded by mu.
	mu           sync.Mutex
	sleeps       []time.Duration  // backoff waits the CLI asked for
	progress     [][2]int         // (done, total) progress callbacks
	initBody     map[string]any   // decoded upload request
	partRequests [][]int          // part_numbers of every parts call
	issued       map[int]int      // URLs issued per part
	attempts     []fakePutAttempt // every PUT, in arrival order
	accepted     map[int][]byte   // body of each accepted part
	completes    int
	aborts       int
	size         int64
	partSize     int64
	inflight     int
	maxInflight  int
	arrived      int
	ready        chan struct{}
	gateTimedOut bool
}

type fakeAPIReply struct {
	status int
	body   string
}

func newUploadFixture(t *testing.T) *uploadFixture {
	t.Helper()

	f := &uploadFixture{
		defaultPartSize: 5 << 20,
		expiredFirst:    map[int]bool{},
		storeStatuses:   map[int][]int{},
		issued:          map[int]int{},
		accepted:        map[int][]byte{},
		ready:           make(chan struct{}),
	}

	store := http.NewServeMux()
	store.HandleFunc("PUT /parts/{n}", f.handlePartPut)
	store.HandleFunc("PUT /single", f.handleSinglePut)
	f.store = httptest.NewServer(store)
	t.Cleanup(f.store.Close)

	api := http.NewServeMux()
	const uploads = "/api/v1/repositories/{name}/uploads"
	api.HandleFunc("POST "+uploads, f.handleInit)
	api.HandleFunc("POST "+uploads+"/{id}/parts", f.handleParts)
	api.HandleFunc("POST "+uploads+"/{id}/complete", f.handleComplete)
	api.HandleFunc("POST "+uploads+"/{id}/abort", f.handleAbort)
	f.api = httptest.NewServer(api)
	t.Cleanup(f.api.Close)

	return f
}

func writeJSONReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *uploadFixture) handleInit(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONReply(w, http.StatusBadRequest, `{"error":"bad body","code":"invalid_request"}`)
		return
	}

	size := int64(req["size"].(float64))
	multipart, _ := req["multipart"].(bool)

	if size > maxSinglePutBytes && !multipart {
		writeJSONReply(w, http.StatusBadRequest, `{"error":"too big","code":"invalid_size"}`)
		return
	}

	f.mu.Lock()
	f.initBody = req
	f.size = size
	f.mu.Unlock()

	if !multipart || f.ignoreMultipart {
		writeJSONReply(w, http.StatusCreated, `{"id":"`+fakeUploadID+`","method":"PUT","upload_url":"`+f.store.URL+`/single?X-Amz-Signature=secretsig","headers":{}}`)
		return
	}

	partSize := f.defaultPartSize
	if v, ok := req["part_size"].(float64); ok {
		partSize = int64(v)
	}

	count := max(1, (size+partSize-1)/partSize)
	if f.badPartCount {
		count++
	}

	f.mu.Lock()
	f.partSize = partSize
	f.mu.Unlock()

	writeJSONReply(w, http.StatusCreated, fmt.Sprintf(`{"id":%q,"multipart":true,"part_size":%d,"part_count":%d,"expires_at":"2030-01-01T00:00:00Z"}`,
		fakeUploadID, partSize, count))
}

func (f *uploadFixture) handleParts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PartNumbers []int `json:"part_numbers"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.PartNumbers) == 0 || len(req.PartNumbers) > 100 {
		writeJSONReply(w, http.StatusBadRequest, `{"error":"bad part numbers","code":"invalid_request"}`)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.partRequests = append(f.partRequests, slices.Clone(req.PartNumbers))

	type part struct {
		PartNumber int               `json:"part_number"`
		UploadURL  string            `json:"upload_url"`
		Method     string            `json:"method"`
		Headers    map[string]string `json:"headers"`
		Size       int64             `json:"size"`
	}

	parts := make([]part, 0, len(req.PartNumbers))

	for _, n := range req.PartNumbers {
		f.issued[n]++

		size := min(f.partSize, f.size-int64(n-1)*f.partSize)
		if f.wrongPartSize {
			size++
		}

		parts = append(parts, part{
			PartNumber: n,
			UploadURL:  fmt.Sprintf("%s/parts/%d?v=%d&X-Amz-Signature=secretsig", f.store.URL, n, f.issued[n]),
			Method:     http.MethodPut,
			Headers:    map[string]string{"X-Test-Part": strconv.Itoa(n)},
			Size:       size,
		})
	}

	body, _ := json.Marshal(map[string]any{"parts": parts, "expires_at": "2030-01-01T00:00:00Z"})
	writeJSONReply(w, http.StatusOK, string(body))
}

func (f *uploadFixture) handleComplete(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.completes++
	n := f.completes
	override := f.completeReply
	f.mu.Unlock()

	if override != nil {
		writeJSONReply(w, override.status, override.body)
		return
	}

	status := http.StatusCreated
	if n > 1 {
		status = http.StatusOK
	}

	f.mu.Lock()
	sum, _ := f.initBody["sha256"].(string)
	body := fmt.Sprintf(`{"id":"a-1","path":%q,"size":%d,"sha256":%q}`, f.initBody["path"], f.size, sum)
	f.mu.Unlock()

	writeJSONReply(w, status, body)
}

func (f *uploadFixture) handleAbort(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.aborts++
	f.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// enter registers a PUT in flight and, when gated, holds it until gateSize
// PUTs are in flight together. It reports false when they never arrived.
func (f *uploadFixture) enter() bool {
	f.mu.Lock()
	f.inflight++
	f.maxInflight = max(f.maxInflight, f.inflight)

	if f.gateSize > 0 {
		f.arrived++
		if f.arrived == f.gateSize {
			close(f.ready)
		}
	}

	gated := f.gateSize > 0
	f.mu.Unlock()

	if !gated {
		return true
	}

	select {
	case <-f.ready:
		return true
	case <-time.After(10 * time.Second):
		f.mu.Lock()
		f.gateTimedOut = true
		f.mu.Unlock()

		return false
	}
}

func (f *uploadFixture) handlePartPut(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	issue, _ := strconv.Atoi(r.URL.Query().Get("v"))

	entered := f.enter()
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()

	status := http.StatusOK

	switch {
	case !entered:
		status = http.StatusInternalServerError
	case f.expiredFirst[n] && issue == 1:
		status = http.StatusForbidden
	case len(f.storeStatuses[n]) > 0:
		status = f.storeStatuses[n][0]
		f.storeStatuses[n] = f.storeStatuses[n][1:]
	case int64(len(body)) != min(f.partSize, f.size-int64(n-1)*f.partSize):
		status = http.StatusBadRequest
	}

	f.attempts = append(f.attempts, fakePutAttempt{
		part: n, status: status, body: body, contentLength: r.ContentLength,
		chunked: len(r.TransferEncoding) > 0, hasAuth: r.Header.Get("Authorization") != "", header: r.Header.Clone(),
	})

	if status == http.StatusOK {
		f.accepted[n] = body
	}

	f.inflight--
	f.mu.Unlock()

	w.WriteHeader(status)
}

func (f *uploadFixture) handleSinglePut(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	f.attempts = append(f.attempts, fakePutAttempt{status: http.StatusOK, body: body, contentLength: r.ContentLength, hasAuth: r.Header.Get("Authorization") != ""})
	f.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

// uploader returns an uploader against the fake API whose backoff does not
// wait but records what it was asked to wait.
func (f *uploadFixture) uploader() *presignedUploader {
	retry := newRetrier(partRetries, partRetryMaxWait)
	retry.jitter = func(d time.Duration) time.Duration { return d }
	retry.sleep = func(_ context.Context, d time.Duration) error {
		f.mu.Lock()
		f.sleeps = append(f.sleeps, d)
		f.mu.Unlock()

		return nil
	}

	return &presignedUploader{
		client:         newClient(f.api.URL, "t"),
		singlePutLimit: maxSinglePutBytes,
		retry:          retry,
		progress: func(done, total int) {
			f.mu.Lock()
			f.progress = append(f.progress, [2]int{done, total})
			f.mu.Unlock()
		},
	}
}

func (f *uploadFixture) attemptsOf(part int) []fakePutAttempt {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []fakePutAttempt

	for _, a := range f.attempts {
		if a.part == part {
			out = append(out, a)
		}
	}

	return out
}

// assertParts checks that the accepted parts are exactly the planned ranges of
// content, sent with a Content-Length, no chunking, the signed headers and
// without the OpenArtifact token.
func (f *uploadFixture) assertParts(t *testing.T, content []byte, partSize int64) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	want := int((int64(len(content)) + partSize - 1) / partSize)
	if len(f.accepted) != want {
		t.Fatalf("accepted %d parts, want %d", len(f.accepted), want)
	}

	for n := 1; n <= want; n++ {
		lo := int64(n-1) * partSize
		hi := min(lo+partSize, int64(len(content)))

		if !bytes.Equal(f.accepted[n], content[lo:hi]) {
			t.Errorf("part %d: got %d bytes that differ from file[%d:%d]", n, len(f.accepted[n]), lo, hi)
		}
	}

	for _, a := range f.attempts {
		switch {
		case a.hasAuth:
			t.Errorf("part %d: the OpenArtifact token was sent to the object store", a.part)
		case a.chunked || a.contentLength != int64(len(a.body)):
			t.Errorf("part %d: Content-Length %d (chunked %v) for %d bytes", a.part, a.contentLength, a.chunked, len(a.body))
		case a.header.Get("X-Test-Part") != strconv.Itoa(a.part):
			t.Errorf("part %d: signed headers not forwarded: %v", a.part, a.header)
		}
	}
}

// counts returns how often the API saw complete and abort.
func (f *uploadFixture) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.completes, f.aborts
}

// writeUploadFile writes size pseudo-random bytes (so a misplaced part cannot
// pass for the right one) and returns the path and content.
func writeUploadFile(t *testing.T, name string, size int) (string, []byte) {
	t.Helper()

	content := make([]byte, size)
	_, _ = rand.NewChaCha8([32]byte{1, 2, 3}).Read(content)

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	return path, content
}

func hexSum(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

func TestMultipartUploadSendsExactParts(t *testing.T) {
	const partSize = 5 << 20

	path, content := writeUploadFile(t, "big.bin", 2*partSize+(1<<20)+13)
	f := newUploadFixture(t)

	res, err := f.uploader().upload(uploadOptions{
		repo: "files", filePath: path, remotePath: "dist/big.bin", contentType: "application/x-test",
		multipart: true, partSize: partSize, parallel: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.remotePath != "dist/big.bin" || res.size != int64(len(content)) || res.sha256 != hexSum(content) {
		t.Errorf("result = %+v", res)
	}

	wantInit := map[string]any{
		"path": "dist/big.bin", "size": float64(len(content)), "sha256": hexSum(content),
		"content_type": "application/x-test", "multipart": true, "part_size": float64(partSize),
	}
	if !reflect.DeepEqual(f.initBody, wantInit) {
		t.Errorf("upload request = %v, want %v", f.initBody, wantInit)
	}

	f.assertParts(t, content, partSize)

	if completes, aborts := f.counts(); completes != 1 || aborts != 0 {
		t.Errorf("complete called %d times, abort %d times; want 1 and 0", completes, aborts)
	}

	// Every part URL is requested once, in requests of at most 100 numbers.
	var requested []int
	for _, req := range f.partRequests {
		requested = append(requested, req...)
	}

	slices.Sort(requested)

	if !slices.Equal(requested, []int{1, 2, 3}) {
		t.Errorf("part numbers requested = %v, want each of 1..3 once", requested)
	}

	if !slices.Equal(progressDone(f.progress), []int{1, 2, 3}) || f.progress[0][1] != 3 {
		t.Errorf("progress = %v, want 1/3, 2/3, 3/3", f.progress)
	}
}

func progressDone(p [][2]int) []int {
	out := make([]int, len(p))
	for i, v := range p {
		out[i] = v[0]
	}

	return out
}

// The CLI end to end: flags, the sha256 pass, the parts and the same one-line
// result the single-PUT upload prints.
func TestMultipartCLIUpload(t *testing.T) {
	const partSize = 5 << 20

	path, content := writeUploadFile(t, "big.bin", 2*partSize+(1<<20))
	f := newUploadFixture(t)

	res := runOA(t, f.api, "", "artifact", "upload", "--presigned", "--multipart", "--part-size", "5M", "--parallel", "2", "files", path)
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	want := fmt.Sprintf("Uploaded 'big.bin' to 'files' as 'big.bin' (%d bytes, sha256 %s).\n", len(content), hexSum(content))
	// Off a terminal nothing but the result is printed (stderr only carries the
	// warning about the --token argument the harness passes).
	if res.stdout != want || strings.Contains(res.stderr, "parts") || strings.Contains(res.stderr, "note:") {
		t.Errorf("stdout %q stderr %q, want stdout %q and no progress or note on stderr", res.stdout, res.stderr, want)
	}

	f.assertParts(t, content, partSize)

	if completes, aborts := f.counts(); completes != 1 || aborts != 0 {
		t.Errorf("complete called %d times, abort %d times; want 1 and 0", completes, aborts)
	}

	if strings.Contains(res.stdout+res.stderr, "secretsig") {
		t.Error("a part URL leaked")
	}
}

// Without --part-size the server chooses the part size; --multipart alone must
// not send one.
func TestMultipartCLIUploadLetsTheServerPickThePartSize(t *testing.T) {
	path, content := writeUploadFile(t, "small.bin", 1<<20)
	f := newUploadFixture(t)

	res := runOA(t, f.api, "", "artifact", "upload", "files", path, "--presigned", "--multipart")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	if _, has := f.initBody["part_size"]; has || f.initBody["multipart"] != true {
		t.Errorf("upload request = %v, want multipart without part_size", f.initBody)
	}

	f.assertParts(t, content, f.defaultPartSize)
}

func TestMultipartRetriesTransientAndExpiredParts(t *testing.T) {
	const partSize = 100

	path, content := writeUploadFile(t, "retry.bin", 3*partSize)
	f := newUploadFixture(t)
	f.storeStatuses[2] = []int{http.StatusInternalServerError, http.StatusServiceUnavailable}
	f.expiredFirst[3] = true

	// One worker keeps the order of the backoff waits deterministic.
	if _, err := f.uploader().upload(uploadOptions{repo: "files", filePath: path, multipart: true, partSize: partSize, parallel: 1}); err != nil {
		t.Fatal(err)
	}

	f.assertParts(t, content, partSize)

	if n := len(f.attemptsOf(2)); n != 3 {
		t.Errorf("part 2 was sent %d times, want 3 (two failures, then success)", n)
	}

	// The two failures back off exponentially; the expired URL does not wait.
	if want := []time.Duration{retryBaseWait, 2 * retryBaseWait}; !slices.Equal(f.sleeps, want) {
		t.Errorf("backoff waits = %v, want %v", f.sleeps, want)
	}

	// Part 3 was sent with its first URL (403), then with a fresh one asked for on its own.
	got := f.attemptsOf(3)
	if len(got) != 2 || got[0].status != http.StatusForbidden || got[1].status != http.StatusOK {
		t.Fatalf("part 3 attempts = %+v, want 403 then 200", got)
	}

	if f.issued[3] != 2 || !slices.ContainsFunc(f.partRequests, func(r []int) bool { return slices.Equal(r, []int{3}) }) {
		t.Errorf("part 3 URLs issued %d times in requests %v, want a second request for [3] alone", f.issued[3], f.partRequests)
	}

	if completes, aborts := f.counts(); completes != 1 || aborts != 0 {
		t.Errorf("complete called %d times, abort %d times; want 1 and 0", completes, aborts)
	}
}

func TestMultipartAbortsWhenAPartFailsForGood(t *testing.T) {
	const partSize = 100

	for _, tc := range []struct {
		name       string
		statuses   []int
		expired    bool
		wantPuts   int
		wantSleeps int
		wantStatus string
	}{
		{name: "server errors exhaust the attempts", statuses: []int{500, 500, 500, 500}, wantPuts: 3, wantSleeps: 2, wantStatus: "HTTP 500"},
		{name: "a client error is final", statuses: []int{400}, wantPuts: 1, wantStatus: "HTTP 400"},
		{name: "a fresh URL that is refused too is final", statuses: []int{403}, expired: true, wantPuts: 2, wantStatus: "HTTP 403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := writeUploadFile(t, "fail.bin", 3*partSize)
			f := newUploadFixture(t)
			f.storeStatuses[2] = tc.statuses
			f.expiredFirst[2] = tc.expired

			_, err := f.uploader().upload(uploadOptions{repo: "files", filePath: path, multipart: true, partSize: partSize, parallel: 1})
			if err == nil {
				t.Fatal("the upload succeeded although part 2 never did")
			}

			if !strings.Contains(err.Error(), "part 2 of 3") || !strings.Contains(err.Error(), tc.wantStatus) || strings.Contains(err.Error(), "secretsig") {
				t.Errorf("error = %q, want it to name part 2 and %s without the URL", err, tc.wantStatus)
			}

			if n := len(f.attemptsOf(2)); n != tc.wantPuts {
				t.Errorf("part 2 was sent %d times, want %d", n, tc.wantPuts)
			}

			if len(f.sleeps) != tc.wantSleeps {
				t.Errorf("backoff waits = %v, want %d", f.sleeps, tc.wantSleeps)
			}

			if n := len(f.attemptsOf(3)); n != 0 {
				t.Errorf("part 3 was sent %d times after part 2 failed, want none", n)
			}

			if completes, aborts := f.counts(); completes != 0 || aborts != 1 {
				t.Errorf("complete called %d times, abort %d times; want 0 and 1", completes, aborts)
			}
		})
	}
}

// A dead object store is retried like any transient failure and, once the CLI
// gives up, the error does not echo the signed URL.
func TestMultipartUnreachableStoreRedactsURLAndAborts(t *testing.T) {
	path, _ := writeUploadFile(t, "dead.bin", 200)
	f := newUploadFixture(t)
	f.store.Close()

	_, err := f.uploader().upload(uploadOptions{repo: "files", filePath: path, multipart: true, partSize: 100, parallel: 2})
	if err == nil || strings.Contains(err.Error(), "secretsig") || strings.Contains(err.Error(), f.store.URL) {
		t.Fatalf("error = %v, want a failure that does not contain the part URL", err)
	}

	if _, aborts := f.counts(); aborts != 1 {
		t.Errorf("abort called %d times, want 1", aborts)
	}
}

func TestMultipartCompleteIncompleteIsAHardError(t *testing.T) {
	path, _ := writeUploadFile(t, "inc.bin", 200)
	f := newUploadFixture(t)
	f.completeReply = &fakeAPIReply{status: http.StatusConflict, body: `{"error":"incomplete","code":"upload_incomplete","message":"part 2 is missing"}`}

	_, err := f.uploader().upload(uploadOptions{repo: "files", filePath: path, multipart: true, partSize: 100, parallel: 1})
	if err == nil || !strings.Contains(err.Error(), "part 2 is missing") {
		t.Fatalf("error = %v, want the server's message", err)
	}

	if completes, aborts := f.counts(); completes != 1 || aborts != 0 {
		t.Errorf("complete called %d times, abort %d times; want 1 and 0", completes, aborts)
	}
}

func TestMultipartRejectsUnusableServerPlans(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*uploadFixture)
		wantErr   error
		wantAbort int
	}{
		{name: "server ignored the multipart request", configure: func(f *uploadFixture) { f.ignoreMultipart = true }, wantErr: errMultipartUnsupported, wantAbort: 1},
		{name: "part count does not cover the file", configure: func(f *uploadFixture) { f.badPartCount = true }, wantErr: errMultipartPlan, wantAbort: 1},
		{name: "part URL claims another size", configure: func(f *uploadFixture) { f.wrongPartSize = true }, wantErr: errPartTarget, wantAbort: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := writeUploadFile(t, "plan.bin", 200)
			f := newUploadFixture(t)
			tc.configure(f)

			_, err := f.uploader().upload(uploadOptions{repo: "files", filePath: path, multipart: true, partSize: 100, parallel: 1})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}

			if len(f.attempts) != 0 {
				t.Errorf("%d PUTs reached the object store, want none", len(f.attempts))
			}

			if completes, aborts := f.counts(); completes != 0 || aborts != tc.wantAbort {
				t.Errorf("complete called %d times, abort %d times; want 0 and %d", completes, aborts, tc.wantAbort)
			}
		})
	}
}

// Parts go out in parallel, but never more than --parallel at once, and part
// URLs are requested in batches rather than one call per part.
func TestMultipartParallelismIsBounded(t *testing.T) {
	const (
		partSize = 100
		parts    = 12
		workers  = 3
	)

	path, content := writeUploadFile(t, "par.bin", parts*partSize)
	f := newUploadFixture(t)
	f.gateSize = workers

	if _, err := f.uploader().upload(uploadOptions{repo: "files", filePath: path, multipart: true, partSize: partSize, parallel: workers}); err != nil {
		t.Fatal(err)
	}

	f.assertParts(t, content, partSize)

	if f.gateTimedOut {
		t.Fatalf("the first %d parts were not in flight together: parts are not uploaded in parallel", workers)
	}

	if f.maxInflight != workers {
		t.Errorf("up to %d PUTs in flight, want exactly %d", f.maxInflight, workers)
	}

	if len(f.partRequests) >= parts {
		t.Errorf("%d part URL requests for %d parts, want batches", len(f.partRequests), parts)
	}
}

// Above the single-PUT limit the upload turns multipart by itself; at or below
// it, nothing changes unless asked for.
func TestUploadSelectsMultipartByThreshold(t *testing.T) {
	for _, tc := range []struct {
		name          string
		size          int
		opts          uploadOptions
		wantMultipart bool
		wantPartSize  bool
	}{
		{name: "below the limit stays a single PUT", size: 50},
		{name: "at the limit stays a single PUT", size: 100},
		{name: "above the limit goes in parts", size: 101, wantMultipart: true},
		{name: "forced at any size", size: 50, opts: uploadOptions{multipart: true}, wantMultipart: true},
		{name: "a part size implies multipart", size: 50, opts: uploadOptions{partSize: 40}, wantMultipart: true, wantPartSize: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, content := writeUploadFile(t, "sel.bin", tc.size)
			f := newUploadFixture(t)
			f.defaultPartSize = 60

			up := f.uploader()
			up.singlePutLimit = 100

			tc.opts.repo, tc.opts.filePath, tc.opts.parallel = "files", path, 2

			if _, err := up.upload(tc.opts); err != nil {
				t.Fatal(err)
			}

			_, hasMultipart := f.initBody["multipart"]
			_, hasPartSize := f.initBody["part_size"]

			if hasMultipart != tc.wantMultipart || hasPartSize != tc.wantPartSize {
				t.Errorf("upload request %v: multipart sent %v (want %v), part_size sent %v (want %v)",
					f.initBody, hasMultipart, tc.wantMultipart, hasPartSize, tc.wantPartSize)
			}

			if !tc.wantMultipart {
				if len(f.attempts) != 1 || f.attempts[0].part != 0 || !bytes.Equal(f.attempts[0].body, content) || len(f.partRequests) != 0 {
					t.Errorf("want one single PUT of the whole file and no part requests, got %d PUTs, %d part requests", len(f.attempts), len(f.partRequests))
				}

				return
			}

			partSize := f.defaultPartSize
			if tc.wantPartSize {
				partSize = tc.opts.partSize
			}

			f.assertParts(t, content, partSize)
		})
	}
}

func TestRouteUpload(t *testing.T) {
	big, _ := writeUploadFile(t, "big.bin", 101)
	small, _ := writeUploadFile(t, "small.bin", 100)

	for _, tc := range []struct {
		name         string
		opts         uploadOptions
		wantSwitched bool
	}{
		{name: "a plain upload above the limit switches", opts: uploadOptions{filePath: big}, wantSwitched: true},
		{name: "a plain upload at the limit stays", opts: uploadOptions{filePath: small}},
		{name: "an explicit presigned upload stays as it is", opts: uploadOptions{filePath: big, presigned: true}},
		{name: "a missing file is left to the upload to report", opts: uploadOptions{filePath: filepath.Join(t.TempDir(), "absent")}},
		{name: "a directory is left to the upload to report", opts: uploadOptions{filePath: t.TempDir()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, switched := routeUpload(tc.opts, 100)

			if switched != tc.wantSwitched || got.presigned != (tc.opts.presigned || tc.wantSwitched) {
				t.Errorf("switched %v, presigned %v; want switched %v", switched, got.presigned, tc.wantSwitched)
			}
		})
	}
}

func TestParsePartSize(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int64
		wantErr string
	}{
		{in: "5M", want: 5 << 20},
		{in: "128M", want: 128 << 20},
		{in: "128MiB", want: 128 << 20},
		{in: "128m", want: 128 << 20},
		{in: "1G", want: 1 << 30},
		{in: "1gib", want: 1 << 30},
		{in: "3G", want: 3 << 30},
		{in: "4000M", want: 4000 << 20},
		{in: "5242880", want: 5 << 20},
		{in: "134217728", want: 128 << 20},
		{in: " 64M ", want: 64 << 20},
		{in: "4M", wantErr: "5 MiB to 4000 MiB"},
		{in: "5242879", wantErr: "5 MiB to 4000 MiB"},
		{in: "4001M", wantErr: "5 MiB to 4000 MiB"},
		{in: "4G", wantErr: "5 MiB to 4000 MiB"},
		{in: "0", wantErr: "5 MiB to 4000 MiB"},
		{in: "99999999999999G", wantErr: "5 MiB to 4000 MiB"},
		{in: "", wantErr: "whole number"},
		{in: "M", wantErr: "whole number"},
		{in: "-5M", wantErr: "whole number"},
		{in: "1.5G", wantErr: "unknown unit"},
		{in: "128 M", wantErr: "unknown unit"},
		{in: "12X", wantErr: "unknown unit"},
		{in: "128K", wantErr: "unknown unit"},
		{in: "99999999999999999999", wantErr: "whole number"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parsePartSize(tc.in)

			if tc.wantErr != "" {
				if !errors.Is(err, errPartSize) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parsePartSize(%q) = %d, %v; want an error containing %q", tc.in, got, err, tc.wantErr)
				}

				return
			}

			if err != nil || got != tc.want {
				t.Fatalf("parsePartSize(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestParseUploadArgs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		want    uploadOptions
		wantErr string
	}{
		{
			name: "defaults",
			args: []string{"files", "f.bin"},
			want: uploadOptions{repo: "files", filePath: "f.bin", parallel: 4},
		},
		{
			name: "every presigned flag, before and after the positionals",
			args: []string{"--presigned", "--path", "p/f.bin", "files", "--content-type", "text/x", "f.bin", "--multipart", "--part-size", "128M", "--parallel", "8"},
			want: uploadOptions{
				repo: "files", filePath: "f.bin", presigned: true, remotePath: "p/f.bin", contentType: "text/x",
				multipart: true, partSize: 128 << 20, parallel: 8,
			},
		},
		{
			name: "a part size alone is accepted",
			args: []string{"--presigned", "--part-size", "1G", "files", "f.bin"},
			want: uploadOptions{repo: "files", filePath: "f.bin", presigned: true, partSize: 1 << 30, parallel: 4},
		},
		{name: "multipart needs presigned", args: []string{"--multipart", "files", "f.bin"}, wantErr: "require --presigned"},
		{name: "part size needs presigned", args: []string{"--part-size", "5M", "files", "f.bin"}, wantErr: "require --presigned"},
		{name: "parallel needs presigned", args: []string{"--parallel", "2", "files", "f.bin"}, wantErr: "require --presigned"},
		{name: "path needs presigned", args: []string{"--path", "p", "files", "f.bin"}, wantErr: "require --presigned"},
		{name: "parallel below 1", args: []string{"--presigned", "--parallel", "0", "files", "f.bin"}, wantErr: "--parallel must be between 1 and 16"},
		{name: "parallel above 16", args: []string{"--presigned", "--parallel", "17", "files", "f.bin"}, wantErr: "--parallel must be between 1 and 16"},
		{name: "part size out of range", args: []string{"--presigned", "--part-size", "4M", "files", "f.bin"}, wantErr: "5 MiB to 4000 MiB"},
		{name: "part size malformed", args: []string{"--presigned", "--part-size", "big", "files", "f.bin"}, wantErr: "invalid part size"},
		{name: "missing file", args: []string{"--presigned", "files"}, wantErr: "Usage: oa artifact upload"},
		{name: "extra argument", args: []string{"files", "f.bin", "g.bin"}, wantErr: "Usage: oa artifact upload"},
		{name: "unknown flag", args: []string{"--chunked", "files", "f.bin"}, wantErr: "flag provided but not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUploadArgs(tc.args, flag.ContinueOnError, io.Discard)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}

				return
			}

			if err != nil || got != tc.want {
				t.Fatalf("parseUploadArgs = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestCheckPartPlan(t *testing.T) {
	const partSize = 5 << 20

	for _, tc := range []struct {
		name     string
		size     int64
		partSize int64
		wantErr  bool
	}{
		{name: "the most parts allowed", size: maxPartCount * partSize, partSize: partSize},
		{name: "one byte too many", size: maxPartCount*partSize + 1, partSize: partSize, wantErr: true},
		{name: "the server picks the part size", size: 6 << 40, partSize: 0},
		{name: "an empty file", size: 0, partSize: partSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPartPlan(tc.size, tc.partSize)
			if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, errTooManyParts)) {
				t.Errorf("checkPartPlan(%d, %d) = %v, wantErr %v", tc.size, tc.partSize, err, tc.wantErr)
			}
		})
	}
}

func TestPartLength(t *testing.T) {
	// 11 MiB in 5 MiB parts: 5, 5, 1; outside the plan there is nothing.
	const mib = 1 << 20

	for number, want := range map[int]int64{0: 0, 1: 5 * mib, 2: 5 * mib, 3: mib, 4: 0} {
		if got := partLength(11*mib, 5*mib, number); got != want {
			t.Errorf("partLength(part %d) = %d, want %d", number, got, want)
		}
	}

	if partCount(0, 5*mib) != 1 || partLength(0, 5*mib, 1) != 0 {
		t.Error("an empty file must be one empty part")
	}
}

// The multipart calls are matched against the routes the server really
// registers, so a wrong path or method in the CLI fails here.
func TestMultipartCLIHitsServedRoutes(t *testing.T) {
	const uploads = "/api/v1/repositories/{name}/uploads"

	file := filepath.Join(t.TempDir(), "tiny.bin")
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, storeSrv := newObjectStore(t)

	cs, srv := newContractServer(t, map[string]reply{
		"POST " + uploads: {status: http.StatusCreated, body: `{"id":"u-1","multipart":true,"part_size":5242880,"part_count":1,"expires_at":"2030-01-01T00:00:00Z"}`},
		"POST " + uploads + "/{id}/parts": {status: http.StatusOK, body: `{"parts":[{"part_number":1,"upload_url":"` + storeSrv.URL +
			`/parts/1?X-Amz-Signature=secretsig","method":"PUT","headers":{"X-Test":"1"},"size":7}],"expires_at":"2030-01-01T00:00:00Z"}`},
		"POST " + uploads + "/{id}/complete": {status: http.StatusCreated, body: `{"id":"a-1","path":"tiny.bin","size":7,"sha256":"abc"}`},
	})

	res := runOA(t, srv, "", "artifact", "upload", "--presigned", "--multipart", "files", file)
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	if len(cs.bad) != 0 {
		t.Fatalf("CLI hit routes the server does not serve: %v", cs.bad)
	}

	var got []string
	for _, r := range cs.reqs {
		got = append(got, r.method+" "+r.pattern)
	}

	if want := []string{"POST " + uploads, "POST " + uploads + "/{id}/parts", "POST " + uploads + "/{id}/complete"}; !slices.Equal(got, want) {
		t.Fatalf("API calls = %v, want %v", got, want)
	}

	if body := string(cs.reqs[1].body); body != `{"part_numbers":[1]}` {
		t.Errorf("parts request = %s", body)
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if store.puts != 1 || string(store.body) != "payload" || store.hasAuth || store.header.Get("X-Test") != "1" {
		t.Errorf("object store got %d PUTs with body %q (auth %v, headers %v)", store.puts, store.body, store.hasAuth, store.header)
	}
}

// A part the object store refuses for good makes the CLI abort the upload on
// the server, over a route the server serves, and never complete it.
func TestMultipartCLIAbortsOverServedRoute(t *testing.T) {
	const uploads = "/api/v1/repositories/{name}/uploads"

	file := filepath.Join(t.TempDir(), "tiny.bin")
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	t.Cleanup(refusing.Close)

	cs, srv := newContractServer(t, map[string]reply{
		"POST " + uploads: {status: http.StatusCreated, body: `{"id":"u-1","multipart":true,"part_size":5242880,"part_count":1,"expires_at":"2030-01-01T00:00:00Z"}`},
		"POST " + uploads + "/{id}/parts": {status: http.StatusOK, body: `{"parts":[{"part_number":1,"upload_url":"` + refusing.URL +
			`/parts/1?X-Amz-Signature=secretsig","method":"PUT","headers":{},"size":7}],"expires_at":"2030-01-01T00:00:00Z"}`},
		"POST " + uploads + "/{id}/abort": {status: http.StatusNoContent},
	})

	res := runOA(t, srv, "", "artifact", "upload", "--presigned", "--multipart", "files", file)
	if res.code == 0 || !strings.Contains(res.stderr, "part 1 of 1") || strings.Contains(res.stderr, "secretsig") {
		t.Fatalf("exit %d, stderr %q; want a failure naming the part without the URL", res.code, res.stderr)
	}

	if len(cs.bad) != 0 {
		t.Fatalf("CLI hit routes the server does not serve: %v", cs.bad)
	}

	var got []string
	for _, r := range cs.reqs {
		got = append(got, r.method+" "+r.pattern)
	}

	if want := []string{"POST " + uploads, "POST " + uploads + "/{id}/parts", "POST " + uploads + "/{id}/abort"}; !slices.Equal(got, want) {
		t.Errorf("API calls = %v, want %v", got, want)
	}
}
