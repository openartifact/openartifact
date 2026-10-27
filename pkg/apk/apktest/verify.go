// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apktest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // Alpine's own indexes carry SHA-1 .SIGN.RSA. signatures
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// sigRSA256Prefix names a SHA-256 signature entry, sigRSAPrefix the older
	// SHA-1 one.
	sigRSA256Prefix = ".SIGN.RSA256."
	sigRSAPrefix    = ".SIGN.RSA."
	sigPrefix       = ".SIGN."

	// maxSignatureSegment and maxSignature bound the leading signature member
	// VerifyIndex reads: a 16384-bit RSA signature is 2048 bytes.
	maxSignatureSegment = 64 << 10
	maxSignature        = 4096
)

// ErrSignature is wrapped by every error VerifyIndex returns.
var ErrSignature = errors.New("apk index signature check failed")

func sigError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSignature, fmt.Sprintf(format, args...))
}

// VerifyIndex verifies the signature in front of an APKINDEX.tar.gz, as apk
// does, and returns the name of the key that signed it. data must start with a
// gzip member whose tar holds one .SIGN.RSA256.<key name> (SHA-256) or
// .SIGN.RSA.<key name> (SHA-1) entry; the signature covers every byte after
// that member. keys maps key names, as in /etc/apk/keys, to public keys. Errors
// wrap [ErrSignature].
func VerifyIndex(data []byte, keys map[string]*rsa.PublicKey) (string, error) {
	name, sig, end, err := readSignature(data)
	if err != nil {
		return "", err
	}

	signed := data[end:]
	if len(signed) == 0 {
		return "", sigError("nothing follows the signature")
	}

	var (
		algorithm crypto.Hash
		digest    []byte
		keyName   string
	)

	switch {
	case strings.HasPrefix(name, sigRSA256Prefix):
		algorithm, keyName = crypto.SHA256, strings.TrimPrefix(name, sigRSA256Prefix)
		sum := sha256.Sum256(signed)
		digest = sum[:]
	case strings.HasPrefix(name, sigRSAPrefix):
		algorithm, keyName = crypto.SHA1, strings.TrimPrefix(name, sigRSAPrefix)
		sum := sha1.Sum(signed) //nolint:gosec // see the import
		digest = sum[:]
	default:
		return "", sigError("unsupported signature %q", short(name))
	}

	pub := keys[keyName]
	if keyName == "" || pub == nil {
		return "", sigError("no trusted key %q", short(keyName))
	}

	if err := rsa.VerifyPKCS1v15(pub, algorithm, digest, sig); err != nil {
		return "", sigError("key %q: %v", short(keyName), err)
	}

	return keyName, nil
}

// short shortens s for an error message.
func short(s string) string {
	const limit = 64

	if len(s) > limit {
		return s[:limit] + "..."
	}

	return s
}

// readSignature reads the gzip member at the start of data, which must hold
// exactly one signature entry. It returns the entry name, the signature and
// the offset after the member.
func readSignature(data []byte) (string, []byte, int, error) {
	rd := bytes.NewReader(data) // a ByteReader: the gzip reader stops at the end of the member

	gz, err := gzip.NewReader(rd)
	if err != nil {
		return "", nil, 0, sigError("the index does not start with a gzip member: %v", err)
	}

	gz.Multistream(false)

	raw, err := io.ReadAll(io.LimitReader(gz, maxSignatureSegment+1))
	if err != nil || len(raw) > maxSignatureSegment {
		return "", nil, 0, sigError("the signature member is unreadable or too large")
	}

	end := len(data) - rd.Len()
	tr := tar.NewReader(bytes.NewReader(raw))

	hdr, err := tr.Next()
	if err != nil || !strings.HasPrefix(hdr.Name, sigPrefix) {
		return "", nil, 0, sigError("the index is not signed")
	}

	if hdr.Typeflag != tar.TypeReg || hdr.Size > maxSignature {
		return "", nil, 0, sigError("malformed signature entry")
	}

	sig, err := io.ReadAll(tr)
	if err != nil {
		return "", nil, 0, sigError("reading the signature: %v", err)
	}

	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return "", nil, 0, sigError("the signature member holds more than one entry")
	}

	return hdr.Name, sig, end, nil
}
