// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnsibleUploadPublishesMultipart(t *testing.T) {
	var gotPath, gotSHA, gotFile, gotBody, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")

		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		gotSHA = r.FormValue("sha256")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		defer func() { _ = f.Close() }()

		b, _ := io.ReadAll(f)
		gotFile, gotBody = hdr.Filename, string(b)

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task":"/repos/col/api/v3/imports/collections/1/"}`))
	}))
	t.Cleanup(srv.Close)

	tgt := &importTarget{c: &Client{baseURL: srv.URL, token: "tok", httpClient: srv.Client()}}
	digest := sha("tarball")

	res, err := ansibleUpload(context.Background(), tgt, uploadRequest{
		Dst:   "col",
		Asset: sourceAsset{Path: "dist/community-general-1.0.0.tar.gz", SHA256: digest, Size: 7},
		Body:  strings.NewReader("tarball"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/repos/col/api/v3/artifacts/collections/" || gotAuth != "Bearer tok" {
		t.Errorf("request = %s with %q", gotPath, gotAuth)
	}

	if gotSHA != digest || gotFile != "community-general-1.0.0.tar.gz" || gotBody != "tarball" {
		t.Errorf("form = sha %q file %q body %q", gotSHA, gotFile, gotBody)
	}

	if res.SHA256 != digest {
		t.Errorf("stored sha256 = %q, want %q", res.SHA256, digest)
	}
}

func TestAnsibleUploadRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	t.Cleanup(srv.Close)

	tgt := &importTarget{c: &Client{baseURL: srv.URL, httpClient: srv.Client()}}

	tests := []struct {
		name  string
		asset sourceAsset
		want  error
	}{
		{"duplicate version", sourceAsset{Path: "a-b-1.0.0.tar.gz", SHA256: "x"}, errUploadConflict},
		{"not a collection file name", sourceAsset{Path: "bundle.tar.gz", SHA256: "x"}, errImportUsage},
		{"missing digest", sourceAsset{Path: "a-b-1.0.0.tar.gz"}, errImportUsage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ansibleUpload(context.Background(), tgt, uploadRequest{Dst: "col", Asset: tt.asset, Body: strings.NewReader("x")})
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestAnsibleRemoveDeletesVersion(t *testing.T) {
	var method, p string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, p = r.Method, r.URL.Path

		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	tgt := &importTarget{c: &Client{baseURL: srv.URL, httpClient: srv.Client()}}

	if err := ansibleRemove(context.Background(), tgt, "col", sourceAsset{Path: "a-b-1.0.0-rc.1.tar.gz"}, uploadResult{}); err != nil {
		t.Fatal(err)
	}

	if method != http.MethodDelete || p != "/repos/col/api/v3/collections/a/b/versions/1.0.0-rc.1/" {
		t.Errorf("request = %s %s", method, p)
	}
}
