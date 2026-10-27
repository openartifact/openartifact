// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

// sumEntry is one line of a provider version's SHA256SUMS file.
type sumEntry struct {
	sha256   string
	filename string
}

// buildSHA256SUMS renders the checksum file Terraform verifies a provider
// against: "<sha256>  <file name>" lines in file name order, so the content
// (and with it the signature input) does not depend on the order the database
// listed the platforms in.
func buildSHA256SUMS(entries []sumEntry) []byte {
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b sumEntry) int { return strings.Compare(a.filename, b.filename) })

	var buf bytes.Buffer

	for _, e := range sorted {
		fmt.Fprintf(&buf, "%s  %s\n", e.sha256, e.filename)
	}

	return buf.Bytes()
}

var errNoSignature = errors.New("armored block holds no signature")

// binarySignature converts the ASCII-armored detached signature the signer
// produces into the binary form Terraform expects at shasums_signature_url
// (the .sig file of a provider release is a binary OpenPGP signature).
func binarySignature(armored []byte) ([]byte, error) {
	block, err := armor.Decode(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("decode signature armor: %w", err)
	}

	if block.Type != "PGP SIGNATURE" {
		return nil, errNoSignature
	}

	raw, err := io.ReadAll(block.Body)
	if err != nil {
		return nil, fmt.Errorf("read signature: %w", err)
	}

	if len(raw) == 0 {
		return nil, errNoSignature
	}

	return raw, nil
}

// keyID is the 16-hex-digit OpenPGP key ID of a fingerprint: its last 8 bytes.
func keyID(fingerprint string) string {
	if len(fingerprint) <= 16 {
		return fingerprint
	}

	return fingerprint[len(fingerprint)-16:]
}
