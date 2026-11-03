// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

type formField struct{ name, value string }

// formBody builds a multipart body with the fields first and the file part
// last, or the file part first when fileFirst.
func formBody(t *testing.T, fileName string, content []byte, fileFirst bool, fields ...formField) (*bytes.Buffer, string) {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	writeFile := func() {
		fw, err := mw.CreateFormFile("file", fileName)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = fw.Write(content)
	}

	if fileFirst {
		writeFile()
	}

	for _, f := range fields {
		_ = mw.WriteField(f.name, f.value)
	}

	if !fileFirst {
		writeFile()
	}

	_ = mw.Close()

	return &body, mw.FormDataContentType()
}

func formRequest(t *testing.T, body io.Reader, contentType, target string) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, target, body)
	req.Header.Set("Content-Type", contentType)

	return req
}

// ctxKey is a request-context key of the tests that rewrap the request.
type ctxKey struct{}

func TestReadFormFieldsAndFileInEitherOrder(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789"), 300_000) // 3 MB: spools to disk
	sum := sha256.Sum256(content)
	wantMD5 := md5.Sum(content)

	for _, fileFirst := range []bool{false, true} {
		md5Sum := md5.New()

		body, ct := formBody(t, "pkg.bin", content, fileFirst,
			formField{"name", "demo"}, formField{"tag", "a"}, formField{"tag", "b"})

		form, err := base.ReadForm(formRequest(t, body, ct, "/up?extra=q&name=ignored"),
			base.FormSpec{FileField: "file", MaxFile: 4 << 20, Hashes: []hash.Hash{md5Sum}})
		if err != nil {
			t.Fatalf("fileFirst=%v: ReadForm: %v", fileFirst, err)
		}

		if form.Filename != "pkg.bin" || form.File.Size() != int64(len(content)) ||
			form.File.SHA256Hex() != hex.EncodeToString(sum[:]) {
			t.Fatalf("fileFirst=%v: file = %q %d %s", fileFirst, form.Filename, form.File.Size(), form.File.SHA256Hex())
		}

		if got := hex.EncodeToString(md5Sum.Sum(nil)); got != hex.EncodeToString(wantMD5[:]) {
			t.Errorf("fileFirst=%v: extra hash = %s", fileFirst, got)
		}

		if got := form.Value("name"); got != "demo" {
			t.Errorf("name = %q, want the form's value before the query's", got)
		}

		if got := form.Value("extra"); got != "q" {
			t.Errorf("extra = %q, want the query fallback", got)
		}

		if got := form.Values("tag"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("tag values = %v", got)
		}

		form.Close()
	}
}

func TestReadFormLimits(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 2048)

	t.Run("file over MaxFile", func(t *testing.T) {
		body, ct := formBody(t, "f", big, false)

		_, err := base.ReadForm(formRequest(t, body, ct, "/"), base.FormSpec{FileField: "file", MaxFile: 1024})
		if !errors.Is(err, base.ErrContentTooLarge) || !base.IsTooLarge(err) {
			t.Fatalf("err = %v, want ErrContentTooLarge", err)
		}
	})

	t.Run("fields over MaxFields", func(t *testing.T) {
		body, ct := formBody(t, "f", []byte("x"), false, formField{"a", strings.Repeat("v", 600)}, formField{"b", strings.Repeat("v", 600)})

		_, err := base.ReadForm(formRequest(t, body, ct, "/"), base.FormSpec{FileField: "file", MaxFile: 1024, MaxFields: 1024})
		if !errors.Is(err, base.ErrFormFieldsTooLarge) || !base.IsTooLarge(err) {
			t.Fatalf("err = %v, want ErrFormFieldsTooLarge", err)
		}
	})

	t.Run("body over the request cap", func(t *testing.T) {
		body, ct := formBody(t, "f", big, false)
		req := formRequest(t, body, ct, "/")
		req.Body = http.MaxBytesReader(httptest.NewRecorder(), req.Body, 512)

		_, err := base.ReadForm(req, base.FormSpec{FileField: "file", MaxFile: 1 << 20})
		if !base.IsTooLarge(err) {
			t.Fatalf("err = %v, want a body-too-large error", err)
		}
	})

	t.Run("no file part", func(t *testing.T) {
		var body bytes.Buffer

		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("name", "x")
		_ = mw.Close()

		form, err := base.ReadForm(formRequest(t, &body, mw.FormDataContentType(), "/"), base.FormSpec{FileField: "file", MaxFile: 1024})
		if !errors.Is(err, base.ErrNoFormFile) {
			t.Fatalf("err = %v, want ErrNoFormFile", err)
		}

		if form.Value("name") != "x" {
			t.Errorf("fields must stay readable without the file part")
		}
	})

	t.Run("not multipart", func(t *testing.T) {
		_, err := base.ReadForm(formRequest(t, strings.NewReader("a=b"), "application/x-www-form-urlencoded", "/"),
			base.FormSpec{FileField: "file", MaxFile: 1024})
		if !errors.Is(err, base.ErrInvalidForm) {
			t.Fatalf("err = %v, want ErrInvalidForm", err)
		}
	})
}

// TestReadFormLeavesNoTempFile serves large uploads through a real HTTP server
// behind a middleware that copies the request (every routed request is a copy):
// net/http removes a multipart form's temp files only for the original request,
// which is how ParseMultipartForm leaked one full-size file per upload.
func TestReadFormLeavesNoTempFile(t *testing.T) {
	assertEmpty := formattest.IsolatedTemp(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form, err := base.ReadForm(r, base.FormSpec{FileField: "file", MaxFile: 64 << 20})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer form.Close()

		_, _ = io.Copy(io.Discard, form.File.Reader())
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, 1)))
	}))
	defer srv.Close()

	content := bytes.Repeat([]byte("z"), 40<<20)

	for range 3 {
		body, ct := formBody(t, "big.bin", content, false, formField{"name", "x"})

		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, body)
		if err != nil {
			t.Fatal(err)
		}

		req.Header.Set("Content-Type", ct)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}

	assertEmpty()
}
