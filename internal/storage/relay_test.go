// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

const relayTestID = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"

// relayEnv is a relay-capable store mounted behind a chi router, like main.go does.
type relayEnv struct {
	store   StagingStore
	multi   MultipartStagingStore
	srv     *httptest.Server
	clock   *time.Time
	staging string
}

func newRelayRouter(h http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Put(RelayRoute, h.ServeHTTP)

	return r
}

func newFSRelayEnv(t *testing.T) (*relayEnv, *FilesystemStore) {
	t.Helper()

	base := t.TempDir()

	// The public URL is only known once the server runs: start it first.
	var handler http.Handler

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)

	fsys, err := NewFilesystemBlobStore(StorageConfig{Type: "filesystem", Path: base, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	handler = newRelayRouter(fsys.RelayHandler())

	now := time.Now()
	fsys.now = func() time.Time { return now }

	return &relayEnv{store: fsys, multi: fsys, srv: srv, clock: &now, staging: filepath.Join(base, "staging")}, fsys
}

type relayResult struct {
	status int
	etag   string
}

func relayPut(t *testing.T, rawURL string, body []byte) relayResult {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	return relayResult{status: resp.StatusCode, etag: resp.Header.Get("ETag")}
}

func mustStatus(t *testing.T, resp relayResult, want int) {
	t.Helper()

	if resp.status != want {
		t.Fatalf("status = %d, want %d", resp.status, want)
	}
}

func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

func TestRelayStagingSupportedNeedsPublicURL(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemBlobStore(StorageConfig{Type: "filesystem", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	if fsys.StagingSupported() || fsys.MultipartSupported() {
		t.Fatal("staging must be unsupported without PublicURL")
	}

	if _, err := fsys.PresignStagingPut(context.Background(), relayTestID, 1, time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Fatalf("err = %v, want ErrPresignNotSupported", err)
	}
}

func TestRelaySinglePutAndPromote(t *testing.T) {
	t.Parallel()

	env, fsys := newFSRelayEnv(t)
	ctx := context.Background()
	data := []byte("hello relay world")

	put, err := env.store.PresignStagingPut(ctx, relayTestID, int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(put.URL, env.srv.URL+"/upload-relay/"+relayTestID+"/1?exp=") {
		t.Fatalf("url = %q", put.URL)
	}

	if _, err := env.store.StatStaging(ctx, relayTestID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("stat before upload err = %v", err)
	}

	resp := relayPut(t, put.URL, data)
	mustStatus(t, resp, http.StatusOK)

	if resp.etag == "" {
		t.Error("missing ETag")
	}

	obj, err := env.store.StatStaging(ctx, relayTestID)
	if err != nil || obj.Size != int64(len(data)) {
		t.Fatalf("stat = %+v, %v", obj, err)
	}

	rc, err := env.store.OpenStaging(ctx, relayTestID, obj.Version)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := io.ReadAll(rc)
	_ = rc.Close()

	if !bytes.Equal(got, data) {
		t.Fatalf("staged bytes = %q", got)
	}

	if _, err := env.store.OpenStaging(ctx, relayTestID, "bogus"); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("open wrong version err = %v", err)
	}

	checkRelayPromote(t, env, fsys, data, obj)
}

func checkRelayPromote(t *testing.T, env *relayEnv, fsys *FilesystemStore, data []byte, obj StagedObject) {
	t.Helper()

	ctx := context.Background()
	sha := sha256Of(data)

	if err := env.store.PromoteStaging(ctx, relayTestID, sha, "bogus", nil); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("promote wrong version err = %v", err)
	}

	if err := env.store.PromoteStaging(ctx, relayTestID, sha, obj.Version, map[string]string{"content-type": "text/x"}); err != nil {
		t.Fatal(err)
	}

	blob, err := fsys.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := io.ReadAll(blob)
	_ = blob.Close()

	if !bytes.Equal(got, data) {
		t.Fatalf("blob bytes = %q", got)
	}

	meta, err := fsys.GetMetadata(ctx, sha)
	if err != nil || meta["_sha256"] != sha || meta["_size"] != "17" || meta["content-type"] != "text/x" {
		t.Fatalf("metadata = %v, %v", meta, err)
	}

	// Promote is a no-op once the blob exists, even after the staging object is gone.
	if err := env.store.DeleteStaging(ctx, relayTestID); err != nil {
		t.Fatal(err)
	}

	if err := env.store.PromoteStaging(ctx, relayTestID, sha, obj.Version, nil); err != nil {
		t.Fatalf("repeat promote: %v", err)
	}

	// The blob survives deleting the staging object, and List ignores staging.
	ids, _, err := fsys.List(ctx, "", 10)
	if err != nil || len(ids) != 1 || ids[0] != sha {
		t.Fatalf("List = %v, %v", ids, err)
	}
}

func TestRelayRejectsTamperExpiryAndLength(t *testing.T) {
	t.Parallel()

	env, _ := newFSRelayEnv(t)
	ctx := context.Background()
	data := []byte("0123456789")

	put, err := env.store.PresignStagingPut(ctx, relayTestID, int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(put.URL)

	tampered := *u
	q := tampered.Query()
	q.Set("exp", "9999999999")
	tampered.RawQuery = q.Encode()

	mustStatus(t, relayPut(t, tampered.String(), data), http.StatusForbidden)

	badSig := *u
	q = badSig.Query()
	q.Set("sig", strings.Repeat("0", 64))
	badSig.RawQuery = q.Encode()

	mustStatus(t, relayPut(t, badSig.String(), data), http.StatusForbidden)

	noSig := *u
	noSig.RawQuery = ""

	mustStatus(t, relayPut(t, noSig.String(), data), http.StatusForbidden)

	// Another upload's id with this signature is unknown.
	other := strings.Replace(put.URL, relayTestID, "11111111-1111-4111-8111-111111111111", 1)
	mustStatus(t, relayPut(t, other, data), http.StatusNotFound)

	// Wrong body length is rejected without creating the object.
	mustStatus(t, relayPut(t, put.URL, data[:5]), http.StatusBadRequest)
	mustStatus(t, relayPut(t, put.URL, append(data, 'x')), http.StatusBadRequest)

	if _, err := env.store.StatStaging(ctx, relayTestID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("object must not exist, err = %v", err)
	}

	// Chunked bodies (unknown length) get 411.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, put.URL, io.NopCloser(bytes.NewReader(data)))
	req.ContentLength = -1

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	mustStatus(t, relayResult{status: resp.StatusCode}, http.StatusLengthRequired)

	// Expiry follows the injected clock.
	*env.clock = env.clock.Add(2 * time.Minute)

	mustStatus(t, relayPut(t, put.URL, data), http.StatusGone)
}

func TestRelayOverwriteRefused(t *testing.T) {
	t.Parallel()

	env, _ := newFSRelayEnv(t)
	ctx := context.Background()

	put, err := env.store.PresignStagingPut(ctx, relayTestID, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	mustStatus(t, relayPut(t, put.URL, []byte("abc")), http.StatusOK)

	before, _ := env.store.StatStaging(ctx, relayTestID)

	mustStatus(t, relayPut(t, put.URL, []byte("xyz")), http.StatusConflict)

	after, _ := env.store.StatStaging(ctx, relayTestID)
	if before != after {
		t.Fatalf("object changed: %+v -> %+v", before, after)
	}
}

func TestRelayMultipartOutOfOrder(t *testing.T) {
	t.Parallel()

	env, fsys := newFSRelayEnv(t)
	ctx := context.Background()

	const partSize = MinPartSize

	data := bytes.Repeat([]byte("abcdefghij"), int(2*partSize+123)/10+1)[:2*partSize+123]
	size := int64(len(data))

	handle, err := env.multi.InitiateMultipart(ctx, relayTestID, size, partSize)
	if err != nil || handle != "" {
		t.Fatalf("initiate = %q, %v", handle, err)
	}

	count := PartCount(size, partSize)
	if count != 3 {
		t.Fatalf("count = %d", count)
	}

	urls := make([]string, count+1)

	for n := 1; n <= count; n++ {
		p, err := env.multi.PresignStagingPart(ctx, relayTestID, handle, n, PartLength(size, partSize, n), time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		urls[n] = p.URL
	}

	// A wrong planned length is refused at presign time.
	if _, err := env.multi.PresignStagingPart(ctx, relayTestID, handle, 1, partSize-1, time.Minute); err == nil {
		t.Fatal("expected error for wrong part length")
	}

	part := func(n int) []byte {
		start := int64(n-1) * partSize

		return data[start : start+PartLength(size, partSize, n)]
	}

	mustStatus(t, relayPut(t, urls[3], part(3)), http.StatusOK)
	mustStatus(t, relayPut(t, urls[1], part(1)), http.StatusOK)

	err = env.multi.CompleteMultipart(ctx, relayTestID, handle, size, partSize)
	if !errors.Is(err, ErrMultipartIncomplete) || !strings.Contains(err.Error(), "[2]") {
		t.Fatalf("complete err = %v, want incomplete missing [2]", err)
	}

	if _, err := env.store.StatStaging(ctx, relayTestID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("object exists before last part: %v", err)
	}

	// A part is rewritable until the object is finalized.
	mustStatus(t, relayPut(t, urls[1], part(1)), http.StatusOK)
	mustStatus(t, relayPut(t, urls[2], part(2)), http.StatusOK)

	for range 2 { // idempotent
		if err := env.multi.CompleteMultipart(ctx, relayTestID, handle, size, partSize); err != nil {
			t.Fatal(err)
		}
	}

	obj, err := env.store.StatStaging(ctx, relayTestID)
	if err != nil || obj.Size != size {
		t.Fatalf("stat = %+v, %v", obj, err)
	}

	// After finalization further part PUTs are refused.
	mustStatus(t, relayPut(t, urls[2], part(2)), http.StatusConflict)

	sha := sha256Of(data)
	if err := env.store.PromoteStaging(ctx, relayTestID, sha, obj.Version, nil); err != nil {
		t.Fatal(err)
	}

	blob, err := fsys.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := io.ReadAll(blob)
	_ = blob.Close()

	if !bytes.Equal(got, data) {
		t.Fatal("assembled bytes differ")
	}
}

func TestRelayConcurrentFinalizeIsIdempotent(t *testing.T) {
	t.Parallel()

	env, _ := newFSRelayEnv(t)
	ctx := context.Background()

	const partSize = MinPartSize

	size := partSize + 7
	data := bytes.Repeat([]byte{'z'}, int(size))

	if _, err := env.multi.InitiateMultipart(ctx, relayTestID, size, partSize); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	statuses := make([]int, 2)

	for n := 1; n <= 2; n++ {
		p, err := env.multi.PresignStagingPart(ctx, relayTestID, "", n, PartLength(size, partSize, n), time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		wg.Go(func() {
			start := int64(n-1) * partSize
			statuses[n-1] = relayPut(t, p.URL, data[start:start+PartLength(size, partSize, n)]).status
		})
	}

	wg.Wait()

	for i, s := range statuses {
		if s != http.StatusOK {
			t.Fatalf("part %d status = %d", i+1, s)
		}
	}

	if err := env.multi.CompleteMultipart(ctx, relayTestID, "", size, partSize); err != nil {
		t.Fatal(err)
	}

	if obj, err := env.store.StatStaging(ctx, relayTestID); err != nil || obj.Size != size {
		t.Fatalf("stat = %+v, %v", obj, err)
	}
}

func TestRelayDeleteAndWalk(t *testing.T) {
	t.Parallel()

	env, fsys := newFSRelayEnv(t)
	ctx := context.Background()

	done := "22222222-2222-4222-8222-222222222222"
	partial := "33333333-3333-4333-8333-333333333333"

	p, err := env.store.PresignStagingPut(ctx, done, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	mustStatus(t, relayPut(t, p.URL, []byte("ok")), http.StatusOK)

	if _, err := env.multi.InitiateMultipart(ctx, partial, 2*MinPartSize, MinPartSize); err != nil {
		t.Fatal(err)
	}

	pp, err := env.multi.PresignStagingPart(ctx, partial, "", 1, MinPartSize, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	mustStatus(t, relayPut(t, pp.URL, make([]byte, MinPartSize)), http.StatusOK)

	// Foreign files are never reported.
	if err := os.WriteFile(filepath.Join(env.staging, "not-a-uuid"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	collect := func(cutoff time.Time) map[string]bool {
		got := map[string]bool{}

		err := env.store.WalkStaging(ctx, cutoff, func(ids []string) error {
			for _, id := range ids {
				got[id] = true
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	if got := collect(time.Now().Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("fresh uploads reported: %v", got)
	}

	got := collect(time.Now().Add(time.Hour))
	if len(got) != 2 || !got[done] || !got[partial] {
		t.Fatalf("walk = %v, want %s and %s", got, done, partial)
	}

	for _, id := range []string{done, partial} {
		if err := env.store.DeleteStaging(ctx, id); err != nil {
			t.Fatal(err)
		}

		if err := env.store.DeleteStaging(ctx, id); err != nil {
			t.Fatalf("second delete: %v", err)
		}
	}

	if got := collect(time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("leftovers after delete: %v", got)
	}

	// The signing key and foreign file are untouched; blobs are not listed.
	if _, err := os.Stat(filepath.Join(env.staging, relaySigningFile)); err != nil {
		t.Fatal(err)
	}

	if ids, _, err := fsys.List(ctx, "", 10); err != nil || len(ids) != 0 {
		t.Fatalf("List = %v, %v", ids, err)
	}
}

func TestRelaySigningKeyIsShared(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a := newRelay(dir, "http://x")
	b := newRelay(dir, "http://x")

	if a == nil || b == nil || !bytes.Equal(a.key, b.key) {
		t.Fatal("relays on one directory must share the signing key")
	}

	info, err := os.Stat(filepath.Join(dir, relaySigningFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
}

// fakeOCIRegistry is a minimal in-process registry: HEAD/GET blobs and the
// monolithic upload flow.
func fakeOCIRegistry(t *testing.T) (*httptest.Server, func(string) ([]byte, bool)) {
	t.Helper()

	var (
		mu    sync.Mutex
		blobs = map[string][]byte{}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			w.Header().Set("Location", r.URL.Path+"session")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/blobs/uploads/session"):
			body, _ := io.ReadAll(r.Body)
			blobs[strings.TrimPrefix(r.URL.Query().Get("digest"), "sha256:")] = body
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(r.URL.Path, "/blobs/sha256:"):
			body, ok := blobs[r.URL.Path[strings.LastIndex(r.URL.Path, ":")+1:]]
			if !ok {
				w.WriteHeader(http.StatusNotFound)

				return
			}

			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.WriteHeader(http.StatusOK)

				return
			}

			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, func(sha string) ([]byte, bool) {
		mu.Lock()
		defer mu.Unlock()

		b, ok := blobs[sha]

		return b, ok
	}
}

func TestRelayOCIPromote(t *testing.T) {
	t.Parallel()

	reg, lookup := fakeOCIRegistry(t)

	var handler http.Handler

	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(pub.Close)

	store, err := NewOCIBlobStore(StorageConfig{
		Type: "oci", Endpoint: reg.URL, Bucket: "repo", OCIInsecure: true,
		StagingPath: filepath.Join(t.TempDir(), "spool"), PublicURL: pub.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	handler = newRelayRouter(store.RelayHandler())

	if !store.StagingSupported() || !store.MultipartSupported() {
		t.Fatal("oci staging must be supported")
	}

	ctx := context.Background()
	data := []byte("oci relay payload")

	put, err := store.PresignStagingPut(ctx, relayTestID, int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	mustStatus(t, relayPut(t, put.URL, data), http.StatusOK)

	obj, err := store.StatStaging(ctx, relayTestID)
	if err != nil {
		t.Fatal(err)
	}

	sha := sha256Of(data)

	if err := store.PromoteStaging(ctx, relayTestID, sha, "bogus", nil); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("promote wrong version err = %v", err)
	}

	// A digest that is not the staged bytes is refused by Put.
	if err := store.PromoteStaging(ctx, relayTestID, sha256Of([]byte("other")), obj.Version, nil); err == nil {
		t.Fatal("expected digest mismatch")
	}

	if err := store.PromoteStaging(ctx, relayTestID, sha, obj.Version, nil); err != nil {
		t.Fatal(err)
	}

	if got, ok := lookup(sha); !ok || !bytes.Equal(got, data) {
		t.Fatalf("registry blob = %q, %v", got, ok)
	}
}
