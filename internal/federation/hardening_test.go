// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestValidateStamp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	wallAt := func(d time.Duration) int64 { return now.Add(d).UnixMicro() }

	tests := []struct {
		name  string
		stamp Stamp
		ok    bool
	}{
		{"now", Stamp{Wall: wallAt(0), Counter: 3}, true},
		{"long ago", Stamp{Wall: 1000}, true},
		{"epoch", Stamp{}, true},
		{"just inside the skew", Stamp{Wall: wallAt(skew)}, true},
		{"just past the skew", Stamp{Wall: wallAt(skew + time.Second)}, false},
		{"a year ahead", Stamp{Wall: wallAt(365 * 24 * time.Hour)}, false},
		{"the largest wall", Stamp{Wall: math.MaxInt64}, false},
		{"negative wall", Stamp{Wall: -1}, false},
		{"largest accepted counter", Stamp{Wall: wallAt(0), Counter: maxStampCounter}, true},
		{"counter past the bound", Stamp{Wall: wallAt(0), Counter: maxStampCounter + 1}, false},
		{"counter near the int32 limit", Stamp{Wall: wallAt(0), Counter: math.MaxInt32}, false},
		{"negative counter", Stamp{Wall: wallAt(0), Counter: -1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateStamp(tt.stamp, now, skew)
			if tt.ok && err != nil {
				t.Fatalf("validateStamp(%+v) = %v, want accepted", tt.stamp, err)
			}

			if !tt.ok && !errors.Is(err, errStampInvalid) {
				t.Fatalf("validateStamp(%+v) = %v, want errStampInvalid", tt.stamp, err)
			}
		})
	}
}

func TestEngineCheckStampDefersAndHonoursTheConfiguredSkew(t *testing.T) {
	t.Parallel()

	ahead := Stamp{Wall: time.Now().Add(time.Hour).UnixMicro()}

	tests := []struct {
		name string
		skew time.Duration
		ok   bool
	}{
		{"default skew", 0, false},
		{"two hours allowed", 2 * time.Hour, true},
		{"one minute", time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := &Engine{}
			e.MaxClockSkew = tt.skew

			err := e.checkStamp(ahead)
			if tt.ok {
				if err != nil {
					t.Fatalf("checkStamp = %v, want accepted", err)
				}

				return
			}

			var de *deferError
			if !errors.As(err, &de) || de.reason != "clock_skew" {
				t.Fatalf("checkStamp = %v, want a clock_skew deferral", err)
			}
		})
	}
}

func TestEngineCheckBlobBoundsTheDeclaredSize(t *testing.T) {
	t.Parallel()

	sha := sumHex([]byte("payload"))

	tests := []struct {
		name   string
		max    int64
		ref    BlobRef
		reason string // empty: accepted
	}{
		{"declared size", 0, BlobRef{SHA256: sha, Size: 7}, ""},
		{"no limit", 0, BlobRef{SHA256: sha, Size: math.MaxInt64}, ""},
		{"at the limit", 100, BlobRef{SHA256: sha, Size: 100}, ""},
		{"past the limit", 100, BlobRef{SHA256: sha, Size: 101}, "blob_too_large"},
		{"empty blob", 0, BlobRef{SHA256: emptyBlobSHA256, Size: 0}, ""},
		{"size 0 for another blob", 0, BlobRef{SHA256: sha, Size: 0}, "invalid_blob"},
		{"negative size", 0, BlobRef{SHA256: sha, Size: -1}, "invalid_blob"},
		{"negative size for the empty blob", 0, BlobRef{SHA256: emptyBlobSHA256, Size: -1}, "invalid_blob"},
		{"not a digest", 0, BlobRef{SHA256: "../../etc/passwd", Size: 1}, "invalid_blob"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := &Engine{}
			e.MaxBlobBytes = tt.max

			err := e.checkBlob(tt.ref)
			if tt.reason == "" {
				if err != nil {
					t.Fatalf("checkBlob(%+v) = %v, want accepted", tt.ref, err)
				}

				return
			}

			var de *deferError
			if !errors.As(err, &de) || de.reason != tt.reason {
				t.Fatalf("checkBlob(%+v) = %v, want a %s deferral", tt.ref, err, tt.reason)
			}
		})
	}
}

func TestEngineCheckBlobsRefusesOneDigestWithTwoSizes(t *testing.T) {
	t.Parallel()

	sha := sumHex([]byte("payload"))
	e := &Engine{}

	if err := e.checkBlobs([]BlobRef{{SHA256: sha, Size: 7}, {SHA256: sha, Size: 7}}); err != nil {
		t.Fatalf("same size twice = %v, want accepted", err)
	}

	if err := e.checkBlobs([]BlobRef{{SHA256: sha, Size: 7}, {SHA256: sha, Size: 8}}); err == nil {
		t.Fatal("two sizes for one digest were accepted")
	}
}

// The declared size is the cap of the download: a peer cannot leave it open by
// declaring 0, and the body is never read past the declared size.
func TestDownloadIsHeldToTheDeclaredSize(t *testing.T) {
	t.Parallel()

	content := randomBytes(64 << 10)

	tests := []struct {
		name string
		size int64
	}{
		{"size 0 for a blob that has bytes", 0},
		{"size below the real length", int64(len(content)) - 1},
		{"size above the real length", int64(len(content)) + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			peer := newFakePeer(t, content)
			peer.ref.Size = tt.size
			// Chunked, whole blob, whatever size the unit declares.
			peer.behave = func(_ int, w http.ResponseWriter, _ *http.Request) bool {
				_, _ = w.Write(content)

				return true
			}
			env := newDownloadEnv(t, peer)

			path, err := env.run(context.Background())

			var de *deferError
			if !errors.As(err, &de) || de.reason != "digest_mismatch" {
				t.Fatalf("download = %q, %v; want a digest_mismatch deferral", path, err)
			}

			if _, err := os.Stat(env.partial()); !os.IsNotExist(err) {
				t.Errorf("partial file survived: %v", err)
			}
		})
	}
}

func TestDownloadOfTheEmptyBlobNeedsNoRequest(t *testing.T) {
	t.Parallel()

	peer := newFakePeer(t, nil)
	env := newDownloadEnv(t, peer)

	if peer.ref.Size != 0 || peer.ref.SHA256 != emptyBlobSHA256 {
		t.Fatalf("fixture ref = %+v", peer.ref)
	}

	path, err := env.run(context.Background())
	if err != nil {
		t.Fatalf("download of the empty blob: %v", err)
	}

	mustEqualFile(t, path, nil)

	if got := peer.ranges(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}
