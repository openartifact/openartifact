// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Terraform and OpenTofu send no credentials to the URLs a registry hands out
// in X-Terraform-Get, download_url, shasums_url and shasums_signature_url. Those
// URLs are therefore capabilities: the authenticated registry call (where RBAC
// and quarantine are decided) mints a short-lived signed ticket that names
// exactly one file, and the unauthenticated download route only serves what a
// valid ticket names.

// ticketTTL is how long a minted download URL stays valid.
const ticketTTL = 15 * time.Minute

// ticketVersion is the first field of every payload; a change of the format
// bumps it (and the key derivation label) so old tickets stop verifying.
const ticketVersion = "v1"

// ticketKeyLabel separates the ticket key from every other use of the server
// HMAC secret.
const ticketKeyLabel = "terraform-download-v1"

// ticketKind names what a ticket entitles its bearer to download.
type ticketKind string

const (
	kindModule         ticketKind = "module"
	kindProviderZip    ticketKind = "provider-zip"
	kindProviderSums   ticketKind = "provider-shasums"
	kindProviderSumSig ticketKind = "provider-sig"
)

// ticket is the decoded payload. a, b, c are the coordinates of the file:
// module name, system, version; or provider type, version, "os_arch" (zip) or
// "" (checksum file and its signature).
type ticket struct {
	repoID  string
	kind    ticketKind
	a, b, c string
	expires time.Time
}

var errBadTicket = errors.New("invalid download ticket")

// deriveTicketKey turns the server HMAC secret into the ticket signing key. It
// returns nil for an empty secret, which disables tickets.
func deriveTicketKey(secret []byte) []byte {
	if len(secret) == 0 {
		return nil
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ticketKeyLabel))

	return mac.Sum(nil)
}

func signTicket(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)

	return mac.Sum(nil)
}

// encodeTicket renders t as "<payload>.<signature>", both base64url.
func encodeTicket(key []byte, t ticket) string {
	payload := []byte(strings.Join([]string{
		ticketVersion, t.repoID, string(t.kind), t.a, t.b, t.c, strconv.FormatInt(t.expires.Unix(), 10),
	}, "|"))

	enc := base64.RawURLEncoding

	return enc.EncodeToString(payload) + "." + enc.EncodeToString(signTicket(key, payload))
}

// decodeTicket verifies a ticket's signature and expiry and returns its
// payload. Every failure is the same error: the caller must not tell a client
// why a ticket was refused.
func decodeTicket(key []byte, s string, now time.Time) (ticket, error) {
	if len(key) == 0 {
		return ticket{}, errBadTicket
	}

	enc := base64.RawURLEncoding

	rawPayload, rawSig, ok := strings.Cut(s, ".")
	if !ok {
		return ticket{}, errBadTicket
	}

	payload, err := enc.DecodeString(rawPayload)
	if err != nil {
		return ticket{}, errBadTicket
	}

	sig, err := enc.DecodeString(rawSig)
	if err != nil || !hmac.Equal(sig, signTicket(key, payload)) {
		return ticket{}, errBadTicket
	}

	fields := strings.Split(string(payload), "|")
	if len(fields) != 7 || fields[0] != ticketVersion {
		return ticket{}, errBadTicket
	}

	exp, err := strconv.ParseInt(fields[6], 10, 64)
	if err != nil {
		return ticket{}, errBadTicket
	}

	t := ticket{
		repoID: fields[1], kind: ticketKind(fields[2]), a: fields[3], b: fields[4], c: fields[5], expires: time.Unix(exp, 0),
	}
	if !now.Before(t.expires) || !t.valid() {
		return ticket{}, errBadTicket
	}

	return t, nil
}

// valid reports whether the coordinates have the shape their kind requires. A
// correctly signed ticket always does; the check keeps a ticket from a future
// or buggy minter from reaching the database with garbage.
func (t ticket) valid() bool {
	if t.repoID == "" {
		return false
	}

	switch t.kind {
	case kindModule:
		return validName(t.a) && validName(t.b) && validVersion(t.c)
	case kindProviderZip:
		osName, arch, ok := strings.Cut(t.c, "_")

		return validName(t.a) && validVersion(t.b) && ok && validPlatformPart(osName) && validPlatformPart(arch)
	case kindProviderSums, kindProviderSumSig:
		return validName(t.a) && validVersion(t.b) && t.c == ""
	}

	return false
}
