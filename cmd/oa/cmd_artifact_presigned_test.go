// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// objectStore stands in for S3: it records what the CLI PUT to the presigned URL.
type objectStore struct {
	mu      sync.Mutex
	body    []byte
	header  http.Header
	hasAuth bool
	puts    int
}

func newObjectStore(t *testing.T) (*objectStore, *httptest.Server) {
	t.Helper()

	st := &objectStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)

		st.mu.Lock()
		st.body, st.header, st.hasAuth, st.puts = b, r.Header.Clone(), r.Header.Get("Authorization") != "", st.puts+1
		st.mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return st, srv
}

func TestArtifactUploadPresigned(t *testing.T) {
	content := []byte("presigned payload")
	sum := sha256.Sum256(content)
	file := filepath.Join(t.TempDir(), "app.bin")

	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}

	store, storeSrv := newObjectStore(t)

	replies := map[string]reply{
		"POST /api/v1/repositories/{name}/uploads": {status: http.StatusCreated, body: `{"id":"u-1","method":"PUT","upload_url":"` +
			storeSrv.URL + `/staging/u-1?X-Amz-Signature=secretsig","headers":{"X-Amz-Meta-Foo":"bar"}}`},
		"POST /api/v1/repositories/{name}/uploads/{id}/complete": {status: http.StatusCreated, body: `{"id":"a-1","path":"dist/app.bin","size":17,"sha256":"` + hex.EncodeToString(sum[:]) + `"}`},
	}

	cs, srv := newContractServer(t, replies)

	// Flags after the positional arguments must work too.
	res := runOA(t, srv, "", "artifact", "upload", "files", file, "--presigned", "--path", "dist/app.bin", "--content-type", "application/x-test")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	if len(cs.bad) != 0 {
		t.Fatalf("CLI hit unknown routes: %v", cs.bad)
	}

	if len(cs.reqs) != 2 {
		t.Fatalf("API calls = %d, want 2 (init, complete)", len(cs.reqs))
	}

	var init struct {
		Path        string `json:"path"`
		Size        int64  `json:"size"`
		SHA256      string `json:"sha256"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal(cs.reqs[0].body, &init); err != nil {
		t.Fatal(err)
	}

	if init.Path != "dist/app.bin" || init.Size != int64(len(content)) || init.SHA256 != hex.EncodeToString(sum[:]) || init.ContentType != "application/x-test" {
		t.Errorf("init body = %+v", init)
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if store.puts != 1 || string(store.body) != string(content) {
		t.Errorf("object store got %d PUTs with body %q", store.puts, store.body)
	}

	if store.hasAuth {
		t.Error("the OpenArtifact token was sent to the object store")
	}

	if store.header.Get("X-Amz-Meta-Foo") != "bar" {
		t.Errorf("signed headers not forwarded: %v", store.header)
	}

	if !strings.Contains(res.stdout, "dist/app.bin") || strings.Contains(res.stdout+res.stderr, "secretsig") {
		t.Errorf("stdout %q stderr %q", res.stdout, res.stderr)
	}
}

func TestArtifactUploadPresignedDefaultsPathAndRedactsURLErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "named.bin")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// An unreachable object store: the error must not echo the signed URL.
	replies := map[string]reply{
		"POST /api/v1/repositories/{name}/uploads": {status: http.StatusCreated, body: `{"id":"u-1","upload_url":"http://127.0.0.1:1/staging/u-1?X-Amz-Signature=secretsig"}`},
	}
	cs, srv := newContractServer(t, replies)

	res := runOA(t, srv, "", "artifact", "upload", "--presigned", "files", file)
	if res.code == 0 {
		t.Fatal("upload to an unreachable object store succeeded")
	}

	if strings.Contains(res.stderr+res.stdout, "secretsig") {
		t.Errorf("signed URL leaked: %q", res.stderr)
	}

	var init struct {
		Path string `json:"path"`
	}

	_ = json.Unmarshal(cs.reqs[0].body, &init)

	if init.Path != "named.bin" {
		t.Errorf("default path = %q, want the file name", init.Path)
	}
}

func TestArtifactUploadFlagsRequirePresigned(t *testing.T) {
	_, srv := newContractServer(t, nil)

	if res := runOA(t, srv, "", "artifact", "upload", "--path", "p", "files", "f"); res.code == 0 {
		t.Error("--path without --presigned must fail")
	}

	if res := runOA(t, srv, "", "artifact", "upload", "files"); res.code == 0 || !strings.Contains(res.stderr, "Usage") {
		t.Errorf("missing file: exit %d, stderr %q", res.code, res.stderr)
	}
}

// A complete call that answers "in progress" (an earlier attempt is still
// verifying) is retried until it returns the artifact.
func TestArtifactUploadPresignedRetriesInProgress(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.bin")
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, storeSrv := newObjectStore(t)

	var (
		mu        sync.Mutex
		completes int
	)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/uploads"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"u-1","upload_url":"`+storeSrv.URL+`/staging/u-1?X-Amz-Signature=s"}`)
		case strings.HasSuffix(r.URL.Path, "/complete"):
			mu.Lock()
			completes++
			n := completes
			mu.Unlock()

			if n < 3 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"busy","code":"upload_in_progress"}`)

				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"a-1","path":"app.bin","size":7,"sha256":"abc"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)

	res := runOA(t, api, "", "artifact", "upload", "--presigned", "files", file)
	if res.code != 0 || !strings.Contains(res.stdout, "Uploaded") {
		t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}

	mu.Lock()
	defer mu.Unlock()

	if completes != 3 {
		t.Errorf("complete calls = %d, want 3 (two in progress, then the result)", completes)
	}
}

// Any other 409 (for example upload_changed) is a failure, not a retry.
func TestArtifactUploadPresignedFailsOnOtherConflicts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.bin")
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, storeSrv := newObjectStore(t)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/uploads") {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"u-1","upload_url":"`+storeSrv.URL+`/staging/u-1?X-Amz-Signature=s"}`)

			return
		}

		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"changed","code":"upload_changed","message":"the uploaded object changed"}`)
	}))
	t.Cleanup(api.Close)

	res := runOA(t, api, "", "artifact", "upload", "--presigned", "files", file)
	if res.code == 0 || !strings.Contains(res.stderr, "changed") {
		t.Fatalf("exit %d, stderr %q; want a failure naming the conflict", res.code, res.stderr)
	}
}
