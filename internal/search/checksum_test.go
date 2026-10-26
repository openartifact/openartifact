// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	t.Parallel()

	sha256 := strings.Repeat("ab", 32)
	sha1 := strings.Repeat("cd", 20)
	md5 := strings.Repeat("0f", 16)

	tests := []struct {
		name       string
		in         string
		wantDigest string
		wantAlgo   string
		wantOK     bool
	}{
		{"sha256", sha256, sha256, AlgorithmSHA256, true},
		{"sha1", sha1, sha1, AlgorithmSHA1, true},
		{"md5", md5, md5, AlgorithmMD5, true},
		{"uppercase is lowered", strings.ToUpper(md5), md5, AlgorithmMD5, true},
		{"surrounding space is trimmed", " " + sha1 + " ", sha1, AlgorithmSHA1, true},
		{"prefix is not a checksum", sha256[:12], "", "", false},
		{"non hex", strings.Repeat("zz", 16), "", "", false},
		{"empty", "", "", "", false},
		{"algorithm prefix", "sha256:" + sha256, "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			digest, algo, ok := ParseChecksum(tc.in)
			if digest != tc.wantDigest || algo != tc.wantAlgo || ok != tc.wantOK {
				t.Errorf("ParseChecksum(%q) = %q, %q, %v; want %q, %q, %v", tc.in, digest, algo, ok, tc.wantDigest, tc.wantAlgo, tc.wantOK)
			}
		})
	}
}

func TestHandlerChecksum(t *testing.T) {
	sha1 := strings.Repeat("cd", 20)
	hit := ChecksumResult{Repository: "libs", Format: "maven", Name: "org.acme:core", Version: "1.0", Filename: "org/acme/core/1.0/core-1.0.jar", Checksum: sha1, Algorithm: AlgorithmSHA1}

	tests := []struct {
		name         string
		url          string
		searcher     mockSearcher
		wantStatus   int
		wantTotal    int
		wantAlgo     string
		wantErrCode  string
		wantSearched bool
	}{
		{name: "finds the file", url: "?checksum=" + sha1, searcher: mockSearcher{checksumResults: []ChecksumResult{hit}}, wantStatus: http.StatusOK, wantTotal: 1, wantAlgo: AlgorithmSHA1, wantSearched: true},
		{name: "no match is an empty list", url: "?checksum=" + sha1, wantStatus: http.StatusOK, wantAlgo: AlgorithmSHA1, wantSearched: true},
		{name: "q is not needed", url: "?checksum=" + strings.Repeat("0f", 16), wantStatus: http.StatusOK, wantAlgo: AlgorithmMD5, wantSearched: true},
		{name: "prefix is rejected", url: "?checksum=abcdef", wantStatus: http.StatusBadRequest, wantErrCode: "invalid_checksum"},
		{name: "bad limit is rejected first", url: "?checksum=" + sha1 + "&limit=0", wantStatus: http.StatusBadRequest, wantErrCode: "invalid_limit"},
		{name: "too many matches", url: "?checksum=" + sha1, searcher: mockSearcher{checksumErr: ErrChecksumTooBroad}, wantStatus: http.StatusBadRequest, wantErrCode: "query_too_broad", wantSearched: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ms := tc.searcher
			h, cleanup := newTestHandler(t, &ms)

			defer cleanup()

			w := httptest.NewRecorder()
			h.ServeHTTP(w, requestWithPrincipal(http.MethodGet, "/api/v1/search"+tc.url, adminPrincipal()))

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body)
			}

			if searched := ms.gotAlgorithm != ""; searched != tc.wantSearched {
				t.Errorf("searched = %v, want %v", searched, tc.wantSearched)
			}

			if tc.wantErrCode != "" {
				if !strings.Contains(w.Body.String(), tc.wantErrCode) {
					t.Errorf("body %s lacks error code %q", w.Body, tc.wantErrCode)
				}

				return
			}

			var body struct {
				Total    int              `json:"total"`
				Packages []ChecksumResult `json:"packages"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if body.Total != tc.wantTotal || len(body.Packages) != tc.wantTotal || ms.gotAlgorithm != tc.wantAlgo {
				t.Errorf("total = %d, packages = %d, algorithm = %q; want %d, %d, %q", body.Total, len(body.Packages), ms.gotAlgorithm, tc.wantTotal, tc.wantTotal, tc.wantAlgo)
			}
		})
	}
}

func TestHandlerFlagged(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantStatus  int
		wantFlagged bool
	}{
		{"flagged with a query", "?q=lodash&flagged=true", http.StatusOK, true},
		{"flagged alone lists every flagged package", "?flagged=true", http.StatusOK, true},
		{"flagged false is the default", "?q=lodash&flagged=false", http.StatusOK, false},
		{"bad value", "?q=lodash&flagged=maybe", http.StatusBadRequest, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ms := &mockSearcher{count: 0}
			h, cleanup := newTestHandler(t, ms)

			defer cleanup()

			w := httptest.NewRecorder()
			h.ServeHTTP(w, requestWithPrincipal(http.MethodGet, "/api/v1/search"+tc.url, adminPrincipal()))

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body)
			}

			if ms.gotFilter.Flagged != tc.wantFlagged {
				t.Errorf("filter.Flagged = %v, want %v", ms.gotFilter.Flagged, tc.wantFlagged)
			}
		})
	}
}
