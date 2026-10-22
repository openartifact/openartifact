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

// A PATCH racing the completion of the same session must wait for it (and
// then find the session gone): the stored bytes are the bytes that were hashed.
func TestCompleteUpload_ConcurrentPatchCannotChangeStoredBytes(t *testing.T) {
	layer := []byte("the real layer content")
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	store := &hookStore{BlobStore: g.h.blobStore}
	g.h.blobStore = store

	loc := startUpload(t, g, "reg/app")

	var (
		patchDone atomic.Bool
		patchCode atomic.Int32
		patched   = make(chan struct{})
	)

	store.hook = func() {
		go func() {
			defer close(patched)

			rec := g.do(http.MethodPatch, loc, "ATTACKER BYTES")
			patchCode.Store(int32(rec.Code))
			patchDone.Store(true)
		}()

		time.Sleep(150 * time.Millisecond)

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

// If the bytes the store receives differ from the verified ones, completion
// fails and no object exists under the claimed digest.
func TestCompleteUpload_SwappedBytesAreRejectedAndNotStored(t *testing.T) {
	layer := []byte("the real layer content")
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

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
}
