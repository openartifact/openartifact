// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/pkg/composer/composertest"
)

func TestComposerUploadPutsTheZip(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotType string

	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth, gotType = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"ok","id":"p-1","sha256":"ABC"}`))
	}))
	t.Cleanup(srv.Close)

	tgt := &importTarget{c: &Client{baseURL: srv.URL, token: "tok", httpClient: srv.Client()}}
	zip := composertest.Build(t, composertest.Spec{Name: "acme/tools", Version: "1.2.0"})

	for _, tc := range []struct {
		name, version, wantPath string
	}{
		{"version from composer.json", "", "/repos/col/packages/upload/acme/tools/1.2.0"},
		{"version flag wins", "dev-feature/x", "/repos/col/packages/upload/acme/tools/dev-feature%2Fx"},
	} {
		res, err := composerUpload(context.Background(), tgt, uploadRequest{
			Dst: "col", Asset: sourceAsset{Path: "dist/tools.zip"}, Body: bytes.NewReader(zip), Meta: uploadMeta{Version: tc.version},
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if gotMethod != http.MethodPut || gotPath != tc.wantPath || gotAuth != "Bearer tok" || gotType != "application/zip" {
			t.Errorf("%s: request = %s %s with %q, %q", tc.name, gotMethod, gotPath, gotAuth, gotType)
		}

		if !bytes.Equal(gotBody, zip) {
			t.Errorf("%s: the body is not the zip", tc.name)
		}

		if res.ID != "p-1" || res.SHA256 != "abc" {
			t.Errorf("%s: result = %+v", tc.name, res)
		}
	}
}

func TestComposerUploadRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	t.Cleanup(srv.Close)

	tgt := &importTarget{c: &Client{baseURL: srv.URL, httpClient: srv.Client()}}

	tests := []struct {
		name string
		body []byte
		want error
	}{
		{"duplicate version", composertest.Build(t, composertest.Spec{Name: "a/b", Version: "1.0.0"}), errUploadConflict},
		{"no version anywhere", composertest.Build(t, composertest.Spec{Name: "a/b"}), errImportUsage},
		{"not a zip", []byte("hello"), errImportUsage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := composerUpload(
				context.Background(),
				tgt,
				uploadRequest{Dst: "col", Asset: sourceAsset{Path: "x.zip"}, Body: bytes.NewReader(tt.body)},
			)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
