// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errBoom = errors.New("boom")

type failingReader struct {
	data []byte
	read bool
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.read {
		return 0, errBoom
	}

	f.read = true

	return copy(p, f.data), nil
}

func TestDownloadToAtomic(t *testing.T) {
	t.Parallel()

	payload := []byte("hello world")

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		dest := filepath.Join(t.TempDir(), "out.bin")
		if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}

		resp, err := http.Get(srv.URL) //nolint:noctx // test server
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		n, err := downloadTo(resp, dest)
		if err != nil || n != int64(len(payload)) {
			t.Fatalf("n=%d err=%v", n, err)
		}

		got, _ := os.ReadFile(dest)
		if !bytes.Equal(got, payload) {
			t.Fatalf("dest = %q", got)
		}

		entries, _ := os.ReadDir(filepath.Dir(dest))
		if len(entries) != 1 {
			t.Fatalf("leftover temp files: %v", entries)
		}
	})

	t.Run("short body leaves no dest", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		}))
		defer srv.Close()

		dir := t.TempDir()
		dest := filepath.Join(dir, "out.bin")

		resp, err := http.Get(srv.URL) //nolint:noctx // test server
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		if _, err := downloadTo(resp, dest); err == nil {
			t.Fatal("expected error for truncated body")
		}

		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("expected empty dir, got %v", entries)
		}
	})

	t.Run("failed read keeps existing dest", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		dest := filepath.Join(dir, "out.bin")

		if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}

		resp := &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: -1,
			Body:          io.NopCloser(&failingReader{data: []byte("partial")}),
		}

		if _, err := downloadTo(resp, dest); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v", err)
		}

		got, _ := os.ReadFile(dest)
		if string(got) != "old" {
			t.Fatalf("dest clobbered: %q", got)
		}

		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("leftover temp: %v", entries)
		}
	})
}

func TestWriteFileAtomicMode(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "sub", "oa")
	path := filepath.Join(dir, "config.json")

	if err := writeFileAtomic(path, []byte("one"), configDirMode, configFileMode); err != nil {
		t.Fatal(err)
	}

	assertMode(t, path, 0o600)
	assertMode(t, dir, 0o700)

	// Pre-existing world-readable file must be tightened.
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, []byte("two"), configDirMode, configFileMode); err != nil {
		t.Fatal(err)
	}

	assertMode(t, path, 0o600)

	got, _ := os.ReadFile(path)
	if string(got) != "two" {
		t.Fatalf("content = %q", got)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestMultipartStreamUpload(t *testing.T) {
	t.Parallel()

	payload := make([]byte, 3<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	var (
		gotBody []byte
		gotLen  int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength

		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		gotBody, _ = io.ReadAll(f)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	body, ct := multipartStream(bytes.NewReader(payload), "file", "pkg.bin")
	defer func() { _ = body.Close() }()

	c := newClient(srv.URL, "tok")

	resp, err := c.do(http.MethodPost, "/up", body, ct)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}

	if gotLen != -1 {
		t.Fatalf("ContentLength = %d, want -1 (chunked, unbuffered)", gotLen)
	}

	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("body mismatch: got %d bytes", len(gotBody))
	}
}

func TestMultipartStreamPropagatesReadError(t *testing.T) {
	t.Parallel()

	body, _ := multipartStream(&failingReader{data: []byte("abc")}, "file", "x")
	defer func() { _ = body.Close() }()

	if _, err := io.ReadAll(body); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
}

func TestReadSecretFromStdinPipe(t *testing.T) {
	t.Parallel()

	r := bufio.NewReader(strings.NewReader("s3cret\r\nnext\n"))

	// A closed non-terminal fd must fall back to line reading from r.
	first, err := readSecretFrom(r, -1, true, "pw: ")
	if err != nil || first != "s3cret" {
		t.Fatalf("first = %q err=%v", first, err)
	}

	second, err := readSecretFrom(r, -1, true, "pw: ")
	if err != nil || second != "next" {
		t.Fatalf("second = %q err=%v", second, err)
	}
}

func TestPromptsShareOneReader(t *testing.T) {
	// not parallel: swaps the package-level stdin reader.
	old := stdinReader

	t.Cleanup(func() { stdinReader = old })

	stdinReader = bufio.NewReader(strings.NewReader("alice\nhunter2\n"))

	if u := promptLine("user: "); u != "alice" {
		t.Fatalf("user = %q", u)
	}

	if p := readPasswordStdin(); p != "hunter2" {
		t.Fatalf("password = %q", p)
	}
}

func TestCheckRedirect(t *testing.T) {
	t.Parallel()

	mk := func(u string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, u, nil) //nolint:noctx // test only
		return r
	}

	via := []*http.Request{mk("https://a.example/x")}

	if err := checkRedirect(mk("http://a.example/y"), via); !errors.Is(err, errHTTPSDowngrade) {
		t.Fatalf("downgrade err = %v", err)
	}

	cross := mk("https://b.example/y")
	cross.Header.Set("Authorization", "Bearer t")

	if err := checkRedirect(cross, via); err != nil {
		t.Fatal(err)
	}

	if cross.Header.Get("Authorization") != "" {
		t.Fatal("Authorization not stripped on cross-host redirect")
	}

	same := mk("https://a.example/z")
	same.Header.Set("Authorization", "Bearer t")

	if err := checkRedirect(same, via); err != nil || same.Header.Get("Authorization") == "" {
		t.Fatalf("same-host redirect changed: %v", err)
	}
}
