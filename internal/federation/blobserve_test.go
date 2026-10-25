// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/storage"
)

// plainStore hides the RangeReader of the store it wraps.
type plainStore struct{ storage.BlobStore }

func TestServeBlobRanges(t *testing.T) {
	t.Parallel()

	content := []byte("0123456789abcdefghij") // 20 bytes
	peer := newFakePeer(t, content)

	tests := []struct {
		name         string
		rng          string
		wantStatus   int
		wantBody     string
		wantRange    string // Content-Range
		wantLength   string
		plainStorage bool
	}{
		{"no range", "", http.StatusOK, string(content), "", "20", false},
		{"open ended", "bytes=5-", http.StatusPartialContent, "56789abcdefghij", "bytes 5-19/20", "15", false},
		{"bounded", "bytes=2-4", http.StatusPartialContent, "234", "bytes 2-4/20", "3", false},
		{"clamped end", "bytes=15-99", http.StatusPartialContent, "fghij", "bytes 15-19/20", "5", false},
		{"from zero", "bytes=0-", http.StatusPartialContent, string(content), "bytes 0-19/20", "20", false},
		{"store without ranged reads", "bytes=5-", http.StatusPartialContent, "56789abcdefghij", "bytes 5-19/20", "15", true},
		{"at the end", "bytes=20-", http.StatusRequestedRangeNotSatisfiable, "", "bytes */20", "", false},
		{"past the end", "bytes=40-", http.StatusRequestedRangeNotSatisfiable, "", "bytes */20", "", false},
		{"suffix", "bytes=-3", http.StatusRequestedRangeNotSatisfiable, "", "bytes */20", "", false},
		{"multiple", "bytes=0-1,3-4", http.StatusRequestedRangeNotSatisfiable, "", "bytes */20", "", false},
		{"garbage", "bytes=zz", http.StatusRequestedRangeNotSatisfiable, "", "bytes */20", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := peer.srv
			if tt.plainStorage {
				srv = &Server{blobs: plainStore{peer.srv.blobs}}
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/blob", http.NoBody)
			if tt.rng != "" {
				req.Header.Set("Range", tt.rng)
			}

			rec := httptest.NewRecorder()
			srv.serveBlob(rec, req, &peer.ref)

			res := rec.Result()
			defer res.Body.Close()

			body, _ := io.ReadAll(res.Body)

			if res.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", res.StatusCode, tt.wantStatus, body)
			}

			if tt.wantStatus != http.StatusRequestedRangeNotSatisfiable {
				if string(body) != tt.wantBody {
					t.Errorf("body = %q, want %q", body, tt.wantBody)
				}

				if got := res.Header.Get("Content-Length"); got != tt.wantLength {
					t.Errorf("Content-Length = %q, want %q", got, tt.wantLength)
				}

				if got := res.Header.Get("Accept-Ranges"); got != "bytes" {
					t.Errorf("Accept-Ranges = %q, want bytes", got)
				}
			}

			if got := res.Header.Get("Content-Range"); got != tt.wantRange {
				t.Errorf("Content-Range = %q, want %q", got, tt.wantRange)
			}
		})
	}
}

func TestServeBlobUnknownSizeIgnoresTheRange(t *testing.T) {
	t.Parallel()

	peer := newFakePeer(t, []byte("0123456789"))
	ref := peer.ref
	ref.Size = 0

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/blob", http.NoBody)
	req.Header.Set("Range", "bytes=4-")

	rec := httptest.NewRecorder()
	peer.srv.serveBlob(rec, req, &ref)

	if rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
		t.Fatalf("got %d %q, want the whole blob with 200", rec.Code, rec.Body.String())
	}
}

func TestServeBlobMissingBytes(t *testing.T) {
	t.Parallel()

	peer := newFakePeer(t, []byte("0123456789"))
	ref := BlobRef{SHA256: sumHex([]byte("something else")), Size: 14}

	for _, rng := range []string{"", "bytes=2-"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/blob", http.NoBody)
		if rng != "" {
			req.Header.Set("Range", rng)
		}

		rec := httptest.NewRecorder()
		peer.srv.serveBlob(rec, req, &ref)

		if rec.Code != http.StatusNotFound {
			t.Errorf("Range %q: status = %d, want 404", rng, rec.Code)
		}
	}
}
