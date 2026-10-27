// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"strings"
	"testing"
	"time"
)

func ticketNow() time.Time { return time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC) }

func sampleTickets() []ticket {
	exp := ticketNow().Add(ticketTTL)

	return []ticket{
		{repoID: "repo-1", kind: kindModule, a: "vpc", b: "aws", c: "1.2.3", expires: exp},
		{repoID: "repo-1", kind: kindProviderZip, a: "aws", b: "1.2.3-rc.1", c: "linux_amd64", expires: exp},
		{repoID: "repo-1", kind: kindProviderSums, a: "aws", b: "1.2.3", expires: exp},
		{repoID: "repo-1", kind: kindProviderSumSig, a: "aws", b: "1.2.3", expires: exp},
	}
}

func TestTicketRoundTrip(t *testing.T) {
	key := deriveTicketKey([]byte(testSecret))

	for _, want := range sampleTickets() {
		t.Run(string(want.kind), func(t *testing.T) {
			got, err := decodeTicket(key, encodeTicket(key, want), ticketNow())
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if !got.expires.Equal(want.expires) {
				t.Fatalf("expires = %v, want %v", got.expires, want.expires)
			}

			got.expires, want.expires = time.Time{}, time.Time{}
			if got != want {
				t.Fatalf("decoded %+v, want %+v", got, want)
			}
		})
	}
}

func TestTicketRefused(t *testing.T) {
	key := deriveTicketKey([]byte(testSecret))
	otherKey := deriveTicketKey([]byte("another-secret-another-secret-00"))
	good := sampleTickets()[0]
	enc := encodeTicket(key, good)
	payload, sig, _ := strings.Cut(enc, ".")

	flip := func(s string) string {
		b := []byte(s)
		b[len(b)/2] ^= 1

		return string(b)
	}

	expired := good
	expired.expires = ticketNow().Add(-time.Second)

	badShape := good
	badShape.a = "../etc"

	otherKind := good
	otherKind.kind = "provider-evil"

	noRepo := good
	noRepo.repoID = ""

	for _, tc := range []struct {
		name string
		key  []byte
		in   string
		now  time.Time
	}{
		{"expired", key, encodeTicket(key, expired), ticketNow()},
		{"exactly at expiry", key, enc, good.expires},
		{"signed by another key", key, encodeTicket(otherKey, good), ticketNow()},
		{"no key configured", nil, enc, ticketNow()},
		{"tampered payload", key, flip(payload) + "." + sig, ticketNow()},
		{"tampered signature", key, payload + "." + flip(sig), ticketNow()},
		{"signature dropped", key, payload, ticketNow()},
		{"empty signature", key, payload + ".", ticketNow()},
		{"empty", key, "", ticketNow()},
		{"garbage", key, "not a ticket", ticketNow()},
		{"bad coordinates", key, encodeTicket(key, badShape), ticketNow()},
		{"unknown kind", key, encodeTicket(key, otherKind), ticketNow()},
		{"no repository", key, encodeTicket(key, noRepo), ticketNow()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeTicket(tc.key, tc.in, tc.now); err == nil {
				t.Fatal("ticket accepted")
			}
		})
	}
}

func TestDeriveTicketKey(t *testing.T) {
	if deriveTicketKey(nil) != nil || deriveTicketKey([]byte{}) != nil {
		t.Fatal("an empty secret must not yield a key")
	}

	a, b := deriveTicketKey([]byte(testSecret)), deriveTicketKey([]byte(testSecret))
	if string(a) != string(b) || string(a) == string(testSecret) {
		t.Fatal("the ticket key must be a stable derivation, not the secret itself")
	}
}
