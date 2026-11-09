// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package csaf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
)

const (
	errHashMismatch   fetchError = "hash mismatch"
	errHashFile       fetchError = "invalid hash file"
	errUnsigned       fetchError = "document is not signed"
	errNoKey          fetchError = "no OpenPGP key to verify the signature"
	errBadSignature   fetchError = "invalid OpenPGP signature"
	errSignatureFetch fetchError = "signature file unavailable"
)

// checkHashes verifies the listed hash files of the document, or the first of
// <url>.sha512 and <url>.sha256 that exists when the listing tells none
// (CSAF 2.0 7.1.18). A listed hash file must be available.
func (r *run) checkHashes(ctx context.Context, c *candidate, body []byte) error {
	if len(c.hashURLs) == 0 && c.probeHash {
		for _, ext := range []string{".sha512", ".sha256"} {
			sum, err := r.f.get(ctx, c.url+ext, maxSidecarBytes)
			if errors.Is(err, errNotFound) {
				continue
			}

			if err != nil {
				return wrap("hash file", err)
			}

			return verifyHash(sum, body)
		}

		return nil
	}

	for _, u := range c.hashURLs {
		sum, err := r.f.get(ctx, u, maxSidecarBytes)
		if err != nil {
			return wrap("hash file", err)
		}

		if err := verifyHash(sum, body); err != nil {
			return err
		}
	}

	return nil
}

// verifyHash checks body against a "<hex>  <filename>" hash file; the
// algorithm (SHA-256 or SHA-512) follows from the digest length.
func verifyHash(file, body []byte) error {
	fields := strings.Fields(string(file))
	if len(fields) == 0 {
		return errHashFile
	}

	want, err := hex.DecodeString(fields[0])
	if err != nil {
		return errHashFile
	}

	var got []byte

	switch len(want) {
	case sha256.Size:
		s := sha256.Sum256(body)
		got = s[:]
	case sha512.Size:
		s := sha512.Sum512(body)
		got = s[:]
	default:
		return errHashFile
	}

	if !bytes.Equal(got, want) {
		return errHashMismatch
	}

	return nil
}

// checkSignature verifies the detached armored signature of the document
// (the listed one, else <url>.asc; CSAF 2.0 7.1.19). An unsigned document, or
// one without a key to check it against, passes only when no signature is
// required; a signature that does not verify against the keys never passes.
func (r *run) checkSignature(ctx context.Context, c *candidate, body []byte) error {
	sigURL := c.sigURL
	if sigURL == "" {
		sigURL = c.url + ".asc"
	}

	sig, err := r.f.get(ctx, sigURL, maxSidecarBytes)

	switch {
	case errors.Is(err, errNotFound):
		if r.src.RequireSignature {
			return errUnsigned
		}

		return nil
	case err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return errSignatureFetch
	}

	keys, err := r.keyring(ctx)
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		if r.src.RequireSignature {
			return errNoKey
		}

		return nil
	}

	if _, err := openpgp.CheckArmoredDetachedSignature(keys, bytes.NewReader(body), bytes.NewReader(sig), nil); err != nil {
		return errBadSignature
	}

	return nil
}

// keyring returns the keys signatures are checked against: the pinned keys,
// else the provider metadata's public_openpgp_keys whose fingerprint matches
// (loaded once; a key that cannot be fetched or read is skipped).
func (r *run) keyring(ctx context.Context) (openpgp.EntityList, error) {
	if r.loaded {
		return r.keys, nil
	}

	for _, ref := range r.keyRefs {
		fp := normalizeFingerprint(ref.Fingerprint)
		if fp == "" {
			continue
		}

		u, err := resolve(r.keyBase, ref.URL)
		if err != nil {
			continue
		}

		armored, err := r.f.get(ctx, u.String(), maxSidecarBytes)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			continue
		}

		ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
		if err != nil {
			continue
		}

		for _, e := range ring {
			if hex.EncodeToString(e.PrimaryKey.Fingerprint) == fp {
				r.keys = append(r.keys, e)
			}
		}
	}

	r.loaded = true

	return r.keys, nil
}

// normalizeFingerprint lower-cases a hex fingerprint and drops its spaces.
func normalizeFingerprint(fp string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fp), " ", ""))
}
