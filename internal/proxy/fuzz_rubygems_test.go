// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/pkg/compactindex"
)

// FuzzRubyGemsVerifiers feeds arbitrary upstream bodies to every verifier of
// the RubyGems proxy: none may panic, whatever a hostile server answers.
func FuzzRubyGemsVerifiers(f *testing.F) {
	f.Add([]byte("created_at: 2024-01-01T00:00:00Z\n---\ndemo 1.0.0 d41d8cd98f00b204e9800998ecf8427e\n"))
	f.Add([]byte("---\n1.0.0 |checksum:" + string(bytes.Repeat([]byte("a"), 64)) + "\n"))
	f.Add([]byte("---\ndemo\n"))
	f.Add([]byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})
	f.Add([]byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 0xff})

	kinds := []compactindex.Kind{
		compactindex.KindVersions, compactindex.KindNames, compactindex.KindInfo, compactindex.KindGemspec, compactindex.KindSpecs,
	}

	f.Fuzz(func(_ *testing.T, data []byte) {
		for _, k := range kinds {
			_ = rubygemsVerifier(k)(bytes.NewReader(data))
		}
	})
}

// FuzzRubyGemsByteRange checks byteRange never answers outside the file: a
// partial answer lies within size, whatever Range and If-Range say.
func FuzzRubyGemsByteRange(f *testing.F) {
	f.Add("bytes=0-3", "", int64(10))
	f.Add("bytes=5-", `"x"`, int64(10))
	f.Add("bytes=-4", "", int64(3))
	f.Add("bytes=9-1", "", int64(0))
	f.Add("bytes=0-1,3-4", "", int64(100))

	f.Fuzz(func(t *testing.T, rng, ifRange string, size int64) {
		if size < 0 {
			return
		}

		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.Header.Set("Range", rng)
		r.Header.Set("If-Range", ifRange)

		start, length, status := byteRange(r, size, `"x"`)

		switch status {
		case http.StatusPartialContent:
			if start < 0 || length <= 0 || start+length > size {
				t.Fatalf("range %q of %d bytes = %d+%d", rng, size, start, length)
			}
		case http.StatusOK:
			if start != 0 || length != size {
				t.Fatalf("whole answer = %d+%d of %d", start, length, size)
			}
		case http.StatusRequestedRangeNotSatisfiable:
		default:
			t.Fatalf("status %d", status)
		}
	})
}
