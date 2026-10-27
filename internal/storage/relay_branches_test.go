// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Without a public URL there is no relay: every staging entry point says so
// and the relay route answers 404 instead of serving anything.
func TestRelayDisabledStoreRefusesEveryEntryPoint(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemBlobStore(StorageConfig{Type: "filesystem", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	sha := sha256Of([]byte("x"))

	checks := map[string]error{
		"InitiateMultipart": func() error { _, err := fsys.InitiateMultipart(ctx, relayTestID, 1, MinPartSize); return err }(),
		"PresignStagingPart": func() error {
			_, err := fsys.PresignStagingPart(ctx, relayTestID, "", 1, 1, time.Minute)
			return err
		}(),
		"CompleteMultipart": fsys.CompleteMultipart(ctx, relayTestID, "", 1, MinPartSize),
		"StatStaging":       func() error { _, err := fsys.StatStaging(ctx, relayTestID); return err }(),
		"OpenStaging":       func() error { _, err := fsys.OpenStaging(ctx, relayTestID, "v"); return err }(),
		"DeleteStaging":     fsys.DeleteStaging(ctx, relayTestID),
		"WalkStaging":       fsys.WalkStaging(ctx, time.Now(), func([]string) error { return nil }),
		"PromoteStaging":    fsys.PromoteStaging(ctx, relayTestID, sha, "v", nil),
	}

	for name, err := range checks {
		if !errors.Is(err, ErrPresignNotSupported) {
			t.Errorf("%s = %v, want ErrPresignNotSupported", name, err)
		}
	}

	rec := httptest.NewRecorder()
	fsys.RelayHandler().
		ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/upload-relay/"+relayTestID+"/1", strings.NewReader("x")))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("relay handler status = %d, want 404", rec.Code)
	}
}

// Staging ids become file names: anything but a canonical lowercase UUID is
// refused before touching the disk.
func TestRelayRejectsNonUUIDStagingIDs(t *testing.T) {
	env, fsys := newFSRelayEnv(t)
	ctx := context.Background()

	t.Run("ids", func(t *testing.T) {
		for _, id := range []string{"", "../../etc/passwd", strings.ToUpper(relayTestID), relayTestID + "x", "0a1b2c3d_4e5f_4a6b_8c7d_9e0f1a2b3c4d"} {
			t.Run(id, func(t *testing.T) {
				if _, err := fsys.PresignStagingPut(ctx, id, 1, time.Minute); !errors.Is(err, errRelayBadID) {
					t.Errorf("PresignStagingPut = %v", err)
				}

				if _, err := fsys.InitiateMultipart(ctx, id, 1, MinPartSize); !errors.Is(err, errRelayBadID) {
					t.Errorf("InitiateMultipart = %v", err)
				}

				if _, err := fsys.PresignStagingPart(ctx, id, "", 1, 1, time.Minute); !errors.Is(err, errRelayBadID) {
					t.Errorf("PresignStagingPart = %v", err)
				}

				if err := fsys.CompleteMultipart(ctx, id, "", 1, MinPartSize); !errors.Is(err, errRelayBadID) {
					t.Errorf("CompleteMultipart = %v", err)
				}

				if err := fsys.DeleteStaging(ctx, id); !errors.Is(err, errRelayBadID) {
					t.Errorf("DeleteStaging = %v", err)
				}

				if _, err := fsys.StatStaging(ctx, id); !errors.Is(err, ErrStagingNotFound) {
					t.Errorf("StatStaging = %v", err)
				}

				if _, err := fsys.OpenStaging(ctx, id, "v"); !errors.Is(err, ErrStagingNotFound) {
					t.Errorf("OpenStaging = %v", err)
				}
			})
		}
	})

	if entries, _ := os.ReadDir(env.staging); len(tempOrDataFiles(entries)) != 0 {
		t.Fatalf("rejected ids created staging files: %v", entries)
	}
}

func tempOrDataFiles(entries []os.DirEntry) []string {
	var names []string

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}

	return names
}

func TestRelayRefusesInvalidUploadPlans(t *testing.T) {
	t.Parallel()

	_, fsys := newFSRelayEnv(t)
	ctx := context.Background()

	if _, err := fsys.PresignStagingPut(ctx, relayTestID, -1, time.Minute); !errors.Is(err, errRelayBadPlan) {
		t.Errorf("negative size = %v, want errRelayBadPlan", err)
	}

	if _, err := fsys.InitiateMultipart(ctx, relayTestID, 10*MinPartSize, MinPartSize-1); !errors.Is(err, ErrInvalidPartPlan) {
		t.Errorf("tiny part size = %v, want ErrInvalidPartPlan", err)
	}

	// A part cannot be presigned for an upload that was never planned.
	if _, err := fsys.PresignStagingPart(ctx, relayTestID, "", 1, MinPartSize, time.Minute); err == nil {
		t.Error("PresignStagingPart without a plan succeeded")
	}

	if err := fsys.CompleteMultipart(ctx, relayTestID, "", 2*MinPartSize, MinPartSize); !errors.Is(err, ErrMultipartIncomplete) {
		t.Errorf("CompleteMultipart of an unknown upload = %v, want ErrMultipartIncomplete", err)
	}

	size := 2*MinPartSize + 1
	if _, err := fsys.InitiateMultipart(ctx, relayTestID, size, MinPartSize); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		number int
		length int64
	}{
		{"part zero", 0, MinPartSize},
		{"part past the plan", 4, 1},
		{"short last part", 3, 2},
		{"oversized first part", 1, MinPartSize + 1},
	}

	for _, tc := range tests {
		if _, err := fsys.PresignStagingPart(ctx, relayTestID, "", tc.number, tc.length, time.Minute); !errors.Is(err, errRelayBadPlan) {
			t.Errorf("%s = %v, want errRelayBadPlan", tc.name, err)
		}
	}

	if err := fsys.CompleteMultipart(ctx, relayTestID, "", size, 2*MinPartSize); !errors.Is(err, errRelayPlanDiffer) {
		t.Errorf("CompleteMultipart with another plan = %v, want errRelayPlanDiffer", err)
	}
}

func relayDo(t *testing.T, method, rawURL string, body io.Reader) int {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, body)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	return resp.StatusCode
}

func TestRelayPutRouting(t *testing.T) {
	env, fsys := newFSRelayEnv(t)
	ctx := context.Background()
	data := []byte("routed payload")

	put, err := fsys.PresignStagingPut(ctx, relayTestID, int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		url  string
		body io.Reader
		want int
	}{
		{"id is not a uuid", strings.Replace(put.URL, relayTestID, "not-a-uuid", 1), bytes.NewReader(data), http.StatusNotFound},
		{"part is not a number", strings.Replace(put.URL, "/1?", "/one?", 1), bytes.NewReader(data), http.StatusNotFound},
		{"part beyond the plan", strings.Replace(put.URL, "/1?", "/2?", 1), bytes.NewReader(data), http.StatusNotFound},
		{
			"unplanned upload",
			strings.Replace(put.URL, relayTestID, "ffffffff-ffff-4fff-8fff-ffffffffffff", 1),
			bytes.NewReader(data),
			http.StatusNotFound,
		},
		{"no signature", env.srv.URL + "/upload-relay/" + relayTestID + "/1", bytes.NewReader(data), http.StatusForbidden},
		// A body of unknown length (chunked) cannot be checked against the plan.
		{"chunked body", put.URL, io.NopCloser(bytes.NewReader(data)), http.StatusLengthRequired},
		{"short body", put.URL, bytes.NewReader(data[:3]), http.StatusBadRequest},
	}

	t.Run("refused", func(t *testing.T) {
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := relayDo(t, http.MethodPut, tc.url, tc.body); got != tc.want {
					t.Fatalf("status = %d, want %d", got, tc.want)
				}
			})
		}
	})

	if _, err := fsys.StatStaging(ctx, relayTestID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("a refused PUT published an object: %v", err)
	}

	mustStatus(t, relayPut(t, put.URL, data), http.StatusOK)

	// Once the object exists the upload is closed to further writes.
	if got := relayDo(t, http.MethodPut, put.URL, bytes.NewReader(data)); got != http.StatusConflict {
		t.Fatalf("PUT after publish = %d, want 409", got)
	}

	if err := fsys.DeleteStaging(ctx, relayTestID); err != nil {
		t.Fatal(err)
	}

	if got := relayDo(t, http.MethodPut, put.URL, bytes.NewReader(data)); got != http.StatusNotFound {
		t.Fatalf("PUT after DeleteStaging = %d, want 404", got)
	}

	leftovers, _ := filepath.Glob(filepath.Join(env.staging, relayTestID+"*"))
	if len(leftovers) != 0 {
		t.Fatalf("DeleteStaging left files: %v", leftovers)
	}
}

// The relay key is created once per staging directory; a corrupt key file
// disables staging rather than signing with a truncated secret.
func TestRelaySigningKeyHandling(t *testing.T) {
	t.Parallel()

	t.Run("stable across restarts", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		first := newRelay(dir, "http://x")
		second := newRelay(dir, "http://x")

		if first == nil || second == nil || !bytes.Equal(first.key, second.key) || len(first.key) != relayKeyLen {
			t.Fatal("restarting over the same directory must reuse the signing key")
		}
	})

	t.Run("truncated key disables staging", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, relaySigningFile), []byte("short"), 0o600); err != nil {
			t.Fatal(err)
		}

		if r := newRelay(dir, "http://x"); r != nil {
			t.Fatal("relay started with a truncated signing key")
		}
	})

	t.Run("empty settings", func(t *testing.T) {
		t.Parallel()

		if newRelay("", "http://x") != nil || newRelay(t.TempDir(), "") != nil {
			t.Fatal("relay started without a directory or public URL")
		}
	})

	t.Run("directory is a file", func(t *testing.T) {
		t.Parallel()

		file := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		if newRelay(file, "http://x") != nil {
			t.Fatal("relay started inside a regular file")
		}
	})

	t.Run("public url is normalised", func(t *testing.T) {
		t.Parallel()

		r := newRelay(t.TempDir(), "http://x/base///")
		if r == nil || r.publicURL != "http://x/base" {
			t.Fatalf("publicURL = %v", r)
		}
	})
}
