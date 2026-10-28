// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

const emptySHA256Base32 = "0mdqa9w1p6cmli6976v4wi0sw9r4p5prkj7lzfd1877wk11c9c73"

func TestEncodeBase32KnownAnswer(t *testing.T) {
	sum := sha256.Sum256(nil)

	if got := EncodeBase32(sum[:]); got != emptySHA256Base32 {
		t.Fatalf("EncodeBase32(sha256(\"\")) = %q, want %q", got, emptySHA256Base32)
	}

	back, err := DecodeBase32(emptySHA256Base32, sha256.Size)
	if err != nil || !bytes.Equal(back, sum[:]) {
		t.Fatalf("DecodeBase32 = %x, %v; want %x", back, err, sum)
	}
}

func TestBase32RoundTrip(t *testing.T) {
	for size := 1; size <= 40; size++ {
		in := make([]byte, size)
		for i := range in {
			in[i] = byte(i*37 + size*11 + 5)
		}

		enc := EncodeBase32(in)
		if len(enc) != base32Len(size) {
			t.Fatalf("size %d: encoded length %d, want %d", size, len(enc), base32Len(size))
		}

		out, err := DecodeBase32(enc, size)
		if err != nil || !bytes.Equal(out, in) {
			t.Fatalf("size %d: round trip = %x, %v; want %x", size, out, err, in)
		}
	}
}

func TestDecodeBase32Rejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		size int
	}{
		{"letter e", "e" + emptySHA256Base32[1:], 32},
		{"letter o", "o" + emptySHA256Base32[1:], 32},
		{"letter u", "u" + emptySHA256Base32[1:], 32},
		{"letter t", "t" + emptySHA256Base32[1:], 32},
		{"upper case", strings.ToUpper(emptySHA256Base32), 32},
		{"too short", emptySHA256Base32[1:], 32},
		{"too long", "0" + emptySHA256Base32, 32},
		{"empty", "", 32},
		{"wrong size", emptySHA256Base32, 20},
		{"bits past the last byte", "z" + emptySHA256Base32[1:], 32},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeBase32(tc.in, tc.size); !errors.Is(err, ErrInvalidBase32) {
				t.Fatalf("DecodeBase32(%q, %d) error = %v, want ErrInvalidBase32", tc.in, tc.size, err)
			}
		})
	}
}

func TestBase32Empty(t *testing.T) {
	if got := EncodeBase32(nil); got != "" {
		t.Fatalf("EncodeBase32(nil) = %q, want empty", got)
	}

	if b, err := DecodeBase32("", 0); err != nil || len(b) != 0 {
		t.Fatalf("DecodeBase32(\"\", 0) = %x, %v", b, err)
	}

	if _, err := DecodeBase32("0", 0); !errors.Is(err, ErrInvalidBase32) {
		t.Fatalf("DecodeBase32(\"0\", 0) error = %v, want ErrInvalidBase32", err)
	}
}
