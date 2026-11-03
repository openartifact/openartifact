// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"encoding/binary"
	"math/rand/v2"
	"net/http"
)

const hexDigits = "0123456789abcdef"

// Request and trace ids correlate log lines and spans; they are not secrets.
// math/rand/v2's top-level functions draw from the runtime's per-thread ChaCha8
// generator, seeded from the OS, so ids are unpredictable and collision free in
// practice without a crypto/rand read (a syscall-free path) per request, and
// each id is built in one allocation (its string).

// requestIDHeader is the canonical form of the request-id header. The map is
// indexed directly: Header.Get("X-Request-ID") would canonicalise the key to
// "X-Request-Id" (an allocation) on every call.
const requestIDHeader = "X-Request-Id"

func requestIDOf(h http.Header) string {
	if v := h[requestIDHeader]; len(v) > 0 {
		return v[0]
	}

	return ""
}

func setRequestID(h http.Header, id string) { h[requestIDHeader] = []string{id} }

// newUUID returns a random version-4 UUID in its canonical 36-character form.
func newUUID() string {
	var raw [16]byte

	binary.BigEndian.PutUint64(raw[:8], rand.Uint64()&^(0xf<<12)|4<<12)   //nolint:gosec // correlation id, not a secret; version 4
	binary.BigEndian.PutUint64(raw[8:], rand.Uint64()&^(0x3<<62)|0x2<<62) //nolint:gosec // correlation id; RFC 4122 variant

	var b [36]byte

	pos := 0

	for i, v := range raw {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			b[pos] = '-'
			pos++
		}

		b[pos], b[pos+1] = hexDigits[v>>4], hexDigits[v&0xf]
		pos += 2
	}

	return string(b[:])
}

// generateTraceID returns a random 16-byte trace ID as 32 lowercase hex
// characters.
func generateTraceID() string {
	var raw [traceIDLen / 2]byte

	binary.BigEndian.PutUint64(raw[:8], rand.Uint64()) //nolint:gosec // correlation id, not a secret
	binary.BigEndian.PutUint64(raw[8:], rand.Uint64()) //nolint:gosec // correlation id, not a secret

	var b [traceIDLen]byte

	for i, v := range raw {
		b[2*i], b[2*i+1] = hexDigits[v>>4], hexDigits[v&0xf]
	}

	return string(b[:])
}
