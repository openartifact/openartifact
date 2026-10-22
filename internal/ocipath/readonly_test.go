// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocipath

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRejectNonPull(t *testing.T) {
	tests := []struct {
		method string
		kind   Kind
		want   int
		reject bool
		code   string
	}{
		{http.MethodGet, KindManifest, 0, false, ""},
		{http.MethodHead, KindBlob, 0, false, ""},
		{http.MethodGet, KindTags, 0, false, ""},
		{http.MethodPut, KindManifest, http.StatusMethodNotAllowed, true, "UNSUPPORTED"},
		{http.MethodDelete, KindBlob, http.StatusMethodNotAllowed, true, "UNSUPPORTED"},
		{http.MethodPost, KindUploadStart, http.StatusMethodNotAllowed, true, "UNSUPPORTED"},
		{http.MethodPatch, KindUpload, http.StatusMethodNotAllowed, true, "UNSUPPORTED"},
		{http.MethodGet, KindUpload, http.StatusNotFound, true, "BLOB_UPLOAD_UNKNOWN"},
		{http.MethodGet, KindUnknown, http.StatusNotFound, true, "NAME_UNKNOWN"},
	}

	for _, tt := range tests {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(t.Context(), tt.method, "/v2/x", nil)

		if got := RejectNonPull(w, r, tt.kind); got != tt.reject {
			t.Errorf("%s kind %v: rejected = %v, want %v", tt.method, tt.kind, got, tt.reject)
			continue
		}

		if !tt.reject {
			continue
		}

		if w.Code != tt.want || !strings.Contains(w.Body.String(), tt.code) {
			t.Errorf("%s kind %v: %d %q, want %d %s", tt.method, tt.kind, w.Code, w.Body.String(), tt.want, tt.code)
		}
	}
}

func TestNotFoundCode(t *testing.T) {
	want := map[Kind]string{KindBlob: "BLOB_UNKNOWN", KindManifest: "MANIFEST_UNKNOWN", KindTags: "NAME_UNKNOWN", KindReferrers: "NAME_UNKNOWN"}

	for kind, code := range want {
		if got, msg := NotFoundCode(kind); got != code || msg == "" {
			t.Errorf("NotFoundCode(%v) = %q, %q; want %s", kind, got, msg, code)
		}
	}
}
