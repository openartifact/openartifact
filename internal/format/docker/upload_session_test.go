// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uploadID is the session UUID of an upload Location.
func uploadID(loc string) string { return loc[strings.LastIndex(loc, "/")+1:] }

func (g *registry) stagedFile(loc string) string {
	return filepath.Join(g.h.uploadDir, uploadID(loc))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

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
	staged := g.stagedFile(loc)

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

	if got, err := os.ReadFile(staged); err != nil || string(got) != "layer owned" {
		t.Fatalf("staged file = %q, %v; foreign requests must not touch it", got, err)
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

// Cancelling removes the staged bytes and the session, meters nothing, and the
// session is gone for every later request.
func TestCancelUpload_DiscardsStateAndReleasesQuota(t *testing.T) {
	g, db := quotaRegistry(t, 1000, "reg")
	loc := startUpload(t, g, "reg/app")

	g.do(http.MethodPatch, loc, strings.Repeat("x", 300))

	staged := g.stagedFile(loc)
	if !fileExists(staged) {
		t.Fatal("no staged file after PATCH")
	}

	if rec := g.do(http.MethodDelete, loc, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	if fileExists(staged) {
		t.Error("staged file survived cancel")
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
// session, staged file and metering are all cleaned up.
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

			if fileExists(g.stagedFile(loc)) || len(db.uploads) != 0 || len(db.blobs) != 0 {
				t.Errorf("leftovers: file=%v sessions=%d blobs=%d", fileExists(g.stagedFile(loc)), len(db.uploads), len(db.blobs))
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
// chunks were staged: nothing stays reserved, staged or stored.
func TestCompleteUpload_FinalChunkOverQuotaReleasesEverything(t *testing.T) {
	g, db := quotaRegistry(t, 100, "reg")
	layer := []byte(strings.Repeat("y", 150))
	loc := startUpload(t, g, "reg/app")

	if rec := g.do(http.MethodPatch, loc, string(layer[:60])); rec.Code != http.StatusAccepted {
		t.Fatalf("patch = %d", rec.Code)
	}

	assertDenied(t, g.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer[60:])))

	if fileExists(g.stagedFile(loc)) || len(db.uploads) != 0 || len(db.blobs) != 0 {
		t.Errorf("leftovers: file=%v sessions=%d blobs=%d", fileExists(g.stagedFile(loc)), len(db.uploads), len(db.blobs))
	}

	assertUsage(t, "after refusal", db.usage("reg"), 0, 0)
}

// Bytes a failed request left past the accepted offset are dropped, so the
// blob is exactly what the client sent; a lost staging file ends the session.
func TestCompleteUpload_StagedFileRecovery(t *testing.T) {
	t.Run("stray bytes are truncated", func(t *testing.T) {
		g := newRegistry(t, newRegFakeDB("reg"))
		loc := startUpload(t, g, "reg/app")
		g.do(http.MethodPatch, loc, "abc")

		f, err := os.OpenFile(g.stagedFile(loc), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = f.WriteString("JUNK")
		_ = f.Close()

		if rec := g.do(http.MethodPut, loc+"?digest="+digestOf([]byte("abcdef")), "def"); rec.Code != http.StatusCreated {
			t.Fatalf("complete = %d; body=%s", rec.Code, rec.Body.String())
		}

		rec := g.do(http.MethodGet, "/v2/reg/app/blobs/"+digestOf([]byte("abcdef")), "")
		if rec.Body.String() != "abcdef" {
			t.Fatalf("blob = %q, want abcdef", rec.Body.String())
		}
	})

	t.Run("lost staging file", func(t *testing.T) {
		g := newRegistry(t, newRegFakeDB("reg"))
		loc := startUpload(t, g, "reg/app")
		g.do(http.MethodPatch, loc, "abc")

		if err := os.Remove(g.stagedFile(loc)); err != nil {
			t.Fatal(err)
		}

		rec := g.do(http.MethodPut, loc+"?digest="+digestOf([]byte("abcdef")), "def")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "BLOB_UPLOAD_UNKNOWN") {
			t.Fatalf("complete = %d %s, want 404 BLOB_UPLOAD_UNKNOWN", rec.Code, rec.Body.String())
		}
	})
}

func TestOpenUploadFile(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		offset   int64
		wantSize int64
		wantErr  error
	}{
		{"new file at zero", "", 0, 0, nil},
		{"exact", "abcd", 4, 4, nil},
		{"truncates stray tail", "abcdXYZ", 4, 4, nil},
		{"shorter than offset", "ab", 4, 0, errUploadFileShort},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "up")
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			f, err := openUploadFile(path, tc.offset)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}

			if err != nil {
				return
			}

			defer func() { _ = f.Close() }()

			info, err := os.Stat(path)
			if err != nil || info.Size() != tc.wantSize {
				t.Fatalf("size = %v, %v, want %d", info, err, tc.wantSize)
			}
		})
	}

	if _, err := openUploadFile(filepath.Join(t.TempDir(), "missing", "up"), 0); err == nil {
		t.Error("open in a missing directory succeeded")
	}
}
