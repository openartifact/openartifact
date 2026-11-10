// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rpmsign adds OpenPGP signatures to RPM packages.
//
// An RPM carries its signatures in the signature header that sits between the
// lead and the metadata header. Signing therefore rewrites only that header:
// the package header and the payload are copied byte for byte, and
// the result is a different file (and a different SHA-256) from the input.
package rpmsign

import (
	"errors"
	"fmt"
	"io"

	rpmutils "github.com/M0Rf30/pkgformat/rpm"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/M0Rf30/pkgformat/gpg"
)

// ErrKeyNotSupported means the signer cannot expose a private key to sign packages with.
var ErrKeyNotSupported = errors.New("rpmsign: signer cannot sign packages")

// KeyHolder is implemented by signers that hold their private key locally.
type KeyHolder interface {
	SigningKey() (*packet.PrivateKey, error)
}

// Sign writes a copy of the RPM in src to dst signed by signer, replacing any
// signature the package already had. Format-4 packages get the header-only
// (RSAHEADER) and header-plus-payload (PGP) signatures; format-6 packages
// (rpm 6.0+) get an OPENPGP header signature, as rpmsign 6 writes, with the
// payload covered by the digests of the signed header. src is read from the
// start and must be seekable.
func Sign(src io.ReadSeeker, dst io.Writer, signer gpg.Signer) error {
	holder, ok := signer.(KeyHolder)
	if !ok {
		return ErrKeyNotSupported
	}

	key, err := holder.SigningKey()
	if err != nil {
		return fmt.Errorf("rpmsign: %w", err)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rpmsign: rewinding package: %w", err)
	}

	header, err := rpmutils.SignRpmStream(src, key, nil)
	if err != nil {
		return fmt.Errorf("rpmsign: %w", err)
	}

	// The dump holds the lead followed by the new signature header (without
	// the padding of the old one), exactly as long as the area it replaces
	// starts: everything after OriginalSignatureHeaderSize is untouched.
	head, err := header.DumpSignatureHeader(false)
	if err != nil {
		return fmt.Errorf("rpmsign: encoding signature header: %w", err)
	}

	if _, err := dst.Write(head); err != nil {
		return fmt.Errorf("rpmsign: writing signature header: %w", err)
	}

	if _, err := src.Seek(int64(header.OriginalSignatureHeaderSize()), io.SeekStart); err != nil {
		return fmt.Errorf("rpmsign: skipping the old signature header: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("rpmsign: copying package body: %w", err)
	}

	return nil
}
