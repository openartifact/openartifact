// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getNPMWith(f *fixture, path string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, path, f.backend())

	return w
}

// The merged document is identified by the members' documents: unchanged
// members give the same bytes and validator, and any member change moves both.
func TestNPMGroup_MergeFollowsMemberDocuments(t *testing.T) {
	f := npmGroup(t)
	f.set("hosted", "lib", 200, packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))
	f.set("mirror", "lib", 200, packument(t, "mirror", "lib", map[string]string{"2.0.0": "BBB2"}, map[string]string{"latest": "2.0.0"}))

	first, again := f.get(http.MethodGet, "lib"), f.get(http.MethodGet, "lib")
	if first.Code != http.StatusOK || !bytes.Equal(first.Body.Bytes(), again.Body.Bytes()) || first.Header().Get("ETag") != again.Header().Get("ETag") {
		t.Fatalf("repeat request differs: %d, etags %q %q", first.Code, first.Header().Get("ETag"), again.Header().Get("ETag"))
	}

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("merged packument has no validator")
	}

	// A member publishes: a client holding the old validator gets the new merge.
	f.set("hosted", "lib", 200, packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA", "1.1.0": "AAA1"}, map[string]string{"latest": "1.1.0"}))

	changed := getNPMWith(f, "lib", map[string]string{"If-None-Match": etag})
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag || !strings.Contains(changed.Body.String(), `"1.1.0"`) {
		t.Fatalf("after member change: status %d, etag %q, body %s", changed.Code, changed.Header().Get("ETag"), changed.Body.String())
	}
}

func TestNPMGroup_RevalidationAndGzip(t *testing.T) {
	f := npmGroup(t)

	versions := map[string]string{}
	for _, v := range []string{"1.0.0", "1.0.1", "1.0.2", "1.0.3", "1.0.4", "1.0.5", "1.0.6", "1.0.7"} {
		versions[v] = "M" + v
	}

	f.set("hosted", "lib", 200, packument(t, "hosted", "lib", versions, map[string]string{"latest": "1.0.7"}))

	plain := f.get(http.MethodGet, "lib")

	if rec := getNPMWith(f, "lib", map[string]string{"If-None-Match": plain.Header().Get("ETag")}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("revalidation = %d with %d body bytes, want 304 and none", rec.Code, rec.Body.Len())
	}

	zipped := getNPMWith(f, "lib", map[string]string{"Accept-Encoding": "gzip"})
	if zipped.Header().Get("Content-Encoding") != "gzip" || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("encodings: plain %q, gzip %q", plain.Header().Get("Content-Encoding"), zipped.Header().Get("Content-Encoding"))
	}

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := io.ReadAll(zr); !bytes.Equal(got, plain.Body.Bytes()) {
		t.Error("decompressed merged packument differs")
	}
}
