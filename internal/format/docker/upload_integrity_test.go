// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

// hookStore runs hook just before the wrapped store reads the bytes it is
// asked to Put; swap, when set, replaces those bytes.
type hookStore struct {
	storage.BlobStore

	hook func()
	swap []byte
}

func (s *hookStore) Put(ctx context.Context, data io.Reader, md map[string]string) (string, error) {
	if s.hook != nil {
		s.hook()
	}

	if s.swap != nil {
		data = strings.NewReader(string(s.swap))
	}

	return s.BlobStore.Put(ctx, data, md)
}

func startUpload(t *testing.T, g *registry, name string) string {
	t.Helper()

	start := g.do(http.MethodPost, "/v2/"+name+"/blobs/uploads/", "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("upload start = %d", start.Code)
	}

	return start.Header().Get("Location")
}

// hookUploads runs beforePromote just before the upload store makes the
// completed upload the blob.
type hookUploads struct {
	storage.UploadStore

	beforePromote func()
}

func (s *hookUploads) PromoteUpload(ctx context.Context, id, sha256, version string, metadata map[string]string) error {
	if s.beforePromote != nil {
		s.beforePromote()
	}

	return s.UploadStore.PromoteUpload(ctx, id, sha256, version, metadata)
}

func (l *uploadLocks) maxRefs() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	most := 0
	for _, e := range l.m {
		most = max(most, e.refs)
	}

	return most
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached before the deadline")
		}

		time.Sleep(time.Millisecond)
	}
}

// A PATCH racing the completion of the same session must wait for it (and
// then find the session gone): the stored bytes are the bytes that were hashed.
func TestCompleteUpload_ConcurrentPatchCannotChangeStoredBytes(t *testing.T) {
	layer := []byte("the real layer content")
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	hook := &hookUploads{UploadStore: g.h.uploads}
	g.h.SetUploadStore(hook)

	loc := startUpload(t, g, "reg/app")

	var (
		patchDone atomic.Bool
		patchCode atomic.Int32
		patched   = make(chan struct{})
	)

	hook.beforePromote = func() {
		go func() {
			defer close(patched)

			rec := g.do(http.MethodPatch, loc, "ATTACKER BYTES")
			patchCode.Store(int32(rec.Code))
			patchDone.Store(true)
		}()

		// refs == 2 means the PATCH is queued behind the completion's lock.
		waitFor(t, func() bool { return g.h.sessions.maxRefs() >= 2 })

		if patchDone.Load() {
			t.Error("PATCH ran while the upload was being completed")
		}
	}

	if rec := g.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer)); rec.Code != http.StatusCreated {
		t.Fatalf("complete = %d; body=%s", rec.Code, rec.Body.String())
	}

	<-patched

	if got := patchCode.Load(); got != http.StatusNotFound {
		t.Errorf("PATCH after completion = %d, want 404 (session gone)", got)
	}

	rec := g.do(http.MethodGet, "/v2/reg/app/blobs/"+digestOf(layer), "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(layer) {
		t.Fatalf("stored blob = %d %q, want the pushed layer", rec.Code, rec.Body.String())
	}
}

// A PATCH that reaches the session through another replica while a completion
// holds it is refused, not interleaved with it.
func TestCompleteUpload_PatchOnAnotherReplicaIsRefusedWhileCompleting(t *testing.T) {
	layer := []byte("the real layer content")
	db := newRegFakeDB("reg")
	a := newRegistry(t, db)
	b := otherReplica(t, a, db)

	hook := &hookUploads{UploadStore: a.h.uploads}
	a.h.SetUploadStore(hook)

	loc := startUpload(t, a, "reg/app")

	var patchCode int

	hook.beforePromote = func() {
		patchCode = b.do(http.MethodPatch, loc, "ATTACKER BYTES").Code
	}

	if rec := a.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer)); rec.Code != http.StatusCreated {
		t.Fatalf("complete = %d; body=%s", rec.Code, rec.Body.String())
	}

	if patchCode != http.StatusConflict {
		t.Errorf("PATCH on another replica during the completion = %d, want 409", patchCode)
	}

	rec := b.do(http.MethodGet, "/v2/reg/app/blobs/"+digestOf(layer), "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(layer) {
		t.Fatalf("stored blob = %d %q, want the pushed layer", rec.Code, rec.Body.String())
	}
}

// If the upload object is overwritten after its digest was verified and before
// it becomes the blob, completion fails and no object exists under the claimed
// digest.
func TestCompleteUpload_ObjectSwappedAfterVerificationIsRejectedAndNotStored(t *testing.T) {
	layer := []byte("the real layer content")
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	hook := &hookUploads{UploadStore: g.h.uploads}
	g.h.SetUploadStore(hook)

	loc := startUpload(t, g, "reg/app")
	id := uploadID(loc)

	hook.beforePromote = func() {
		err := hook.AppendUpload(context.Background(), id, storage.UploadPosition{}, strings.NewReader("attacker bytes!"),
			func(context.Context, string, int64) error { return nil })
		if err != nil {
			t.Errorf("overwrite the upload object: %v", err)
		}
	}

	rec := g.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "DIGEST_INVALID") {
		t.Fatalf("complete = %d %s, want 400 DIGEST_INVALID", rec.Code, rec.Body.String())
	}

	hexDigest := strings.TrimPrefix(digestOf(layer), "sha256:")

	if ok, _ := g.h.blobStore.Exists(context.Background(), hexDigest); ok {
		t.Fatal("an object was stored under the claimed digest")
	}

	if db.blobs[scoped("id-reg", digestOf(layer))] != nil {
		t.Fatal("a blob row was created")
	}
}

// A blob store that cannot hold uploads (oci) keeps them in a host directory
// and the completed upload is streamed into Put: the push still completes, and
// if the bytes Put receives differ from the verified ones nothing is stored
// under the claimed digest.
func TestCompleteUpload_StreamsIntoPutWhenTheStoreCannotPromote(t *testing.T) {
	layer := []byte("the real layer content")

	t.Run("completes", func(t *testing.T) {
		db := newRegFakeDB("reg")
		g := newRegistry(t, db)
		g.h.SetUploadStore(storage.NewDirUploadStore(t.TempDir()))

		loc := startUpload(t, g, "reg/app")
		if rec := g.do(http.MethodPatch, loc, string(layer[:5])); rec.Code != http.StatusAccepted {
			t.Fatalf("patch = %d", rec.Code)
		}

		if rec := g.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer[5:])); rec.Code != http.StatusCreated {
			t.Fatalf("complete = %d; body=%s", rec.Code, rec.Body.String())
		}

		rec := g.do(http.MethodGet, "/v2/reg/app/blobs/"+digestOf(layer), "")
		if rec.Code != http.StatusOK || rec.Body.String() != string(layer) {
			t.Fatalf("pulled blob = %d %q, want the pushed layer", rec.Code, rec.Body.String())
		}

		if n := len(uploadObjects(t, g.h)); n != 0 {
			t.Fatalf("upload files left = %d, want 0", n)
		}
	})

	t.Run("swapped bytes", func(t *testing.T) {
		db := newRegFakeDB("reg")
		g := newRegistry(t, db)
		g.h.SetUploadStore(storage.NewDirUploadStore(t.TempDir()))

		store := &hookStore{BlobStore: g.h.blobStore, swap: []byte("attacker bytes")}
		g.h.blobStore = store

		loc := startUpload(t, g, "reg/app")

		rec := g.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("complete = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}

		hexDigest := strings.TrimPrefix(digestOf(layer), "sha256:")

		if ok, _ := store.Exists(context.Background(), hexDigest); ok {
			t.Fatal("an object was stored under the claimed digest")
		}

		if db.blobs[scoped("id-reg", digestOf(layer))] != nil {
			t.Fatal("a blob row was created")
		}
	})
}
