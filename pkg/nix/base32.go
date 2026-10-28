// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nix implements the parts of the Nix binary cache protocol the
// server needs: the nixbase32 encoding, store path names, the narinfo file
// format and the ed25519 signatures narinfo carry.
package nix

import (
	"errors"
	"strings"
)

// base32Alphabet is Nix's alphabet: the digits and the lower case letters
// without e, o, u and t.
const base32Alphabet = "0123456789abcdfghijklmnpqrsvwxyz"

// ErrInvalidBase32 is returned for text that is not nixbase32.
var ErrInvalidBase32 = errors.New("invalid nixbase32")

// base32Len is the length of the nixbase32 encoding of n bytes.
func base32Len(n int) int {
	if n <= 0 {
		return 0
	}

	return (n*8-1)/5 + 1
}

// EncodeBase32 returns the nixbase32 encoding of b. Unlike RFC 4648 Nix emits
// the least significant digit last, i.e. it encodes the bytes in reverse.
func EncodeBase32(b []byte) string {
	n := base32Len(len(b))

	var sb strings.Builder

	sb.Grow(n)

	for i := n - 1; i >= 0; i-- {
		bit := i * 5
		idx, off := bit/8, uint(bit%8)

		c := uint(b[idx]) >> off
		if idx+1 < len(b) {
			c |= uint(b[idx+1]) << (8 - off)
		}

		sb.WriteByte(base32Alphabet[c&0x1f])
	}

	return sb.String()
}

// DecodeBase32 decodes nixbase32 text into size bytes. The text must have the
// exact length of such an encoding and no bits may be set beyond the last
// byte, so a value has exactly one spelling.
func DecodeBase32(s string, size int) ([]byte, error) {
	if len(s) != base32Len(size) {
		return nil, ErrInvalidBase32
	}

	out := make([]byte, size)

	for n := range len(s) {
		d := strings.IndexByte(base32Alphabet, s[len(s)-1-n])
		if d < 0 {
			return nil, ErrInvalidBase32
		}

		bit := n * 5
		idx, off := bit/8, uint(bit%8)

		out[idx] |= byte(d << off)

		carry := byte(d >> (8 - off))
		if idx+1 < size {
			out[idx+1] |= carry
		} else if carry != 0 {
			return nil, ErrInvalidBase32
		}
	}

	return out, nil
}
