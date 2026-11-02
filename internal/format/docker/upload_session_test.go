// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// uploadID is the session UUID of an upload Location.
func uploadID(loc string) string { return loc[strings.LastIndex(loc, "/")+1:] }

// chunkCount is how many chunk objects the registry's chunk store holds.
func (g *registry) chunkCount() int { return len(chunkObjects(g.t, g.h)) }

// A session belongs to the repository that opened it: no other repository can
// read, extend, complete or cancel it, and it keeps working for its owner.
func TestUploadSession_NotUsableFromAnotherRepository(t *testing.T) {
	layer := []byte("layer owned by repo a")
	db := newRegFakeDB("a", "b")
	g := newRegistry(t, db)

	loc := startUpload(t, g, "a/app")
	if rec := g.do(http.MethodPatch, loc, "layer owned"); rec.Code != http.StatusAccepted {
		t.Fatalf("patch = %d", rec.Code)
	}

	foreign := strings.Replace(loc, "/v2/a/", "/v2/b/", 1)
	tests := []struct {
		name, method, target, body string
	}{
		{"status", http.MethodGet, foreign, ""},
		{"patch", http.MethodPatch, foreign, "evil"},
		{"complete", http.MethodPut, foreign + "?digest=" + digestOf(layer), string(layer)},
		{"cancel", http.MethodDelete, foreign, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := g.do(tc.method, tc.target, tc.body)
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "BLOB_UPLOAD_UNKNOWN") {
				t.Fatalf("%s via other repo = %d %s, want 404 BLOB_UPLOAD_UNKNOWN", tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	if got := g.chunkCount(); got != 1 {
		t.Fatalf("chunk objects = %d, want the owner's one; foreign requests must not touch it", got)
	}

	if len(db.blobs) != 0 {
		t.Fatalf("blob rows = %d after foreign attempts, want none", len(db.blobs))
	}

	// The owner finishes the push unaffected.
	if rec := g.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer[len("layer owned"):])); rec.Code != http.StatusCreated {
		t.Fatalf("owner complete = %d; body=%s", rec.Code, rec.Body.String())
	}

	if rec := g.do(http.MethodGet, "/v2/b/app/blobs/"+digestOf(layer), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("blob visible from repo b = %d, want 404", rec.Code)
	}
}

func TestUploadStatus(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))
	loc := startUpload(t, g, "reg/app")

	rec := g.do(http.MethodGet, loc, "")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Range") != "0-0" {
		t.Fatalf("empty session = %d Range %q, want 204 0-0", rec.Code, rec.Header().Get("Range"))
	}

	g.do(http.MethodPatch, loc, "12345")

	rec = g.do(http.MethodGet, loc, "")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Range") != "0-4" ||
		rec.Header().Get("Docker-Upload-Uuid") != uploadID(loc) {
		t.Fatalf("after 5 bytes = %d Range %q uuid %q", rec.Code, rec.Header().Get("Range"), rec.Header().Get("Docker-Upload-Uuid"))
	}

	rec = g.do(http.MethodGet, "/v2/reg/app/blobs/uploads/no-such-session", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404", rec.Code)
	}
}

// Cancelling removes the chunks and the session, meters nothing, and the
// session is gone for every later request.
func TestCancelUpload_DiscardsStateAndReleasesQuota(t *testing.T) {
	g, db := quotaRegistry(t, 1000, "reg")
	loc := startUpload(t, g, "reg/app")

	g.do(http.MethodPatch, loc, strings.Repeat("x", 300))

	if g.chunkCount() != 1 {
		t.Fatal("no chunk object after PATCH")
	}

	if rec := g.do(http.MethodDelete, loc, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	if g.chunkCount() != 0 {
		t.Error("chunk objects survived cancel")
	}

	if len(db.uploads) != 0 {
		t.Errorf("sessions left = %d, want 0", len(db.uploads))
	}

	assertUsage(t, "after cancel", db.usage("reg"), 0, 0)

	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		target := loc
		if method == http.MethodPut {
			target += "?digest=" + digestOf([]byte("x"))
		}

		if rec := g.do(method, target, "x"); rec.Code != http.StatusNotFound {
			t.Errorf("%s after cancel = %d, want 404", method, rec.Code)
		}
	}
}

// A completion whose bytes do not hash to the claimed digest is refused and the
// session, chunks and metering are all cleaned up.
func TestCompleteUpload_DigestMismatchDiscardsSession(t *testing.T) {
	tests := []struct {
		name   string
		chunk  string // sent with PATCH first
		final  string
		digest string
	}{
		{"monolithic", "", "actual bytes", digestOf([]byte("claimed bytes"))},
		{"chunked", "first ", "second", digestOf([]byte("first third"))},
		{"malformed digest", "", "bytes", "sha256:zz"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, db := quotaRegistry(t, 1000, "reg")
			loc := startUpload(t, g, "reg/app")

			if tc.chunk != "" {
				g.do(http.MethodPatch, loc, tc.chunk)
			}

			rec := g.do(http.MethodPut, loc+"?digest="+tc.digest, tc.final)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "DIGEST_INVALID") {
				t.Fatalf("complete = %d %s, want 400 DIGEST_INVALID", rec.Code, rec.Body.String())
			}

			if g.chunkCount() != 0 || len(db.uploads) != 0 || len(db.blobs) != 0 {
				t.Errorf("leftovers: chunks=%d sessions=%d blobs=%d", g.chunkCount(), len(db.uploads), len(db.blobs))
			}

			assertUsage(t, "after mismatch", db.usage("reg"), 0, 0)
		})
	}

	g := newRegistry(t, newRegFakeDB("reg"))
	loc := startUpload(t, g, "reg/app")

	if rec := g.do(http.MethodPut, loc, "x"); rec.Code != http.StatusBadRequest {
		t.Errorf("complete without digest = %d, want 400", rec.Code)
	}
}

// A final chunk that does not fit the quota is refused with 507 after earlier
// chunks were uploaded: nothing stays reserved, uploaded or stored.
func TestCompleteUpload_FinalChunkOverQuotaReleasesEverything(t *testing.T) {
	g, db := quotaRegistry(t, 100, "reg")
	layer := []byte(strings.Repeat("y", 150))
	loc := startUpload(t, g, "reg/app")

	if rec := g.do(http.MethodPatch, loc, string(layer[:60])); rec.Code != http.StatusAccepted {
		t.Fatalf("patch = %d", rec.Code)
	}

	assertDenied(t, g.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer[60:])))

	if g.chunkCount() != 0 || len(db.uploads) != 0 || len(db.blobs) != 0 {
		t.Errorf("leftovers: chunks=%d sessions=%d blobs=%d", g.chunkCount(), len(db.uploads), len(db.blobs))
	}

	assertUsage(t, "after refusal", db.usage("reg"), 0, 0)
}

// A session whose chunks cannot be read back ends with 404 BLOB_UPLOAD_UNKNOWN
// so the client restarts the push: a chunk object that is gone, and a session
// the rows of which do not add up to its offset (opened by a server that kept
// the bytes on its own disk).
func TestCompleteUpload_UnreadableSessionIsUnknown(t *testing.T) {
	t.Run("chunk object lost", func(t *testing.T) {
		g := newRegistry(t, newRegFakeDB("reg"))
		loc := startUpload(t, g, "reg/app")
		g.do(http.MethodPatch, loc, "abc")

		for _, id := range chunkObjects(t, g.h) {
			if err := g.h.chunks.DeleteChunk(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		}

		rec := g.do(http.MethodPut, loc+"?digest="+digestOf([]byte("abcdef")), "def")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "BLOB_UPLOAD_UNKNOWN") {
			t.Fatalf("complete = %d %s, want 404 BLOB_UPLOAD_UNKNOWN", rec.Code, rec.Body.String())
		}
	})

	t.Run("offset without chunk rows", func(t *testing.T) {
		db := newRegFakeDB("reg")
		g := newRegistry(t, db)
		loc := startUpload(t, g, "reg/app")
		db.uploads[scoped("id-reg", uploadID(loc))].Offset = 3

		rec := g.do(http.MethodPut, loc+"?digest="+digestOf([]byte("abcdef")), "def")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "BLOB_UPLOAD_UNKNOWN") {
			t.Fatalf("complete = %d %s, want 404 BLOB_UPLOAD_UNKNOWN", rec.Code, rec.Body.String())
		}
	})
}
